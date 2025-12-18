package converter

import (
	"context"
	"net/http"
	"testing"

	openaiSDK "github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/anthropic"
)

// mockEngine is a simple mock that verifies the request and returns a fixed response
type mockEngine struct {
	t *testing.T

	// Expectations
	expectedRequestCheck func(t *testing.T, req *octollm.Request)

	// Response to return
	responseToReturn *octollm.Response
	errorToReturn    error
}

func newMockEngine(t *testing.T) *mockEngine {
	return &mockEngine{
		t: t,
	}
}

func (m *mockEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	if m.expectedRequestCheck != nil {
		m.expectedRequestCheck(m.t, req)
	}
	if m.errorToReturn != nil {
		return nil, m.errorToReturn
	}
	return m.responseToReturn, nil
}

func testChatCompletionsToClaudeMessages_NonStream(t *testing.T, claudeReqJSON, expectedOpenaiReqJSON, openaiRespJSON, expectedClaudeRespJSON string) {
	ctx := context.Background()

	// Create request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", "http://localhost/v1/messages", nil)
	require.NoError(t, err)

	req := octollm.NewRequest(httpReq, octollm.APIFormatClaudeMessages)
	req.Body = octollm.NewBodyFromBytes(
		[]byte(claudeReqJSON),
		&octollm.JSONParser[anthropic.MessageNewParams]{},
	)

	// Create mock engine with verification
	mockEng := newMockEngine(t)
	mockEng.expectedRequestCheck = func(t *testing.T, req *octollm.Request) {
		// Verify format was changed
		assert.Equal(t, octollm.APIFormatChatCompletions, req.Format)

		// Parse the converted request
		bytes, err := req.Body.Bytes()
		require.NoError(t, err)

		assert.JSONEq(t, expectedOpenaiReqJSON, string(bytes))
	}

	mockEng.responseToReturn = &octollm.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body: octollm.NewBodyFromBytes(
			[]byte(openaiRespJSON),
			&octollm.JSONParser[openaiSDK.ChatCompletion]{},
		),
	}

	// Create converter and process
	converter := NewChatCompletionsToClaudeMessages(mockEng)
	resp, err := converter.Process(req)

	// Verify no error
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify response was converted back to Claude format
	respBytes, err := resp.Body.Bytes()
	require.NoError(t, err)

	assert.JSONEq(t, expectedClaudeRespJSON, string(respBytes))
}

func TestChatCompletionsToClaudeMessages_NonStream_SimpleText(t *testing.T) {
	claudeReqJSON := `{
		"model": "claude-3-5-haiku",
		"max_tokens": 1024,
		"messages": [
			{
				"role": "user",
				"content": "Hello, how are you?"
			}
		]
	}`

	expectedOpenaiReqJSON := `{
		"model": "claude-3-5-haiku",
		"max_tokens": 1024,
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "text",
						"text": "Hello, how are you?"
					}
				]
			}
		]
	}`

	openaiRespJSON := `{
		"id": "chatcmpl-123",
		"object": "chat.completion",
		"created": 1677652288,
		"model": "gpt-4",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "I'm doing well, thank you!"
			},
			"finish_reason": "stop"
		}],
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 8,
			"total_tokens": 18
		}
	}`

	expectedClaudeRespJSON := `{
		"id": "chatcmpl-123",
		"type": "message",
		"role": "assistant",
		"model": "gpt-4",
		"content": [
			{
				"type": "text",
				"text": "I'm doing well, thank you!"
			}
		],
		"stop_reason": "end_turn",
		"usage": {
			"input_tokens": 10,
			"output_tokens": 8
		}
	}`

	testChatCompletionsToClaudeMessages_NonStream(t, claudeReqJSON, expectedOpenaiReqJSON, openaiRespJSON, expectedClaudeRespJSON)
}

