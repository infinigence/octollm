package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }
func int64Ptr(i int64) *int64 { return &i }
func boolPtr(b bool) *bool    { return &b }

func TestClaudeMessagesRequest_Marshal_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name          string
		JSON          string
		Object        ClaudeMessagesRequest
		UnmarshalOnly bool
	}{
		{
			Name: "StringContent",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":"Hello, Claude!"}]}`,
			Object: ClaudeMessagesRequest{
				Model:     "claude-3-5-sonnet-20241022",
				MaxTokens: 1024,
				Messages: []*MessageParam{
					{Role: "user", Content: MessageContentString("Hello, Claude!")},
				},
			},
		},
		{
			Name: "ArrayContent",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"text","text":"Hello, Claude!"}]}]}`,
			Object: ClaudeMessagesRequest{
				Model:     "claude-3-5-sonnet-20241022",
				MaxTokens: 1024,
				Messages: []*MessageParam{
					{Role: "user", Content: MessageContentBlockArray{&TextBlockParam{Type: "text", Text: "Hello, Claude!"}}},
				},
			},
		},
		{
			Name: "ObjectContent",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":{"type":"text","text":"Hello, Claude!"}}]}`,
			Object: ClaudeMessagesRequest{
				Model:     "claude-3-5-sonnet-20241022",
				MaxTokens: 1024,
				Messages: []*MessageParam{
					{Role: "user", Content: MessageContentBlockArray{&TextBlockParam{Type: "text", Text: "Hello, Claude!"}}},
				},
			},
			UnmarshalOnly: true, // single block object unmarshals to array, but array marshals as array
		},
		{
			Name: "SystemString",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"system":"You are a helpful assistant","messages":[{"role":"user","content":"Hello, Claude!"}]}`,
			Object: ClaudeMessagesRequest{
				Model:     "claude-3-5-sonnet-20241022",
				MaxTokens: 1024,
				System:    SystemString("You are a helpful assistant"),
				Messages: []*MessageParam{
					{Role: "user", Content: MessageContentString("Hello, Claude!")},
				},
			},
		},
		{
			Name: "SystemBlocks",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"system":[{"type":"text","text":"You are a helpful assistant"},{"type":"text","text":"Be concise"}],"messages":[{"role":"user","content":"Hello, Claude!"}]}`,
			Object: ClaudeMessagesRequest{
				Model:     "claude-3-5-sonnet-20241022",
				MaxTokens: 1024,
				System: SystemBlocks{
					{Type: "text", Text: "You are a helpful assistant"},
					{Type: "text", Text: "Be concise"},
				},
				Messages: []*MessageParam{
					{Role: "user", Content: MessageContentString("Hello, Claude!")},
				},
			},
		},
		{
			Name: "ToolResult",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":[{"tool_use_id":"call_4cba1ad8bc8e4ba2983278ac","type":"tool_result","content":[{"type":"text","text":"API Error: 404"},{"type":"text","text":"agentId: a168307"}]},{"type":"text","text":"[Request interrupted by user]"},{"type":"text","text":"帮我看看这个项目是在做什么","cache_control":{"type":"ephemeral"}}]}]}`,
			Object: ClaudeMessagesRequest{
				Model:     "claude-3-5-sonnet-20241022",
				MaxTokens: 1024,
				Messages: []*MessageParam{
					{
						Role: "user",
						Content: MessageContentBlockArray{
							&ToolResultBlockParam{
								Type:      "tool_result",
								ToolUseID: "call_4cba1ad8bc8e4ba2983278ac",
								Content: MessageContentBlockArray{
									&TextBlockParam{Type: "text", Text: "API Error: 404"},
									&TextBlockParam{Type: "text", Text: "agentId: a168307"},
								},
							},
							&TextBlockParam{Type: "text", Text: "[Request interrupted by user]"},
							&TextBlockParam{
								Type:         "text",
								Text:         "帮我看看这个项目是在做什么",
								CacheControl: &CacheControl{Type: "ephemeral"},
							},
						},
					},
				},
			},
		},
		{
			Name:          "TopLevelNull",
			JSON:          `null`,
			Object:        ClaudeMessagesRequest{},
			UnmarshalOnly: true,
		},
	}

	for _, tc := range testCases {
		if !tc.UnmarshalOnly {
			t.Run("Marshal_"+tc.Name, func(t *testing.T) {
				data, err := json.Marshal(tc.Object)
				require.NoError(t, err)
				assert.JSONEq(t, tc.JSON, string(data))
			})
		}
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			var req ClaudeMessagesRequest
			err := json.Unmarshal([]byte(tc.JSON), &req)
			require.NoError(t, err)
			assert.Equal(t, tc.Object, req)
		})
	}
}

func TestMessageParam_UnmarshalJSON_NullContent(t *testing.T) {
	var msg MessageParam
	err := json.Unmarshal([]byte(`null`), &msg)
	require.ErrorContains(t, err, "content field cannot be null or empty")
}

