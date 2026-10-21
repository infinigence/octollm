package limiter

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/infinigence/octollm/pkg/engines/load-balancer/common"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func withHitKind(req *octollm.Request, kind common.CacheHitKind) *octollm.Request {
	return req.WithContext(context.WithValue(req.Context(), common.ContextKeyCacheHitKind, kind))
}

func staticQoS(own, pub []int, ratio float64) *QoSLimits {
	return &QoSLimits{AvailableTotalMax: pub, PriorityLevelMax: own, HeadroomRatio: ratio}
}

func zcard(t *testing.T, client *redis.Client, key string) int64 {
	t.Helper()
	n, err := client.ZCard(context.Background(), key).Result()
	assert.NoError(t, err)
	return n
}

func TestQosIndex(t *testing.T) {
	assert.Equal(t, 2, qosIndex(0, 3))
	assert.Equal(t, 1, qosIndex(1, 3))
	assert.Equal(t, 0, qosIndex(2, 3))
	assert.Equal(t, 0, qosIndex(9, 3))
	assert.Equal(t, 2, qosIndex(-1, 3))
	assert.Equal(t, 0, qosIndex(0, 1))
}

func TestEffectiveOwnAndTotalLimits(t *testing.T) {
	own, total := effectiveOwnAndTotalLimits(100, 95, 0.1, common.CacheMiss)
	assert.Equal(t, 100, own)
	assert.Equal(t, 95, total)

	own, total = effectiveOwnAndTotalLimits(100, 95, 0.1, common.CacheHitWeak)
	assert.Equal(t, 100, own)
	assert.Equal(t, 95, total)

	own, total = effectiveOwnAndTotalLimits(100, 95, 0.1, common.CacheHitStrong)
	assert.Equal(t, 110, own)
	assert.Equal(t, 104, total)

	own, total = effectiveOwnAndTotalLimits(100, 95, 0, common.CacheHitStrong)
	assert.Equal(t, 100, own)
	assert.Equal(t, 95, total)
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

func TestNewConcurrencyColorLimiterEngine_Validation(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{}

	_, err := NewConcurrencyColorLimiterEngine(nil, "k", staticQoS([]int{10}, []int{10}, 0), time.Second, "ns", next)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "redisClient")

	_, err = NewConcurrencyColorLimiterEngine(nil, "k", &QoSLimits{}, time.Second, "ns", next)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty")

	_, err = NewConcurrencyColorLimiterEngine(client, "k", staticQoS([]int{2}, []int{1}, 0), 0, "ns", next)
	assert.Error(t, err)

	_, err = NewConcurrencyColorLimiterEngine(client, "k", staticQoS([]int{1}, []int{1, 2}, 0), time.Second, "ns", next)
	assert.Error(t, err)

	_, err = NewConcurrencyColorLimiterEngine(client, "k", staticQoS([]int{1}, []int{1}, -0.1), time.Second, "ns", next)
	assert.Error(t, err)

	e, err := NewConcurrencyColorLimiterEngine(client, "k", staticQoS([]int{10, 5}, []int{10, 8}, 0.1), time.Second, "ns", next)
	assert.NoError(t, err)
	assert.NotNil(t, e.limitsFunc)
	assert.NotNil(t, e.acquireScript)
	assert.NotNil(t, e.releaseScript)
	assert.NotNil(t, e.renewScript)
}

func TestConcurrencyColorLimiter_PassThroughWhenDisabled(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{resp: &octollm.Response{StatusCode: 200}}
	_, err := NewConcurrencyColorLimiterEngine(client, "k", &QoSLimits{}, time.Second, "ns", next)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty")

	_, err = NewConcurrencyColorLimiterEngineWithLimitsFunc(client, "k2", time.Second, "ns", nil, next)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "limitsFunc")
}

