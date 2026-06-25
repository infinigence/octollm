package moderator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeAdapter_ExtractTextFromRequest(t *testing.T) {
	adapter := &ClaudeAdapter{}

	// 创建测试请求
	req := &anthropic.ClaudeMessagesRequest{
		Model:  "claude-3-5-sonnet-20241022",
		System: anthropic.SystemString("You are a helpful assistant."),
		Messages: []*anthropic.MessageParam{
			{
				Role:    "user",
				Content: anthropic.MessageContentString("Hello, how are you?"),
			},
			{
				Role:    "assistant",
				Content: anthropic.MessageContentString("I'm doing great, thank you!"),
			},
		},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[anthropic.ClaudeMessagesRequest]{})
	body.SetParsed(req)

	text, err := adapter.ExtractTextFromBody(context.Background(), body)
	require.NoError(t, err)

	textStr := string(text)
	assert.Contains(t, textStr, "You are a helpful assistant.")
	assert.Contains(t, textStr, "Hello, how are you?")
	assert.Contains(t, textStr, "I'm doing great, thank you!")
}

func TestClaudeAdapter_extractTextFromRequest_JSON_messages_null_skipped(t *testing.T) {
	// messages 数组里的 JSON null 解成 []*MessageParam 的元素 nil → msg == nil、不会 panic，只收集非 nil 消息
	jsonStr := `{
		"model": "claude-3-5-sonnet-20241022",
		"max_tokens": 1024,
		"messages": [
			null,
			{
				"role": "user",
				"content": "after-null-slot"
			}
		]
	}`

	var req anthropic.ClaudeMessagesRequest
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &req))

	require.Len(t, req.Messages, 2)
	require.Nil(t, req.Messages[0])
	require.NotNil(t, req.Messages[1])

	adapter := &ClaudeAdapter{}
	got, err := adapter.extractTextFromRequest(context.Background(), &req)
	require.NoError(t, err)
	assert.Equal(t, "after-null-slot", string(got))
}

func TestClaudeAdapter_ExtractTextFromNonStreamResponse(t *testing.T) {
	adapter := &ClaudeAdapter{}

	// 创建测试响应
	text := "This is a test response."
	resp := &anthropic.ClaudeMessagesResponse{
		ID:         "msg_123",
		Type:       "message",
		Role:       "assistant",
		Model:      "claude-3-5-sonnet-20241022",
		StopReason: "end_turn",
		Content: anthropic.MessageContentBlockArray{
			&anthropic.TextBlockParam{
				Type: "text",
				Text: text,
			},
			&anthropic.ToolUseBlockParam{
				Type:  "tool_use",
				ID:    "call_xxx",
				Name:  "Read",
				Input: json.RawMessage([]byte(`{"file_path": "test.txt"}`)),
			},
		},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[anthropic.ClaudeMessagesResponse]{})
	body.SetParsed(resp)

	extractedText, err := adapter.ExtractTextFromBody(context.Background(), body)
	require.NoError(t, err)
	assert.Equal(t, `This is a test response.{"file_path": "test.txt"}`, string(extractedText))
}

func TestClaudeAdapter_extractTextFromNonStreamResponse_JSON_skips_null_and_tool_use(t *testing.T) {
	jsonStr := `{
			"id": "msg_123",
			"type": "message",
			"role": "assistant",
			"content": [
				null,
				{
					"type": "tool_use",
					"id": "call_xxx",
					"name": "Read",
					"input": null
				}
			],
			"model": "claude-3-5-sonnet-20241022",
			"stop_reason": null,
			"usage": {
				"input_tokens": 10,
				"output_tokens": 0
			}
	}`

	var resp anthropic.ClaudeMessagesResponse
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &resp))

	adapter := &ClaudeAdapter{}
	got, err := adapter.extractTextFromNonStreamResponse(context.Background(), &resp)
	require.NoError(t, err)

	// 标准解码器保留 null 元素为 nil 指针，不过滤，len == 2
	require.Len(t, resp.Content, 2)
	assert.Nil(t, resp.Content[0])
	require.NotNil(t, resp.Content[1])

	assert.Equal(t, "null", string(got))
}

func TestClaudeAdapter_ExtractTextFromStreamResponse(t *testing.T) {
	adapter := &ClaudeAdapter{}

	// 测试 content_block_delta
	deltaText := "Hello"
	event := &anthropic.ClaudeMessagesStreamEvent{
		Type:  "content_block_delta",
		Index: intPtr(0),
		Delta: &anthropic.DeltaUnion{
			ContentBlockDelta: anthropic.ContentBlockDelta{
				Type: "text_delta",
				Text: &deltaText,
			},
		},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[anthropic.ClaudeMessagesStreamEvent]{})
	body.SetParsed(event)

	text, err := adapter.ExtractTextFromBody(context.Background(), body)
	require.NoError(t, err)
	assert.Equal(t, "Hello", string(text))
}

