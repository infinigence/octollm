package ruleengine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
)

func TestMessage5Hash_Features(t *testing.T) {
	type testCase struct {
		name     string
		req      *openai.ChatCompletionRequest
		expected string
	}

	testCases := []testCase{
		{
			name: "AB",
			req: &openai.ChatCompletionRequest{
				Model: "gpt-3.5-turbo",
				Messages: []*openai.Message{
					{
						Role:    "system",
						Content: openai.MessageContentString("系统消息"),
					},
					{
						Role:    "_input",
						Content: openai.MessageContentString("一23四五6七89十1二三45六7八九"),
					},
				},
			},
			expected: "8dca59b1-a89afb9a",
		},
		{
			name: "ABC",
			req: &openai.ChatCompletionRequest{
				Model: "gpt-3.5-turbo",
				Messages: []*openai.Message{
					{
						Role:    "system",
						Content: openai.MessageContentString("系统消息"),
					},
					{
						Role:    "_input",
						Content: openai.MessageContentString("一23四五6七89十1二三45六7八九"),
					},
					{
						Role:    "user",
						Content: openai.MessageContentString("下一个数字是多少？"),
					},
				},
			},
			expected: "8dca59b1-a89afb9a-5bf09c53",
		},
		{
			name: "ADC",
			req: &openai.ChatCompletionRequest{
				Model: "gpt-3.5-turbo",
				Messages: []*openai.Message{
					{
						Role:    "system",
						Content: openai.MessageContentString("系统消息"),
					},
					{
						Role:    "_input",
						Content: openai.MessageContentString("Some other resource inputs. Some other resource inputs. Some other resource inputs. Some other resource inputs. Some other resource inputs. Some other resource inputs."),
					},
					{
						Role:    "user",
						Content: openai.MessageContentString("下一个数字是多少？"),
					},
				},
			},
			expected: "8dca59b1-c5f9b440-569d3b59",
		},
		{
			name: "assistant_toolcall",
			req: &openai.ChatCompletionRequest{
				Model: "gpt-3.5-turbo",
				Messages: []*openai.Message{
					{
						Role:    "system",
						Content: openai.MessageContentString("系统消息"),
					},
					{
						Role: "assistant",
						ToolCalls: []*openai.ToolCall{
							{
								ID:    "call_123",
								Index: 0,
								Type:  "function",
								Function: &openai.ToolCallFunction{
									Name:      "get_current_weather",
									Arguments: `{"location":"San Francisco, CA","unit":"fahrenheit"}`,
								},
							},
						},
					},
				},
			},
			expected: "8dca59b1-c5049a1a",
		},
		{
			name: "empty-content",
			req: &openai.ChatCompletionRequest{
				Model: "gpt-3.5-turbo",
				Messages: []*openai.Message{
					{
						Role:    "system",
						Content: openai.MessageContentString("系统消息"),
					},
					{
						Role: "_input",
					},
					{
						Role:    "_input",
						Content: openai.MessageContentString("一23四五6七89十1二三45六7八九"),
					},
				},
			},
			expected: "8dca59b1-a89afb9a",
		},
		{
			name: "nil-message",
			req: &openai.ChatCompletionRequest{
				Model: "gpt-3.5-turbo",
				Messages: []*openai.Message{
					{
						Role:    "system",
						Content: openai.MessageContentString("系统消息"),
					},
					nil,
					{
						Role:    "_input",
						Content: openai.MessageContentString("一23四五6七89十1二三45六7八九"),
					},
				},
			},
			expected: "8dca59b1-a89afb9a",
		},
		{
			name: "image",
			req: &openai.ChatCompletionRequest{
				Model: "gpt-3.5-turbo",
				Messages: []*openai.Message{
					{
						Role:    "system",
						Content: openai.MessageContentString("系统消息"),
					},
					{
						Role: "user",
						Content: openai.MessageContentArray{
							{
								Type: "image_url",
								ImageURL: &openai.MessageContentItemImageURL{
									URL: "https://example.com/image.jpg",
								},
							},
						},
					},
					{
						Role:    "user",
						Content: openai.MessageContentString("一23四五6七89十1二三45六7八九"),
					},
				},
			},
			expected: "8dca59b1-3debb7ef-d362f574",
		},
		{
			name: "empty",
			req: &openai.ChatCompletionRequest{
				Model:    "gpt-3.5-turbo",
				Messages: []*openai.Message{},
			},
			expected: "",
		},
		{
			name: "more_than_5_msg",
			req: &openai.ChatCompletionRequest{
				Model: "gpt-3.5-turbo",
				Messages: []*openai.Message{
					{
						Role:    "system",
						Content: openai.MessageContentString("系统消息"),
					},
					{
						Role:    "_input",
						Content: openai.MessageContentString("一23四五6七89十1二三45六7八九"),
					},
					{
						Role:    "user",
						Content: openai.MessageContentString("下一个数字是多少？"),
					},
					{
						Role:    "assistant",
						Content: openai.MessageContentString("10"),
					},
					{
						Role:    "user",
						Content: openai.MessageContentString("下一个数字是多少？"),
					},
					{
						Role:    "assistant",
						Content: openai.MessageContentString("11"),
					},
				},
			},
			expected: "8dca59b1-a89afb9a-5bf09c53-54fb23c2-ad98232b",
		},
	}

	t.Run("string", func(t *testing.T) {
		extractor := &Message5HashExtractor{}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				req := testhelper.CreateTestRequest(testhelper.WithBody(tc.req))
				val, err := extractor.Features(req)
				require.NoError(t, err)
				str, _ := val.(string)
				assert.Equal(t, tc.expected, str, "message5Hash mismatch")
			})
		}
	})

	t.Run("array", func(t *testing.T) {
		extractor := &Message5HashArrayExtractor{}
		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				req := testhelper.CreateTestRequest(testhelper.WithBody(tc.req))
				val, err := extractor.Features(req)
				require.NoError(t, err)
				if tc.expected == "" {
					assert.Empty(t, val, "message5HashArray should be empty")
				} else {
					expectedArray := strings.Split(tc.expected, "-")
					assert.Equal(t, expectedArray, val, "message5HashArray mismatch")
				}
			})
		}
	})
}

