package ruleengine

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/internal/testhelper"
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
									Name: "get_current_weather",
									Arguments: `{
										"location": "San Francisco, CA",
										"unit": "fahrenheit"
									}`,
								},
							},
						},
					},
				},
			},
			expected: "8dca59b1-c2125b03",
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
