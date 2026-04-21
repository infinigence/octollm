package moderator

import (
	"context"
	"fmt"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
)

// UniversalAdapter 通用适配器，根据 Response 格式自动选择对应的 adapter
type UniversalAdapter struct {
	openaiAdapter          *OpenAIAdapter
	openaiResponsesAdapter *OpenAIResponseAdapter
	claudeAdapter          *ClaudeAdapter
}

var _ TextModeratorAdapter = (*UniversalAdapter)(nil)

// NewUniversalAdapter 创建通用适配器
func NewUniversalAdapter() *UniversalAdapter {
	return &UniversalAdapter{
		openaiAdapter:          &OpenAIAdapter{},
		openaiResponsesAdapter: &OpenAIResponseAdapter{},
		claudeAdapter:          &ClaudeAdapter{},
	}
}

// NewUniversalAdapterWithConfig 创建带配置的通用适配器
func NewUniversalAdapterWithConfig(
	replacementTextForStreaming string,
	replacementTextForNonStreaming string,
	replacementFinishReason string,
) *UniversalAdapter {
	return &UniversalAdapter{
		openaiAdapter: &OpenAIAdapter{
			ReplacementTextForStreaming:    replacementTextForStreaming,
			ReplacementTextForNonStreaming: replacementTextForNonStreaming,
			ReplacementFinishReason:        replacementFinishReason,
		},
		openaiResponsesAdapter: &OpenAIResponseAdapter{
			ReplacementTextForStreaming:    replacementTextForStreaming,
			ReplacementTextForNonStreaming: replacementTextForNonStreaming,
		},
		claudeAdapter: &ClaudeAdapter{
			ReplacementTextForStreaming:    replacementTextForStreaming,
			ReplacementTextForNonStreaming: replacementTextForNonStreaming,
			ReplacementStopReason:          replacementFinishReason,
		},
	}
}

// ExtractTextFromBody 从 body 中提取文本，自动识别格式
func (a *UniversalAdapter) ExtractTextFromBody(ctx context.Context, body *octollm.UnifiedBody) ([]rune, error) {
	parsed, err := body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("parse body error: %w", err)
	}

	// 根据类型选择对应的 adapter
	switch parsed.(type) {
	case *openai.ResponsesRequest, *openai.ResponsesResponse, *openai.ResponseStreamChunk:
		return a.openaiResponsesAdapter.ExtractTextFromBody(ctx, body)
	case *openai.ChatCompletionRequest, *openai.ChatCompletionResponse, *openai.ChatCompletionStreamChunk:
		return a.openaiAdapter.ExtractTextFromBody(ctx, body)
	case *anthropic.ClaudeMessagesRequest, *anthropic.ClaudeMessagesResponse, *anthropic.ClaudeMessagesStreamEvent:
		return a.claudeAdapter.ExtractTextFromBody(ctx, body)
	default:
		return nil, fmt.Errorf("unsupported body type: %T", parsed)
	}
}

// GetReplacementBody 获取替换的 body，自动识别格式
func (a *UniversalAdapter) GetReplacementBody(ctx context.Context, body *octollm.UnifiedBody) *octollm.UnifiedBody {
	parsed, err := body.Parsed()
	if err != nil {
		return nil
	}

	// 根据类型选择对应的 adapter
	switch parsed.(type) {
	case *openai.ResponsesResponse, *openai.ResponseStreamChunk:
		return a.openaiResponsesAdapter.GetReplacementBody(ctx, body)
	case *openai.ChatCompletionResponse, *openai.ChatCompletionStreamChunk:
		return a.openaiAdapter.GetReplacementBody(ctx, body)
	case *anthropic.ClaudeMessagesResponse, *anthropic.ClaudeMessagesStreamEvent:
		return a.claudeAdapter.GetReplacementBody(ctx, body)
	default:
		return nil
	}
}