// TestMessage5Hash_Features_Anthropic mirrors TestMessage5Hash_Features using the Anthropic
// messages format. Each case is the equivalent of its OpenAI counterpart:
//   - system field → treated as first message (converter prepend logic)
//   - assistant tool_use block → Input JSON used as hash text (converter tool-call logic)
//
// Expected hash values are identical to the OpenAI cases.
func TestMessage5Hash_Features_Anthropic(t *testing.T) {
	type testCase struct {
		name     string
		req      anthropic.ClaudeMessagesRequest
		expected string
	}

	testCases := []testCase{
		{
			name: "AB",
			req: anthropic.ClaudeMessagesRequest{
				Model:  "gpt-3.5-turbo",
				System: anthropic.SystemString("系统消息"),
				Messages: []*anthropic.MessageParam{
					{Role: "user", Content: []anthropic.MessageContent{anthropic.MessageContentString("一23四五6七89十1二三45六7八九")}},
				},
			},
			expected: "8dca59b1-a89afb9a",
		},
		{
			name: "ABC",
			req: anthropic.ClaudeMessagesRequest{
				Model:  "gpt-3.5-turbo",
				System: anthropic.SystemString("系统消息"),
				Messages: []*anthropic.MessageParam{
					{Role: "user", Content: []anthropic.MessageContent{anthropic.MessageContentString("一23四五6七89十1二三45六7八九")}},
					{Role: "user", Content: []anthropic.MessageContent{anthropic.MessageContentString("下一个数字是多少？")}},
				},
			},
			expected: "8dca59b1-a89afb9a-5bf09c53",
		},
		{
			name: "ADC",
			req: anthropic.ClaudeMessagesRequest{
				Model:  "gpt-3.5-turbo",
				System: anthropic.SystemString("系统消息"),
				Messages: []*anthropic.MessageParam{
					{Role: "user", Content: []anthropic.MessageContent{anthropic.MessageContentString("Some other resource inputs. Some other resource inputs. Some other resource inputs. Some other resource inputs. Some other resource inputs. Some other resource inputs.")}},
					{Role: "user", Content: []anthropic.MessageContent{anthropic.MessageContentString("下一个数字是多少？")}},
				},
			},
			expected: "8dca59b1-c5f9b440-569d3b59",
		},
		{
			// assistant message with tool_use block; Input JSON is used as hash text
			name: "assistant_toolcall",
			req: anthropic.ClaudeMessagesRequest{
				Model:  "gpt-3.5-turbo",
				System: anthropic.SystemString("系统消息"),
				Messages: []*anthropic.MessageParam{
					{
						Role: "assistant",
						Content: []anthropic.MessageContent{
							&anthropic.MessageContentBlock{
								Type: "tool_use",
								MessageContentToolUse: &anthropic.MessageContentToolUse{
									ID:    "call_123",
									Name:  "get_current_weather",
									Input: json.RawMessage(`{"location":"San Francisco, CA","unit":"fahrenheit"}`),
								},
							},
						},
					},
				},
			},
			expected: "8dca59b1-c5049a1a",
		},
		{
			// empty-content: message with empty string is skipped
			name: "empty-content",
			req: anthropic.ClaudeMessagesRequest{
				Model:  "gpt-3.5-turbo",
				System: anthropic.SystemString("系统消息"),
				Messages: []*anthropic.MessageParam{
					{Role: "user", Content: []anthropic.MessageContent{anthropic.MessageContentString("")}},
					{Role: "user", Content: []anthropic.MessageContent{anthropic.MessageContentString("一23四五6七89十1二三45六7八九")}},
				},
			},
			expected: "8dca59b1-a89afb9a",
		},
		{
			// nil-message: nil entry in slice is skipped
			name: "nil-message",
			req: anthropic.ClaudeMessagesRequest{
				Model:  "gpt-3.5-turbo",
				System: anthropic.SystemString("系统消息"),
				Messages: []*anthropic.MessageParam{
					nil,
					{Role: "user", Content: []anthropic.MessageContent{anthropic.MessageContentString("一23四五6七89十1二三45六7八九")}},
				},
			},
			expected: "8dca59b1-a89afb9a",
		},
		// {
		// 	// image block has no extractable text → skipped in hash
		// 	name: "image",
		// 	req: anthropic.ClaudeMessagesRequest{
		// 		Model:  "gpt-3.5-turbo",
		// 		System: anthropic.SystemString("系统消息"),
		// 		Messages: []*anthropic.MessageParam{
		// 			{
		// 				Role: "user",
		// 				Content: []anthropic.MessageContent{
		// 					&anthropic.MessageContentBlock{
		// 						Type:   "image",
		// 						Source: &anthropic.MessageContentSource{Type: "url", Url: "https://example.com/image.jpg"},
		// 					},
		// 				},
		// 			},
		// 			{Role: "user", Content: []anthropic.MessageContent{anthropic.MessageContentString("一23四五6七89十1二三45六7八九")}},
		// 		},
		// 	},
		// 	expected: "8dca59b1-a89afb9a",
		// },
		{
			name: "empty",
			req: anthropic.ClaudeMessagesRequest{
				Model:    "gpt-3.5-turbo",
				Messages: []*anthropic.MessageParam{},
			},
			expected: "",
		},
		{
			// 1 system + 5 messages = 6 entries, capped at 5
			name: "more_than_5_msg",
			req: anthropic.ClaudeMessagesRequest{
				Model:  "gpt-3.5-turbo",
				System: anthropic.SystemString("系统消息"),
				Messages: []*anthropic.MessageParam{
					{Role: "user", Content: []anthropic.MessageContent{anthropic.MessageContentString("一23四五6七89十1二三45六7八九")}},
					{Role: "user", Content: []anthropic.MessageContent{anthropic.MessageContentString("下一个数字是多少？")}},
					{Role: "assistant", Content: []anthropic.MessageContent{anthropic.MessageContentString("10")}},
					{Role: "user", Content: []anthropic.MessageContent{anthropic.MessageContentString("下一个数字是多少？")}},
					{Role: "assistant", Content: []anthropic.MessageContent{anthropic.MessageContentString("11")}},
				},
			},
			expected: "8dca59b1-a89afb9a-5bf09c53-54fb23c2-ad98232b",
		},
		{
			name: "ignore_anthropic_header",
			req: anthropic.ClaudeMessagesRequest{
				Model: "gpt-3.5-turbo",
				System: anthropic.SystemBlocks{
					{
						Type: "text",
						Text: "x-anthropic-billing-header: cc_version=2.1.117.e85; cc_entrypoint=cli; cch=00000;",
					},
					{
						Type: "text",
						Text: "You are Claude Code, Anthropic's official CLI for Claude.",
					},
					{
						Type: "text",
						Text: "Generate a concise, sentence-case title (3-7 words) that captures the main topic or goal of this coding session. The title should be clear enough that the user recognizes the session in a list. Use sentence case: capitalize only the first word and proper nouns.\n\nReturn JSON with a single \"title\" field.\n\nGood examples:\n{\"title\": \"Fix login button on mobile\"}\n{\"title\": \"Add OAuth authentication\"}\n{\"title\": \"Debug failing CI tests\"}\n{\"title\": \"Refactor API client error handling\"}\n\nBad (too vague): {\"title\": \"Code changes\"}\nBad (too long): {\"title\": \"Investigate and fix the issue where the login button does not respond on mobile devices\"}\nBad (wrong case): {\"title\": \"Fix Login Button On Mobile\"}",
					},
				},
				Messages: []*anthropic.MessageParam{
					{Role: "user", Content: []anthropic.MessageContent{anthropic.MessageContentString("hello")}},
				},
			},
			expected: "d6a59d1f-5f5cd586-29790936",
		},
	}

	t.Run("string", func(t *testing.T) {
		extractor := &Message5HashExtractor{}
		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				req := testhelper.CreateTestRequest(testhelper.WithBody(tc.req))
				val, err := extractor.Features(req)
				require.NoError(t, err)
				str, _ := val.(string)
				assert.Equal(t, tc.expected, str, "message5Hash mismatch")
			})
		}
	})

	t.Run("array", func(t *testing.T) {
		extractor := &Message5HashArrayExtractor{}
		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				req := testhelper.CreateTestRequest(testhelper.WithBody(tc.req))
				val, err := extractor.Features(req)
				require.NoError(t, err)
				if tc.expected == "" {
					assert.Empty(t, val, "message5HashArray should be empty")
				} else {
					expectedArray := strings.Split(tc.expected, "-")
					assert.Equal(t, expectedArray, val, "message5HashArray mismatch")
				}
			})
		}
	})
}
