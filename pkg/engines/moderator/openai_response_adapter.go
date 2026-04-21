package moderator

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
)

// OpenAIResponseAdapter extracts and replaces text for the OpenAI Responses API
// (POST /v1/responses) request and response bodies.
type OpenAIResponseAdapter struct {
	ReplacementTextForStreaming    string
	ReplacementTextForNonStreaming string
}

var _ TextModeratorAdapter = (*OpenAIResponseAdapter)(nil)

func (a *OpenAIResponseAdapter) ExtractTextFromBody(ctx context.Context, body *octollm.UnifiedBody) ([]rune, error) {
	parsed, err := body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("parse body error: %w", err)
	}
	switch parsed := parsed.(type) {
	case *openai.ResponsesRequest:
		return a.extractTextFromResponsesRequest(ctx, parsed)
	case *openai.ResponsesResponse:
		return a.extractTextFromResponsesNonStreamResponse(ctx, parsed)
	case *openai.ResponseStreamChunk:
		return a.extractTextFromResponsesStreamChunk(ctx, parsed)
	default:
		return nil, fmt.Errorf("unsupported body type: %T", parsed)
	}
}

func (a *OpenAIResponseAdapter) GetReplacementBody(ctx context.Context, body *octollm.UnifiedBody) *octollm.UnifiedBody {
	parsed, err := body.Parsed()
	if err != nil {
		slog.DebugContext(ctx, fmt.Sprintf("parse body error: %s", err))
		return nil
	}
	switch parsed := parsed.(type) {
	case *openai.ResponsesResponse:
		r := a.getReplacementResponsesNonStreamResponse(ctx, parsed)
		if r == nil {
			return nil
		}
		body.SetParsed(r)
		return body
	case *openai.ResponseStreamChunk:
		r := a.getReplacementResponsesStreamResponse(ctx, parsed)
		if r == nil {
			return nil
		}
		body.SetParsed(r)
		return body
	default:
		return nil
	}
}

func (a *OpenAIResponseAdapter) extractTextFromResponsesRequest(ctx context.Context, body *openai.ResponsesRequest) ([]rune, error) {
	if body.Input == nil {
		return []rune{}, nil
	}
	return []rune(body.Input.ExtractText()), nil
}

func (a *OpenAIResponseAdapter) extractTextFromResponsesNonStreamResponse(ctx context.Context, body *openai.ResponsesResponse) ([]rune, error) {
	r := []rune{}
	for _, outputItem := range body.Output {
		if outputItem == nil {
			continue
		}
		r = append(r, []rune(outputItem.ExtractText())...)
	}
	return r, nil
}

func (a *OpenAIResponseAdapter) extractTextFromResponsesStreamChunk(ctx context.Context, body *openai.ResponseStreamChunk) ([]rune, error) {
	switch body.Type {
	case "response.output_text.delta":
		return []rune(body.Delta), nil
	case "response.output_text.done":
		return []rune(body.Text), nil
	case "response.content_part.added", "response.content_part.done":
		if body.Part == nil {
			return []rune{}, nil
		}
		return []rune(body.Part.ExtractText()), nil
	case "response.output_item.added", "response.output_item.done":
		if body.Item == nil {
			return []rune{}, nil
		}
		return []rune(body.Item.ExtractText()), nil
	case "response.completed":
		if body.Response == nil {
			return []rune{}, nil
		}
		return a.extractTextFromResponsesNonStreamResponse(ctx, body.Response)
	default:
		return []rune{}, nil
	}
}

func (a *OpenAIResponseAdapter) getReplacementResponsesNonStreamResponse(ctx context.Context, resp *openai.ResponsesResponse) *openai.ResponsesResponse {
	if a.ReplacementTextForNonStreaming == "" {
		return nil
	}

	return &openai.ResponsesResponse{
		Id: resp.Id,
		Output: []*openai.ResponsesOutputItem{
			{
				Type: "message",
				Role: "assistant",
				Content: []*openai.ResponsesOutputContentItem{
					{
						Type: "output_text",
						Text: a.ReplacementTextForNonStreaming,
					},
				},
			},
		},
		Usage: resp.Usage,
	}
}

func (a *OpenAIResponseAdapter) getReplacementResponsesStreamResponse(ctx context.Context, resp *openai.ResponseStreamChunk) *openai.ResponseStreamChunk {
	if a.ReplacementTextForStreaming == "" {
		return nil
	}

	switch resp.Type {
	case "response.output_text.delta":
		r := *resp
		r.Delta = a.ReplacementTextForStreaming
		return &r
	case "response.output_text.done":
		r := *resp
		r.Text = a.ReplacementTextForStreaming
		return &r
	case "response.content_part.added", "response.content_part.done":
		r := *resp
		r.Part = &openai.ResponsesOutputContentItem{
			Type: "output_text",
			Text: a.ReplacementTextForStreaming,
		}
		return &r
	case "response.output_item.added", "response.output_item.done":
		r := *resp
		r.Item = &openai.ResponsesOutputItem{
			Type: "message",
			Role: "assistant",
			Content: []*openai.ResponsesOutputContentItem{
				{
					Type: "output_text",
					Text: a.ReplacementTextForStreaming,
				},
			},
		}
		return &r
	case "response.completed":
		r := *resp
		if resp.Response == nil {
			r.Response = a.getReplacementResponsesNonStreamResponse(ctx, &openai.ResponsesResponse{})
			return &r
		}
		r.Response = a.getReplacementResponsesNonStreamResponse(ctx, resp.Response)
		return &r
	default:
		return nil
	}
}
