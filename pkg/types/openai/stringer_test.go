package openai

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustUnmarshalChatReq(t *testing.T, jsonStr string) ChatCompletionRequest {
	t.Helper()
	var req ChatCompletionRequest
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &req))
	return req
}

func TestEmbeddingRequest_String(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }
	tests := []struct {
		name     string
		req      EmbeddingRequest
		expected string
	}{
		{
			name: "string input with normalize",
			req: EmbeddingRequest{
				Model:               "text-embedding-3-small",
				Input:               EmbeddingRequestInputString("hello"),
				NormalizeEmbeddings: boolPtr(true),
			},
			expected: `(EmbeddingRequest) {
  Model: "text-embedding-3-small"
  Input: string(len=5)
  NormalizeEmbeddings: true
}`,
		},
		{
			name: "array input",
			req: EmbeddingRequest{
				Model: "text-embedding-3-small",
				Input: EmbeddingRequestInputStringArray{"hello", "world"},
			},
			expected: `(EmbeddingRequest) {
  Model: "text-embedding-3-small"
  Input: array(total_len=10)
}`,
		},
		{
			name: "nil input",
			req: EmbeddingRequest{
				Model: "text-embedding-3-small",
			},
			expected: `(EmbeddingRequest) {
  Model: "text-embedding-3-small"
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.req.String())
		})
	}
}

func TestToolChoiceObject_String(t *testing.T) {
	tests := []struct {
		name     string
		tc       ToolChoiceObject
		expected string
	}{
		{
			name:     "function",
			tc:       ToolChoiceObject{Type: "function", Function: &ToolChoiceFunction{Name: "get_weather"}},
			expected: "function(get_weather)",
		},
		{
			name:     "function without param",
			tc:       ToolChoiceObject{Type: "function"},
			expected: "function",
		},
		{
			name:     "allowed_tools",
			tc:       ToolChoiceObject{Type: "allowed_tools", AllowedTools: &ToolChoiceAllowedTools{Mode: "auto"}},
			expected: "allowed_tools",
		},
		{
			name:     "custom",
			tc:       ToolChoiceObject{Type: "custom", Custom: &ToolChoiceCustom{Name: "my_tool"}},
			expected: "custom(my_tool)",
		},
		{
			name:     "custom without param",
			tc:       ToolChoiceObject{Type: "custom"},
			expected: "custom",
		},
		{
			name:     "unknown type",
			tc:       ToolChoiceObject{Type: "other"},
			expected: "other",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.tc.String())
		})
	}
}

func TestChatCompletionRequest_String_ToolChoice(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected string
	}{
		{
			name: "tool_choice string auto",
			json: `{
				"model": "gpt-4",
				"messages": [{"role": "user", "content": "hi"}],
				"tool_choice": "auto"
			}`,
			expected: `(ChatCompletionRequest) {
  Model: "gpt-4"
  Messages: len(1)
    (Message) {Role: "user", Content: len(2), }
  ToolChoice: auto
}`,
		},
		{
			name: "tool_choice object function",
			json: `{
				"model": "gpt-4",
				"messages": [{"role": "user", "content": "hi"}],
				"tool_choice": {"type": "function", "function": {"name": "get_weather"}}
			}`,
			expected: `(ChatCompletionRequest) {
  Model: "gpt-4"
  Messages: len(1)
    (Message) {Role: "user", Content: len(2), }
  ToolChoice: function(get_weather)
}`,
		},
		{
			name: "tool_choice object allowed_tools",
			json: `{
				"model": "gpt-4",
				"messages": [{"role": "user", "content": "hi"}],
				"tool_choice": {"type": "allowed_tools", "allowed_tools": {"mode": "auto", "tools": []}}
			}`,
			expected: `(ChatCompletionRequest) {
  Model: "gpt-4"
  Messages: len(1)
    (Message) {Role: "user", Content: len(2), }
  ToolChoice: allowed_tools
}`,
		},
		{
			name: "tool_choice object custom",
			json: `{
				"model": "gpt-4",
				"messages": [{"role": "user", "content": "hi"}],
				"tool_choice": {"type": "custom", "custom": {"name": "my_tool"}}
			}`,
			expected: `(ChatCompletionRequest) {
  Model: "gpt-4"
  Messages: len(1)
    (Message) {Role: "user", Content: len(2), }
  ToolChoice: custom(my_tool)
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := mustUnmarshalChatReq(t, tt.json)
			assert.Equal(t, tt.expected, req.String())
		})
	}
}

