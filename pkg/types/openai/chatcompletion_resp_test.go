package openai

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChatCompletionStreamChunk_Error covers the raw in-stream error field on
// the encoding/json path. The jlexer fast path is held to the same results by
// the parity table in chatcompletion_resp_jlexer_test.go.
func TestChatCompletionStreamChunk_Error(t *testing.T) {
	t.Run("normal chunk has no error key after round trip", func(t *testing.T) {
		raw := `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`
		var chunk ChatCompletionStreamChunk
		require.NoError(t, json.Unmarshal([]byte(raw), &chunk))
		assert.Nil(t, chunk.Error)

		out, err := json.Marshal(&chunk)
		require.NoError(t, err)
		assert.NotContains(t, string(out), "error", "normal chunk must not gain an error key")
	})

	t.Run("error null is captured as the null literal", func(t *testing.T) {
		raw := `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[],"error":null}`
		var chunk ChatCompletionStreamChunk
		require.NoError(t, json.Unmarshal([]byte(raw), &chunk))
		// Consumers treat the null literal as "no error".
		require.NotNil(t, chunk.Error)
		assert.Equal(t, "null", string(chunk.Error))
	})

	t.Run("error object preserved verbatim", func(t *testing.T) {
		rawErr := `{"message":"Rate limit reached","type":"rate_limit_error","code":"rate_limit_exceeded","extra":{"k":"v"}}`
		raw := `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[],"error":` + rawErr + `}`
		var chunk ChatCompletionStreamChunk
		require.NoError(t, json.Unmarshal([]byte(raw), &chunk))
		require.NotNil(t, chunk.Error)
		assert.JSONEq(t, rawErr, string(chunk.Error), "raw error JSON must be preserved, including unknown fields")
	})
}
