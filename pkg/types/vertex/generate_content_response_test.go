package vertex

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStreamGenerateContentResponse_Error covers the raw in-stream error field:
// ordinary chunks must re-serialize without an error key, and the Google error
// envelope must survive verbatim.
func TestStreamGenerateContentResponse_Error(t *testing.T) {
	t.Run("normal chunk has no error key after round trip", func(t *testing.T) {
		raw := `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]}}],"modelVersion":"gemini-2.5-pro"}`
		var chunk StreamGenerateContentResponse
		require.NoError(t, json.Unmarshal([]byte(raw), &chunk))
		assert.Nil(t, chunk.Error)

		out, err := json.Marshal(&chunk)
		require.NoError(t, err)
		assert.NotContains(t, string(out), "error", "normal chunk must not gain an error key")
	})

	t.Run("google error envelope preserved verbatim", func(t *testing.T) {
		rawErr := `{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}`
		var chunk StreamGenerateContentResponse
		require.NoError(t, json.Unmarshal([]byte(`{"error":`+rawErr+`}`), &chunk))
		require.NotNil(t, chunk.Error)
		assert.JSONEq(t, rawErr, string(chunk.Error))
	})

	t.Run("error null is captured as the null literal", func(t *testing.T) {
		var chunk StreamGenerateContentResponse
		require.NoError(t, json.Unmarshal([]byte(`{"candidates":[],"error":null}`), &chunk))
		assert.Equal(t, "null", string(chunk.Error))
	})
}
