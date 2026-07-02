package openai

import (
	"encoding/json"
	"testing"

	"github.com/mailru/easyjson/jlexer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChatCompletionResponse_ParseJLexer(t *testing.T) {
	testCases := []struct {
		Name string
		JSON string
	}{
		{
			Name: "basic response with usage",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion","created":1700000000,"model":"gpt-4","choices":[{"index":0,"message":{"role":"assistant","content":"Hello there!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		},
		{
			Name: "with token details and fingerprint",
			JSON: `{"id":"chatcmpl-456","object":"chat.completion","created":1700000001,"model":"o1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":4,"total_tokens":12,"completion_tokens_details":{"reasoning_tokens":3},"prompt_tokens_details":{"cached_tokens":2}},"system_fingerprint":"fp_abc","service_tier":"default"}`,
		},
		{
			Name: "empty response",
			JSON: `{}`,
		},
		{
			Name: "null choices",
			JSON: `{"id":"chatcmpl-789","choices":null}`,
		},
		{
			Name: "null usage",
			JSON: `{"id":"chatcmpl-000","usage":null}`,
		},
		{
			Name: "blocked response",
			JSON: `{"id":"chatcmpl-blocked","object":"chat.completion","created":1700000000,"model":"gpt-4","choices":[],"blocked":true}`,
		},
		{
			Name: "empty choices array",
			JSON: `{"id":"chatcmpl-empty","choices":[]}`,
		},
		{
			Name: "null finish_reason in choice",
			JSON: `{"id":"chatcmpl-null","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":null}]}`,
		},
		{
			Name: "null message in choice",
			JSON: `{"id":"chatcmpl-null","choices":[{"index":0,"message":null}]}`,
		},
		{
			Name: "no message in choice",
			JSON: `{"id":"chatcmpl-null","choices":[{"index":0}]}`,
		},
		{
			Name: "null string fields in response",
			JSON: `{"id":null,"object":null,"model":null,"choices":[]}`,
		},
		{
			Name: "response with unknown fields",
			JSON: `{"id":"chatcmpl-1","created":100,"model":"gpt-4","choices":[],"extra":"field","nested":{"a":1}}`,
		},
		{
			Name: "response with null blocked",
			JSON: `{"id":"chatcmpl-1","choices":[],"blocked":null}`,
		},
		{
			Name: "response with null system_fingerprint and service_tier",
			JSON: `{"id":"chatcmpl-1","choices":[],"system_fingerprint":null,"service_tier":null}`,
		},
		{
			Name: "response with usage null details",
			JSON: `{"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"completion_tokens_details":null,"prompt_tokens_details":null}}`,
		},
		{
			Name: "response with usage only completion_tokens_details",
			JSON: `{"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"completion_tokens_details":{"reasoning_tokens":3,"audio_tokens":1}}}`,
		},
		{
			Name: "response with usage only prompt_tokens_details",
			JSON: `{"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":5,"audio_tokens":2}}}`,
		},
		{
			Name: "top-level null response",
			JSON: `null`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			var stdResp ChatCompletionResponse
			err := json.Unmarshal([]byte(tc.JSON), &stdResp)
			require.NoError(t, err, "standard json.Unmarshal should not error")

			in := jlexer.Lexer{Data: []byte(tc.JSON)}
			jResp := &ChatCompletionResponse{}
			jResp.ParseJLexer(&in)
			require.NoError(t, in.Error(), "jlexer parser should not error")
			assert.Equal(t, &stdResp, jResp, "jlexer parser output should match standard json.Unmarshal")
		})
	}
}

func TestChatCompletionStreamChunk_ParseJLexer(t *testing.T) {
	testCases := []struct {
		Name string
		JSON string
	}{
		{
			Name: "FirstChunk_RoleAndEmptyContent",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`,
		},
		{
			Name: "TextDelta",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
		},
		{
			Name: "TextDeltaContinued",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[{"index":0,"delta":{"content":" world!"},"finish_reason":null}]}`,
		},
		{
			Name: "ToolCallStart",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc123","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`,
		},
		{
			Name: "ToolCallArgumentsDelta",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"loc"}}]},"finish_reason":null}]}`,
		},
		{
			Name: "FinalChunk_WithUsage",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":12,"total_tokens":21}}`,
		},
		{
			Name: "ChunkWithSystemFingerprint",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","system_fingerprint":"fp_abc123","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		},
		{
			Name: "ChunkWithServiceTier",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","service_tier":"standard","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		},
		{
			Name: "FinalChunk_WithUsageDetails",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"completion_tokens_details":{"reasoning_tokens":20,"audio_tokens":5},"prompt_tokens_details":{"cached_tokens":10,"audio_tokens":2}}}`,
		},
		{
			Name: "ReasoningContent_String",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"o1","choices":[{"index":0,"delta":{"reasoning_content":"Let me think..."},"finish_reason":null}]}`,
		},
		{
			Name: "EmptyChunk",
			JSON: `{}`,
		},
		{
			Name: "EmptyChoicesArray",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[]}`,
		},
		{
			Name: "NullFinishReason",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		},
		{
			Name: "MultiChoice",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null},{"index":1,"delta":{"content":"Hi"},"finish_reason":null}]}`,
		},
		{
			Name: "ArrayContent_TextAndImage",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[{"index":0,"delta":{"content":[{"type":"text","text":"What's in this image?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBOR","detail":"high"}}]},"finish_reason":null}]}`,
		},
		{
			Name: "ImageURLAsString",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[{"index":0,"delta":{"content":[{"type":"image_url","image_url":"https://example.com/img.png"}]},"finish_reason":null}]}`,
		},
		{
			Name: "NullStringFields",
			JSON: `{"id":null,"object":null,"model":null,"choices":[]}`,
		},
		{
			Name: "NullUsage",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[],"usage":null}`,
		},
		{
			Name: "NullSystemFingerprint",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","system_fingerprint":null,"choices":[]}`,
		},
		{
			Name: "NullServiceTier",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","service_tier":null,"choices":[]}`,
		},
		{
			Name: "NullChoices",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":null}`,
		},
		{
			Name: "NullDeltaInChoice",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[{"index":0,"delta":null,"finish_reason":null}]}`,
		},
		{
			Name: "TopLevelNull",
			JSON: `null`,
		},
		{
			Name: "UnknownFieldsIgnored",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[],"extra":"field","nested":{"a":1}}`,
		},
		{
			Name: "UsageWithNullDetails",
			JSON: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"completion_tokens_details":null,"prompt_tokens_details":null}}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			var stdChunk ChatCompletionStreamChunk
			err := json.Unmarshal([]byte(tc.JSON), &stdChunk)
			require.NoError(t, err, "standard json.Unmarshal should not error")

			in := jlexer.Lexer{Data: []byte(tc.JSON)}
			jlexerChunk := &ChatCompletionStreamChunk{}
			jlexerChunk.ParseJLexer(&in)
			require.NoError(t, in.Error(), "jlexer parser should not error")
			assert.Equal(t, &stdChunk, jlexerChunk, "jlexer parser output should match standard json.Unmarshal")
		})
	}
}
