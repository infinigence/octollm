package octollm_test

import (
	"context"
	"testing"
	"time"

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

func TestRequestOriginalFormat(t *testing.T) {
	t.Run("NewRequest records the entry format", func(t *testing.T) {
		req := testhelper.CreateTestRequest(testhelper.WithFormat(octollm.APIFormatClaudeMessages))

		v, ok := octollm.GetCtxValue[octollm.APIFormat](req, octollm.ContextKeyOriginalFormat)
		assert.True(t, ok)
		assert.Equal(t, octollm.APIFormatClaudeMessages, v)
	})

	t.Run("survives Format being changed after conversion", func(t *testing.T) {
		req := testhelper.CreateTestRequest(testhelper.WithFormat(octollm.APIFormatClaudeMessages))

		req.Format = octollm.APIFormatChatCompletions

		v, ok := octollm.GetCtxValue[octollm.APIFormat](req, octollm.ContextKeyOriginalFormat)
		assert.True(t, ok)
		assert.Equal(t, octollm.APIFormatClaudeMessages, v, "original format must not follow Format mutations")
	})
}

func TestStreamChanDrainRemaining(t *testing.T) {
	// drainContext mirrors how callers are expected to bound the drain.
	drainContext := func(ctx context.Context) (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.WithoutCancel(ctx), octollm.DefaultDrainTimeout)
	}

	t.Run("returns when producer closes the channel", func(t *testing.T) {
		ch := make(chan *octollm.StreamChunk)
		sc := octollm.NewStreamChan(ch, nil)

		go func() {
			defer close(ch)
			for range 3 {
				ch <- &octollm.StreamChunk{}
			}
		}()

		ctx, cancel := drainContext(context.Background())
		defer cancel()

		done := make(chan struct{})
		go func() {
			defer close(done)
			sc.DrainRemaining(ctx)
		}()

		select {
		case <-done:
		case <-time.After(octollm.DefaultDrainTimeout / 2):
			t.Fatal("drain did not return after the producer closed the channel")
		}
	})

	t.Run("gives up on a producer that never closes", func(t *testing.T) {
		ch := make(chan *octollm.StreamChunk)
		sc := octollm.NewStreamChan(ch, nil)

		ctx, cancel := drainContext(context.Background())
		defer cancel()

		start := time.Now()
		sc.DrainRemaining(ctx)
		elapsed := time.Since(start)

		assert.GreaterOrEqual(t, elapsed, octollm.DefaultDrainTimeout)
		assert.Less(t, elapsed, octollm.DefaultDrainTimeout*3)
	})

	t.Run("keeps draining after the request context is canceled", func(t *testing.T) {
		ch := make(chan *octollm.StreamChunk)
		sc := octollm.NewStreamChan(ch, nil)

		reqCtx, cancelReq := context.WithCancel(context.Background())
		ctx, cancel := drainContext(reqCtx)
		defer cancel()
		// The client goes away mid-drain; the producer must still be released.
		cancelReq()

		released := make(chan struct{})
		go func() {
			defer close(released)
			sc.DrainRemaining(ctx)
		}()

		select {
		case <-released:
			t.Fatal("drain gave up when the request context was canceled")
		case <-time.After(octollm.DefaultDrainTimeout / 2):
		}

		close(ch)
		select {
		case <-released:
		case <-time.After(octollm.DefaultDrainTimeout):
			t.Fatal("drain did not return after the producer closed the channel")
		}
	})

	t.Run("stops when the drain context is done", func(t *testing.T) {
		ch := make(chan *octollm.StreamChunk)
		sc := octollm.NewStreamChan(ch, nil)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		start := time.Now()
		sc.DrainRemaining(ctx)
		assert.Less(t, time.Since(start), octollm.DefaultDrainTimeout)
	})

	t.Run("nil receiver is a no-op", func(t *testing.T) {
		var sc *octollm.StreamChan
		assert.NotPanics(t, func() { sc.DrainRemaining(context.Background()) })
	})
}
