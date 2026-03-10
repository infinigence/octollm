package limiter

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"

	"github.com/infinigence/octollm/pkg/octollm"
)

func newRequestColorLimiterTestRequest(t *testing.T, nameSpace string, priority int) *octollm.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com", strings.NewReader("body"))
	assert.NoError(t, err)
	r := octollm.NewRequest(req, octollm.APIFormatUnknown)
	ctx := context.WithValue(r.Context(), contextKey(fmt.Sprintf("%s:%s", nameSpace, ContextKeyPriority)),
		fmt.Sprintf("%s%d", ContextValuePrefixPriority, priority))
	return r.WithContext(ctx)
}

func TestNewRequestColorLimiterEngine_Validation(t *testing.T) {
	next := &nextStubEngine{}

	// next must not be nil
	_, err := NewRequestColorLimiterEngine(nil, "k", []int{100}, time.Minute, "ns", nil)
	assert.Error(t, err)

	// empty limits -> disabled (pass-through)
	e, err := NewRequestColorLimiterEngine(nil, "k", nil, time.Minute, "ns", next)
	assert.NoError(t, err)
	assert.Nil(t, e.limits)
	assert.Nil(t, e.ttls)

	// window must be positive
	_, err = NewRequestColorLimiterEngine(nil, "k", []int{100}, 0, "ns", next)
	assert.Error(t, err)

	// valid config
	e, err = NewRequestColorLimiterEngine(nil, "k", []int{200, 100, 50}, time.Minute, "ns", next)
	assert.NoError(t, err)
	assert.Len(t, e.limits, 3)
	assert.Len(t, e.ttls, 3)
}

func TestRequestColorLimiter_PassThroughWhenDisabled(t *testing.T) {
	next := &nextStubEngine{}

	e, err := NewRequestColorLimiterEngine(nil, "k", nil, time.Minute, "ns", next)
	assert.NoError(t, err)

	req := newRequestColorLimiterTestRequest(t, "ns", 0)
	resp, err := e.Process(req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, 1, next.callCount)
}

func TestRequestColorLimiter_PriorityInContext(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{}
	ns := "limiter-ns"

	// limits=[5, 3, 2]: tier 0=5, tier 1=3, tier 2=2. All share tier 0.
	// Use separate keyPrefix per test to isolate (tier 0 is shared within same keyPrefix)
	e, err := NewRequestColorLimiterEngine(client, "limiter-p2", []int{5, 3, 2}, time.Minute, ns, next)
	assert.NoError(t, err)

	// Priority 2 (tier 0 only): 5 requests allowed
	for i := 0; i < 5; i++ {
		req := newRequestColorLimiterTestRequest(t, ns, 2)
		resp, err := e.Process(req)
		assert.NoError(t, err)
		assert.NotNil(t, resp)
	}
	assert.Equal(t, 5, next.callCount)

	// 6th with priority 2 -> rejected
	req := newRequestColorLimiterTestRequest(t, ns, 2)
	_, err = e.Process(req)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrRateLimitReached)
	assert.Equal(t, 5, next.callCount)

	// Priority 1: use fresh engine/keyPrefix so tier 0 has tokens
	e1, _ := NewRequestColorLimiterEngine(client, "limiter-p1", []int{5, 3, 2}, time.Minute, ns, next)
	for i := 0; i < 3; i++ {
		req := newRequestColorLimiterTestRequest(t, ns, 1)
		resp, err := e1.Process(req)
		assert.NoError(t, err)
		assert.NotNil(t, resp)
	}
	assert.Equal(t, 8, next.callCount)

	req = newRequestColorLimiterTestRequest(t, ns, 1)
	_, err = e1.Process(req)
	assert.Error(t, err)
	assert.Equal(t, 8, next.callCount)

	// Priority 0: use fresh engine/keyPrefix
	e0, _ := NewRequestColorLimiterEngine(client, "limiter-p0", []int{5, 3, 2}, time.Minute, ns, next)
	for i := 0; i < 2; i++ {
		req := newRequestColorLimiterTestRequest(t, ns, 0)
		resp, err := e0.Process(req)
		assert.NoError(t, err)
		assert.NotNil(t, resp)
	}
	assert.Equal(t, 10, next.callCount)

	req = newRequestColorLimiterTestRequest(t, ns, 0)
	_, err = e0.Process(req)
	assert.Error(t, err)
	assert.Equal(t, 10, next.callCount)
}

func TestRequestColorLimiter_MarkerChain(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{}
	ns := "chain-ns"

	// Marker: limits=[2, 5, 10]. Limiter: limits=[10, 5, 2], shared tier 0.
	// 8 pass: 2 p2 + 3 p1 + 2 p0 (limiter tier0 and tier2 constrain p0 to 2)
	limiter, err := NewRequestColorLimiterEngine(client, "chain-limiter", []int{10, 5, 2}, time.Minute, ns, next)
	assert.NoError(t, err)

	marker, err := NewRequestColorMarkerEngine(client, "chain-marker", []int{2, 5, 10}, time.Minute, ns, limiter)
	assert.NoError(t, err)

	// Expect 8 through: 2 p2 + 3 p1 + 2 p0 (limiter tier0 and tier2 constrain p0 to 2)
	for i := 0; i < 8; i++ {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com", strings.NewReader("body"))
		r := octollm.NewRequest(req, octollm.APIFormatUnknown)
		resp, err := marker.Process(r)
		assert.NoError(t, err)
		assert.NotNil(t, resp)
	}
	assert.Equal(t, 8, next.callCount)

	// 9th: marker may allow (tier 2 has 3 left) but limiter rejects; or marker rejects
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com", strings.NewReader("body"))
	r := octollm.NewRequest(req, octollm.APIFormatUnknown)
	_, err = marker.Process(r)
	assert.Error(t, err)
	assert.Equal(t, 8, next.callCount)
}

func TestRequestColorLimiter_NoPriorityDefaultsToLowest(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{}

	e, err := NewRequestColorLimiterEngine(client, "limiter-noprio", []int{10, 2}, time.Minute, "ns", next)
	assert.NoError(t, err)

	// Request without priority in context -> defaults to priority 0 (lowest tier, 2 requests)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com", strings.NewReader("body"))
	r := octollm.NewRequest(req, octollm.APIFormatUnknown)

	for i := 0; i < 2; i++ {
		resp, err := e.Process(r)
		assert.NoError(t, err)
		assert.NotNil(t, resp)
	}
	assert.Equal(t, 2, next.callCount)

	// 3rd rejected
	_, err = e.Process(r)
	assert.Error(t, err)
	assert.Equal(t, 2, next.callCount)
}
