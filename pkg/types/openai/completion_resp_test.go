package openai

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCompletionResponse_Error covers the raw in-stream error field on
// CompletionResponse (and CompletionStreamChunk, its stream alias).
func TestCompletionResponse_Error(t *testing.T) {
	raw := `{"id":"cmpl-1","created":1694268190,"model":"gpt-4","object":"text_completion.chunk","choices":[],"error":{"type":"server_error"}}`
	var resp CompletionStreamChunk
	require.NoError(t, json.Unmarshal([]byte(raw), &resp))
	require.NotNil(t, resp.Error)
	assert.JSONEq(t, `{"type":"server_error"}`, string(resp.Error))
}