func TestConcurrencyColorLimiter_HighestPriorityWritesBothKeys(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ns := "ns"
	next := &nextStubEngine{resp: &octollm.Response{StatusCode: 200, Body: octollm.NewBodyFromBytes([]byte("ok"), nil)}}
	e, err := NewConcurrencyColorLimiterEngine(client, "hi-key", staticQoS([]int{5, 5}, []int{5, 4}, 0), 10*time.Second, ns, next)
	require.NoError(t, err)

	p := 1
	resp, err := e.Process(newConcurrencyColorLimiterTestRequest(t, ns, &p))
	require.NoError(t, err)
	assert.Equal(t, int64(1), zcard(t, client, "hi-key:own_1"))
	assert.Equal(t, int64(1), zcard(t, client, "hi-key:public"))
	assert.Equal(t, int64(0), zcard(t, client, "hi-key:own_2"))

	require.NoError(t, resp.Body.Close())
	assert.Equal(t, int64(0), zcard(t, client, "hi-key:own_1"))
	assert.Equal(t, int64(0), zcard(t, client, "hi-key:public"))
}

func TestConcurrencyColorLimiter_OwnBucketFull(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ns := "ns"
	next := &nextNewBodyPerCallEngine{}
	e, err := NewConcurrencyColorLimiterEngine(client, "own-key", staticQoS([]int{100, 1}, []int{100, 100}, 0), 10*time.Second, ns, next)
	require.NoError(t, err)

	p0 := 0
	resp, err := e.Process(newConcurrencyColorLimiterTestRequest(t, ns, &p0))
	require.NoError(t, err)
	assert.Equal(t, int64(1), zcard(t, client, "own-key:own_2"))
	assert.Equal(t, int64(1), zcard(t, client, "own-key:public"))

	_, err = e.Process(newConcurrencyColorLimiterTestRequest(t, ns, &p0))
	assert.ErrorIs(t, err, ErrRateLimitReached)

	require.NoError(t, resp.Body.Close())
}

func TestConcurrencyColorLimiter_PublicBucketFull(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ns := "ns"
	next := &nextNewBodyPerCallEngine{}
	e, err := NewConcurrencyColorLimiterEngine(client, "pub-key", staticQoS([]int{100, 100}, []int{1, 1}, 0), 10*time.Second, ns, next)
	require.NoError(t, err)

	pHigh := 1
	resp, err := e.Process(newConcurrencyColorLimiterTestRequest(t, ns, &pHigh))
	require.NoError(t, err)
	assert.Equal(t, int64(1), zcard(t, client, "pub-key:public"))

	pLow := 0
	_, err = e.Process(newConcurrencyColorLimiterTestRequest(t, ns, &pLow))
	assert.ErrorIs(t, err, ErrRateLimitReached)
	assert.Equal(t, int64(0), zcard(t, client, "pub-key:own_2"))

	require.NoError(t, resp.Body.Close())
}

func TestConcurrencyColorLimiter_BothBucketsPassThenRelease(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ns := "ns"
	next := &nextStubEngine{resp: &octollm.Response{StatusCode: 200, Body: octollm.NewBodyFromBytes([]byte("ok"), nil)}}
	e, err := NewConcurrencyColorLimiterEngine(client, "ok-key", staticQoS([]int{2, 2}, []int{2, 2}, 0), 10*time.Second, ns, next)
	require.NoError(t, err)

	p0 := 0
	req := newConcurrencyColorLimiterTestRequest(t, ns, &p0)
	resp, err := e.Process(req)
	require.NoError(t, err)
	assertLimiterAllow(t, req, 0)
	assert.Equal(t, int64(1), zcard(t, client, "ok-key:own_2"))
	assert.Equal(t, int64(1), zcard(t, client, "ok-key:public"))
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, int64(0), zcard(t, client, "ok-key:own_2"))
	assert.Equal(t, int64(0), zcard(t, client, "ok-key:public"))
}