func testChatCompletionsToClaudeMessages_convertRequestBody(t *testing.T, claudeReqJSON, expectedOpenaiReqJSON string) {
	ctx := context.Background()
	converter := NewChatCompletionsToClaudeMessages(nil)

	srcBody := octollm.NewBodyFromBytes(
		[]byte(claudeReqJSON),
		&octollm.JSONParser[anthropic.MessageNewParams]{},
	)

	dstBody, err := converter.convertRequestBody(ctx, srcBody)
	require.NoError(t, err)

	// Parse the converted request
	bytes, err := dstBody.Bytes()
	require.NoError(t, err)

	if !assert.JSONEq(t, expectedOpenaiReqJSON, string(bytes)) {
		t.Logf("Got: %s", string(bytes))
	}
}

func TestChatCompletionsToClaudeMessages_convertRequestBody_SimpleTextWithMeta(t *testing.T) {
	claudeReqJSON := `{
		"model": "claude-3-5-haiku",
		"max_tokens": 1024,
		"stream": true,
		"temperature": 0.5,
		"top_p": 0.9,
		"messages": [
			{
				"role": "user",
				"content": "Hello, how are you?"
			}
		]
	}`

	expectedOpenaiReqJSON := `{
		"model": "claude-3-5-haiku",
		"max_tokens": 1024,
		"stream": true,
		"temperature": 0.5,
		"top_p": 0.9,
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "text",
						"text": "Hello, how are you?"
					}
				]
			}
		]
	}`

	testChatCompletionsToClaudeMessages_convertRequestBody(t, claudeReqJSON, expectedOpenaiReqJSON)
}

func TestChatCompletionsToClaudeMessages_convertRequestBody_MultipleText(t *testing.T) {
	claudeReqJSON := `{
		"model": "claude-3-5-haiku",
		"max_tokens": 1024,
		"system": [
			{
				"type": "text",
				"text": "You are a helpful assistant."
			}
		],
		"messages": [
			{
				"role": "user",
				"content": "Hello, how are you?"
			},
			{
				"role": "assistant",
				"content": "I'm doing well, thank you!"
			},
			{
				"role": "user",
				"content": [
					{
						"type": "text",
						"text": "What's in the image?"
					},
					{
						"type": "image",
						"source": {
							"type": "base64",
							"media_type": "image/png",
							"data": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+P+/HgAFhAJ/wlseKgAAAABJRU5ErkJggg=="
						}
					},
					{
						"type": "image",
						"source": {
							"type": "url",
							"url": "https://example.com/image.png"
						}
					}
				]
			}
		]
	}`

	expectedOpenaiReqJSON := `{
		"model": "claude-3-5-haiku",
		"max_tokens": 1024,
		"messages": [
			{
				"role": "system",
				"content": "You are a helpful assistant."
			},
			{
				"role": "user",
				"content": [
					{
						"type": "text",
						"text": "Hello, how are you?"
					}
				]
			},
			{
				"role": "assistant",
				"content": [
					{
						"type": "text",
						"text": "I'm doing well, thank you!"
					}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "text",
						"text": "What's in the image?"
					},
					{
						"type": "image_url",
						"image_url": {
							"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+P+/HgAFhAJ/wlseKgAAAABJRU5ErkJggg=="
						}
					},
					{
						"type": "image_url",
						"image_url": {
							"url": "https://example.com/image.png"
						}
					}
				]
			}
		]
	}`

	testChatCompletionsToClaudeMessages_convertRequestBody(t, claudeReqJSON, expectedOpenaiReqJSON)
}