func TestMessageContent_Marshal_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name          string
		JSON          string
		Object        MessageContent
		UnmarshalOnly bool
	}{
		{
			Name:   "String",
			JSON:   `"hello"`,
			Object: MessageContentString("hello"),
		},
		{
			Name:   "BlockArray",
			JSON:   `[{"type":"text","text":"Hello!"}]`,
			Object: MessageContentBlockArray{&TextBlockParam{Type: "text", Text: "Hello!"}},
		},
		{
			Name:          "SingleBlockObject",
			JSON:          `{"type":"text","text":"Hello!"}`,
			Object:        MessageContentBlockArray{&TextBlockParam{Type: "text", Text: "Hello!"}},
			UnmarshalOnly: true, // single block object unmarshals to array, but array marshals as array
		},
		{
			Name: "ImageBlockBase64",
			JSON: `[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]`,
			Object: MessageContentBlockArray{&ImageBlockParam{
				Type:   "image",
				Source: &MessageContentSource{Type: "base64", MediaType: "image/png", Data: json.RawMessage(`"iVBORw0KGgo="`)},
			}},
		},
		{
			Name: "ImageBlockURL",
			JSON: `[{"type":"image","source":{"type":"url","url":"https://example.com/img.png"}}]`,
			Object: MessageContentBlockArray{&ImageBlockParam{
				Type:   "image",
				Source: &MessageContentSource{Type: "url", Url: "https://example.com/img.png"},
			}},
		},
		{
			Name: "DocumentBlock",
			JSON: `[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="},"title":"doc.pdf","context":"A document","citations":{"enabled":true}}]`,
			Object: MessageContentBlockArray{&DocumentBlockParam{
				Type:      "document",
				Source:    &MessageContentSource{Type: "base64", MediaType: "application/pdf", Data: json.RawMessage(`"JVBERi0="`)},
				Title:     "doc.pdf",
				Context:   "A document",
				Citations: &DocumentCitations{Enabled: true},
			}},
		},
		{
			Name: "ThinkingBlock",
			JSON: `[{"type":"thinking","thinking":"Let me think...","signature":"sig_abc"}]`,
			Object: MessageContentBlockArray{&ThinkingBlockParam{
				Type:      "thinking",
				Thinking:  "Let me think...",
				Signature: "sig_abc",
			}},
		},
		{
			Name: "RedactedThinkingBlock",
			JSON: `[{"type":"redacted_thinking","data":"base64data"}]`,
			Object: MessageContentBlockArray{&RedactedThinkingBlockParam{
				Type: "redacted_thinking",
				Data: "base64data",
			}},
		},
		{
			Name: "ToolUseBlock",
			JSON: `[{"type":"tool_use","id":"toolu_123","name":"get_weather","input":{"location":"SF"}}]`,
			Object: MessageContentBlockArray{&ToolUseBlockParam{
				Type:  "tool_use",
				ID:    "toolu_123",
				Name:  "get_weather",
				Input: json.RawMessage(`{"location":"SF"}`),
			}},
		},
		{
			Name: "ToolUseBlockEmptyInput",
			JSON: `[{"type":"tool_use","id":"toolu_456","name":"no_args","input":{}}]`,
			Object: MessageContentBlockArray{&ToolUseBlockParam{
				Type:  "tool_use",
				ID:    "toolu_456",
				Name:  "no_args",
				Input: json.RawMessage(`{}`),
			}},
		},
		{
			Name: "ToolResultBlockStringContent",
			JSON: `[{"type":"tool_result","tool_use_id":"toolu_123","content":"15 degrees"}]`,
			Object: MessageContentBlockArray{&ToolResultBlockParam{
				Type:      "tool_result",
				ToolUseID: "toolu_123",
				Content:   MessageContentString("15 degrees"),
			}},
		},
		{
			Name: "ToolResultBlockArrayContent",
			JSON: `[{"type":"tool_result","tool_use_id":"toolu_123","content":[{"type":"text","text":"15 degrees"}]}]`,
			Object: MessageContentBlockArray{&ToolResultBlockParam{
				Type:      "tool_result",
				ToolUseID: "toolu_123",
				Content:   MessageContentBlockArray{&TextBlockParam{Type: "text", Text: "15 degrees"}},
			}},
		},
		{
			Name: "ToolResultBlockNoContent",
			JSON: `[{"type":"tool_result","tool_use_id":"toolu_123"}]`,
			Object: MessageContentBlockArray{&ToolResultBlockParam{
				Type:      "tool_result",
				ToolUseID: "toolu_123",
			}},
		},
		{
			Name: "ToolResultBlockIsError",
			JSON: `[{"type":"tool_result","tool_use_id":"toolu_123","content":"error occurred","is_error":true}]`,
			Object: MessageContentBlockArray{&ToolResultBlockParam{
				Type:      "tool_result",
				ToolUseID: "toolu_123",
				Content:   MessageContentString("error occurred"),
				IsError:   boolPtr(true),
			}},
		},
		{
			Name: "GeneralBlockUnknownType",
			JSON: `[{"type":"custom_block"}]`,
			Object: MessageContentBlockArray{&GeneralBlockParam{
				Type: "custom_block",
			}},
		},
		{
			Name:   "Null",
			JSON:   `null`,
			Object: nil,
		},
	}

	for _, tc := range testCases {
		if !tc.UnmarshalOnly {
			t.Run("Marshal_"+tc.Name, func(t *testing.T) {
				data, err := json.Marshal(tc.Object)
				require.NoError(t, err)
				assert.JSONEq(t, tc.JSON, string(data))
			})
		}
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			var f messageContentField
			err := json.Unmarshal([]byte(tc.JSON), &f)
			require.NoError(t, err)
			assert.Equal(t, tc.Object, f.Value)
		})
	}
}