func mustUnmarshalCompletionReq(t *testing.T, jsonStr string) CompletionRequest {
	t.Helper()
	var req CompletionRequest
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &req))
	return req
}

func TestChatCompletionRequest_String(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected string
	}{
		{
			name: "two messages with options",
			json: `{
				"model": "gpt-4",
				"messages": [
					{"role": "system", "content": "You are a helpful assistant."},
					{"role": "user", "content": "Hello!"}
				],
				"max_tokens": 100,
				"stop": "end",
				"temperature": 0.7
			}`,
			// "You are a helpful assistant." = 28 bytes, "Hello!" = 6 bytes
			expected: `(ChatCompletionRequest) {
  Model: "gpt-4"
  Messages: len(2)
    (Message) {Role: "system", Content: len(28), }
    (Message) {Role: "user", Content: len(6), }
  MaxTokens: 100
  Temperature: 0.700000
  Stop: end
}`,
		},
		{
			name: "array content with image_url string",
			json: `{
				"model": "gpt-4o",
				"messages": [{
					"role": "user",
					"content": [
						{"type": "text", "text": "Describe this image"},
						{"type": "image_url", "image_url": "https://example.com/img.png"}
					]
				}],
				"stop": ["stop1", "stop2"]
			}`,
			// "Describe this image" = 19 bytes, "https://example.com/img.png" = 27 bytes
			expected: `(ChatCompletionRequest) {
  Model: "gpt-4o"
  Messages: len(1)
    (Message) {Role: "user", Content: [text(len=19), image_url(len=27), ], }
  Stop: [stop1 stop2]
}`,
		},
		{
			name: "array content with image_url struct",
			json: `{
				"model": "gpt-4o",
				"messages": [{
					"role": "user",
					"content": [
						{"type": "text", "text": "Describe this image"},
						{"type": "image_url", "image_url": {"url": "https://example.com/img.jpg", "detail": "high"}}
					]
				}]
			}`,
			// "https://example.com/img.jpg" = 27 bytes, detail=high
			expected: `(ChatCompletionRequest) {
  Model: "gpt-4o"
  Messages: len(1)
    (Message) {Role: "user", Content: [text(len=19), image_url(len=27,detail=high), ], }
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := mustUnmarshalChatReq(t, tt.json)
			assert.Equal(t, tt.expected, req.String())
		})
	}
}

func TestCompletionRequest_String(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected string
	}{
		{
			name: "string prompt with options",
			json: `{
				"model": "gpt-3.5-turbo-instruct",
				"prompt": "Say this is a test",
				"max_tokens": 7,
				"temperature": 0
			}`,
			// "Say this is a test" = 18 bytes; temperature=0 is set so it prints
			expected: `(CompletionRequest) {
  Model: "gpt-3.5-turbo-instruct"
  Prompt: len(20)
  MaxTokens: 7
  Temperature: 0.000000
  Stream: false
}`,
		},
		{
			name: "array prompt",
			json: `{
				"model": "gpt-3.5-turbo-instruct",
				"prompt": ["Hello", " ", "World"],
				"max_tokens": 100
			}`,
			expected: `(CompletionRequest) {
  Model: "gpt-3.5-turbo-instruct"
  Prompt: array(23)
  MaxTokens: 100
  Stream: false
}`,
		},
		{
			name: "stream true",
			json: `{
				"model": "gpt-3.5-turbo-instruct",
				"prompt": "Test",
				"stream": true
			}`,
			// "Test" = 4 bytes
			expected: `(CompletionRequest) {
  Model: "gpt-3.5-turbo-instruct"
  Prompt: len(6)
  Stream: true
}`,
		},
		{
			name: "logprobs",
			json: `{
				"model": "gpt-3.5-turbo-instruct",
				"prompt": "Test",
				"logprobs": true
			}`,
			expected: `(CompletionRequest) {
  Model: "gpt-3.5-turbo-instruct"
  Prompt: len(6)
  Stream: false
  LogProbs: true
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := mustUnmarshalCompletionReq(t, tt.json)
			assert.Equal(t, tt.expected, req.String())
		})
	}
}

