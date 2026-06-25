package moderator

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/anthropic"
)

type ClaudeAdapter struct {
	ReplacementTextForStreaming    string
	ReplacementTextForNonStreaming string
	ReplacementStopReason          string
}

var _ TextModeratorAdapter = (*ClaudeAdapter)(nil)

func (a *ClaudeAdapter) ExtractTextFromBody(ctx context.Context, body *octollm.UnifiedBody) ([]rune, error) {
	parsed, err := body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("parse body error: %w", err)
	}
	switch parsed := parsed.(type) {
	case *anthropic.ClaudeMessagesRequest:
		return a.extractTextFromRequest(ctx, parsed)
	case *anthropic.ClaudeMessagesResponse:
		return a.extractTextFromNonStreamResponse(ctx, parsed)
	case *anthropic.ClaudeMessagesStreamEvent:
		return a.extractTextFromStreamResponse(ctx, parsed)
	default:
		return nil, fmt.Errorf("unsupported body type: %T", parsed)
	}
}

func (a *ClaudeAdapter) extractTextFromRequest(ctx context.Context, body *anthropic.ClaudeMessagesRequest) ([]rune, error) {
	r := []rune{}

	// 提取 System content
	if body.System != nil {
		switch system := body.System.(type) {
		case anthropic.SystemString:
			r = append(r, []rune(string(system))...)
		case anthropic.SystemBlocks:
			for _, block := range system {
				r = append(r, []rune(block.Text)...)
			}
		}
	}

	// 提取 Messages
	for _, msg := range body.Messages {
		if msg == nil || msg.Content == nil {
			continue
		}
		r = append(r, []rune(msg.Content.ExtractText())...)
	}

	return r, nil
}

func (a *ClaudeAdapter) extractTextFromNonStreamResponse(ctx context.Context, body *anthropic.ClaudeMessagesResponse) ([]rune, error) {
	r := []rune{}

	for _, block := range body.Content {
		if block == nil {
			continue
		}
		r = append(r, []rune(block.ExtractText())...)
	}

	return r, nil
}

func (a *ClaudeAdapter) extractTextFromStreamResponse(ctx context.Context, body *anthropic.ClaudeMessagesStreamEvent) ([]rune, error) {
	r := []rune{}

	// 从 content_block_delta 中提取文本
	if body.Type == "content_block_delta" {
		if body.Delta == nil {
			return nil, fmt.Errorf("content_block_delta: delta is nil")
		}
		delta := &body.Delta.ContentBlockDelta
		if delta.Text != nil {
			r = append(r, []rune(*delta.Text)...)
		}
		if delta.Thinking != nil {
			r = append(r, []rune(*delta.Thinking)...)
		}
		if delta.PartialJSON != nil {
			r = append(r, []rune(*delta.PartialJSON)...)
		}
	}

	// 从 message_start 中提取初始内容
	if body.Type == "message_start" && body.Message != nil {
		for _, block := range body.Message.Content {
			if block == nil {
				continue
			}
			r = append(r, []rune(block.ExtractText())...)
		}
	}

	// 从 content_block_start 中提取初始内容
	if body.Type == "content_block_start" && body.ContentBlock != nil {
		r = append(r, []rune(body.ContentBlock.ExtractText())...)
	}

	return r, nil
}

func (a *ClaudeAdapter) GetReplacementBody(ctx context.Context, body *octollm.UnifiedBody) *octollm.UnifiedBody {
	parsed, err := body.Parsed()
	if err != nil {
		slog.DebugContext(ctx, fmt.Sprintf("parse body error: %s", err))
		return nil
	}
	switch parsed := parsed.(type) {
	case *anthropic.ClaudeMessagesResponse:
		r := a.getReplacementNonStreamResponse(ctx, parsed)
		if r == nil {
			return nil
		}
		body.SetParsed(r)
		return body
	case *anthropic.ClaudeMessagesStreamEvent:
		r := a.getReplacementStreamResponse(ctx, parsed)
		if r == nil {
			return nil
		}
		body.SetParsed(r)
		return body
	default:
		return nil
	}
}

func (a *ClaudeAdapter) getReplacementNonStreamResponse(ctx context.Context, resp *anthropic.ClaudeMessagesResponse) *anthropic.ClaudeMessagesResponse {
	if a.ReplacementTextForNonStreaming == "" {
		return nil
	}

	// 创建替换响应
	text := a.ReplacementTextForNonStreaming
	r := &anthropic.ClaudeMessagesResponse{
		ID:         resp.ID,
		Type:       resp.Type,
		Role:       "assistant",
		Model:      resp.Model,
		StopReason: a.ReplacementStopReason,
		Content: anthropic.MessageContentBlockArray{
			&anthropic.TextBlockParam{
				Type: "text",
				Text: text,
			},
		},
		Usage: resp.Usage,
	}

	return r
}

func (a *ClaudeAdapter) getReplacementStreamResponse(ctx context.Context, event *anthropic.ClaudeMessagesStreamEvent) *anthropic.ClaudeMessagesStreamEvent {
	if event.Type != "content_block_delta" || a.ReplacementTextForStreaming == "" {
		return nil
	}

	r := &anthropic.ClaudeMessagesStreamEvent{
		Type:  "content_block_delta",
		Index: event.Index,
		Delta: &anthropic.DeltaUnion{
			ContentBlockDelta: anthropic.ContentBlockDelta{
				Type: "text_delta",
				Text: &a.ReplacementTextForStreaming,
			},
		},
	}

	return r
}