func TestChatCompletionsToClaudeMessages_convertRequestBody_ToolCall(t *testing.T) {
	claudeReqJSON := `{
		"model": "claude-3-5-haiku",
		"max_tokens": 1024,
		"messages": [
			{
				"role": "user",
				"content": "Hello, how are you?"
			}
		],
		"tools": [
			{
    			"name": "get_stock_price",
    			"description": "Get the current stock price for a given ticker symbol.",
    			"input_schema": {
      				"type": "object",
      				"properties": {
        				"ticker": {
          					"type": "string",
          					"description": "The stock ticker symbol, e.g. AAPL for Apple Inc."
        				}
      				},
      				"required": ["ticker"]
    			}
  			}
		]
	}`

	expectedOpenaiReqJSON := `{
		"model": "claude-3-5-haiku",
		"max_tokens": 1024,
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "text",
						"text": "Hello, how are you?"
					}
				]
			}
		],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "get_stock_price",
					"description": "Get the current stock price for a given ticker symbol.",
					"parameters": {
						"type": "object",
						"properties": {
							"ticker": {
								"type": "string",
								"description": "The stock ticker symbol, e.g. AAPL for Apple Inc."
							}
						},
						"required": ["ticker"]
					}
				}
			}
		]
	}`

	testChatCompletionsToClaudeMessages_convertRequestBody(t, claudeReqJSON, expectedOpenaiReqJSON)
}

func TestChatCompletionsToClaudeMessages_convertRequestBody_ToolResult(t *testing.T) {
	claudeReqJSON := `{
		"model": "claude-sonnet-4-5",
		"max_tokens": 1024,
		"tools": [
			{
				"name": "get_weather",
				"description": "Get the current weather in a given location",
				"input_schema": {
					"type": "object",
					"properties": {
						"location": {
							"type": "string",
							"description": "The city and state, e.g. San Francisco, CA"
						},
						"unit": {
							"type": "string",
							"enum": ["celsius", "fahrenheit"],
							"description": "The unit of temperature, either \"celsius\" or \"fahrenheit\""
						}
					},
					"required": ["location"]
				}
			}
		],
		"messages": [
			{
				"role": "user",
				"content": "What is the weather like in San Francisco?"
			},
			{
				"role": "assistant",
				"content": [
					{
						"type": "text",
						"text": "I'll check the current weather in San Francisco for you."
					},
					{
						"type": "tool_use",
						"id": "toolu_01A09q90qw90lq917835lq9",
						"name": "get_weather",
						"input": {"location": "San Francisco, CA", "unit": "celsius"}
					}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "toolu_01A09q90qw90lq917835lq9",
						"content": "15 degrees"
					}
				]
			}
		]
	}`

	expectedOpenaiReqJSON := `{
		"model": "claude-sonnet-4-5",
		"max_tokens": 1024,
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "text",
						"text": "What is the weather like in San Francisco?"
					}
				]
			},
			{
				"role": "assistant",
				"content": [
					{
						"type": "text",
						"text": "I'll check the current weather in San Francisco for you."
					}
				],
				"tool_calls": [
					{
						"id": "toolu_01A09q90qw90lq917835lq9",
						"type": "function",
						"function": {
							"name": "get_weather",
							"arguments": "{\"location\":\"San Francisco, CA\",\"unit\":\"celsius\"}"
						}
					}
				]
			},
			{
				"role": "tool",
				"content": [
					{
						"type": "text",
						"text": "15 degrees"
					}
				],
				"tool_call_id": "toolu_01A09q90qw90lq917835lq9"
			}
		],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "get_weather",
					"description": "Get the current weather in a given location",
					"parameters": {
						"type": "object",
						"properties": {
							"location": {
								"type": "string",
								"description": "The city and state, e.g. San Francisco, CA"
							},
							"unit": {
								"type": "string",
								"enum": ["celsius", "fahrenheit"],
								"description": "The unit of temperature, either \"celsius\" or \"fahrenheit\""
							}
						},
						"required": ["location"]
					}
				}
			}
		]
	}`

	testChatCompletionsToClaudeMessages_convertRequestBody(t, claudeReqJSON, expectedOpenaiReqJSON)
}

