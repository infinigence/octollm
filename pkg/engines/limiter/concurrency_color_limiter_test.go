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
		name         string
		in           []int
		wantOut      []int
		wantFiltered bool
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
			name:         "all_non_increasing",
			in:           []int{100, 10, 5, 1},
			wantOut:      []int{100, 10, 5, 1},
			wantFiltered: false,
		},
		{
			name:         "stops_on_increase",
			in:           []int{100, 50, 75, 10},
			wantOut:      []int{100, 50},
			wantFiltered: true,
		},
		{
			name:         "allows_equal_plateau",
			in:           []int{5, 4, 4, 3},
			wantOut:      []int{5, 4, 4, 3},
			wantFiltered: false,
		},
		{
			name:         "stops_after_plateau_on_increase",
			in:           []int{5, 4, 4, 5},
			wantOut:      []int{5, 4, 4},
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

func TestBuildTotalPlusPerPriorityLimits(t *testing.T) {
	got, err := buildTotalPlusPerPriorityLimits(80, []int{70, 50, 30})
	assert.NoError(t, err)
	assert.Equal(t, []int{80, 70, 50, 30}, got)

	got, err = buildTotalPlusPerPriorityLimits(80, []int{0, 50, 30})
	assert.NoError(t, err)
	assert.Equal(t, []int{80, 0, 50, 30}, got)

	got, err = buildTotalPlusPerPriorityLimits(0, []int{70, 50, 30})
	assert.NoError(t, err)
	assert.Equal(t, []int{150, 70, 50, 30}, got)

	got, err = buildTotalPlusPerPriorityLimits(0, []int{-1, 50})
	assert.NoError(t, err)
	assert.Equal(t, []int{50, 0, 50}, got)

	got, err = buildTotalPlusPerPriorityLimits(80, []int{80, 50})
	assert.NoError(t, err)
	assert.Equal(t, []int{80, 80, 50}, got)

	got, err = buildTotalPlusPerPriorityLimits(10, []int{5, 6, 1})
	assert.NoError(t, err)
	assert.Equal(t, []int{10, 5, 6, 1}, got)

	got, err = buildTotalPlusPerPriorityLimits(5, nil)
	assert.NoError(t, err)
	assert.Equal(t, []int{5}, got)

	got, err = buildTotalPlusPerPriorityLimits(0, nil)
	assert.NoError(t, err)
	assert.Nil(t, got)
}

func TestNewConcurrencyColorLimiterEngine_ValidationAndFiltering(t *testing.T) {
	next := &nextStubEngine{}

	e, err := NewConcurrencyColorLimiterEngine(nil, "k", 0, nil, time.Second, "ns", next)
	assert.NoError(t, err)
	assert.Nil(t, e.concurrencyRates)
	assert.Nil(t, e.acquireSingleScript)
	assert.Nil(t, e.acquireDualScript)

	_, err = NewConcurrencyColorLimiterEngine(nil, "k", 2, []int{1}, 0, "ns", next)
	assert.Error(t, err)

	e, err = NewConcurrencyColorLimiterEngine(nil, "k", 10, []int{10}, time.Second, "ns", next)
	assert.NoError(t, err)
	assert.Equal(t, []int{10, 10}, e.concurrencyRates)

	e, err = NewConcurrencyColorLimiterEngine(nil, "k", 10, []int{5, 6, 1}, time.Second, "ns", next)
	assert.NoError(t, err)
	assert.Equal(t, []int{10, 5, 6, 1}, e.concurrencyRates)
}

func TestConcurrencyColorLimiter_PassThroughWhenDisabled(t *testing.T) {
	next := &nextStubEngine{resp: &octollm.Response{StatusCode: 200}}
	e, err := NewConcurrencyColorLimiterEngine(nil, "k", 0, nil, time.Second, "ns", next)
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
	e, err := NewConcurrencyColorLimiterEngine(client, "limiter-key", 2, []int{1}, 10*time.Second, ns, next)
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

// [4,3,3,3]: global cap 4 on tier 0; lower tiers 3 each — reservation still caps p2/p1/p0 differently.
func TestConcurrencyColorLimiter_EqualTierLimits_ReservationCaps(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ns := "limiter-ns-eq333"
	next := &nextNewBodyPerCallEngine{}
	e, err := NewConcurrencyColorLimiterEngine(client, "limiter-key-eq333", 4, []int{3, 3, 3}, 10*time.Second, ns, next)
	assert.NoError(t, err)

	p3 := 3
	var held []*octollm.Response
	for i := 0; i < 4; i++ {
		resp, err := e.Process(newConcurrencyColorLimiterTestRequest(t, ns, &p3))
		assert.NoError(t, err, "p3 request %d", i+1)
		assert.NotNil(t, resp)
		held = append(held, resp)
	}
	_, err = e.Process(newConcurrencyColorLimiterTestRequest(t, ns, &p3))
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrRateLimitReached)
	for _, r := range held {
		assert.NoError(t, r.Body.Close())
	}
	held = held[:0]

	p2 := 2
	for i := 0; i < 3; i++ {
		resp, err := e.Process(newConcurrencyColorLimiterTestRequest(t, ns, &p2))
		assert.NoError(t, err, "p2 request %d", i+1)
		assert.NotNil(t, resp)
		held = append(held, resp)
	}
	_, err = e.Process(newConcurrencyColorLimiterTestRequest(t, ns, &p2))
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrRateLimitReached)
	for _, r := range held {
		assert.NoError(t, r.Body.Close())
	}
	held = held[:0]

	p1 := 1
	for i := 0; i < 2; i++ {
		resp, err := e.Process(newConcurrencyColorLimiterTestRequest(t, ns, &p1))
		assert.NoError(t, err, "p1 request %d", i+1)
		assert.NotNil(t, resp)
		held = append(held, resp)
	}
	_, err = e.Process(newConcurrencyColorLimiterTestRequest(t, ns, &p1))
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrRateLimitReached)
	for _, r := range held {
		assert.NoError(t, r.Body.Close())
	}
	held = held[:0]

	p0 := 0
	resp, err := e.Process(newConcurrencyColorLimiterTestRequest(t, ns, &p0))
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	_, err = e.Process(newConcurrencyColorLimiterTestRequest(t, ns, &p0))
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrRateLimitReached)
	assert.NoError(t, resp.Body.Close())
}

