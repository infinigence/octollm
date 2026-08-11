package traffic_replication

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/stretchr/testify/assert"
)

func newTestRequest(t *testing.T) *octollm.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.com/api?foo=bar", strings.NewReader(`{"model":"gpt-4"}`))
	assert.NoError(t, err)
	u := octollm.NewRequest(req, octollm.APIFormatChatCompletions)
	u.Query.Set("a", "1")
	u.Header.Set("X-Custom", "val1")
	return u
}

func TestCloneForReplication(t *testing.T) {
	t.Run("nil request returns nil", func(t *testing.T) {
		clone, cancel := CloneForReplication(nil, time.Minute)
		assert.Nil(t, clone)
		assert.Nil(t, cancel)
	})

	t.Run("clone has separate context with deadline", func(t *testing.T) {
		req := newTestRequest(t)
		clone, cancel := CloneForReplication(req, 5*time.Second)
		defer cancel()

		assert.NotNil(t, clone)
		assert.NotNil(t, cancel)

		deadline, ok := clone.Context().Deadline()
		assert.True(t, ok, "clone context should have deadline")
		assert.WithinDuration(t, time.Now().Add(5*time.Second), deadline, 2*time.Second)
	})

	t.Run("zero expiration uses default", func(t *testing.T) {
		req := newTestRequest(t)
		clone, cancel := CloneForReplication(req, 0)
		defer cancel()

		deadline, ok := clone.Context().Deadline()
		assert.True(t, ok)
		assert.GreaterOrEqual(t, time.Until(deadline), 9*time.Minute, "should use 10 min default")
	})

	t.Run("metadata: replication flag set; source metadata not copied", func(t *testing.T) {
		req := newTestRequest(t)
		const customKey = "traffic_replication_test_custom_meta"
		req.SetMetadataValue(customKey, "source-only")

		clone, cancel := CloneForReplication(req, time.Minute)
		defer cancel()

		assert.NotNil(t, clone)

		v, ok := clone.GetMetadataValue(IsTrafficReplication)
		assert.True(t, ok, "clone should carry IsTrafficReplication")
		assert.Equal(t, true, v)

		_, ok = clone.GetMetadataValue(customKey)
		assert.False(t, ok, "clone must not inherit arbitrary source metadata")

		_, ok = req.GetMetadataValue(customKey)
		assert.True(t, ok, "source metadata unchanged")

		assert.Equal(t, true, clone.Context().Value(IsDuplicateRequest))
	})

	t.Run("deep copy URL Query Header Body", func(t *testing.T) {
		req := newTestRequest(t)
		clone, cancel := CloneForReplication(req, time.Minute)
		defer cancel()

		// Modify clone and verify original unchanged
		clone.URL.Path = "/modified"
		assert.NotEqual(t, "/modified", req.URL.Path)

		clone.Query.Set("a", "999")
		assert.Equal(t, "1", req.Query.Get("a"))

		clone.Header.Set("X-Custom", "modified")
		assert.Equal(t, "val1", req.Header.Get("X-Custom"))

		// Body: clone has own copy
		cloneBytes, err := clone.Body.Bytes()
		assert.NoError(t, err)
		assert.Contains(t, string(cloneBytes), "gpt-4")
	})

	t.Run("cancel releases context", func(t *testing.T) {
		req := newTestRequest(t)
		clone, cancel := CloneForReplication(req, 10*time.Minute)
		assert.NotNil(t, clone)

		cancel()
		assert.Error(t, clone.Context().Err())
	})

	t.Run("clone inherits original format from source context", func(t *testing.T) {
		req := newTestRequest(t)
		clone, cancel := CloneForReplication(req, time.Minute)
		defer cancel()

		v, ok := octollm.GetCtxValue[octollm.APIFormat](clone, octollm.ContextKeyOriginalFormat)
		assert.True(t, ok)
		assert.Equal(t, octollm.APIFormatChatCompletions, v)
	})
}
