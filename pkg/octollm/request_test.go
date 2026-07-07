package octollm_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/vertex"
)

func TestRequestIsStream(t *testing.T) {
	t.Run("detects stream from body", func(t *testing.T) {
		req := testhelper.CreateTestRequest(testhelper.WithBody(`{"model":"m","stream":true}`))
		isStream, err := req.IsStream()
		require.NoError(t, err)
		assert.True(t, isStream)
	})

	t.Run("caches result into context", func(t *testing.T) {
		req := testhelper.CreateTestRequest(testhelper.WithBody(`{"model":"m","stream":true}`))

		_, ok := octollm.GetCtxValue[bool](req, octollm.ContextKeyIsStream)
		assert.False(t, ok, "flag must not be set before the first call")

		_, err := req.IsStream()
		require.NoError(t, err)

		v, ok := octollm.GetCtxValue[bool](req, octollm.ContextKeyIsStream)
		assert.True(t, ok, "flag must be cached after the first call")
		assert.True(t, v)
	})

	t.Run("vertex stream action is detected and cached", func(t *testing.T) {
		// A URL-action protocol: VertexAIHandler extracts the action from the
		// URL and stores it in the context; the Gemini body has no stream field.
		ctx := context.WithValue(context.Background(), octollm.ContextKeyAction, "streamGenerateContent")
		req := testhelper.CreateTestRequest(
			testhelper.WithContext(ctx),
			testhelper.WithBody(vertex.GenerateContentRequest{
				Contents: []vertex.Content{
					{Role: "user", Parts: []vertex.Part{{Text: "who are you?"}}},
				},
			}),
		)
		isStream, err := req.IsStream()
		require.NoError(t, err)
		assert.True(t, isStream)

		v, ok := octollm.GetCtxValue[bool](req, octollm.ContextKeyIsStream)
		assert.True(t, ok)
		assert.True(t, v)
	})

	t.Run("parse error is returned and not cached", func(t *testing.T) {
		req := testhelper.CreateTestRequest(testhelper.WithBody(`not json`))
		_, err := req.IsStream()
		require.Error(t, err)

		_, ok := octollm.GetCtxValue[bool](req, octollm.ContextKeyIsStream)
		assert.False(t, ok, "a failed classification must not be cached")
	})

	t.Run("nil request and nil body are non-stream", func(t *testing.T) {
		var nilReq *octollm.Request
		isStream, err := nilReq.IsStream()
		require.NoError(t, err)
		assert.False(t, isStream)

		req := octollm.NewEmptyRequest(context.Background())
		isStream, err = req.IsStream()
		require.NoError(t, err)
		assert.False(t, isStream)
	})
}