func TestConcurrencyColorLimiter_NoPriorityDefaultsToLowest(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ns := "ns"
	next := &nextStubEngine{resp: &octollm.Response{StatusCode: 200, Body: octollm.NewBodyFromBytes([]byte("ok"), nil)}}

	// rates=[10,1] => default priority 0 maps to tier1, limit 1
	e, err := NewConcurrencyColorLimiterEngine(client, "noprio-key", 10, []int{1}, 10*time.Second, ns, next)
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

	// Marker: independent caps [1,1,0] — two non-zero bands (p1 then p0); third has no slot.
	markerRates := []int{1, 1, 0}

	// Limiter: built as concurrency=4, rates=[3,2] -> [4,3,2]; matches three marker priorities.
	next := &nextNewBodyPerCallEngine{}
	limiter, err := NewConcurrencyColorLimiterEngine(client, "chain-limiter", 4, []int{3, 2}, 10*time.Second, ns, next)
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

	// Same marker shape as previous test but with equal low tiers ([1,1,2]).
	markerRates := []int{1, 1, 2}

	// Limiter: concurrency=2, rates=[1,1] -> [2,1,1]; priority 1 needs tier0Count < tier0Limit-reservedSlots = 1,
	// so the second in-flight (tier0 already 1) is rejected at the limiter, not the marker.
	next := &nextNewBodyPerCallEngine{}
	limiter, err := NewConcurrencyColorLimiterEngine(client, "chain-limiter-2", 2, []int{1, 1}, 10*time.Second, ns, next)
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
