package moderator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/vertex"
)

type VertexAdapter struct {
	ReplacementTextForStreaming    string
	ReplacementTextForNonStreaming string
	ReplacementFinishReason        string
}

var _ TextModeratorAdapter = (*VertexAdapter)(nil)

func (a *VertexAdapter) ExtractTextFromBody(ctx context.Context, body *octollm.UnifiedBody) ([]rune, error) {
	parsed, err := body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("parse body error: %w", err)
	}
	switch parsed := parsed.(type) {
	case *vertex.GenerateContentRequest:
		return a.extractTextFromRequest(parsed)
	case *vertex.GenerateContentResponse:
		return a.extractTextFromResponse(parsed)
	case *vertex.StreamGenerateContentResponse:
		return a.extractTextFromStreamResponse(parsed)
	default:
		return nil, fmt.Errorf("unsupported body type: %T", parsed)
	}
}

func (a *VertexAdapter) extractTextFromRequest(req *vertex.GenerateContentRequest) ([]rune, error) {
	r := []rune{}
	if req.SystemInstruction != nil {
		r = append(r, extractTextFromParts(req.SystemInstruction.Parts)...)
	}
	for _, content := range req.Contents {
		r = append(r, extractTextFromParts(content.Parts)...)
	}
	return r, nil
}

func (a *VertexAdapter) extractTextFromResponse(resp *vertex.GenerateContentResponse) ([]rune, error) {
	r := []rune{}
	for _, candidate := range resp.Candidates {
		if candidate.Content != nil {
			r = append(r, extractTextFromParts(candidate.Content.Parts)...)
		}
	}
	return r, nil
}

func (a *VertexAdapter) extractTextFromStreamResponse(resp *vertex.StreamGenerateContentResponse) ([]rune, error) {
	r := []rune{}
	for _, candidate := range resp.Candidates {
		if candidate.Content != nil {
			r = append(r, extractTextFromParts(candidate.Content.Parts)...)
		}
	}
	return r, nil
}

// extractTextFromParts collects text and function argument strings from a Part slice.
func extractTextFromParts(parts []vertex.Part) []rune {
	r := []rune{}
	for _, part := range parts {
		if part.Text != "" {
			r = append(r, []rune(part.Text)...)
		}
		if part.FunctionCall != nil {
			if raw, err := json.Marshal(part.FunctionCall.Args); err == nil {
				r = append(r, []rune(string(raw))...)
			}
		}
	}
	return r
}

func (a *VertexAdapter) GetReplacementBody(ctx context.Context, body *octollm.UnifiedBody) *octollm.UnifiedBody {
	parsed, err := body.Parsed()
	if err != nil {
		slog.DebugContext(ctx, fmt.Sprintf("parse body error: %s", err))
		return nil
	}
	switch parsed := parsed.(type) {
	case *vertex.GenerateContentResponse:
		r := a.getReplacementNonStreamResponse(parsed)
		if r == nil {
			return nil
		}
		body.SetParsed(r)
		return body
	case *vertex.StreamGenerateContentResponse:
		r := a.getReplacementStreamResponse(parsed)
		if r == nil {
			return nil
		}
		body.SetParsed(r)
		return body
	default:
		return nil
	}
}

func (a *VertexAdapter) getReplacementNonStreamResponse(resp *vertex.GenerateContentResponse) *vertex.GenerateContentResponse {
	if a.ReplacementTextForNonStreaming == "" {
		return nil
	}
	finishReason := a.ReplacementFinishReason
	if finishReason == "" {
		finishReason = "SAFETY"
	}
	r := &vertex.GenerateContentResponse{
		ModelVersion: resp.ModelVersion,
		UsageMetadata: resp.UsageMetadata,
		Candidates: []vertex.Candidate{
			{
				Content: &vertex.Content{
					Role:  "model",
					Parts: []vertex.Part{{Text: a.ReplacementTextForNonStreaming}},
				},
				FinishReason: finishReason,
			},
		},
	}
	return r
}

func (a *VertexAdapter) getReplacementStreamResponse(resp *vertex.StreamGenerateContentResponse) *vertex.StreamGenerateContentResponse {
	if a.ReplacementTextForStreaming == "" {
		return nil
	}
	finishReason := a.ReplacementFinishReason
	if finishReason == "" {
		finishReason = "SAFETY"
	}
	r := &vertex.StreamGenerateContentResponse{
		ModelVersion: resp.ModelVersion,
		Candidates: []vertex.Candidate{
			{
				Content: &vertex.Content{
					Role:  "model",
					Parts: []vertex.Part{{Text: a.ReplacementTextForStreaming}},
				},
				FinishReason: finishReason,
			},
		},
	}
	return r
}