func TestConcurrencyColorLimiter_NoPriorityDefaultsToLowest(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ns := "ns"
	next := &nextStubEngine{resp: &octollm.Response{StatusCode: 200, Body: octollm.NewBodyFromBytes([]byte("ok"), nil)}}
	e, err := NewConcurrencyColorLimiterEngine(client, "noprio-key", staticQoS([]int{10, 1}, []int{10, 10}, 0), 10*time.Second, ns, next)
	require.NoError(t, err)

	resp1, err := e.Process(newConcurrencyColorLimiterTestRequest(t, ns, nil))
	require.NoError(t, err)
	assert.Equal(t, int64(1), zcard(t, client, "noprio-key:own_2"))

	_, err = e.Process(newConcurrencyColorLimiterTestRequest(t, ns, nil))
	assert.ErrorIs(t, err, ErrRateLimitReached)
	require.NoError(t, resp1.Body.Close())
}

func TestConcurrencyColorLimiter_StrongHeadroomVsMiss(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ns := "ns"
	next := &nextNewBodyPerCallEngine{}
	e, err := NewConcurrencyColorLimiterEngine(client, "hr-key", staticQoS([]int{10}, []int{10}, 0.1), 10*time.Second, ns, next)
	require.NoError(t, err)

	var held []*octollm.Response
	p0 := 0
	for i := 0; i < 10; i++ {
		resp, err := e.Process(withHitKind(newConcurrencyColorLimiterTestRequest(t, ns, &p0), common.CacheMiss))
		require.NoError(t, err, "miss %d", i+1)
		held = append(held, resp)
	}
	_, err = e.Process(withHitKind(newConcurrencyColorLimiterTestRequest(t, ns, &p0), common.CacheMiss))
	assert.ErrorIs(t, err, ErrRateLimitReached)
	assert.NotErrorIs(t, err, ErrStrongCacheRateLimitReached)
	_, err = e.Process(withHitKind(newConcurrencyColorLimiterTestRequest(t, ns, &p0), common.CacheHitWeak))
	assert.ErrorIs(t, err, ErrRateLimitReached)
	assert.NotErrorIs(t, err, ErrStrongCacheRateLimitReached)

	strong, err := e.Process(withHitKind(newConcurrencyColorLimiterTestRequest(t, ns, &p0), common.CacheHitStrong))
	require.NoError(t, err)
	assert.Equal(t, int64(11), zcard(t, client, "hr-key:public"))
	assert.Equal(t, int64(11), zcard(t, client, "hr-key:own_1"))

	_, err = e.Process(withHitKind(newConcurrencyColorLimiterTestRequest(t, ns, &p0), common.CacheHitStrong))
	assert.ErrorIs(t, err, ErrStrongCacheRateLimitReached)
	assert.ErrorIs(t, err, ErrRateLimitReached)

	require.NoError(t, strong.Body.Close())
	for _, r := range held {
		require.NoError(t, r.Body.Close())
	}
}

func TestConcurrencyColorLimiter_ZeroHeadroomStrongEqualsMiss(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ns := "ns"
	next := &nextNewBodyPerCallEngine{}
	e, err := NewConcurrencyColorLimiterEngine(client, "hr0-key", staticQoS([]int{1}, []int{1}, 0), 10*time.Second, ns, next)
	require.NoError(t, err)

	p0 := 0
	resp, err := e.Process(withHitKind(newConcurrencyColorLimiterTestRequest(t, ns, &p0), common.CacheMiss))
	require.NoError(t, err)
	_, err = e.Process(withHitKind(newConcurrencyColorLimiterTestRequest(t, ns, &p0), common.CacheHitStrong))
	assert.ErrorIs(t, err, ErrStrongCacheRateLimitReached)
	assert.ErrorIs(t, err, ErrRateLimitReached)
	require.NoError(t, resp.Body.Close())
}