func mustUnmarshalChatResp(t *testing.T, jsonStr string) ChatCompletionResponse {
	t.Helper()
	var resp ChatCompletionResponse
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &resp))
	return resp
}

func TestChatCompletionResponse_String(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected string
	}{
		{
			name: "basic response with usage",
			json: `{
				"id": "chatcmpl-123",
				"object": "chat.completion",
				"created": 1700000000,
				"model": "gpt-4",
				"choices": [{
					"index": 0,
					"message": {"role": "assistant", "content": "Hello there!"},
					"finish_reason": "stop"
				}],
				"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
			}`,
			// "Hello there!" = 12 bytes
			expected: `(ChatCompletionResponse) {
  ID: "chatcmpl-123"
  Model: "gpt-4"
  Object: "chat.completion"
  Created: 1700000000
  Choices: len(1)
    Choice{index=0, finish_reason=stop, message=(Message) {Role: "assistant", Content: len(12), }}
  Usage: prompt=10, completion=5, total=15
}`,
		},
		{
			name: "with token details and fingerprint",
			json: `{
				"id": "chatcmpl-456",
				"object": "chat.completion",
				"created": 1700000001,
				"model": "o1",
				"choices": [{
					"index": 0,
					"message": {"role": "assistant", "content": "ok"},
					"finish_reason": "stop"
				}],
				"usage": {
					"prompt_tokens": 8,
					"completion_tokens": 4,
					"total_tokens": 12,
					"completion_tokens_details": {"reasoning_tokens": 3},
					"prompt_tokens_details": {"cached_tokens": 2}
				},
				"system_fingerprint": "fp_abc",
				"service_tier": "default"
			}`,
			expected: `(ChatCompletionResponse) {
  ID: "chatcmpl-456"
  Model: "o1"
  Object: "chat.completion"
  Created: 1700000001
  Choices: len(1)
    Choice{index=0, finish_reason=stop, message=(Message) {Role: "assistant", Content: len(2), }}
  Usage: prompt=8, completion=4, total=12, reasoning=3, cached=2
  SystemFingerprint: "fp_abc"
  ServiceTier: "default"
}`,
		},
		{
			name: "usage with cache_write_tokens",
			json: `{
				"id": "chatcmpl-789",
				"object": "chat.completion",
				"created": 1700000002,
				"model": "gpt-4",
				"choices": [{
					"index": 0,
					"message": {"role": "assistant", "content": "ok"},
					"finish_reason": "stop"
				}],
				"usage": {
					"prompt_tokens": 100,
					"completion_tokens": 5,
					"total_tokens": 105,
					"prompt_tokens_details": {"cached_tokens": 20, "cache_write_tokens": 30}
				}
			}`,
			expected: `(ChatCompletionResponse) {
  ID: "chatcmpl-789"
  Model: "gpt-4"
  Object: "chat.completion"
  Created: 1700000002
  Choices: len(1)
    Choice{index=0, finish_reason=stop, message=(Message) {Role: "assistant", Content: len(2), }}
  Usage: prompt=100, completion=5, total=105, cached=20, cache_write=30
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := mustUnmarshalChatResp(t, tt.json)
			assert.Equal(t, tt.expected, resp.String())
		})
	}
}

func mustUnmarshalCompletionResp(t *testing.T, jsonStr string) CompletionResponse {
	t.Helper()
	var resp CompletionResponse
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &resp))
	return resp
}

func TestCompletionResponse_String(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected string
	}{
		{
			name: "single choice",
			json: `{
				"id": "cmpl-1",
				"object": "text_completion",
				"created": 1700000000,
				"model": "gpt-3.5-turbo-instruct",
				"choices": [{"text": "Hello", "index": 0, "finish_reason": "stop"}],
				"usage": {"prompt_tokens": 5, "completion_tokens": 1, "total_tokens": 6}
			}`,
			// "Hello" = 5 bytes
			expected: `(CompletionResponse) {
  ID: "cmpl-1"
  Model: "gpt-3.5-turbo-instruct"
  Object: "text_completion"
  Created: 1700000000
  Choices: len(1)
    Choice{index=0, text_len=5, finish_reason=stop}
  Usage: prompt=5, completion=1, total=6
}`,
		},
		{
			name: "no usage, with fingerprint",
			json: `{
				"id": "cmpl-2",
				"object": "text_completion",
				"created": 1700000001,
				"model": "gpt-3.5-turbo-instruct",
				"choices": [{"text": "abcd", "index": 0}],
				"system_fingerprint": "fp_xyz"
			}`,
			expected: `(CompletionResponse) {
  ID: "cmpl-2"
  Model: "gpt-3.5-turbo-instruct"
  Object: "text_completion"
  Created: 1700000001
  Choices: len(1)
    Choice{index=0, text_len=4, finish_reason=}
  SystemFingerprint: "fp_xyz"
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := mustUnmarshalCompletionResp(t, tt.json)
			assert.Equal(t, tt.expected, resp.String())
		})
	}
}

