package loadbalancer

import (
	"errors"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
)

func TestShardKeyWRR_Headroom(t *testing.T) {
	okEngine := octollm.EngineFunc(func(_ *octollm.Request) (*octollm.Response, error) {
		return &octollm.Response{StatusCode: 200}, nil
	})
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

	t.Run("cache hit on last-two shard key bypasses headroom ceiling", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		items := []BackendItem{
			{Name: "hot", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9},
			{Name: "cool", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9},
		}
		seedConcurrency(t, rd, "hot", 10)

		const keyPrefix = "wrr-hr-a"
		require.NoError(t, rd.ZAdd(t.Context(), keyPrefix+":sk2", redis.Z{Score: 1, Member: "hot"}).Err())
		provider := newPrimaryShardKeyProvider(t, func(_ *octollm.Request) []string {
			return []string{"sk0", "sk1", "sk2"}
		}, rd, keyPrefix, time.Minute, items)

		req := testhelper.CreateTestRequest()
		resp, err := newWRR(t, items, provider, rd).Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "hot", selectedBackend(t, req), "cache-hit backend should be used even over the ceiling")
	})

	t.Run("leaf mapping without marker does not bypass headroom", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		items := []BackendItem{
			{Name: "hot", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9},
			{Name: "cool", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9},
		}
		seedConcurrency(t, rd, "hot", 10)
		seedConcurrency(t, rd, "cool", 0)

		const keyPrefix = "wrr-hr-leaf-weak"
		require.NoError(t, rd.ZAdd(t.Context(), keyPrefix+":sk2", redis.Z{Score: 1, Member: "hot"}).Err())
		provider := newPrimaryShardKeyProviderWithPolicy(t, func(_ *octollm.Request) []string {
			return []string{"sk0", "sk1", "sk2"}
		}, rd, keyPrefix, time.Minute, items, StrongHitPolicyLeaf)

		req := testhelper.CreateTestRequest()
		resp, err := newWRR(t, items, provider, rd).Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "cool", selectedBackend(t, req), "leaf weak hit over ceiling should be skipped")
	})

	t.Run("leaf mapping and marker bypasses headroom ceiling", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		items := []BackendItem{
			{Name: "hot", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9},
			{Name: "cool", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9},
		}
		seedConcurrency(t, rd, "hot", 10)
		seedConcurrency(t, rd, "cool", 0)

		const keyPrefix = "wrr-hr-leaf-strong"
		require.NoError(t, rd.ZAdd(t.Context(), keyPrefix+":sk2", redis.Z{Score: 1, Member: "hot"}).Err())
		require.NoError(t, rd.Set(t.Context(), keyPrefix+":is-leaf:sk2", "1", time.Minute).Err())
		provider := newPrimaryShardKeyProviderWithPolicy(t, func(_ *octollm.Request) []string {
			return []string{"sk0", "sk1", "sk2"}
		}, rd, keyPrefix, time.Minute, items, StrongHitPolicyLeaf)

		req := testhelper.CreateTestRequest()
		resp, err := newWRR(t, items, provider, rd).Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "hot", selectedBackend(t, req), "leaf strong hit should bypass headroom")
	})

	t.Run("non-cache-hit prioritized backend over ceiling is skipped", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		items := []BackendItem{
			{Name: "hot", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9},
			{Name: "cool", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9},
		}
		seedConcurrency(t, rd, "hot", 10)
		seedConcurrency(t, rd, "cool", 0)

		const keyPrefix = "wrr-hr-b"
		require.NoError(t, rd.ZAdd(t.Context(), keyPrefix+":sk0", redis.Z{Score: 1, Member: "hot"}).Err())
		provider := newPrimaryShardKeyProvider(t, func(_ *octollm.Request) []string {
			return []string{"sk0", "sk1", "sk2"}
		}, rd, keyPrefix, time.Minute, items)

		req := testhelper.CreateTestRequest()
		resp, err := newWRR(t, items, provider, rd).Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "cool", selectedBackend(t, req), "over-ceiling non-cache-hit backend should be skipped for the under-ceiling one")
	})

	t.Run("full miss with all backends over ceiling returns headroom error", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		items := []BackendItem{
			{Name: "a", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9},
			{Name: "b", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9},
		}
		seedConcurrency(t, rd, "a", 10)
		seedConcurrency(t, rd, "b", 10)

		resp, err := newWRR(t, items, nil, rd).Process(testhelper.CreateTestRequest())
		require.ErrorIs(t, err, ErrCacheMissHeadroom)
		assert.Nil(t, resp)
	})

	t.Run("full miss under ceiling is allowed", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		items := []BackendItem{
			{Name: "a", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9},
			{Name: "b", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9},
		}
		seedConcurrency(t, rd, "a", 5)
		seedConcurrency(t, rd, "b", 5)

		resp, err := newWRR(t, items, nil, rd).Process(testhelper.CreateTestRequest())
		require.NoError(t, err)
		require.NotNil(t, resp)
	})

	t.Run("full miss skips over-ceiling backend for one under a higher ceiling", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		items := []BackendItem{
			{Name: "a", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.5},
			{Name: "b", Weight: 1, Engine: okEngine, CacheMissMaxUtilization: 0.9},
		}
		seedConcurrency(t, rd, "a", 6) // 0.6 > 0.5
		seedConcurrency(t, rd, "b", 0) // 0 < 0.9
		lb := newWRR(t, items, nil, rd)
		lb.backends[0].currentWeight = 100
		lb.backends[1].currentWeight = 0
		beforeA := lb.backends[0].currentWeight

		req := testhelper.CreateTestRequest()
		resp, err := lb.Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "b", selectedBackend(t, req))
		assert.Equal(t, beforeA, lb.backends[0].currentWeight, "over-ceiling WRR pick must roll back")
	})

	t.Run("disabled headroom (1.0) serves over-ceiling miss", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		items := []BackendItem{
			{Name: "a", Weight: 10, Engine: okEngine},
			{Name: "b", Weight: 10, Engine: okEngine},
		}
		seedConcurrency(t, rd, "a", 10)
		seedConcurrency(t, rd, "b", 10)

		resp, err := newWRR(t, items, nil, rd).Process(testhelper.CreateTestRequest())
		require.NoError(t, err)
		require.NotNil(t, resp)
	})

	t.Run("all weak cache hits over ceiling returns headroom error", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		items := []BackendItem{
			{Name: "a", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9},
			{Name: "b", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9},
		}
		seedConcurrency(t, rd, "a", 10)
		seedConcurrency(t, rd, "b", 10)

		const keyPrefix = "wrr-hr-f"
		require.NoError(t, rd.ZAdd(t.Context(), keyPrefix+":sk0",
			redis.Z{Score: 1, Member: "a"}, redis.Z{Score: 2, Member: "b"}).Err())
		provider := newPrimaryShardKeyProvider(t, func(_ *octollm.Request) []string {
			return []string{"sk0", "sk1", "sk2"}
		}, rd, keyPrefix, time.Minute, items)

		resp, err := newWRR(t, items, provider, rd).Process(testhelper.CreateTestRequest())
		require.ErrorIs(t, err, ErrCacheMissHeadroom)
		assert.Nil(t, resp)
	})

	t.Run("strong cache hit fails then weak hits over ceiling returns previous error", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		upstreamErr := errors.New("upstream temporarily unavailable")
		failEngine := octollm.EngineFunc(func(_ *octollm.Request) (*octollm.Response, error) {
			return nil, upstreamErr
		})
		items := []BackendItem{
			{Name: "strong", Weight: 10, Engine: failEngine, CacheMissMaxUtilization: 0.9},
			{Name: "weak1", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9},
			{Name: "weak2", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9},
		}
		seedConcurrency(t, rd, "weak1", 10)
		seedConcurrency(t, rd, "weak2", 10)

		const keyPrefix = "wrr-hr-g"
		require.NoError(t, rd.ZAdd(t.Context(), keyPrefix+":sk2", redis.Z{Score: 1, Member: "strong"}).Err())
		require.NoError(t, rd.ZAdd(t.Context(), keyPrefix+":sk0",
			redis.Z{Score: 1, Member: "weak1"}, redis.Z{Score: 2, Member: "weak2"}).Err())
		provider := newPrimaryShardKeyProvider(t, func(_ *octollm.Request) []string {
			return []string{"sk0", "sk1", "sk2"}
		}, rd, keyPrefix, time.Minute, items)

		resp, err := newWRR(t, items, provider, rd).Process(testhelper.CreateTestRequest())
		require.ErrorIs(t, err, upstreamErr, "failover should surface the upstream error, not the headroom sentinel")
		require.NotErrorIs(t, err, ErrCacheMissHeadroom)
		assert.Nil(t, resp)
	})

	t.Run("enabled headroom skips zero denom including strong hit", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		items := []BackendItem{
			{Name: "hot", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9, MaxConcurrencyFn: func(*octollm.Request) int { return 0 }},
			{Name: "cool", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9},
		}

		const keyPrefix = "wrr-hr-zero-denom"
		require.NoError(t, rd.ZAdd(t.Context(), keyPrefix+":sk2", redis.Z{Score: 1, Member: "hot"}).Err())
		provider := newPrimaryShardKeyProvider(t, func(_ *octollm.Request) []string {
			return []string{"sk0", "sk1", "sk2"}
		}, rd, keyPrefix, time.Minute, items)

		req := testhelper.CreateTestRequest()
		resp, err := newWRR(t, items, provider, rd).Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "cool", selectedBackend(t, req), "zero-denom strong hit should be skipped when headroom is on")
	})

	t.Run("disabled headroom does not skip zero denom", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		items := []BackendItem{
			{Name: "hot", Weight: 10, Engine: okEngine, MaxConcurrencyFn: func(*octollm.Request) int { return 0 }},
		}

		const keyPrefix = "wrr-hr-zero-denom-off"
		require.NoError(t, rd.ZAdd(t.Context(), keyPrefix+":sk2", redis.Z{Score: 1, Member: "hot"}).Err())
		provider := newPrimaryShardKeyProvider(t, func(_ *octollm.Request) []string {
			return []string{"sk0", "sk1", "sk2"}
		}, rd, keyPrefix, time.Minute, items)

		req := testhelper.CreateTestRequest()
		resp, err := newWRR(t, items, provider, rd).Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "hot", selectedBackend(t, req), "zero denom is not a cap when headroom is off")
	})

	t.Run("redis failure fail-opens over-ceiling miss", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		items := []BackendItem{
			{Name: "a", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9},
			{Name: "b", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9},
		}
		seedConcurrency(t, rd, "a", 10)
		seedConcurrency(t, rd, "b", 10)
		lb := newWRR(t, items, nil, rd)
		mr.Close()

		resp, err := lb.Process(testhelper.CreateTestRequest())
		require.NoError(t, err)
		require.NotNil(t, resp)
	})
}
