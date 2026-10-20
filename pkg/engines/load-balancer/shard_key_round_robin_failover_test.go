package loadbalancer

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/engines/limiter"
	"github.com/infinigence/octollm/pkg/engines/load-balancer/common"
	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
)

type captureHitKindEngine struct {
	mu    sync.Mutex
	kinds []common.CacheHitKind
	err   error
}

func (e *captureHitKindEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	kind, ok := octollm.GetCtxValue[common.CacheHitKind](req, octollm.ContextKeyCacheHitKind)
	if !ok {
		kind = common.CacheMiss
	}
	e.mu.Lock()
	e.kinds = append(e.kinds, kind)
	e.mu.Unlock()
	if e.err != nil {
		return nil, e.err
	}
	return &octollm.Response{StatusCode: 200}, nil
}

func (e *captureHitKindEngine) kindsCopy() []common.CacheHitKind {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]common.CacheHitKind, len(e.kinds))
	copy(out, e.kinds)
	return out
}

func rejectUnlessStrong(e *captureHitKindEngine) octollm.Engine {
	return octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		kind, ok := octollm.GetCtxValue[common.CacheHitKind](req, octollm.ContextKeyCacheHitKind)
		if !ok {
			kind = common.CacheMiss
		}
		e.mu.Lock()
		e.kinds = append(e.kinds, kind)
		e.mu.Unlock()
		if kind == common.CacheHitStrong {
			return &octollm.Response{StatusCode: 200}, nil
		}
		return nil, limiter.ErrRateLimitReached
	})
}