func TestConcurrencyColorLimiter_IllegalHitKindIsMiss(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ns := "ns"
	next := &nextNewBodyPerCallEngine{}
	e, err := NewConcurrencyColorLimiterEngine(client, "badkind-key", staticQoS([]int{1}, []int{1}, 0.1), 10*time.Second, ns, next)
	require.NoError(t, err)

	p0 := 0
	resp, err := e.Process(withHitKind(newConcurrencyColorLimiterTestRequest(t, ns, &p0), common.CacheMiss))
	require.NoError(t, err)
	_, err = e.Process(withHitKind(newConcurrencyColorLimiterTestRequest(t, ns, &p0), common.CacheHitKind("nope")))
	assert.ErrorIs(t, err, ErrRateLimitReached)
	require.NoError(t, resp.Body.Close())
}

type nextNewBodyPerCallEngine struct {
	callCount int
}

func (n *nextNewBodyPerCallEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	n.callCount++
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
	next := &nextNewBodyPerCallEngine{}
	lim, err := NewConcurrencyColorLimiterEngine(client, "chain-limiter", staticQoS([]int{4, 3, 2}, []int{4, 4, 4}, 0), 10*time.Second, ns, next)
	require.NoError(t, err)
	marker, err := NewConcurrencyColorMarkerEngine(client, "chain-marker", []int{1, 1, 0}, 10*time.Second, ns, lim)
	require.NoError(t, err)

	resp1, err := marker.Process(newConcurrencyColorLimiterTestRequest(t, ns, nil))
	require.NoError(t, err)
	resp2, err := marker.Process(newConcurrencyColorLimiterTestRequest(t, ns, nil))
	require.NoError(t, err)
	_, err = marker.Process(newConcurrencyColorLimiterTestRequest(t, ns, nil))
	assert.ErrorIs(t, err, ErrRateLimitReached)
	assert.Equal(t, 2, next.callCount)

	require.NoError(t, resp1.Body.Close())
	require.NoError(t, resp2.Body.Close())
}

func TestConcurrencyColor_MarkerLimiterChain_LimiterBottleneck_Allows1ThenRejectsAndCleansUp(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ns := "chain-ns-2"
	next := &nextNewBodyPerCallEngine{}
	lim, err := NewConcurrencyColorLimiterEngine(client, "chain-limiter-2", staticQoS([]int{10, 10, 10}, []int{1, 1, 1}, 0), 10*time.Second, ns, next)
	require.NoError(t, err)
	marker, err := NewConcurrencyColorMarkerEngine(client, "chain-marker-2", []int{1, 1, 2}, 10*time.Second, ns, lim)
	require.NoError(t, err)

	resp1, err := marker.Process(newConcurrencyColorLimiterTestRequest(t, ns, nil))
	require.NoError(t, err)
	assert.Equal(t, 1, next.callCount)

	_, err = marker.Process(newConcurrencyColorLimiterTestRequest(t, ns, nil))
	assert.ErrorIs(t, err, ErrRateLimitReached)
	assert.Equal(t, 1, next.callCount)

	require.NoError(t, resp1.Body.Close())
	resp3, err := marker.Process(newConcurrencyColorLimiterTestRequest(t, ns, nil))
	require.NoError(t, err)
	assert.Equal(t, 2, next.callCount)
	require.NoError(t, resp3.Body.Close())
}

func TestConcurrencyColorLimiter_LimitsFuncError(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{resp: &octollm.Response{StatusCode: 200}}
	e, err := NewConcurrencyColorLimiterEngineWithLimitsFunc(client, "err-key", 10*time.Second, "ns", func(*octollm.Request) (*QoSLimits, error) {
		return nil, fmt.Errorf("limits resolver failed")
	}, next)
	require.NoError(t, err)

	_, err = e.Process(newConcurrencyColorLimiterTestRequest(t, "ns", nil))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "limits resolver failed")
	assert.Equal(t, 0, next.callCount)
}