func testChatCompletionsToClaudeMessages_convertNonStreamResponseBody(t *testing.T, openaiRespJSON, expectedClaudeRespJSON string) {
	ctx := context.Background()
	converter := NewChatCompletionsToClaudeMessages(nil)

	srcBody := octollm.NewBodyFromBytes(
		[]byte(openaiRespJSON),
		&octollm.JSONParser[openaiSDK.ChatCompletion]{},
	)

	dstBody, err := converter.convertNonStreamResponseBody(ctx, srcBody)
	require.NoError(t, err)

	// Parse the converted request
	bytes, err := dstBody.Bytes()
	require.NoError(t, err)

	if !assert.JSONEq(t, expectedClaudeRespJSON, string(bytes)) {
		t.Logf("Got: %s", string(bytes))
	}
}

func TestChatCompletionsToClaudeMessages_convertNonStreamResponseBody_SimpleTextFinishLength(t *testing.T) {

	openaiRespJSON := `{
		"id": "chatcmpl-123",
		"object": "chat.completion",
		"created": 1677652288,
		"model": "gpt-4",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "I'm doing well, thank you!"
			},
			"finish_reason": "length"
		}],
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 8,
			"total_tokens": 18
		}
	}`

	expectedClaudeRespJSON := `{
		"id": "chatcmpl-123",
		"type": "message",
		"role": "assistant",
		"model": "gpt-4",
		"content": [
			{
				"type": "text",
				"text": "I'm doing well, thank you!"
			}
		],
		"stop_reason": "max_tokens",
		"usage": {
			"input_tokens": 10,
			"output_tokens": 8
		}
	}`

	testChatCompletionsToClaudeMessages_convertNonStreamResponseBody(t, openaiRespJSON, expectedClaudeRespJSON)
}

func TestChatCompletionsToClaudeMessages_convertNonStreamResponseBody_ToolCallWithoutContent(t *testing.T) {
	openaiRespJSON := `{
		"id": "chatcmpl-abc123",
		"object": "chat.completion",
		"created": 1699896916,
		"model": "gpt-4o-mini",
		"choices": [
			{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": null,
				"tool_calls": [
					{
						"id": "call_abc123",
						"type": "function",
						"function": {
							"name": "get_current_weather",
							"arguments": "{\n\"location\": \"Boston, MA\"\n}"
						}
					}
				]
			},
			"logprobs": null,
			"finish_reason": "tool_calls"
			}
		],
		"usage": {
			"prompt_tokens": 82,
			"completion_tokens": 17,
			"total_tokens": 99,
			"completion_tokens_details": {
			"reasoning_tokens": 0,
			"accepted_prediction_tokens": 0,
			"rejected_prediction_tokens": 0
			}
		}
	}`

	expectedClaudeRespJSON := `{
		"id": "chatcmpl-abc123",
		"type": "message",
		"role": "assistant",
		"model": "gpt-4o-mini",
		"content": [
			{
				"type": "tool_use",
				"id": "call_abc123",
				"name": "get_current_weather",
				"input": {"location": "Boston, MA"}
			}
		],
		"stop_reason": "tool_use",
		"usage": {
			"input_tokens": 82,
			"output_tokens": 17
		}
	}`

	testChatCompletionsToClaudeMessages_convertNonStreamResponseBody(t, openaiRespJSON, expectedClaudeRespJSON)
}

func TestChatCompletionsToClaudeMessages_convertNonStreamResponseBody_ToolCallWithContent(t *testing.T) {
	openaiRespJSON := `{
		"id": "chatcmpl-abc123",
		"object": "chat.completion",
		"created": 1699896916,
		"model": "gpt-4o-mini",
		"choices": [
			{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "Hello, how are you?",
				"tool_calls": [
					{
						"id": "call_abc123",
						"type": "function",
						"function": {
							"name": "get_current_weather",
							"arguments": "{\n\"location\": \"Boston, MA\"\n}"
						}
					}
				]
			},
			"logprobs": null,
			"finish_reason": "tool_calls"
			}
		],
		"usage": {
			"prompt_tokens": 82,
			"completion_tokens": 17,
			"total_tokens": 99,
			"completion_tokens_details": {
			"reasoning_tokens": 0,
			"accepted_prediction_tokens": 0,
			"rejected_prediction_tokens": 0
			}
		}
	}`

	expectedClaudeRespJSON := `{
		"id": "chatcmpl-abc123",
		"type": "message",
		"role": "assistant",
		"model": "gpt-4o-mini",
		"content": [
			{
				"type": "text",
				"text": "Hello, how are you?"
			},
			{
				"type": "tool_use",
				"id": "call_abc123",
				"name": "get_current_weather",
				"input": {"location": "Boston, MA"}
			}
		],
		"stop_reason": "tool_use",
		"usage": {
			"input_tokens": 82,
			"output_tokens": 17
		}
	}`

	testChatCompletionsToClaudeMessages_convertNonStreamResponseBody(t, openaiRespJSON, expectedClaudeRespJSON)
}