func TestShardKeyWRR_HitKindAndLimiterFailover(t *testing.T) {
	selectedBackend := func(t *testing.T, req *octollm.Request) string {
		t.Helper()
		mv, ok := req.GetMetadataValue(backendName)
		require.True(t, ok)
		name, ok := mv.(string)
		require.True(t, ok)
		return name
	}
	newWRR := func(t *testing.T, items []BackendItem, provider AffinityProvider, rd *redis.Client) *ShardKeyWeightedRoundRobin {
		t.Helper()
		lb, err := NewShardKeyWeightedRoundRobin(withTestConcurrencyKeys(items), time.Second, 3, 0, provider, nil, rd)
		require.NoError(t, err)
		return lb
	}

	t.Run("strong affinity is tried even when busy and writes strong", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		hot := &captureHitKindEngine{}
		cool := &captureHitKindEngine{}
		items := []BackendItem{
			{Name: "hot", Weight: 10, Engine: hot},
			{Name: "cool", Weight: 10, Engine: cool},
		}
		seedConcurrency(t, rd, "hot", 10)

		const keyPrefix = "wrr-hk-strong"
		require.NoError(t, rd.ZAdd(t.Context(), keyPrefix+":sk2", redis.Z{Score: 1, Member: "hot"}).Err())
		provider := newPrimaryShardKeyProvider(t, func(_ *octollm.Request) []string {
			return []string{"sk0", "sk1", "sk2"}
		}, rd, keyPrefix, time.Minute, items)

		req := testhelper.CreateTestRequest()
		resp, err := newWRR(t, items, provider, rd).Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "hot", selectedBackend(t, req))
		assert.Equal(t, []common.CacheHitKind{common.CacheHitStrong}, hot.kindsCopy())
		assert.Empty(t, cool.kindsCopy())
	})

	t.Run("weak affinity limiter reject failovers and second attempt is not strong", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		hotCap := &captureHitKindEngine{}
		cool := &captureHitKindEngine{}
		items := []BackendItem{
			{Name: "hot", Weight: 10, Engine: rejectUnlessStrong(hotCap)},
			{Name: "cool", Weight: 10, Engine: cool},
		}

		const keyPrefix = "wrr-hk-weak"
		require.NoError(t, rd.ZAdd(t.Context(), keyPrefix+":sk2", redis.Z{Score: 1, Member: "hot"}).Err())
		provider := newPrimaryShardKeyProviderWithPolicy(t, func(_ *octollm.Request) []string {
			return []string{"sk0", "sk1", "sk2"}
		}, rd, keyPrefix, time.Minute, items, StrongHitPolicyLeaf)

		req := testhelper.CreateTestRequest()
		resp, err := newWRR(t, items, provider, rd).Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "cool", selectedBackend(t, req))
		assert.Equal(t, []common.CacheHitKind{common.CacheHitWeak}, hotCap.kindsCopy())
		assert.Equal(t, []common.CacheHitKind{common.CacheMiss}, cool.kindsCopy())
	})

	t.Run("all limiter rejects return last limiter error", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		reject := octollm.EngineFunc(func(_ *octollm.Request) (*octollm.Response, error) {
			return nil, limiter.ErrRateLimitReached
		})
		items := []BackendItem{
			{Name: "a", Weight: 10, Engine: reject},
			{Name: "b", Weight: 10, Engine: reject},
		}
		resp, err := newWRR(t, items, nil, rd).Process(testhelper.CreateTestRequest())
		require.ErrorIs(t, err, limiter.ErrRateLimitReached)
		assert.Nil(t, resp)
	})

	t.Run("limiter reject does not roll back WRR weights", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		okEngine := octollm.EngineFunc(func(_ *octollm.Request) (*octollm.Response, error) {
			return &octollm.Response{StatusCode: 200}, nil
		})
		reject := octollm.EngineFunc(func(_ *octollm.Request) (*octollm.Response, error) {
			return nil, limiter.ErrRateLimitReached
		})
		items := []BackendItem{
			{Name: "a", Weight: 10, Engine: reject},
			{Name: "b", Weight: 1, Engine: okEngine},
		}
		lb := newWRR(t, items, nil, rd)
		lb.backends[0].currentWeight = 100
		lb.backends[1].currentWeight = 0
		beforeA := lb.backends[0].currentWeight

		req := testhelper.CreateTestRequest()
		resp, err := lb.Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "b", selectedBackend(t, req))
		assert.NotEqual(t, beforeA, lb.backends[0].currentWeight)
	})

	t.Run("real upstream error wins over later limiter rejects", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		upstreamErr := assert.AnError
		failEngine := octollm.EngineFunc(func(_ *octollm.Request) (*octollm.Response, error) {
			return nil, upstreamErr
		})
		reject := octollm.EngineFunc(func(_ *octollm.Request) (*octollm.Response, error) {
			return nil, limiter.ErrRateLimitReached
		})
		items := []BackendItem{
			{Name: "strong", Weight: 10, Engine: failEngine},
			{Name: "weak1", Weight: 10, Engine: reject},
			{Name: "weak2", Weight: 10, Engine: reject},
		}

		const keyPrefix = "wrr-hk-real"
		require.NoError(t, rd.ZAdd(t.Context(), keyPrefix+":sk2", redis.Z{Score: 1, Member: "strong"}).Err())
		require.NoError(t, rd.ZAdd(t.Context(), keyPrefix+":sk0",
			redis.Z{Score: 1, Member: "weak1"}, redis.Z{Score: 2, Member: "weak2"}).Err())
		provider := newPrimaryShardKeyProvider(t, func(_ *octollm.Request) []string {
			return []string{"sk0", "sk1", "sk2"}
		}, rd, keyPrefix, time.Minute, items)

		resp, err := newWRR(t, items, provider, rd).Process(testhelper.CreateTestRequest())
		require.ErrorIs(t, err, upstreamErr)
		require.NotErrorIs(t, err, limiter.ErrRateLimitReached)
		assert.Nil(t, resp)
	})

	t.Run("limiter reject after retry timeout does not call the next backend", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		var calls atomic.Int32
		eng := octollm.EngineFunc(func(_ *octollm.Request) (*octollm.Response, error) {
			if calls.Add(1) == 1 {
				time.Sleep(30 * time.Millisecond)
				return nil, limiter.ErrRateLimitReached
			}
			return &octollm.Response{StatusCode: 200}, nil
		})
		items := []BackendItem{
			{Name: "a", Weight: 10, Engine: eng},
			{Name: "b", Weight: 10, Engine: eng},
		}
		lb, err := NewShardKeyWeightedRoundRobin(withTestConcurrencyKeys(items), 5*time.Millisecond, 10, 0, nil, nil, rd)
		require.NoError(t, err)

		resp, err := lb.Process(testhelper.CreateTestRequest())
		require.ErrorIs(t, err, limiter.ErrRateLimitReached)
		assert.Nil(t, resp)
		assert.Equal(t, int32(1), calls.Load())
	})

	t.Run("limiter timeout keeps an earlier upstream error", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		upstreamErr := assert.AnError
		var cCalls atomic.Int32
		items := []BackendItem{
			{Name: "a", Weight: 10, Engine: octollm.EngineFunc(func(*octollm.Request) (*octollm.Response, error) {
				return nil, upstreamErr
			})},
			{Name: "b", Weight: 10, Engine: octollm.EngineFunc(func(*octollm.Request) (*octollm.Response, error) {
				time.Sleep(30 * time.Millisecond)
				return nil, limiter.ErrRateLimitReached
			})},
			{Name: "c", Weight: 10, Engine: octollm.EngineFunc(func(*octollm.Request) (*octollm.Response, error) {
				cCalls.Add(1)
				return &octollm.Response{StatusCode: 200}, nil
			})},
		}
		lb, err := NewShardKeyWeightedRoundRobin(withTestConcurrencyKeys(items), 5*time.Millisecond, 10, 0, nil, nil, rd)
		require.NoError(t, err)
		for _, backend := range lb.backends {
			switch backend.name {
			case "a":
				backend.currentWeight = 1000
			case "b":
				backend.currentWeight = 100
			case "c":
				backend.currentWeight = 0
			}
		}

		resp, err := lb.Process(testhelper.CreateTestRequest())
		require.ErrorIs(t, err, upstreamErr)
		require.NotErrorIs(t, err, limiter.ErrRateLimitReached)
		assert.Nil(t, resp)
		assert.Equal(t, int32(0), cCalls.Load())
	})
}