func mustUnmarshalEmbeddingResp(t *testing.T, jsonStr string) EmbeddingResponse {
	t.Helper()
	var resp EmbeddingResponse
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &resp))
	return resp
}

func TestEmbeddingResponse_String(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected string
	}{
		{
			name: "two embeddings",
			json: `{
				"object": "list",
				"data": [
					{"object": "embedding", "index": 0, "embedding": [0.1, 0.2, 0.3]},
					{"object": "embedding", "index": 1, "embedding": [0.4, 0.5, 0.6, 0.7]}
				],
				"model": "text-embedding-3-small",
				"usage": {"prompt_tokens": 8, "total_tokens": 8}
			}`,
			expected: `(EmbeddingResponse) {
  Model: "text-embedding-3-small"
  Object: "list"
  Data: len(2)
    Embedding{index=0, dims=3}
    Embedding{index=1, dims=4}
  Usage: prompt=8, total=8
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := mustUnmarshalEmbeddingResp(t, tt.json)
			assert.Equal(t, tt.expected, resp.String())
		})
	}
}

func mustUnmarshalResponsesResp(t *testing.T, jsonStr string) ResponsesResponse {
	t.Helper()
	var resp ResponsesResponse
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &resp))
	return resp
}

func TestResponsesResponse_String(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected string
	}{
		{
			name: "output_text and refusal with detailed usage",
			json: `{
				"id": "resp_1",
				"output": [{
					"id": "msg_1",
					"type": "message",
					"role": "assistant",
					"content": [
						{"type": "output_text", "text": "Hello"},
						{"type": "refusal", "refusal": "Cannot"}
					]
				}],
				"usage": {
					"input_tokens": 10,
					"output_tokens": 5,
					"total_tokens": 15,
					"input_tokens_details": {"cached_tokens": 2},
					"output_tokens_details": {"reasoning_tokens": 3}
				}
			}`,
			// "Hello" = 5, "Cannot" = 6
			expected: `(ResponsesResponse) {
  ID: "resp_1"
  Output: len(1)
    {id=msg_1, type=message, role=assistant, content=[output_text(len=5), refusal(len=6), ]}
  Usage: input=10, output=5, total=15, cached=2, reasoning=3
}`,
		},
		{
			name: "minimal usage",
			json: `{
				"id": "resp_2",
				"output": [],
				"usage": {"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}
			}`,
			expected: `(ResponsesResponse) {
  ID: "resp_2"
  Output: len(0)
  Usage: input=1, output=2, total=3
}`,
		},
		{
			name: "usage with cache_write_tokens",
			json: `{
				"id": "resp_3",
				"output": [],
				"usage": {
					"input_tokens": 100,
					"output_tokens": 5,
					"total_tokens": 105,
					"input_tokens_details": {"cached_tokens": 20, "cache_write_tokens": 30}
				}
			}`,
			expected: `(ResponsesResponse) {
  ID: "resp_3"
  Output: len(0)
  Usage: input=100, output=5, total=105, cached=20, cache_write=30
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := mustUnmarshalResponsesResp(t, tt.json)
			assert.Equal(t, tt.expected, resp.String())
		})
	}
}