func TestChatCompletionsToClaudeMessages_convertNonStreamResponseBody_MultipleToolCallWithContent(t *testing.T) {
	openaiRespJSON := `{
		"id": "chatcmpl-abc123",
		"object": "chat.completion",
		"created": 1699896916,
		"model": "gpt-4o-mini",
		"choices": [
			{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "Hello, how are you?",
				"tool_calls": [
					{
						"id": "call_abc123",
						"type": "function",
						"function": {
							"name": "get_current_weather",
							"arguments": "{\n\"location\": \"Boston, MA\"\n}"
						}
					},
					{
						"id": "call_abc124",
						"type": "function",
						"function": {
							"name": "get_stock_price",
							"arguments": "{\n\"ticker\": \"AAPL\"\n}"
						}
					}
				]
			},
			"logprobs": null,
			"finish_reason": "tool_calls"
			}
		],
		"usage": {
			"prompt_tokens": 82,
			"completion_tokens": 17,
			"total_tokens": 99,
			"completion_tokens_details": {
			"reasoning_tokens": 0,
			"accepted_prediction_tokens": 0,
			"rejected_prediction_tokens": 0
			}
		}
	}`

	expectedClaudeRespJSON := `{
		"id": "chatcmpl-abc123",
		"type": "message",
		"role": "assistant",
		"model": "gpt-4o-mini",
		"content": [
			{
				"type": "text",
				"text": "Hello, how are you?"
			},
			{
				"type": "tool_use",
				"id": "call_abc123",
				"name": "get_current_weather",
				"input": {"location": "Boston, MA"}
			},
			{
				"type": "tool_use",
				"id": "call_abc124",
				"name": "get_stock_price",
				"input": {"ticker": "AAPL"}
			}
		],
		"stop_reason": "tool_use",
		"usage": {
			"input_tokens": 82,
			"output_tokens": 17
		}
	}`

	testChatCompletionsToClaudeMessages_convertNonStreamResponseBody(t, openaiRespJSON, expectedClaudeRespJSON)
}

func testChatCompletionsToClaudeMessages_convertStreamResponse(t *testing.T, openaiRespJSON, expectedClaudeRespJSON []string) {
	ctx := context.Background()
	converter := NewChatCompletionsToClaudeMessages(nil)

	inCh := make(chan *octollm.StreamChunk)
	closed := false
	closeFunc := func() { closed = true }

	go func() {
		defer close(inCh)
		for _, chunk := range openaiRespJSON {
			body := octollm.NewBodyFromBytes([]byte(chunk), &octollm.JSONParser[openaiSDK.ChatCompletionChunk]{})
			inCh <- &octollm.StreamChunk{Body: body}
		}
	}()
	inStream := octollm.NewStreamChan(inCh, closeFunc)

	dstStream, err := converter.convertStreamResponse(ctx, inStream)
	require.NoError(t, err)

	i := 0
	for dstChunk := range dstStream.Chan() {
		bodyBytes, err := dstChunk.Body.Bytes()
		require.NoError(t, err)

		// check event type, anthropic msg body always have type field
		typeStr := gjson.GetBytes(bodyBytes, "type").String()
		require.Equal(t, typeStr, dstChunk.Metadata["event"])

		require.JSONEq(t, string(bodyBytes), expectedClaudeRespJSON[i])
		i++
	}
	dstStream.Close()

	require.Equal(t, i, len(expectedClaudeRespJSON))
	require.True(t, closed)
}

