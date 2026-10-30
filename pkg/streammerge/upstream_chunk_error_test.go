package streammerge

import (
	"encoding/json"
	"testing"

	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var inStreamErrTestParser = &octollm.JSONParser[openai.ChatCompletionStreamChunk]{}

func inStreamErrChunk(t *testing.T, raw string) *octollm.StreamChunk {
	t.Helper()
	return &octollm.StreamChunk{Body: octollm.NewBodyFromBytes([]byte(raw), inStreamErrTestParser)}
}

func TestChatCompletionMerger_InStreamError(t *testing.T) {
	const textChunk = `{"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`
	const errChunk = `{"error":{"message":"RPM limit exceeded","type":"rate_limit_error","code":"rate_limit_exceeded"},"usage":null}`

	t.Run("error chunk fails the merge", func(t *testing.T) {
		m := NewChatCompletionMerger()
		require.NoError(t, m.Merge(inStreamErrChunk(t, textChunk)))
		err := m.Merge(inStreamErrChunk(t, errChunk))
		require.Error(t, err)
		var uce *errutils.UpstreamChunkError
		require.ErrorAs(t, err, &uce)
		assert.JSONEq(t, `{"message":"RPM limit exceeded","type":"rate_limit_error","code":"rate_limit_exceeded"}`, string(uce.ErrorJSON))
		// 完整 chunk 原文也要保留（透传 body 用）。
		assert.JSONEq(t, errChunk, string(uce.Body))
	})

	t.Run("error null is not an error", func(t *testing.T) {
		m := NewChatCompletionMerger()
		require.NoError(t, m.Merge(inStreamErrChunk(t, `{"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"error":null}`)))
	})

	t.Run("normal chunks merge unaffected", func(t *testing.T) {
		m := NewChatCompletionMerger()
		require.NoError(t, m.Merge(inStreamErrChunk(t, textChunk)))
		require.NoError(t, m.Merge(inStreamErrChunk(t, `[DONE]`)))
		body, err := m.Merged()
		require.NoError(t, err)
		raw, err := body.Bytes()
		require.NoError(t, err)
		var resp openai.ChatCompletionResponse
		require.NoError(t, json.Unmarshal(raw, &resp))
		assert.Equal(t, "partial", resp.Choices[0].Message.Content.ExtractText())
	})
}
