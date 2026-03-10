package limiter

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/infinigence/octollm/pkg/errutils"
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
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{}

	// limits=[2, 5, 10]: tier 0 allows 2, tier 1 allows 5, tier 2 allows 10
	// tier 0 + tier 2 consumed together when from tier 0; tier 1 + tier 2 when from tier 1; tier 2 only when from tier 2
	e, err := NewRequestColorMarkerEngine(client, "marker-test", []int{2, 5, 10}, time.Minute, "ns", next)
	assert.NoError(t, err)

	// First 2 requests: acquire from tier 0 -> priority 2
	for i := 0; i < 2; i++ {
		req := newRequestColorMarkerTestRequest(t)
		resp, err := e.Process(req)
		assert.NoError(t, err)
		assert.NotNil(t, resp)
	}
	assert.Equal(t, 2, next.callCount)

	// Next 3 requests: tier 0 exhausted, acquire from tier 1 -> priority 1
	for i := 0; i < 3; i++ {
		req := newRequestColorMarkerTestRequest(t)
		resp, err := e.Process(req)
		assert.NoError(t, err)
		assert.NotNil(t, resp)
	}
	assert.Equal(t, 5, next.callCount)

	// Next 5 requests: tier 1 exhausted, acquire from tier 2 only -> priority 0
	for i := 0; i < 5; i++ {
		req := newRequestColorMarkerTestRequest(t)
		resp, err := e.Process(req)
		assert.NoError(t, err)
		assert.NotNil(t, resp)
	}
	assert.Equal(t, 10, next.callCount)

	// 11th request: all tiers exhausted -> rejected
	req := newRequestColorMarkerTestRequest(t)
	resp, err := e.Process(req)
	assert.Error(t, err)
	assert.Nil(t, resp)
	var upErr *errutils.UpstreamRespError
	assert.ErrorAs(t, err, &upErr)
	assert.Equal(t, 429, upErr.StatusCode)
	assert.Equal(t, 10, next.callCount)
}

func TestRequestColorMarker_RefillOverTime(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{}

	// limits=[2, 4], window=30s: tier 0 rate=2/30, refill 1 token in 15s
	e, err := NewRequestColorMarkerEngine(client, "marker-refill", []int{2, 4}, 30*time.Second, "ns", next)
	assert.NoError(t, err)

	// Exhaust all 4 (2 from tier 0+tier1, 2 from tier 1+tier1)
	for i := 0; i < 4; i++ {
		req := newRequestColorMarkerTestRequest(t)
		_, err := e.Process(req)
		assert.NoError(t, err)
	}
	assert.Equal(t, 4, next.callCount)

	// 5th rejected
	req := newRequestColorMarkerTestRequest(t)
	_, err = e.Process(req)
	assert.Error(t, err)
	assert.Equal(t, 4, next.callCount)

	// Wait for refill (~16s for 1 token in tier 0)
	time.Sleep(18 * time.Second)

	req = newRequestColorMarkerTestRequest(t)
	resp, err := e.Process(req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, 5, next.callCount)
}
