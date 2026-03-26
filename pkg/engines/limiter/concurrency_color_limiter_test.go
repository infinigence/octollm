package limiter

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

func TestFilterDecreasingRates(t *testing.T) {
	tests := []struct {
		name          string
		in            []int
		wantOut       []int
		wantFiltered  bool
	}{
		{
			name:         "empty",
			in:           nil,
			wantOut:      nil,
			wantFiltered: false,
		},
		{
			name:         "single",
			in:           []int{10},
			wantOut:      []int{10},
			wantFiltered: false,
		},
		{
			name:         "all_strictly_decreasing",
			in:           []int{100, 10, 5, 1},
			wantOut:      []int{100, 10, 5, 1},
			wantFiltered: false,
		},
		{
			name:         "stops_on_non_decreasing",
			in:           []int{100, 50, 75, 10},
			wantOut:      []int{100, 50},
			wantFiltered: true,
		},
		{
			name:         "stops_on_equal",
			in:           []int{5, 4, 4, 3},
			wantOut:      []int{5, 4},
			wantFiltered: true,
		},
		{
			name:         "second_elem_not_smaller",
			in:           []int{1, 5, 10},
			wantOut:      []int{1},
			wantFiltered: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, filtered := filterDecreasingRates(tt.in)
			assert.Equal(t, tt.wantOut, got)
			assert.Equal(t, tt.wantFiltered, filtered)
		})
	}
}

func newConcurrencyColorLimiterTestRequest(t *testing.T, ns string, priority *int) *octollm.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com", strings.NewReader("body"))
	assert.NoError(t, err)
	r := octollm.NewRequest(req, octollm.APIFormatUnknown)
	if priority == nil {
		return r
	}
	ctx := context.WithValue(r.Context(), contextKey(fmt.Sprintf("%s:%s", ns, ContextKeyPriority)),
		fmt.Sprintf("%s%d", ContextValuePrefixPriority, *priority))
	return r.WithContext(ctx)
}

func TestNewConcurrencyColorLimiterEngine_ValidationAndFiltering(t *testing.T) {
	next := &nextStubEngine{}

	// empty rates -> disabled (pass-through)
	e, err := NewConcurrencyColorLimiterEngine(nil, "k", nil, time.Second, "ns", next)
	assert.NoError(t, err)
	assert.Nil(t, e.concurrencyRates)
	assert.Nil(t, e.acquireSingleScript)
	assert.Nil(t, e.acquireDualScript)

	// timeout must be positive when enabled
	_, err = NewConcurrencyColorLimiterEngine(nil, "k", []int{2, 1}, 0, "ns", next)
	assert.Error(t, err)

	// non-decreasing rates get filtered (stop at first violation)
	e, err = NewConcurrencyColorLimiterEngine(nil, "k", []int{10, 5, 5, 1}, time.Second, "ns", next)
	assert.NoError(t, err)
	assert.Equal(t, []int{10, 5}, e.concurrencyRates)
}

func TestConcurrencyColorLimiter_PassThroughWhenDisabled(t *testing.T) {
	next := &nextStubEngine{resp: &octollm.Response{StatusCode: 200}}
	e, err := NewConcurrencyColorLimiterEngine(nil, "k", nil, time.Second, "ns", next)
	assert.NoError(t, err)

	req := newConcurrencyColorLimiterTestRequest(t, "ns", nil)
	resp, err := e.Process(req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, 1, next.callCount)
}

func TestConcurrencyColorLimiter_PriorityMappingAndReservation(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ns := "limiter-ns"
	next := &nextStubEngine{resp: &octollm.Response{StatusCode: 200, Body: octollm.NewBodyFromBytes([]byte("ok"), nil)}}

	// rates=[2,1] supports priority 1 (tier0, limit2) and priority 0 (tier1, limit1 with reservedSlots=1)
	e, err := NewConcurrencyColorLimiterEngine(client, "limiter-key", []int{2, 1}, 10*time.Second, ns, next)
	assert.NoError(t, err)

	// Hold one max-priority slot (tier0Count becomes 1)
	p1 := 1
	resp1, err := e.Process(newConcurrencyColorLimiterTestRequest(t, ns, &p1))
	assert.NoError(t, err)
	assert.NotNil(t, resp1)

	// Now priority 0 should be denied because it requires tier0Count < (2-1)=1, but tier0Count is 1
	p0 := 0
	_, err = e.Process(newConcurrencyColorLimiterTestRequest(t, ns, &p0))
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrRateLimitReached)

	// Release and try again: should allow
	assert.NoError(t, resp1.Body.Close())
	resp2, err := e.Process(newConcurrencyColorLimiterTestRequest(t, ns, &p0))
	assert.NoError(t, err)
	assert.NotNil(t, resp2)
	assert.NoError(t, resp2.Body.Close())
}