func TestMessageContent_ExtractText(t *testing.T) {
	testCases := []struct {
		Name         string
		JSON         string
		ExpectedText string
	}{
		{
			Name:         "String",
			JSON:         `"hello"`,
			ExpectedText: "hello",
		},
		{
			Name:         "BlockArray",
			JSON:         `[{"type":"text","text":"Hello!"}]`,
			ExpectedText: "Hello!",
		},
		{
			Name:         "SingleBlockObject",
			JSON:         `{"type":"text","text":"Hello!"}`,
			ExpectedText: "Hello!",
		},
		{
			Name:         "ImageBlockBase64",
			JSON:         `[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]`,
			ExpectedText: "(base64_inline_image/png)",
		},
		{
			Name:         "ImageBlockURL",
			JSON:         `[{"type":"image","source":{"type":"url","url":"https://example.com/img.png"}}]`,
			ExpectedText: "https://example.com/img.png",
		},
		{
			Name:         "DocumentBlock",
			JSON:         `[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="},"title":"doc.pdf","context":"A document","citations":{"enabled":true}}]`,
			ExpectedText: "doc.pdf:A document:(base64_inline_application/pdf)",
		},
		{
			Name:         "ThinkingBlock",
			JSON:         `[{"type":"thinking","thinking":"Let me think...","signature":"sig_abc"}]`,
			ExpectedText: "Let me think...",
		},
		{
			Name:         "RedactedThinkingBlock",
			JSON:         `[{"type":"redacted_thinking","data":"base64data"}]`,
			ExpectedText: "",
		},
		{
			Name:         "ToolUseBlock",
			JSON:         `[{"type":"tool_use","id":"toolu_123","name":"get_weather","input":{"location":"SF"}}]`,
			ExpectedText: `{"location":"SF"}`,
		},
		{
			Name:         "ToolUseBlockEmptyInput",
			JSON:         `[{"type":"tool_use","id":"toolu_456","name":"no_args"}]`,
			ExpectedText: "",
		},
		{
			Name:         "ToolResultBlockStringContent",
			JSON:         `[{"type":"tool_result","tool_use_id":"toolu_123","content":"15 degrees"}]`,
			ExpectedText: "15 degrees",
		},
		{
			Name:         "ToolResultBlockArrayContent",
			JSON:         `[{"type":"tool_result","tool_use_id":"toolu_123","content":[{"type":"text","text":"15 degrees"}]}]`,
			ExpectedText: "15 degrees",
		},
		{
			Name:         "ToolResultBlockNoContent",
			JSON:         `[{"type":"tool_result","tool_use_id":"toolu_123"}]`,
			ExpectedText: "",
		},
		{
			Name:         "ToolResultBlockIsError",
			JSON:         `[{"type":"tool_result","tool_use_id":"toolu_123","content":"error occurred","is_error":true}]`,
			ExpectedText: "error occurred",
		},
		{
			Name:         "GeneralBlockUnknownType",
			JSON:         `[{"type":"custom_block"}]`,
			ExpectedText: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			var f messageContentField
			err := json.Unmarshal([]byte(tc.JSON), &f)
			require.NoError(t, err)
			assert.Equal(t, tc.ExpectedText, f.Value.ExtractText())
		})
	}
}

func TestSystemValue_Marshal_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name   string
		JSON   string
		Object SystemContent
	}{
		{
			Name:   "String",
			JSON:   `"You are a helpful assistant"`,
			Object: SystemString("You are a helpful assistant"),
		},
		{
			Name: "Blocks",
			JSON: `[{"type":"text","text":"You are a helpful assistant"},{"type":"text","text":"Be concise"}]`,
			Object: SystemBlocks{
				{Type: "text", Text: "You are a helpful assistant"},
				{Type: "text", Text: "Be concise"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			var sf systemField
			err := json.Unmarshal([]byte(tc.JSON), &sf)
			require.NoError(t, err)
			assert.Equal(t, tc.Object, sf.Value)
		})
		t.Run("Marshal_"+tc.Name, func(t *testing.T) {
			data, err := json.Marshal(tc.Object)
			require.NoError(t, err)
			assert.JSONEq(t, tc.JSON, string(data))
		})
	}
}