func TestConcurrencyColorLimiter_LimitsFuncNilFails(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{resp: &octollm.Response{StatusCode: 200, Body: octollm.NewBodyFromBytes([]byte("ok"), nil)}}
	e, err := NewConcurrencyColorLimiterEngineWithLimitsFunc(client, "empty-key", 10*time.Second, "ns", func(*octollm.Request) (*QoSLimits, error) {
		return nil, nil
	}, next)
	require.NoError(t, err)

	_, err = e.Process(newConcurrencyColorLimiterTestRequest(t, "ns", nil))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "qos limits must be provided")
	assert.Equal(t, 0, next.callCount)
}

func TestConcurrencyColorLimiter_StaticLimitsImmutableAfterInit(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	own := []int{5, 6}
	e, err := NewConcurrencyColorLimiterEngine(client, "k", staticQoS(own, []int{10, 10}, 0), time.Second, "ns", &nextStubEngine{})
	require.NoError(t, err)
	limits, err := e.limitsFunc(newConcurrencyColorLimiterTestRequest(t, "ns", nil))
	require.NoError(t, err)
	assert.Equal(t, []int{5, 6}, limits.PriorityLevelMax)
	own[0] = 999
	limits, err = e.limitsFunc(newConcurrencyColorLimiterTestRequest(t, "ns", nil))
	require.NoError(t, err)
	assert.Equal(t, []int{5, 6}, limits.PriorityLevelMax)
}

func TestConcurrencyColorLimiter_PublicBucketNoOverAdmit(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ns := "ns"
	next := octollm.EngineFunc(func(*octollm.Request) (*octollm.Response, error) {
		return &octollm.Response{StatusCode: 200, Body: octollm.NewBodyFromBytes([]byte("ok"), nil)}, nil
	})
	e, err := NewConcurrencyColorLimiterEngine(client, "race-key", staticQoS([]int{100}, []int{1}, 0), 10*time.Second, ns, next)
	require.NoError(t, err)

	const n = 16
	p0 := 0
	var wg sync.WaitGroup
	var success atomic.Int32
	held := make([]*octollm.Response, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			resp, err := e.Process(newConcurrencyColorLimiterTestRequest(t, ns, &p0))
			if err != nil {
				assert.ErrorIs(t, err, ErrRateLimitReached)
				return
			}
			held[i] = resp
			success.Add(1)
		}(i)
	}
	wg.Wait()

	assert.Equal(t, int32(1), success.Load())
	assert.Equal(t, int64(1), zcard(t, client, "race-key:public"))
	assert.Equal(t, int64(1), zcard(t, client, "race-key:own_1"))
	for _, resp := range held {
		if resp != nil && resp.Body != nil {
			require.NoError(t, resp.Body.Close())
		}
	}
	assert.Equal(t, int64(0), zcard(t, client, "race-key:public"))
	assert.Equal(t, int64(0), zcard(t, client, "race-key:own_1"))
}

func TestConcurrencyColorLimiter_ReleasesOnStreamClose(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ch := make(chan *octollm.StreamChunk)
	stream := octollm.NewStreamChan(ch, func() { close(ch) })
	next := &nextStubEngine{resp: &octollm.Response{StatusCode: 200, Stream: stream}}
	e, err := NewConcurrencyColorLimiterEngine(client, "stream-key", staticQoS([]int{1}, []int{1}, 0), 10*time.Second, "ns", next)
	require.NoError(t, err)

	p0 := 0
	resp, err := e.Process(newConcurrencyColorLimiterTestRequest(t, "ns", &p0))
	require.NoError(t, err)
	require.NotNil(t, resp.Stream)
	assert.Equal(t, int64(1), zcard(t, client, "stream-key:own_1"))
	assert.Equal(t, int64(1), zcard(t, client, "stream-key:public"))

	resp.Stream.Close()
	assert.Equal(t, int64(0), zcard(t, client, "stream-key:own_1"))
	assert.Equal(t, int64(0), zcard(t, client, "stream-key:public"))
}