func TestChatCompletionsToClaudeMessages_convertStreamResponse_SimpleText(t *testing.T) {
	openaiRespJSON := []string{
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"I'm","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" Kim","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"i","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":",","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" a","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" large","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" language","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" model","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" trained","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" by","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" Moon","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"shot","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" AI","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":".","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":null},"logprobs":null,"finish_reason":"stop","matched_stop":163586}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[],"usage":{"prompt_tokens":31,"total_tokens":46,"completion_tokens":15,"prompt_tokens_details":null,"reasoning_tokens":0}}`,
		`[DONE]`,
	}

	expectedClaudeRespJSON := []string{
		`{"type":"message_start","message":{"id":"c91dda37dc3c4018ac0fecf81cf0052d","type":"message","role":"assistant","content":[],"model":"Kimi-K2-Instruct","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I'm"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" Kim"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"i"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":","}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" a"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" large"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" language"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" model"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" trained"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" by"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" Moon"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"shot"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" AI"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":31,"output_tokens":15}}`,
		`{"type":"message_stop"}`,
	}

	testChatCompletionsToClaudeMessages_convertStreamResponse(t, openaiRespJSON, expectedClaudeRespJSON)
}

func TestChatCompletionsToClaudeMessages_convertStreamResponse_SimpleText_Finish1(t *testing.T) {
	openaiRespJSON := []string{
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"I'm","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" Kim","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"i","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":",","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" a","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" large","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" language","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" model","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" trained","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" by","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" Moon","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"shot","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" AI","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":".","tool_calls":null},"logprobs":null,"finish_reason":"stop","matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[],"usage":{"prompt_tokens":31,"total_tokens":46,"completion_tokens":15,"prompt_tokens_details":null,"reasoning_tokens":0}}`,
		`[DONE]`,
	}

	expectedClaudeRespJSON := []string{
		`{"type":"message_start","message":{"id":"c91dda37dc3c4018ac0fecf81cf0052d","type":"message","role":"assistant","content":[],"model":"Kimi-K2-Instruct","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I'm"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" Kim"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"i"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":","}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" a"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" large"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" language"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" model"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" trained"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" by"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" Moon"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"shot"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" AI"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":31,"output_tokens":15}}`,
		`{"type":"message_stop"}`,
	}

	testChatCompletionsToClaudeMessages_convertStreamResponse(t, openaiRespJSON, expectedClaudeRespJSON)
}

func TestChatCompletionsToClaudeMessages_convertStreamResponse_SimpleText_Finish2(t *testing.T) {
	openaiRespJSON := []string{
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"I'm","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" Kim","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"i","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":",","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" a","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" large","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" language","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" model","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" trained","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" by","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" Moon","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"shot","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" AI","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":".","tool_calls":null},"logprobs":null,"finish_reason":"stop","matched_stop":null}],"usage":{"prompt_tokens":31,"total_tokens":46,"completion_tokens":15,"prompt_tokens_details":null,"reasoning_tokens":0}}`,
		`[DONE]`,
	}

	expectedClaudeRespJSON := []string{
		`{"type":"message_start","message":{"id":"c91dda37dc3c4018ac0fecf81cf0052d","type":"message","role":"assistant","content":[],"model":"Kimi-K2-Instruct","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I'm"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" Kim"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"i"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":","}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" a"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" large"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" language"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" model"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" trained"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" by"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" Moon"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"shot"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" AI"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":31,"output_tokens":15}}`,
		`{"type":"message_stop"}`,
	}

	testChatCompletionsToClaudeMessages_convertStreamResponse(t, openaiRespJSON, expectedClaudeRespJSON)
}

