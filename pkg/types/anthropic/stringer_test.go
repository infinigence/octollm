package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustUnmarshalReq(t *testing.T, jsonStr string) ClaudeMessagesRequest {
	t.Helper()
	var req ClaudeMessagesRequest
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &req))
	return req
}

func TestClaudeMessagesRequest_String(t *testing.T) {
	const model = "claude-3-5-sonnet-20241022"

	tests := []struct {
		name     string
		json     string
		expected string
	}{
		{
			name: "string content",
			json: `{
				"model": "claude-3-5-sonnet-20241022",
				"max_tokens": 1024,
				"messages": [{"role": "user", "content": "Hello, Claude!"}]
			}`,
			// string content → byte length, matching OpenAI Message stringer pattern
			expected: `(ClaudeMessagesRequest) {
  Model: "claude-3-5-sonnet-20241022"
  MaxTokens: 1024
  Messages: len(1)
    (MessageParam) {Role: "user", Content: len(14), }
}`,
		},
		{
			name: "array content",
			json: `{
				"model": "claude-3-5-sonnet-20241022",
				"max_tokens": 1024,
				"messages": [{"role": "user", "content": [{"type": "text", "text": "Hello, Claude!"}]}]
			}`,
			// block array → bracket notation, matching OpenAI array content pattern
			expected: `(ClaudeMessagesRequest) {
  Model: "claude-3-5-sonnet-20241022"
  MaxTokens: 1024
  Messages: len(1)
    (MessageParam) {Role: "user", Content: [text(len=14), ], }
}`,
		},
		{
			name: "object content",
			json: `{
				"model": "claude-3-5-sonnet-20241022",
				"max_tokens": 1024,
				"messages": [{"role": "user", "content": {"type": "text", "text": "Hello, Claude!"}}]
			}`,
			expected: `(ClaudeMessagesRequest) {
  Model: "claude-3-5-sonnet-20241022"
  MaxTokens: 1024
  Messages: len(1)
    (MessageParam) {Role: "user", Content: [text(len=14), ], }
}`,
		},
		{
			name: "system string",
			json: `{
				"model": "claude-3-5-sonnet-20241022",
				"max_tokens": 1024,
				"system": "You are a helpful assistant",
				"messages": [{"role": "user", "content": "Hello, Claude!"}]
			}`,
			// "You are a helpful assistant" = 27 bytes
			expected: `(ClaudeMessagesRequest) {
  Model: "claude-3-5-sonnet-20241022"
  MaxTokens: 1024
  System: len(27)
  Messages: len(1)
    (MessageParam) {Role: "user", Content: len(14), }
}`,
		},
		{
			name: "system blocks",
			json: `{
				"model": "claude-3-5-sonnet-20241022",
				"max_tokens": 1024,
				"system": [
					{"type": "text", "text": "You are a helpful assistant"},
					{"type": "text", "text": "Be concise"}
				],
				"messages": [{"role": "user", "content": "Hello, Claude!"}]
			}`,
			expected: `(ClaudeMessagesRequest) {
  Model: "claude-3-5-sonnet-20241022"
  MaxTokens: 1024
  System: blocks(2)
  Messages: len(1)
    (MessageParam) {Role: "user", Content: len(14), }
}`,
		},
		{
			name: "tool result content",
			json: `{
				"model": "claude-3-5-sonnet-20241022",
				"max_tokens": 1024,
				"messages": [{
					"role": "user",
					"content": [
						{
							"tool_use_id": "call_4cba1ad8bc8e4ba2983278ac",
							"type": "tool_result",
							"content": [
								{"type": "text", "text": "API Error: 404"},
								{"type": "text", "text": "agentId: a168307"}
							]
						},
						{"type": "text", "text": "[Request interrupted by user]"},
						{"type": "text", "text": "帮我看看这个项目是在做什么"}
					]
				}]
			}`,
			// "[Request interrupted by user]" = 29 bytes
			// "帮我看看这个项目是在做什么" = 13 Chinese chars × 3 bytes = 39 bytes
			expected: `(ClaudeMessagesRequest) {
  Model: "claude-3-5-sonnet-20241022"
  MaxTokens: 1024
  Messages: len(1)
    (MessageParam) {Role: "user", Content: [tool_result(id=call_4cba1ad8bc8e4ba2983278ac,len=2), text(len=29), text(len=39), ], }
}`,
		},
	}

	_ = model
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := mustUnmarshalReq(t, tt.json)
			assert.Equal(t, tt.expected, req.String())
		})
	}
}