func mustUnmarshalResponsesReq(t *testing.T, jsonStr string) ResponsesRequest {
	t.Helper()
	var req ResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &req))
	return req
}

func TestResponsesRequest_String(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected string
	}{
		{
			name: "string input with scalar params",
			json: `{
				"model": "gpt-5",
				"stream": true,
				"instructions": "be brief",
				"input": "Hello",
				"max_output_tokens": 100,
				"temperature": 0.5,
				"top_p": 0.9,
				"reasoning": {"effort": "high"},
				"parallel_tool_calls": false
			}`,
			// "be brief" = 8, "Hello" = 5
			expected: `(ResponsesRequest) {
  Model: "gpt-5"
  Stream: true
  Instructions: len(8)
  Input: string(len=5)
  MaxOutputTokens: 100
  Temperature: 0.500000
  TopP: 0.900000
  Reasoning: effort=high
  ParallelToolCalls: false
}`,
		},
		{
			name: "message items with string and array content",
			json: `{
				"model": "gpt-5",
				"input": [
					{"role": "system", "content": "sys"},
					{"type": "message", "role": "user", "content": [
						{"type": "input_text", "text": "hi"},
						{"type": "input_image", "image_url": "http://x/y.png"},
						{"type": "input_file", "file_id": "file_1", "filename": "a.pdf"},
						{"type": "input_audio"}
					]}
				]
			}`,
			// "sys" = 3, "hi" = 2, "http://x/y.png" = 14
			expected: `(ResponsesRequest) {
  Model: "gpt-5"
  Input: len(2)
    {type=message, role=system, content=string(len=3)}
    {type=message, role=user, content=[input_text(len=2), input_image(len=14), input_file(file_id=file_1, filename=a.pdf), input_audio, ]}
}`,
		},
		{
			name: "function call, output and reasoning items",
			json: `{
				"model": "gpt-5",
				"input": [
					{"type": "function_call", "call_id": "call_1", "name": "get_weather", "arguments": "{\"city\":\"SF\"}"},
					{"type": "function_call_output", "call_id": "call_1", "output": "sunny"},
					{"type": "function_call_output", "call_id": "call_2", "output": [{"type": "input_text", "text": "rainy"}]},
					{"type": "reasoning", "id": "rs_1", "summary": [{"type": "summary_text", "text": "think"}], "encrypted_content": "abcd"},
					{"type": "web_search_call", "id": "ws_1"}
				]
			}`,
			// arguments = 13, "sunny" = 5, "rainy" = 5, "think" = 5, "abcd" = 4
			expected: `(ResponsesRequest) {
  Model: "gpt-5"
  Input: len(5)
    {type=function_call, call_id=call_1, name=get_weather, args_len=13}
    {type=function_call_output, call_id=call_1, output=string(len=5)}
    {type=function_call_output, call_id=call_2, output=[input_text(len=5), ]}
    {type=reasoning, id=rs_1, summary=[summary_text(len=5), ], encrypted_len=4}
    {type=raw, len=41}
}`,
		},
		{
			name: "tools with string tool_choice",
			json: `{
				"model": "gpt-5",
				"input": "hi",
				"tools": [{"type": "function", "name": "get_weather", "parameters": {"type": "object"}}],
				"tool_choice": "auto"
			}`,
			expected: `(ResponsesRequest) {
  Model: "gpt-5"
  Input: string(len=2)
  Tools: len(1)
    Tool{type=function, name=get_weather}
  ToolChoice: auto
}`,
		},
		{
			name: "forced function tool_choice",
			json: `{
				"model": "gpt-5",
				"input": "hi",
				"tool_choice": {"type": "function", "name": "get_weather"}
			}`,
			expected: `(ResponsesRequest) {
  Model: "gpt-5"
  Input: string(len=2)
  ToolChoice: function(get_weather)
}`,
		},
		{
			name: "empty request",
			json: `{}`,
			expected: `(ResponsesRequest) {
  Model: ""
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := mustUnmarshalResponsesReq(t, tt.json)
			assert.Equal(t, tt.expected, req.String())
		})
	}
}