func TestChatCompletionsToClaudeMessages_convertStreamResponse_SimpleText_ContinousUsage(t *testing.T) {
	openaiRespJSON := []string{
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}],"usage":{"prompt_tokens":0,"total_tokens":0,"completion_tokens":0}}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"I'm","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}],"usage":{"prompt_tokens":31,"total_tokens":32,"completion_tokens":1}}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" Kim","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}],"usage":{"prompt_tokens":31,"total_tokens":33,"completion_tokens":2}}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"i","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}],"usage":{"prompt_tokens":31,"total_tokens":34,"completion_tokens":3}}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":",","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}],"usage":{"prompt_tokens":31,"total_tokens":35,"completion_tokens":4}}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" a","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}],"usage":{"prompt_tokens":31,"total_tokens":36,"completion_tokens":5}}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" large","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}],"usage":{"prompt_tokens":31,"total_tokens":37,"completion_tokens":6}}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" language","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}],"usage":{"prompt_tokens":31,"total_tokens":38,"completion_tokens":7}}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" model","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}],"usage":{"prompt_tokens":31,"total_tokens":39,"completion_tokens":8}}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" trained","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}],"usage":{"prompt_tokens":31,"total_tokens":40,"completion_tokens":9}}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" by","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}],"usage":{"prompt_tokens":31,"total_tokens":41,"completion_tokens":10}}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" Moon","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}],"usage":{"prompt_tokens":31,"total_tokens":42,"completion_tokens":11}}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"shot","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}],"usage":{"prompt_tokens":31,"total_tokens":43,"completion_tokens":12}}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" AI","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}],"usage":{"prompt_tokens":31,"total_tokens":44,"completion_tokens":13}}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":".","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}],"usage":{"prompt_tokens":31,"total_tokens":45,"completion_tokens":14}}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":null},"logprobs":null,"finish_reason":"stop","matched_stop":163586}],"usage":{"prompt_tokens":31,"total_tokens":46,"completion_tokens":15}}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[],"usage":{"prompt_tokens":31,"total_tokens":47,"completion_tokens":16,"prompt_tokens_details":null,"reasoning_tokens":0}}`,
		`[DONE]`,
	}

	expectedClaudeRespJSON := []string{
		`{"type":"message_start","message":{"id":"c91dda37dc3c4018ac0fecf81cf0052d","type":"message","role":"assistant","content":[],"model":"Kimi-K2-Instruct","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I'm"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" Kim"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"i"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":","}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" a"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" large"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" language"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" model"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" trained"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" by"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" Moon"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"shot"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" AI"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":31,"output_tokens":16}}`,
		`{"type":"message_stop"}`,
	}

	testChatCompletionsToClaudeMessages_convertStreamResponse(t, openaiRespJSON, expectedClaudeRespJSON)
}

