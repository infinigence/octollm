package openai

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResponseStreamChunk_Error covers the raw top-level error field on
// ResponseStreamChunk.
func TestResponseStreamChunk_Error(t *testing.T) {
	raw := `{"type":"error","error":{"type":"too_many_requests","code":"rate_limit_exceeded"},"sequence_number":1}`
	var chunk ResponseStreamChunk
	require.NoError(t, json.Unmarshal([]byte(raw), &chunk))
	require.NotNil(t, chunk.Error)
	assert.JSONEq(t, `{"type":"too_many_requests","code":"rate_limit_exceeded"}`, string(chunk.Error))

	out, err := json.Marshal(&chunk)
	require.NoError(t, err)
	assert.Contains(t, string(out), `"error"`, "responses error chunk keeps the error key on re-serialization")
}
