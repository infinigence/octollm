package limiter

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

func newRequestColorMarkerTestRequest(t *testing.T) *octollm.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com", strings.NewReader("body"))
	assert.NoError(t, err)
	return octollm.NewRequest(req, octollm.APIFormatUnknown)
}

func TestNewRequestColorMarkerEngine_Validation(t *testing.T) {
	next := &nextStubEngine{}

	// next must not be nil
	_, err := NewRequestColorMarkerEngine(nil, "k", []int{10}, time.Minute, "ns", nil)
	assert.Error(t, err)

	// empty limits -> disabled (pass-through)
	e, err := NewRequestColorMarkerEngine(nil, "k", nil, time.Minute, "ns", next)
	assert.NoError(t, err)
	assert.Nil(t, e.limits)
	assert.Nil(t, e.ttls)

	// window must be positive
	_, err = NewRequestColorMarkerEngine(nil, "k", []int{10}, 0, "ns", next)
	assert.Error(t, err)

	// valid config
	e, err = NewRequestColorMarkerEngine(nil, "k", []int{10, 50, 100}, time.Minute, "ns", next)
	assert.NoError(t, err)
	assert.Len(t, e.limits, 3)
	assert.Len(t, e.ttls, 3)

	// non-monotonic kept (independent per-tier bursts)
	e, err = NewRequestColorMarkerEngine(nil, "k", []int{10, 5, 20}, time.Minute, "ns", next)
	assert.NoError(t, err)
	assert.Equal(t, []int{10, 5, 20}, e.limits)

	// only zeros / negatives -> enabled; every tier has limit 0 so nothing is admitted
	e, err = NewRequestColorMarkerEngine(nil, "k", []int{0, 0, -1}, time.Minute, "ns", next)
	assert.NoError(t, err)
	assert.Equal(t, []int{0, 0, 0}, e.limits)
	assert.NotNil(t, e.tokenBucketScript)
}

func TestRequestColorMarker_PassThroughWhenDisabled(t *testing.T) {
	next := &nextStubEngine{}

	e, err := NewRequestColorMarkerEngine(nil, "k", nil, time.Minute, "ns", next)
	assert.NoError(t, err)

	req := newRequestColorMarkerTestRequest(t)
	resp, err := e.Process(req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, 1, next.callCount)
}

func TestRequestColorMarker_MultiTier_AcquirePriority(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{}

	// limits=[2, 5, 10]: independent bursts per tier; 2+5+10 = 17 requests before exhaustion at t=0
	e, err := NewRequestColorMarkerEngine(client, "marker-test", []int{2, 5, 10}, time.Minute, "ns", next)
	assert.NoError(t, err)

	for i := 0; i < 2; i++ {
		req := newRequestColorMarkerTestRequest(t)
		resp, err := e.Process(req)
		assert.NoError(t, err)
		assert.NotNil(t, resp)
	}
	assert.Equal(t, 2, next.callCount)

	for i := 0; i < 5; i++ {
		req := newRequestColorMarkerTestRequest(t)
		resp, err := e.Process(req)
		assert.NoError(t, err)
		assert.NotNil(t, resp)
	}
	assert.Equal(t, 7, next.callCount)

	for i := 0; i < 10; i++ {
		req := newRequestColorMarkerTestRequest(t)
		resp, err := e.Process(req)
		assert.NoError(t, err)
		assert.NotNil(t, resp)
	}
	assert.Equal(t, 17, next.callCount)

	req := newRequestColorMarkerTestRequest(t)
	resp, err := e.Process(req)
	assert.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, ErrRateLimitReached)
	assert.Equal(t, 17, next.callCount)
}

func TestRequestColorMarker_RefillOverTime(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{}

	// limits=[2, 4], window=30s: 6 independent tokens; tier 0 rate=2/30
	e, err := NewRequestColorMarkerEngine(client, "marker-refill", []int{2, 4}, 30*time.Second, "ns", next)
	assert.NoError(t, err)

	for i := 0; i < 6; i++ {
		req := newRequestColorMarkerTestRequest(t)
		_, err := e.Process(req)
		assert.NoError(t, err)
	}
	assert.Equal(t, 6, next.callCount)

	req := newRequestColorMarkerTestRequest(t)
	_, err = e.Process(req)
	assert.Error(t, err)
	assert.Equal(t, 6, next.callCount)

	// Wait for refill (~16s for 1 token in tier 0)
	time.Sleep(18 * time.Second)

	req = newRequestColorMarkerTestRequest(t)
	resp, err := e.Process(req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, 7, next.callCount)
}
