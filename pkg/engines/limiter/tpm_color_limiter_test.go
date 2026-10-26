package limiter

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/engines/load-balancer/common"
	"github.com/infinigence/octollm/pkg/octollm"
)

func TestNewTPMColorLimiterEngine_Validation(t *testing.T) {
	_, client := newTPMTestRedis(t)
	next := &nextStubEngine{}

	_, err := NewTPMColorLimiterEngine(client, "svc", staticQoS([]int{1}, []int{1}, 0), "ns", nil)
	assert.Error(t, err)

	_, err = NewTPMColorLimiterEngine(nil, "svc", staticQoS([]int{1}, []int{1}, 0), "ns", next)
	assert.Error(t, err)

	_, err = NewTPMColorLimiterEngine(client, "svc", staticQoS([]int{1, 2}, []int{1}, 0), "ns", next)
	assert.Error(t, err)

	_, err = NewTPMColorLimiterEngine(client, "svc", staticQoS([]int{1}, []int{1}, -0.1), "ns", next)
	assert.Error(t, err)

	_, err = NewTPMColorLimiterEngine(client, "svc", nil, "ns", next)
	assert.ErrorContains(t, err, "qos limits must be provided")

	_, err = NewTPMColorLimiterEngine(client, "svc", &QoSLimits{}, "ns", next)
	assert.ErrorContains(t, err, "empty")

	_, err = NewTPMColorLimiterEngineWithLimitsFunc(client, "svc", "ns", nil, next)
	assert.ErrorContains(t, err, "limitsFunc")

	e, err := NewTPMColorLimiterEngine(client, "svc", staticQoS([]int{1}, []int{1}, 0), "ns", next)
	require.NoError(t, err)
	assert.NotNil(t, e.limitsFunc)
}

func TestTPMColorLimiter_LimitsFuncNilFails(t *testing.T) {
	mr, client := newTPMTestRedis(t)
	next := &nextStubEngine{}
	e, err := NewTPMColorLimiterEngineWithLimitsFunc(client, "svc", "ns", func(*octollm.Request) (*QoSLimits, error) {
		return nil, nil
	}, next)
	require.NoError(t, err)

	_, err = e.Process(newLimiterTestRequest(t))
	assert.ErrorContains(t, err, "qos limits must be provided")
	assert.NotErrorIs(t, err, ErrRateLimitReached)
	assert.Equal(t, 0, next.callCount)
	assert.Empty(t, mr.Keys())
}

func TestTPMColorLimiter_RecordsIntoOwnAndPublic(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			_, client := newTPMTestRedis(t)
			e, err := NewTPMColorLimiterEngine(client, "svc", staticQoS([]int{300, 300, 300}, []int{400, 380, 360}, 0), "ns", usageEngine(25, stream))
			require.NoError(t, err)

			p := 1
			req := newConcurrencyColorLimiterTestRequest(t, "ns", &p)
			resp, err := e.Process(req)
			require.NoError(t, err)

			lp, _ := GetLimiterPriority(req)
			la, _ := GetLimiterAction(req)
			assert.Equal(t, 1, lp)
			assert.Equal(t, ColorActionAllow, la)

			assert.Equal(t, int64(0), mustReadTPM(t, client, "svc:public"))
			closeResponse(t, resp)
			// priority 1 of 3 bands is QoS index 1, i.e. own_2.
			assert.Equal(t, int64(25), mustReadTPM(t, client, "svc:own_2"))
			assert.Equal(t, int64(25), mustReadTPM(t, client, "svc:public"))
			assert.Equal(t, int64(0), mustReadTPM(t, client, "svc:own_1"))
		})
	}
}