func TestConcurrencyColorLimiter_ReleasesImmediatelyOnNextErrorOrNilResp(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	p0 := 0

	nextErr := &nextStubEngine{
		resp: &octollm.Response{StatusCode: 200, Body: octollm.NewBodyFromBytes([]byte("x"), nil)},
		err:  assert.AnError,
	}
	eErr, err := NewConcurrencyColorLimiterEngine(client, "next-error-key", staticQoS([]int{1}, []int{1}, 0), 10*time.Second, "ns", nextErr)
	require.NoError(t, err)
	_, err = eErr.Process(newConcurrencyColorLimiterTestRequest(t, "ns", &p0))
	assert.Error(t, err)
	assert.Equal(t, int64(0), zcard(t, client, "next-error-key:own_1"))
	assert.Equal(t, int64(0), zcard(t, client, "next-error-key:public"))

	eNil, err := NewConcurrencyColorLimiterEngine(client, "next-nil-key", staticQoS([]int{1}, []int{1}, 0), 10*time.Second, "ns", nilRespEngine{})
	require.NoError(t, err)
	resp, err := eNil.Process(newConcurrencyColorLimiterTestRequest(t, "ns", &p0))
	assert.NoError(t, err)
	assert.Nil(t, resp)
	assert.Equal(t, int64(0), zcard(t, client, "next-nil-key:own_1"))
	assert.Equal(t, int64(0), zcard(t, client, "next-nil-key:public"))
}

func TestConcurrencyColorLimiter_AcquireDropsExpiredMembers(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ns := "ns"
	next := &nextNewBodyPerCallEngine{}
	e, err := NewConcurrencyColorLimiterEngine(client, "exp-key", staticQoS([]int{1}, []int{1}, 0), 10*time.Second, ns, next)
	require.NoError(t, err)

	staleScore := float64(time.Now().Add(-30 * time.Second).Unix())
	ctx := context.Background()
	require.NoError(t, client.ZAdd(ctx, "exp-key:own_1", redis.Z{Score: staleScore, Member: "stale"}).Err())
	require.NoError(t, client.ZAdd(ctx, "exp-key:public", redis.Z{Score: staleScore, Member: "stale"}).Err())

	p0 := 0
	resp, err := e.Process(newConcurrencyColorLimiterTestRequest(t, ns, &p0))
	require.NoError(t, err)
	assert.Equal(t, int64(1), zcard(t, client, "exp-key:own_1"))
	assert.Equal(t, int64(1), zcard(t, client, "exp-key:public"))
	_, err = client.ZScore(ctx, "exp-key:public", "stale").Result()
	assert.ErrorIs(t, err, redis.Nil)
	require.NoError(t, resp.Body.Close())
}

func TestConcurrencyColorLimiter_LimitsFuncDynamicCaps(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ns := "ns"
	var calls atomic.Int32
	e, err := NewConcurrencyColorLimiterEngineWithLimitsFunc(client, "dyn-key", 10*time.Second, ns, func(*octollm.Request) (*QoSLimits, error) {
		if calls.Add(1) == 1 {
			return &QoSLimits{AvailableTotalMax: []int{1}, PriorityLevelMax: []int{1}}, nil
		}
		return &QoSLimits{AvailableTotalMax: []int{2}, PriorityLevelMax: []int{2}}, nil
	}, &nextNewBodyPerCallEngine{})
	require.NoError(t, err)

	p0 := 0
	resp1, err := e.Process(newConcurrencyColorLimiterTestRequest(t, ns, &p0))
	require.NoError(t, err)
	assert.Equal(t, int64(1), zcard(t, client, "dyn-key:own_1"))
	assert.Equal(t, int64(1), zcard(t, client, "dyn-key:public"))

	resp2, err := e.Process(newConcurrencyColorLimiterTestRequest(t, ns, &p0))
	require.NoError(t, err)
	assert.Equal(t, int64(2), zcard(t, client, "dyn-key:own_1"))
	assert.Equal(t, int64(2), zcard(t, client, "dyn-key:public"))
	require.NoError(t, resp1.Body.Close())
	require.NoError(t, resp2.Body.Close())
}