func TestShardKeyWRR_PLoadFailover(t *testing.T) {
	const namespace = "endpoint_priority"
	limits := limiter.PLoadLimits{AvailableTotalMax: []float64{100}, HeadroomRatio: 1.2}
	scoreErr := errors.New("pair missing")

	pLoad := func(t *testing.T, score float64, err error, next octollm.Engine) octollm.Engine {
		t.Helper()
		eng, buildErr := limiter.NewPLoadColorLimiterEngine(limits, func(*octollm.Request) (float64, error) {
			if err != nil {
				return 0, err
			}
			return score, nil
		}, namespace, next)
		require.NoError(t, buildErr)
		return eng
	}
	selectedBackend := func(t *testing.T, req *octollm.Request) string {
		t.Helper()
		mv, ok := req.GetMetadataValue(backendName)
		require.True(t, ok)
		name, ok := mv.(string)
		require.True(t, ok)
		return name
	}
	newWRR := func(t *testing.T, items []BackendItem, provider AffinityProvider, rd *redis.Client) *ShardKeyWeightedRoundRobin {
		t.Helper()
		lb, err := NewShardKeyWeightedRoundRobin(withTestConcurrencyKeys(items), time.Second, 3, 0, provider, nil, rd)
		require.NoError(t, err)
		return lb
	}
	affinity := func(t *testing.T, rd *redis.Client, keyPrefix, policy string, items []BackendItem) AffinityProvider {
		t.Helper()
		require.NoError(t, rd.ZAdd(t.Context(), keyPrefix+":sk2", redis.Z{Score: 1, Member: "a"}).Err())
		return newPrimaryShardKeyProviderWithPolicy(t, func(*octollm.Request) []string {
			return []string{"sk0", "sk1", "sk2"}
		}, rd, keyPrefix, time.Minute, items, policy)
	}

	t.Run("over limit on A fails over to B", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		nextA := &captureHitKindEngine{}
		nextB := &captureHitKindEngine{}
		items := []BackendItem{
			{Name: "a", Weight: 10, Engine: pLoad(t, 150, nil, nextA)},
			{Name: "b", Weight: 10, Engine: pLoad(t, 50, nil, nextB)},
		}
		const keyPrefix = "pload-failover-allow"
		provider := affinity(t, rd, keyPrefix, StrongHitPolicyLeaf, items)

		req := testhelper.CreateTestRequest()
		resp, err := newWRR(t, items, provider, rd).Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "b", selectedBackend(t, req))
		assert.Empty(t, nextA.kindsCopy())
		assert.Equal(t, []common.CacheHitKind{common.CacheMiss}, nextB.kindsCopy())
	})

	t.Run("strong headroom is not inherited by B", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		nextA := &captureHitKindEngine{}
		nextB := &captureHitKindEngine{}
		items := []BackendItem{
			{Name: "a", Weight: 10, Engine: pLoad(t, 250, nil, nextA)},
			{Name: "b", Weight: 10, Engine: pLoad(t, 150, nil, nextB)},
		}
		const keyPrefix = "pload-failover-strong"
		provider := affinity(t, rd, keyPrefix, StrongHitPolicyLastTwo, items)

		_, err := newWRR(t, items, provider, rd).Process(testhelper.CreateTestRequest())
		require.ErrorIs(t, err, limiter.ErrRateLimitReached)
		assert.Empty(t, nextA.kindsCopy())
		assert.Empty(t, nextB.kindsCopy())
	})

	t.Run("all over limit returns rate limit", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		nextA := &captureHitKindEngine{}
		nextB := &captureHitKindEngine{}
		items := []BackendItem{
			{Name: "a", Weight: 10, Engine: pLoad(t, 200, nil, nextA)},
			{Name: "b", Weight: 10, Engine: pLoad(t, 200, nil, nextB)},
		}
		const keyPrefix = "pload-failover-all"
		provider := affinity(t, rd, keyPrefix, StrongHitPolicyLeaf, items)

		_, err := newWRR(t, items, provider, rd).Process(testhelper.CreateTestRequest())
		require.ErrorIs(t, err, limiter.ErrRateLimitReached)
		assert.Empty(t, nextA.kindsCopy())
		assert.Empty(t, nextB.kindsCopy())
	})

	t.Run("score error on A still allows B", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		nextA := &captureHitKindEngine{}
		nextB := &captureHitKindEngine{}
		items := []BackendItem{
			{Name: "a", Weight: 10, Engine: pLoad(t, 0, scoreErr, nextA)},
			{Name: "b", Weight: 10, Engine: pLoad(t, 50, nil, nextB)},
		}
		const keyPrefix = "pload-failover-score-then-allow"
		provider := affinity(t, rd, keyPrefix, StrongHitPolicyLeaf, items)

		req := testhelper.CreateTestRequest()
		resp, err := newWRR(t, items, provider, rd).Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "b", selectedBackend(t, req))
		assert.Empty(t, nextA.kindsCopy())
		assert.Equal(t, []common.CacheHitKind{common.CacheMiss}, nextB.kindsCopy())
	})

	t.Run("score error after rate limit is not a rate limit", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		nextA := &captureHitKindEngine{}
		nextB := &captureHitKindEngine{}
		items := []BackendItem{
			{Name: "a", Weight: 10, Engine: pLoad(t, 150, nil, nextA)},
			{Name: "b", Weight: 10, Engine: pLoad(t, 0, scoreErr, nextB)},
		}
		const keyPrefix = "pload-failover-limit-then-score"
		provider := affinity(t, rd, keyPrefix, StrongHitPolicyLeaf, items)

		_, err := newWRR(t, items, provider, rd).Process(testhelper.CreateTestRequest())
		require.ErrorIs(t, err, scoreErr)
		assert.NotErrorIs(t, err, limiter.ErrRateLimitReached)
		assert.Empty(t, nextA.kindsCopy())
		assert.Empty(t, nextB.kindsCopy())
	})
}