func TestTPMColorLimiter_Admission(t *testing.T) {
	// own caps [300 300 300], public caps [400 380 360] for priority 2, 1, 0.
	limits := staticQoS([]int{300, 300, 300}, []int{400, 380, 360}, 0.1)

	tests := []struct {
		name      string
		priority  *int
		kind      common.CacheHitKind
		own       map[string]int64
		public    int64
		wantAllow bool
	}{
		{"idle backend admits", intPtr(2), common.CacheMiss, nil, 0, true},
		{"own below cap", intPtr(2), common.CacheMiss, map[string]int64{"svc:own_1": 299}, 299, true},
		{"own at cap", intPtr(2), common.CacheMiss, map[string]int64{"svc:own_1": 300}, 300, false},
		{"other band's own does not count", intPtr(2), common.CacheMiss, map[string]int64{"svc:own_2": 300}, 300, true},
		{"public at priority 2 cap", intPtr(2), common.CacheMiss, nil, 400, false},
		{"public room for priority 2 only", intPtr(2), common.CacheMiss, nil, 370, true},
		{"public at priority 1 cap", intPtr(1), common.CacheMiss, nil, 380, false},
		{"public at priority 0 cap", intPtr(0), common.CacheMiss, nil, 360, false},
		{"missing priority uses lowest band", nil, common.CacheMiss, nil, 360, false},
		{"priority above bands uses highest", intPtr(9), common.CacheMiss, nil, 399, true},
		{"strong hit headroom lifts public cap", intPtr(0), common.CacheHitStrong, nil, 395, true},
		{"strong hit headroom lifts own cap", intPtr(2), common.CacheHitStrong, map[string]int64{"svc:own_1": 329}, 329, true},
		{"strong hit beyond headroom", intPtr(0), common.CacheHitStrong, nil, 396, false},
		{"weak hit gets no headroom", intPtr(0), common.CacheHitWeak, nil, 360, false},
		{"invalid kind uses miss", intPtr(0), common.CacheHitKind("bogus"), nil, 360, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, client := newTPMTestRedis(t)
			for k, v := range tt.own {
				mustRecordTPM(t, client, k, v)
			}
			if tt.public > 0 {
				mustRecordTPM(t, client, "svc:public", tt.public)
			}

			next := &nextStubEngine{}
			e, err := NewTPMColorLimiterEngine(client, "svc", limits, "ns", next)
			require.NoError(t, err)

			req := withHitKind(newConcurrencyColorLimiterTestRequest(t, "ns", tt.priority), tt.kind)
			_, err = e.Process(req)
			la, _ := GetLimiterAction(req)
			if tt.wantAllow {
				assert.NoError(t, err)
				assert.Equal(t, 1, next.callCount)
				assert.Equal(t, ColorActionAllow, la)
			} else {
				if tt.kind == common.CacheHitStrong {
					assert.ErrorIs(t, err, ErrTPMStrongCacheRateLimitReached)
				} else {
					assert.ErrorIs(t, err, ErrTPMRateLimitReached)
					assert.NotErrorIs(t, err, ErrTPMStrongCacheRateLimitReached)
				}
				assert.ErrorIs(t, err, ErrRateLimitReached)
				assert.Equal(t, 0, next.callCount)
				assert.Equal(t, ColorActionDeny, la)
			}
		})
	}
}

func TestTPMColorLimiter_ZeroCapRejects(t *testing.T) {
	_, client := newTPMTestRedis(t)
	e, err := NewTPMColorLimiterEngine(client, "svc", staticQoS([]int{0}, []int{100}, 0), "ns", &nextStubEngine{})
	require.NoError(t, err)

	_, err = e.Process(newLimiterTestRequest(t))
	assert.ErrorIs(t, err, ErrTPMRateLimitReached)
}

func TestTPMColorLimiter_LimitsFunc(t *testing.T) {
	_, client := newTPMTestRedis(t)
	mustRecordTPM(t, client, "svc:public", 500)

	capacity := 1000
	e, err := NewTPMColorLimiterEngineWithLimitsFunc(client, "svc", "ns", func(*octollm.Request) (*QoSLimits, error) {
		// d_load style: the caller cuts the public cap (available_total_max) by a live capacity.
		return staticQoS([]int{10000}, []int{min(10000, capacity)}, 0), nil
	}, &nextStubEngine{})
	require.NoError(t, err)

	_, err = e.Process(newLimiterTestRequest(t))
	assert.NoError(t, err)

	capacity = 500
	_, err = e.Process(newLimiterTestRequest(t))
	assert.ErrorIs(t, err, ErrTPMRateLimitReached)
}

func TestTPMColorLimiter_LimitsFuncErrors(t *testing.T) {
	_, client := newTPMTestRedis(t)
	boom := errors.New("boom")
	next := &nextStubEngine{}

	e, err := NewTPMColorLimiterEngineWithLimitsFunc(client, "svc", "ns", func(*octollm.Request) (*QoSLimits, error) {
		return nil, boom
	}, next)
	require.NoError(t, err)
	_, err = e.Process(newLimiterTestRequest(t))
	assert.ErrorIs(t, err, boom)

	e, err = NewTPMColorLimiterEngineWithLimitsFunc(client, "svc", "ns", func(*octollm.Request) (*QoSLimits, error) {
		return staticQoS([]int{1, 2}, []int{1}, 0), nil
	}, next)
	require.NoError(t, err)
	_, err = e.Process(newLimiterTestRequest(t))
	assert.Error(t, err)
	assert.Equal(t, 0, next.callCount)
}

func TestTPMColorLimiter_RedisError(t *testing.T) {
	mr, client := newTPMTestRedis(t)
	next := &nextStubEngine{}
	e, err := NewTPMColorLimiterEngine(client, "svc", staticQoS([]int{1}, []int{1}, 0), "ns", next)
	require.NoError(t, err)
	mr.Close()

	_, err = e.Process(newLimiterTestRequest(t))
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrRateLimitReached)
	assert.Equal(t, 0, next.callCount)
}

func TestTPMColorLimiter_NoRecordOnNextError(t *testing.T) {
	mr, client := newTPMTestRedis(t)
	boom := errors.New("boom")
	e, err := NewTPMColorLimiterEngine(client, "svc", staticQoS([]int{100}, []int{100}, 0), "ns", &nextStubEngine{err: boom})
	require.NoError(t, err)

	_, err = e.Process(newLimiterTestRequest(t))
	assert.ErrorIs(t, err, boom)
	assert.Empty(t, mr.Keys())
}