func TestChatCompletionsToClaudeMessages_convertStreamResponse_ToolCall(t *testing.T) {
	openaiRespJSON := []string{
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"我来","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"帮你","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"查","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"一下","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"。","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":"functions.search:0","index":0,"type":"function","function":{"name":"search","arguments":""}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":0,"type":"function","function":{"name":null,"arguments":"{\"queries"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":0,"type":"function","function":{"name":null,"arguments":"\":"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":0,"type":"function","function":{"name":null,"arguments":"1"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":0,"type":"function","function":{"name":null,"arguments":"}"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124076,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":null},"logprobs":null,"finish_reason":"tool_calls","matched_stop":163586}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124076,"model":"Kimi-K2-Instruct","choices":[],"usage":{"prompt_tokens":177,"total_tokens":246,"completion_tokens":69,"prompt_tokens_details":null}}`,
		`[DONE]`,
	}

	expectedClaudeRespJSON := []string{
		`{"type":"message_start","message":{"id":"1480f67c3ad545f580a0742339bb391b","type":"message","role":"assistant","content":[],"model":"Kimi-K2-Instruct","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"我来"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"帮你"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"查"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"一下"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"。"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"functions.search:0","name":"search","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"queries"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\":"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"1"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":177,"output_tokens":69}}`,
		`{"type":"message_stop"}`,
	}

	testChatCompletionsToClaudeMessages_convertStreamResponse(t, openaiRespJSON, expectedClaudeRespJSON)
}

func TestChatCompletionsToClaudeMessages_convertStreamResponse_MultipleToolCall(t *testing.T) {
	openaiRespJSON := []string{
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"我来","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"帮你","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"查","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"一下","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"。","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":"functions.search:0","index":0,"type":"function","function":{"name":"search","arguments":""}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":0,"type":"function","function":{"name":null,"arguments":"{\"queries"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":0,"type":"function","function":{"name":null,"arguments":"\":"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":0,"type":"function","function":{"name":null,"arguments":"1"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":0,"type":"function","function":{"name":null,"arguments":"}"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":"functions.fetch:1","index":1,"type":"function","function":{"name":"fetch","arguments":""}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":1,"type":"function","function":{"name":null,"arguments":"{\"queries"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":1,"type":"function","function":{"name":null,"arguments":"\":"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":1,"type":"function","function":{"name":null,"arguments":"2"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":1,"type":"function","function":{"name":null,"arguments":"}"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124076,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":null},"logprobs":null,"finish_reason":"tool_calls","matched_stop":163586}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124076,"model":"Kimi-K2-Instruct","choices":[],"usage":{"prompt_tokens":177,"total_tokens":246,"completion_tokens":69,"prompt_tokens_details":null}}`,
		`[DONE]`,
	}

	expectedClaudeRespJSON := []string{
		`{"type":"message_start","message":{"id":"1480f67c3ad545f580a0742339bb391b","type":"message","role":"assistant","content":[],"model":"Kimi-K2-Instruct","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"我来"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"帮你"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"查"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"一下"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"。"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"functions.search:0","name":"search","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"queries"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\":"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"1"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"functions.fetch:1","name":"fetch","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"queries"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\":"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"2"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":177,"output_tokens":69}}`,
		`{"type":"message_stop"}`,
	}

	testChatCompletionsToClaudeMessages_convertStreamResponse(t, openaiRespJSON, expectedClaudeRespJSON)
}

func TestChatCompletionsToClaudeMessages_convertStreamResponse_MultipleToolCall_Sandwich(t *testing.T) {
	openaiRespJSON := []string{
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":"functions.search:0","index":0,"type":"function","function":{"name":"search","arguments":""}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":0,"type":"function","function":{"name":null,"arguments":"{\"queries"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":0,"type":"function","function":{"name":null,"arguments":"\":"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":0,"type":"function","function":{"name":null,"arguments":"1"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":0,"type":"function","function":{"name":null,"arguments":"}"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"我来","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"帮你","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"查","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"一下","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"。","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":"functions.fetch:1","index":1,"type":"function","function":{"name":"fetch","arguments":""}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":1,"type":"function","function":{"name":null,"arguments":"{\"queries"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":1,"type":"function","function":{"name":null,"arguments":"\":"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":1,"type":"function","function":{"name":null,"arguments":"2"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":1,"type":"function","function":{"name":null,"arguments":"}"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124076,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":null},"logprobs":null,"finish_reason":"tool_calls","matched_stop":163586}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124076,"model":"Kimi-K2-Instruct","choices":[],"usage":{"prompt_tokens":177,"total_tokens":246,"completion_tokens":69,"prompt_tokens_details":null}}`,
		`[DONE]`,
	}

	expectedClaudeRespJSON := []string{
		`{"type":"message_start","message":{"id":"1480f67c3ad545f580a0742339bb391b","type":"message","role":"assistant","content":[],"model":"Kimi-K2-Instruct","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"functions.search:0","name":"search","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"queries"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\":"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"1"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"我来"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"帮你"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"查"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"一下"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"。"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"functions.fetch:1","name":"fetch","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"queries"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\":"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"2"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":177,"output_tokens":69}}`,
		`{"type":"message_stop"}`,
	}

	testChatCompletionsToClaudeMessages_convertStreamResponse(t, openaiRespJSON, expectedClaudeRespJSON)
}
