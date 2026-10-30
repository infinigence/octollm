package openai

import (
	"encoding/json"
	"testing"

	"github.com/mailru/easyjson/jlexer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStreamChunkErrorRawMessage covers the raw in-stream error field beyond
// the jlexer-vs-encoding/json parity table in chatcompletion_resp_jlexer_test.go:
// ordinary chunks must re-serialize without an error key, and the raw error
// payload must survive round-trips verbatim.
func TestStreamChunkErrorRawMessage(t *testing.T) {
	t.Run("normal chunk has no error key after round trip", func(t *testing.T) {
		raw := `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`
		var chunk ChatCompletionStreamChunk
		require.NoError(t, json.Unmarshal([]byte(raw), &chunk))
		assert.Nil(t, chunk.Error)

		out, err := json.Marshal(&chunk)
		require.NoError(t, err)
		assert.NotContains(t, string(out), "error", "normal chunk must not gain an error key")
	})

	t.Run("error null is captured, not absent", func(t *testing.T) {
		raw := `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[],"error":null}`
		var chunk ChatCompletionStreamChunk
		require.NoError(t, json.Unmarshal([]byte(raw), &chunk))
		// Parity between encoding/json and the jlexer fast path: both capture
		// the literal null; the stream error engine treats it as "no error".
		require.NotNil(t, chunk.Error)
		assert.Equal(t, "null", string(chunk.Error))

		in := jlexer.Lexer{Data: []byte(raw)}
		jChunk := &ChatCompletionStreamChunk{}
		jChunk.ParseJLexer(&in)
		require.NoError(t, in.Error())
		require.NotNil(t, jChunk.Error)
		assert.Equal(t, "null", string(jChunk.Error))
	})

	t.Run("error object preserved verbatim", func(t *testing.T) {
		rawErr := `{"message":"Rate limit reached","type":"rate_limit_error","code":"rate_limit_exceeded","extra":{"k":"v"}}`
		raw := `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4","choices":[],"error":` + rawErr + `}`
		var chunk ChatCompletionStreamChunk
		require.NoError(t, json.Unmarshal([]byte(raw), &chunk))
		require.NotNil(t, chunk.Error)
		assert.JSONEq(t, rawErr, string(chunk.Error), "raw error JSON must be preserved, including unknown fields")

		// jlexer path must produce the same bytes.
		in := jlexer.Lexer{Data: []byte(raw)}
		jChunk := &ChatCompletionStreamChunk{}
		jChunk.ParseJLexer(&in)
		require.NoError(t, in.Error())
		assert.JSONEq(t, rawErr, string(jChunk.Error))
	})

	t.Run("completion response error field", func(t *testing.T) {
		raw := `{"id":"cmpl-1","created":1694268190,"model":"gpt-4","object":"text_completion.chunk","choices":[],"error":{"type":"server_error"}}`
		var resp CompletionResponse
		require.NoError(t, json.Unmarshal([]byte(raw), &resp))
		require.NotNil(t, resp.Error)
		assert.JSONEq(t, `{"type":"server_error"}`, string(resp.Error))
	})

	t.Run("responses stream chunk top level error field", func(t *testing.T) {
		raw := `{"type":"error","error":{"type":"too_many_requests","code":"rate_limit_exceeded"},"sequence_number":1}`
		var chunk ResponseStreamChunk
		require.NoError(t, json.Unmarshal([]byte(raw), &chunk))
		require.NotNil(t, chunk.Error)
		assert.JSONEq(t, `{"type":"too_many_requests","code":"rate_limit_exceeded"}`, string(chunk.Error))

		out, err := json.Marshal(&chunk)
		require.NoError(t, err)
		assert.Contains(t, string(out), `"error"`, "responses error chunk keeps the error key on re-serialization")
	})
}
