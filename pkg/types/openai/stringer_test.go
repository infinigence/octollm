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
				}]
			}`,
			// "Describe this image" = 19 bytes, "https://example.com/img.png" = 27 bytes
			expected: `(ChatCompletionRequest) {
  Model: "gpt-4o"
  Messages: len(1)
    (Message) {Role: "user", Content: [text(len=19), image_url(len=27), ], }
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
  Prompt: len(18)
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
			// JSON array unmarshals into []interface{}, hits default %T branch
			expected: `(CompletionRequest) {
  Model: "gpt-3.5-turbo-instruct"
  Prompt: []interface {}
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
  Prompt: len(4)
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
  Prompt: len(4)
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