func TestClaudeAdapter_ExtractTextWithRepetition(t *testing.T) {
	adapter := &ClaudeAdapter{}

	// 创建包含重复内容的响应
	repeatedText := strings.Repeat("ABC", 60)
	resp := &anthropic.ClaudeMessagesResponse{
		ID:         "msg_repeat",
		Type:       "message",
		Role:       "assistant",
		Model:      "claude-3-5-sonnet-20241022",
		StopReason: "end_turn",
		Content: anthropic.MessageContentBlockArray{
			&anthropic.TextBlockParam{
				Type: "text",
				Text: repeatedText,
			},
		},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[anthropic.ClaudeMessagesResponse]{})
	body.SetParsed(resp)

	extractedText, err := adapter.ExtractTextFromBody(context.Background(), body)
	require.NoError(t, err)
	assert.Equal(t, repeatedText, string(extractedText))
}

func TestClaudeAdapter_GetReplacementNonStreamResponse(t *testing.T) {
	adapter := &ClaudeAdapter{
		ReplacementTextForNonStreaming: "Content blocked due to policy violation.",
		ReplacementStopReason:          "content_filtered",
	}

	originalText := "Some problematic content"
	resp := &anthropic.ClaudeMessagesResponse{
		ID:         "msg_123",
		Type:       "message",
		Role:       "assistant",
		Model:      "claude-3-5-sonnet-20241022",
		StopReason: "end_turn",
		Content: anthropic.MessageContentBlockArray{
			&anthropic.TextBlockParam{
				Type: "text",
				Text: originalText,
			},
		},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[anthropic.ClaudeMessagesResponse]{})
	body.SetParsed(resp)

	replacementBody := adapter.GetReplacementBody(context.Background(), body)
	require.NotNil(t, replacementBody)

	replacementResp, err := replacementBody.Parsed()
	require.NoError(t, err)

	claudeResp, ok := replacementResp.(*anthropic.ClaudeMessagesResponse)
	require.True(t, ok)
	assert.Equal(t, "content_filtered", claudeResp.StopReason)
	assert.Len(t, claudeResp.Content, 1)
	textBlock, ok := claudeResp.Content[0].(*anthropic.TextBlockParam)
	require.True(t, ok)
	assert.Equal(t, "text", textBlock.Type)
	assert.Equal(t, "Content blocked due to policy violation.", textBlock.Text)
}

func TestClaudeAdapter_GetReplacementStreamResponse(t *testing.T) {
	adapter := &ClaudeAdapter{
		ReplacementTextForStreaming: "[Blocked]",
	}

	deltaText := "Bad content"
	event := &anthropic.ClaudeMessagesStreamEvent{
		Type:  "content_block_delta",
		Index: intPtr(0),
		Delta: &anthropic.DeltaUnion{
			ContentBlockDelta: anthropic.ContentBlockDelta{
				Type: "text_delta",
				Text: &deltaText,
			},
		},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[anthropic.ClaudeMessagesStreamEvent]{})
	body.SetParsed(event)

	replacementBody := adapter.GetReplacementBody(context.Background(), body)
	require.NotNil(t, replacementBody)

	replacementEvent, err := replacementBody.Parsed()
	require.NoError(t, err)

	claudeEvent, ok := replacementEvent.(*anthropic.ClaudeMessagesStreamEvent)
	require.True(t, ok)
	assert.Equal(t, "content_block_delta", claudeEvent.Type)

	replacementDelta := &claudeEvent.Delta.ContentBlockDelta
	require.NotNil(t, replacementDelta)
	assert.Equal(t, "text_delta", replacementDelta.Type)
	assert.NotNil(t, replacementDelta.Text)
	assert.Equal(t, "[Blocked]", *replacementDelta.Text)
}

func TestUniversalAdapter_ClaudeFormat(t *testing.T) {
	adapter := NewUniversalAdapter()

	// 测试 Claude Messages Response
	text := "This is a Claude response."
	resp := &anthropic.ClaudeMessagesResponse{
		ID:         "msg_test",
		Type:       "message",
		Role:       "assistant",
		Model:      "claude-3-5-sonnet-20241022",
		StopReason: "end_turn",
		Content: anthropic.MessageContentBlockArray{
			&anthropic.TextBlockParam{
				Type: "text",
				Text: text,
			},
		},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[anthropic.ClaudeMessagesResponse]{})
	body.SetParsed(resp)

	extractedText, err := adapter.ExtractTextFromBody(context.Background(), body)
	require.NoError(t, err)
	assert.Equal(t, "This is a Claude response.", string(extractedText))
}

func TestUniversalAdapter_ClaudeStreamWithRepetition(t *testing.T) {
	adapter := NewUniversalAdapter()

	// 模拟流式重复检测
	repeatedText := "重复"
	accumulatedText := ""

	for i := 0; i < 60; i++ {
		event := &anthropic.ClaudeMessagesStreamEvent{
			Type:  "content_block_delta",
			Index: intPtr(0),
			Delta: &anthropic.DeltaUnion{
				ContentBlockDelta: anthropic.ContentBlockDelta{
					Type: "text_delta",
					Text: &repeatedText,
				},
			},
		}

		body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[anthropic.ClaudeMessagesStreamEvent]{})
		body.SetParsed(event)

		text, err := adapter.ExtractTextFromBody(context.Background(), body)
		require.NoError(t, err)
		accumulatedText += string(text)
	}

	assert.Equal(t, strings.Repeat("重复", 60), accumulatedText)
}

// 辅助函数
func intPtr(i int) *int {
	return &i
}