// TestTPMColorMarkerAndLimiter_Chain wires the main path of the design: the
// marker colors by tenant TPM, then the backend limiter admits by that color.
func TestTPMColorMarkerAndLimiter_Chain(t *testing.T) {
	_, client := newTPMTestRedis(t)
	mustRecordTPM(t, client, "tenant", 1000) // tenant is in its second band: priority 1
	mustRecordTPM(t, client, "svc:public", 370)

	lim, err := NewTPMColorLimiterEngine(client, "svc", staticQoS([]int{300, 300, 300}, []int{400, 380, 360}, 0), "ns", usageEngine(5, false))
	require.NoError(t, err)
	marker, err := NewTPMColorMarkerEngine(client, "tenant", []int{1000, 600, 300}, "ns", lim)
	require.NoError(t, err)

	req := newLimiterTestRequest(t)
	resp, err := marker.Process(req)
	require.NoError(t, err)
	closeResponse(t, resp)

	mp, _ := GetMarkerPriority(req)
	lp, _ := GetLimiterPriority(req)
	assert.Equal(t, 1, mp)
	assert.Equal(t, 1, lp)
	assert.Equal(t, int64(1005), mustReadTPM(t, client, "tenant"))
	assert.Equal(t, int64(5), mustReadTPM(t, client, "svc:own_2"))
	assert.Equal(t, int64(375), mustReadTPM(t, client, "svc:public"))

	// Past the priority 1 public cap of 380, the next priority 1 request is rejected.
	mustRecordTPM(t, client, "svc:public", 5)
	_, err = marker.Process(newLimiterTestRequest(t))
	assert.ErrorIs(t, err, ErrTPMRateLimitReached)
}

func TestTPMColorLimiter_ReservationSeenByConcurrentRequests(t *testing.T) {
	_, client := newTPMTestRedis(t)
	e, err := NewTPMColorLimiterEngine(client, "svc", staticQoS([]int{150}, []int{1000}, 0), "ns", usageEngine(10, false))
	require.NoError(t, err)
	e.WithTokenEstimate(estimateOf(80))

	// No response is closed: the second request sees own TPM 80, the third 160.
	for range 2 {
		_, err := e.Process(newLimiterTestRequest(t))
		require.NoError(t, err)
	}
	_, err = e.Process(newLimiterTestRequest(t))
	assert.ErrorIs(t, err, ErrTPMRateLimitReached)
	assert.Equal(t, int64(160), mustReadTPM(t, client, "svc:own_1"))
	assert.Equal(t, int64(160), mustReadTPM(t, client, "svc:public"))
}

func TestTPMColorLimiter_SettlesReservationInOwnAndPublic(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name    string
		next    octollm.Engine
		wantErr error
		want    int64
	}{
		{"settled to usage", usageEngine(30, false), nil, 30},
		{"settled to stream usage", usageEngine(130, true), nil, 130},
		{"kept without usage", &nextStubEngine{}, nil, 80},
		{"refunded when next fails", &nextStubEngine{err: boom}, boom, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, client := newTPMTestRedis(t)
			e, err := NewTPMColorLimiterEngine(client, "svc", staticQoS([]int{1000}, []int{1000}, 0), "ns", tt.next)
			require.NoError(t, err)
			e.WithTokenEstimate(estimateOf(80))

			resp, err := e.Process(newLimiterTestRequest(t))
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, int64(80), mustReadTPM(t, client, "svc:public"))
				closeResponse(t, resp)
			}
			assert.Equal(t, tt.want, mustReadTPM(t, client, "svc:own_1"))
			assert.Equal(t, tt.want, mustReadTPM(t, client, "svc:public"))
		})
	}
}

// TestTPMColorMarkerAndLimiter_LimiterRejectRefundsMarker checks that a request
// the backend limiter rejects gives back what the marker reserved for it.
func TestTPMColorMarkerAndLimiter_LimiterRejectRefundsMarker(t *testing.T) {
	_, client := newTPMTestRedis(t)
	mustRecordTPM(t, client, "svc:public", 1000)

	lim, err := NewTPMColorLimiterEngine(client, "svc", staticQoS([]int{1000}, []int{1000}, 0), "ns", usageEngine(5, false))
	require.NoError(t, err)
	lim.WithTokenEstimate(estimateOf(80))
	marker, err := NewTPMColorMarkerEngine(client, "tenant", []int{1000}, "ns", lim)
	require.NoError(t, err)
	marker.WithTokenEstimate(estimateOf(80))

	_, err = marker.Process(newLimiterTestRequest(t))
	assert.ErrorIs(t, err, ErrTPMRateLimitReached)
	assert.Equal(t, int64(0), mustReadTPM(t, client, "tenant"))
	assert.Equal(t, int64(1000), mustReadTPM(t, client, "svc:public"))
}
