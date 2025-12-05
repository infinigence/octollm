package moderator

import (
	"context"
	"fmt"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/openai/openai-go/v3"
	"github.com/sirupsen/logrus"
)

type OpenAIAdapter struct {
	ReplacementTextForStreaming    string
	ReplacementTextForNonStreaming string
	ReplacementFinishReason        string
}

var _ TextModeratorAdapter = (*OpenAIAdapter)(nil)

func (a *OpenAIAdapter) ExtractTextFromBody(ctx context.Context, body *octollm.UnifiedBody) ([]rune, error) {
	parsed, err := body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("parse body error: %w", err)
	}
	switch parsed := parsed.(type) {
	case *openai.ChatCompletionNewParams:
		return a.extracTextFromRequest(ctx, parsed)
	case *openai.ChatCompletion:
		return a.extracTextFromResponse(ctx, parsed)
	case *openai.ChatCompletionChunk:
		return a.extracTextFromChunk(ctx, parsed)
	default:
		return nil, fmt.Errorf("unsupported body type: %T", parsed)
	}
}

func (a *OpenAIAdapter) extracTextFromRequest(ctx context.Context, body *openai.ChatCompletionNewParams) ([]rune, error) {
	r := []rune{}
	for _, msg := range body.Messages {
		r = append(r, a.combinedTextForChatCompletionsMessage(&msg)...)
	}
	return r, nil
}

func (a *OpenAIAdapter) extracTextFromResponse(ctx context.Context, body *openai.ChatCompletion) ([]rune, error) {
	if len(body.Choices) != 1 {
		return nil, fmt.Errorf("only support 1 choice, got %d", len(body.Choices))
	}
	msg := body.Choices[0].Message
	r := []rune(msg.Content)

	for _, toolCall := range msg.ToolCalls {
		r = append(r, []rune(toolCall.Function.Arguments)...)
	}

	return r, nil
}

func (a *OpenAIAdapter) extracTextFromChunk(ctx context.Context, body *openai.ChatCompletionChunk) ([]rune, error) {
	if len(body.Choices) != 1 {
		return nil, fmt.Errorf("only support 1 choice, got %d", len(body.Choices))
	}
	msg := body.Choices[0].Delta

	contentCount := 0
	r := []rune{}
	if len(msg.Content) > 0 {
		contentCount++
		r = append(r, []rune(msg.Content)...)
	}

	for _, toolCall := range msg.ToolCalls {
		if len(toolCall.Function.Arguments) > 0 {
			r = append(r, []rune(toolCall.Function.Arguments)...)
			contentCount++
		}
	}

	if contentCount > 1 {
		return nil, fmt.Errorf("only support 1 content per chunk, got %d", contentCount)
	}

	return r, nil
}

func (a *OpenAIAdapter) combinedTextForChatCompletionsMessage(msg *openai.ChatCompletionMessageParamUnion) []rune {
	switch v := msg.GetContent().AsAny().(type) {
	case *string:
		return []rune(*v)
	case *[]openai.ChatCompletionContentPartTextParam:
		r := []rune{}
		for _, part := range *v {
			r = append(r, []rune(part.Text)...)
		}
		return r
	case *[]openai.ChatCompletionContentPartUnionParam:
		r := []rune{}
		for _, part := range *v {
			t := part.GetText()
			if t != nil {
				r = append(r, []rune(*t)...)
			}
		}
		return r
	case *[]openai.ChatCompletionAssistantMessageParamContentArrayOfContentPartUnion:
		r := []rune{}
		for _, part := range *v {
			t := part.GetText()
			if t != nil {
				r = append(r, []rune(*t)...)
			}
		}
		return r
	default:
		return []rune{}
	}
}

func (a *OpenAIAdapter) GetReplacementBody(ctx context.Context, body *octollm.UnifiedBody) *octollm.UnifiedBody {
	parsed, err := body.Parsed()
	if err != nil {
		logrus.WithContext(ctx).Debugf("parse body error: %s", err)
		return nil
	}
	switch parsed := parsed.(type) {
	case *openai.ChatCompletion:
		r := a.getReplacementResponse(ctx, parsed)
		if r == nil {
			return nil
		}
		body.SetParsed(r)
		return body
	case *openai.ChatCompletionChunk:
		r := a.getReplacementChunk(ctx, parsed)
		if r == nil {
			return nil
		}
		body.SetParsed(r)
		return body
	default:
		return nil
	}
}

func (a *OpenAIAdapter) getReplacementResponse(ctx context.Context, resp *openai.ChatCompletion) *openai.ChatCompletion {
	if a.ReplacementTextForNonStreaming == "" {
		return nil
	}
	r := &openai.ChatCompletion{
		ID:      resp.ID,
		Object:  resp.Object,
		Created: resp.Created,
		Model:   resp.Model,
		Choices: []openai.ChatCompletionChoice{
			{
				Index: resp.Choices[0].Index,
				Message: openai.ChatCompletionMessage{
					Content: a.ReplacementTextForNonStreaming,
				},
				FinishReason: a.ReplacementFinishReason,
			},
		},
		Usage: resp.Usage,
	}
	return r
}

func (a *OpenAIAdapter) getReplacementChunk(ctx context.Context, chunk *openai.ChatCompletionChunk) *openai.ChatCompletionChunk {
	if a.ReplacementTextForStreaming == "" {
		return nil
	}
	r := &openai.ChatCompletionChunk{
		ID:      chunk.ID,
		Object:  chunk.Object,
		Created: chunk.Created,
		Model:   chunk.Model,
		Choices: []openai.ChatCompletionChunkChoice{
			{
				Index: chunk.Choices[0].Index,
				Delta: openai.ChatCompletionChunkChoiceDelta{
					Content: a.ReplacementTextForStreaming,
				},
				FinishReason: a.ReplacementFinishReason,
			},
		},
	}
	return r
}