func TestConcurrencyColorLimiter_NoPriorityDefaultsToLowest(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ns := "ns"
	next := &nextStubEngine{resp: &octollm.Response{StatusCode: 200, Body: octollm.NewBodyFromBytes([]byte("ok"), nil)}}

	// rates=[10,1] => default priority 0 maps to tier1, limit 1
	e, err := NewConcurrencyColorLimiterEngine(client, "noprio-key", []int{10, 1}, 10*time.Second, ns, next)
	assert.NoError(t, err)

	// first allowed
	resp1, err := e.Process(newConcurrencyColorLimiterTestRequest(t, ns, nil))
	assert.NoError(t, err)
	assert.NotNil(t, resp1)

	// second denied while first not closed
	_, err = e.Process(newConcurrencyColorLimiterTestRequest(t, ns, nil))
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrRateLimitReached)

	assert.NoError(t, resp1.Body.Close())
}

type nextNewBodyPerCallEngine struct {
	callCount int
}

func (n *nextNewBodyPerCallEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	n.callCount++
	// Important: return a fresh Body each time so OnClose doesn't get stacked on the same instance.
	return &octollm.Response{
		StatusCode: 200,
		Body:       octollm.NewBodyFromBytes([]byte("ok"), nil),
	}, nil
}

func TestConcurrencyColor_MarkerLimiterChain_MarkerBottleneck_Allows2ThenRejects(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ns := "chain-ns"

	// Marker tiers allow 1 max-priority at once, and share last tier with it (rates=[1,2]).
	// Expected: request #1 -> priority 1 (occupies tier0+tier1), request #2 -> priority 0 (occupies tier1),
	// request #3 -> rejected because marker tier1 is full.
	markerRates := []int{1, 2}

	// Limiter should not be the bottleneck for this test.
	limiterRates := []int{3, 2}

	next := &nextNewBodyPerCallEngine{}
	limiter, err := NewConcurrencyColorLimiterEngine(client, "chain-limiter", limiterRates, 10*time.Second, ns, next)
	assert.NoError(t, err)
	marker, err := NewConcurrencyColorMarkerEngine(client, "chain-marker", markerRates, 10*time.Second, ns, limiter)
	assert.NoError(t, err)

	resp1, err := marker.Process(newConcurrencyColorLimiterTestRequest(t, ns, nil))
	assert.NoError(t, err)
	assert.NotNil(t, resp1)

	resp2, err := marker.Process(newConcurrencyColorLimiterTestRequest(t, ns, nil))
	assert.NoError(t, err)
	assert.NotNil(t, resp2)

	_, err = marker.Process(newConcurrencyColorLimiterTestRequest(t, ns, nil))
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrRateLimitReached)
	assert.Equal(t, 2, next.callCount, "limiter.next should be called only for the allowed requests")

	assert.NoError(t, resp1.Body.Close())
	assert.NoError(t, resp2.Body.Close())
}

func TestConcurrencyColor_MarkerLimiterChain_LimiterBottleneck_Allows1ThenRejectsAndCleansUp(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ns := "chain-ns-2"

	// Same marker as the previous test: request #2 becomes priority 0 after tier0 is full.
	markerRates := []int{1, 2}

	// Limiter reservation makes priority 0 require tier0Count < (tier0Limit-1).
	// With tier0Limit=2 and after priority 1 held (tier0Count=1), priority 0 will be denied.
	limiterRates := []int{2, 1}

	next := &nextNewBodyPerCallEngine{}
	limiter, err := NewConcurrencyColorLimiterEngine(client, "chain-limiter-2", limiterRates, 10*time.Second, ns, next)
	assert.NoError(t, err)
	marker, err := NewConcurrencyColorMarkerEngine(client, "chain-marker-2", markerRates, 10*time.Second, ns, limiter)
	assert.NoError(t, err)

	resp1, err := marker.Process(newConcurrencyColorLimiterTestRequest(t, ns, nil))
	assert.NoError(t, err)
	assert.NotNil(t, resp1)
	assert.Equal(t, 1, next.callCount)

	_, err = marker.Process(newConcurrencyColorLimiterTestRequest(t, ns, nil))
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrRateLimitReached)
	assert.Equal(t, 1, next.callCount, "denied request should not reach limiter.next")

	// If marker cleanup failed for the denied request, this third request could be blocked by leftover marker slots.
	assert.NoError(t, resp1.Body.Close())

	resp3, err := marker.Process(newConcurrencyColorLimiterTestRequest(t, ns, nil))
	assert.NoError(t, err)
	assert.NotNil(t, resp3)
	assert.Equal(t, 2, next.callCount)

	assert.NoError(t, resp3.Body.Close())
}

