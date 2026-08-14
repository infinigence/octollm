package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
)

type affinityProviderFunc func(*octollm.Request) ([]*PrioritizedBackend, AffinityCommitFunc, error)

func (f affinityProviderFunc) Resolve(req *octollm.Request) ([]*PrioritizedBackend, AffinityCommitFunc, error) {
	return f(req)
}

func TestRedisKeyForStrategy(t *testing.T) {
	tests := []struct {
		name      string
		keyPrefix string
		namespace string
		shardKey  string
		want      string
	}{
		{
			name:      "legacy primary key",
			keyPrefix: "maas:cache_aware:model-a",
			namespace: "",
			shardKey:  "sk1",
			want:      "maas:cache_aware:model-a:sk1",
		},
		{
			name:      "namespaced shadow key",
			keyPrefix: "maas:cache_aware:model-a",
			namespace: "trace:v1",
			shardKey:  "sk1",
			want:      "maas:cache_aware:model-a:trace:v1:sk1",
		},
		{
			name:      "empty prefix without namespace",
			keyPrefix: "",
			namespace: "",
			shardKey:  "sk1",
			want:      "sk1",
		},
		{
			name:      "empty prefix with namespace",
			keyPrefix: "",
			namespace: "trace:v1",
			shardKey:  "sk1",
			want:      "trace:v1:sk1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, redisKeyForStrategy(tt.keyPrefix, tt.namespace, tt.shardKey))
		})
	}
}

func TestNewShardKeyAffinityProvider_Validation(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	getter := func(_ *octollm.Request) []string { return []string{"k1"} }

	_, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{})
	assert.Error(t, err)

	_, err = NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: getter,
			IsPrimary:          true,
		}},
	})
	assert.Error(t, err)

	_, err = NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		RedisClient: rd,
		Strategies:  nil,
	})
	assert.Error(t, err)

	_, err = NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		RedisClient: rd,
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: nil,
			IsPrimary:          true,
		}},
	})
	assert.Error(t, err)

	_, err = NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		RedisClient: rd,
		Strategies: []ShardKeyStrategySpec{
			{ShardKeyListGetter: getter, IsPrimary: true},
			{ShardKeyListGetter: getter, IsPrimary: true},
		},
	})
	assert.Error(t, err)

	_, err = NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		RedisClient: rd,
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: getter,
			IsPrimary:          false,
		}},
	})
	assert.Error(t, err)
}

func TestShardKeyAffinityProvider_EmptyShardKeyList(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: func(_ *octollm.Request) []string { return nil },
			IsPrimary:          true,
		}},
		RedisClient:  rd,
		KeyPrefix:    "pfx-empty",
		ShardKeyTTL:  time.Minute,
		BackendNames: []string{"b1"},
	})
	require.NoError(t, err)

	prioritized, commit, err := provider.Resolve(testhelper.CreateTestRequest())
	require.NoError(t, err)
	assert.Empty(t, prioritized)
	assert.NotNil(t, commit)
}

func TestShardKeyAffinityProvider_NoRedisData(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"k1"} },
			IsPrimary:          true,
		}},
		RedisClient:  rd,
		KeyPrefix:    "pfx-miss",
		ShardKeyTTL:  time.Minute,
		BackendNames: []string{"b1"},
	})
	require.NoError(t, err)

	prioritized, commit, err := provider.Resolve(testhelper.CreateTestRequest())
	require.NoError(t, err)
	assert.Empty(t, prioritized)
	assert.NotNil(t, commit)
}

func TestShardKeyAffinityProvider_RedisUnavailableAtResolve(t *testing.T) {
	mr := miniredis.RunT(t)
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"k1"} },
			IsPrimary:          true,
		}},
		RedisClient:  rd,
		KeyPrefix:    "pfx-down",
		ShardKeyTTL:  time.Minute,
		BackendNames: []string{"b1"},
	})
	require.NoError(t, err)

	mr.Close()

	prioritized, _, err := provider.Resolve(testhelper.CreateTestRequest())
	require.NoError(t, err)
	assert.Empty(t, prioritized)
}

func TestShardKeyAffinityProvider_PrimaryResolveAndShadowWriteOnly(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	const keyPrefix = "maas:cache_aware:model-a"
	ctx := context.Background()

	require.NoError(t, rd.ZAdd(ctx, keyPrefix+":primary-key", redis.Z{
		Score: 1, Member: "svc-primary",
	}).Err())
	require.NoError(t, rd.ZAdd(ctx, keyPrefix+":shadow-ns:shadow-key", redis.Z{
		Score: 1, Member: "svc-shadow-only",
	}).Err())

	provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{
			{
				ShardKeyListGetter: func(_ *octollm.Request) []string {
					return []string{"primary-key"}
				},
				IsPrimary: true,
			},
			{
				ShardKeyListGetter: func(_ *octollm.Request) []string {
					return []string{"shadow-key"}
				},
				CacheKeyNamespace: "shadow-ns",
			},
		},
		RedisClient:  rd,
		KeyPrefix:    keyPrefix,
		ShardKeyTTL:  time.Minute,
		BackendNames: []string{"svc-primary", "svc-shadow-only", "svc-open"},
	})
	require.NoError(t, err)

	req := testhelper.CreateTestRequest()
	prioritized, commit, err := provider.Resolve(req)
	require.NoError(t, err)
	require.Len(t, prioritized, 1)
	assert.Equal(t, "svc-primary", prioritized[0].Name)
	assert.True(t, prioritized[0].StrongCacheHit)

	require.NoError(t, commit("svc-open"))

	members, err := rd.ZRange(ctx, keyPrefix+":primary-key", 0, -1).Result()
	require.NoError(t, err)
	assert.Contains(t, members, "svc-open")

	shadowMembers, err := rd.ZRange(ctx, keyPrefix+":shadow-ns:shadow-key", 0, -1).Result()
	require.NoError(t, err)
	assert.Contains(t, shadowMembers, "svc-open")
	// svc-shadow-only was pre-seeded to prove shadow Redis does not affect routing.
	assert.Contains(t, shadowMembers, "svc-shadow-only")
}

func TestShardKeyAffinityProvider_WritesAllShadowStrategies(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	const keyPrefix = "all-shadows"
	provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{
			{ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"primary"} }, IsPrimary: true},
			{ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"shadow-a"} }, CacheKeyNamespace: "a:v1"},
			{ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"shadow-b"} }, CacheKeyNamespace: "b:v1"},
		},
		RedisClient: rd,
		KeyPrefix:   keyPrefix,
		ShardKeyTTL: time.Minute,
	})
	require.NoError(t, err)

	_, commit, err := provider.Resolve(testhelper.CreateTestRequest())
	require.NoError(t, err)
	require.NoError(t, commit("svc-a"))

	for _, key := range []string{
		keyPrefix + ":primary",
		keyPrefix + ":a:v1:shadow-a",
		keyPrefix + ":b:v1:shadow-b",
	} {
		members, err := rd.ZRange(context.Background(), key, 0, -1).Result()
		require.NoError(t, err)
		assert.Equal(t, []string{"svc-a"}, members)
	}
}

func TestShardKeyAffinityProvider_StrongCacheHitFromLastTwoKeys(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	const keyPrefix = "hr-strong"
	ctx := context.Background()
	require.NoError(t, rd.ZAdd(ctx, keyPrefix+":sk0", redis.Z{Score: 1, Member: "weak"}).Err())
	require.NoError(t, rd.ZAdd(ctx, keyPrefix+":sk2", redis.Z{Score: 1, Member: "strong"}).Err())

	provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: func(_ *octollm.Request) []string {
				return []string{"sk0", "sk1", "sk2"}
			},
			IsPrimary: true,
		}},
		RedisClient:  rd,
		KeyPrefix:    keyPrefix,
		ShardKeyTTL:  time.Minute,
		BackendNames: []string{"weak", "strong"},
	})
	require.NoError(t, err)

	prioritized, _, err := provider.Resolve(testhelper.CreateTestRequest())
	require.NoError(t, err)
	require.Len(t, prioritized, 2)
	assert.Equal(t, "strong", prioritized[0].Name)
	assert.True(t, prioritized[0].StrongCacheHit)
	assert.Equal(t, "weak", prioritized[1].Name)
	assert.False(t, prioritized[1].StrongCacheHit)
}

func TestShardKeyAffinityProvider_TrimToThreeMembers(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	const keyPrefix = "trim"
	ctx := context.Background()
	now := time.Now().Unix()
	members := make([]redis.Z, 5)
	for i := range members {
		members[i] = redis.Z{Score: float64(now - int64(50-i*10)), Member: fmt.Sprintf("b%d", i)}
	}
	require.NoError(t, rd.ZAdd(ctx, keyPrefix+":k1", members...).Err())

	provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"k1"} },
			IsPrimary:          true,
		}},
		RedisClient:  rd,
		KeyPrefix:    keyPrefix,
		ShardKeyTTL:  time.Minute,
		BackendNames: []string{"b0", "b1", "b2", "b3", "b4"},
	})
	require.NoError(t, err)

	_, _, err = provider.Resolve(testhelper.CreateTestRequest())
	require.NoError(t, err)

	card, err := rd.ZCard(ctx, keyPrefix+":k1").Result()
	require.NoError(t, err)
	assert.LessOrEqual(t, card, int64(3))
}

func TestShardKeyAffinityProvider_CommitSkippedOnFailureByCaller(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	const keyPrefix = "no-write"
	provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"k1"} },
			IsPrimary:          true,
		}},
		RedisClient: rd,
		KeyPrefix:   keyPrefix,
		ShardKeyTTL: time.Minute,
	})
	require.NoError(t, err)

	_, commit, err := provider.Resolve(testhelper.CreateTestRequest())
	require.NoError(t, err)
	// Simulate LB behavior: commit is only called on success.
	_ = commit

	card, err := rd.ZCard(context.Background(), keyPrefix+":k1").Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), card)
}

func TestShardKeyConcurrency_WithAffinityProvider(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	const keyPrefix = "pfx-provider"
	ctx := context.Background()
	require.NoError(t, rd.ZAdd(ctx, keyPrefix+":sk-priority", redis.Z{Score: 1, Member: "hot"}).Err())

	okEngine := octollm.EngineFunc(func(_ *octollm.Request) (*octollm.Response, error) {
		return &octollm.Response{StatusCode: 200}, nil
	})
	items := []BackendItem{
		{Name: "hot", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9},
		{Name: "cool", Weight: 10, Engine: okEngine, CacheMissMaxUtilization: 0.9},
	}

	provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{
			{
				ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"sk-priority"} },
				IsPrimary:          true,
			},
			{
				ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"sk-shadow"} },
				CacheKeyNamespace:  "trace:v1",
			},
		},
		RedisClient:  rd,
		KeyPrefix:    keyPrefix,
		ShardKeyTTL:  time.Minute,
		BackendNames: []string{"hot", "cool"},
	})
	require.NoError(t, err)

	lb, err := NewShardKeyConcurrency(
		items,
		time.Second, 3,
		provider,
		rd,
		func(_ *octollm.Request, backendName string) string {
			return "concurrency:" + backendName
		},
	)
	require.NoError(t, err)

	req := testhelper.CreateTestRequest()
	resp, err := lb.Process(req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	mv, ok := req.GetMetadataValue(backendName)
	require.True(t, ok)
	assert.Equal(t, "hot", mv.(string))

	primaryMembers, err := rd.ZRange(ctx, keyPrefix+":sk-priority", 0, -1).Result()
	require.NoError(t, err)
	assert.Contains(t, primaryMembers, "hot")

	shadowMembers, err := rd.ZRange(ctx, keyPrefix+":trace:v1:sk-shadow", 0, -1).Result()
	require.NoError(t, err)
	assert.Contains(t, shadowMembers, "hot")

}

func TestShardKeyConcurrency_WithAffinityProvider_RequestFailureDoesNotCommit(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	const keyPrefix = "concurrency-failure"
	provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{
			{ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"primary"} }, IsPrimary: true},
			{ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"shadow"} }, CacheKeyNamespace: "trace:v1"},
		},
		RedisClient: rd,
		KeyPrefix:   keyPrefix,
		ShardKeyTTL: time.Minute,
	})
	require.NoError(t, err)

	failingEngine := octollm.EngineFunc(func(_ *octollm.Request) (*octollm.Response, error) {
		return nil, errors.New("upstream failure")
	})
	lb, err := NewShardKeyConcurrency(
		[]BackendItem{{Name: "svc-a", Weight: 1, Engine: failingEngine}},
		time.Second,
		1,
		provider,
		rd,
		func(_ *octollm.Request, backendName string) string { return "concurrency:" + backendName },
	)
	require.NoError(t, err)

	_, err = lb.Process(testhelper.CreateTestRequest())
	require.Error(t, err)
	for _, key := range []string{keyPrefix + ":primary", keyPrefix + ":trace:v1:shadow"} {
		card, err := rd.ZCard(context.Background(), key).Result()
		require.NoError(t, err)
		assert.Zero(t, card)
	}
}

func TestShardKeyWeightedRoundRobin_WithAffinityProvider(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	const keyPrefix = "wrr-provider"
	ctx := context.Background()
	require.NoError(t, rd.ZAdd(ctx, keyPrefix+":sk1", redis.Z{Score: 1, Member: "A"}).Err())

	engineA := &stubEngine{}
	engineB := &stubEngine{err: errors.New("fail")}

	provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{
			{
				ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"sk1"} },
				IsPrimary:          true,
			},
			{
				ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"sk-shadow"} },
				CacheKeyNamespace:  "ns1",
			},
		},
		RedisClient:  rd,
		KeyPrefix:    keyPrefix,
		ShardKeyTTL:  time.Minute,
		BackendNames: []string{"A", "B"},
	})
	require.NoError(t, err)

	lb, err := NewShardKeyWeightedRoundRobin(
		[]BackendItem{
			{Name: "A", Weight: 10, Engine: engineA},
			{Name: "B", Weight: 5, Engine: engineB},
		},
		time.Second, 3,
		provider,
	)
	require.NoError(t, err)

	req := testhelper.CreateTestRequest()
	resp, err := lb.Process(req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 1, engineA.callCount)

	members, err := rd.ZRange(ctx, keyPrefix+":sk1", 0, -1).Result()
	require.NoError(t, err)
	assert.Contains(t, members, "A")

	shadowMembers, err := rd.ZRange(ctx, keyPrefix+":ns1:sk-shadow", 0, -1).Result()
	require.NoError(t, err)
	assert.Contains(t, shadowMembers, "A")
}

func TestShardKeyWeightedRoundRobin_WithAffinityProvider_RequestFailureDoesNotCommit(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	const keyPrefix = "wrr-failure"
	provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{
			{ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"primary"} }, IsPrimary: true},
			{ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"shadow"} }, CacheKeyNamespace: "trace:v1"},
		},
		RedisClient: rd,
		KeyPrefix:   keyPrefix,
		ShardKeyTTL: time.Minute,
	})
	require.NoError(t, err)

	lb, err := NewShardKeyWeightedRoundRobin(
		[]BackendItem{{
			Name: "svc-a", Weight: 1,
			Engine: octollm.EngineFunc(func(_ *octollm.Request) (*octollm.Response, error) {
				return nil, errors.New("upstream failure")
			}),
		}},
		time.Second,
		1,
		provider,
	)
	require.NoError(t, err)

	_, err = lb.Process(testhelper.CreateTestRequest())
	require.Error(t, err)
	for _, key := range []string{keyPrefix + ":primary", keyPrefix + ":trace:v1:shadow"} {
		card, err := rd.ZCard(context.Background(), key).Result()
		require.NoError(t, err)
		assert.Zero(t, card)
	}
}

// Covers the AffinityProvider contract when Resolve returns a hard error: LBs fail-fast
// and do not invoke backends. The built-in ShardKeyAffinityProvider never takes this path
// for Redis miss/unavailable (it returns empty prioritized + nil err instead).
func TestShardKeyLoadBalancers_AffinityProviderErrors(t *testing.T) {
	providerErr := errors.New("resolve failed")

	t.Run("concurrency does not invoke backend", func(t *testing.T) {
		mr := miniredis.RunT(t)
		defer mr.Close()
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		calls := 0
		lb, err := NewShardKeyConcurrency(
			[]BackendItem{{Name: "svc-a", Weight: 1, Engine: octollm.EngineFunc(func(_ *octollm.Request) (*octollm.Response, error) {
				calls++
				return &octollm.Response{StatusCode: 200}, nil
			})}},
			time.Second,
			1,
			affinityProviderFunc(func(*octollm.Request) ([]*PrioritizedBackend, AffinityCommitFunc, error) {
				return nil, nil, providerErr
			}),
			rd,
			func(_ *octollm.Request, backendName string) string { return "concurrency:" + backendName },
		)
		require.NoError(t, err)

		_, err = lb.Process(testhelper.CreateTestRequest())
		require.ErrorIs(t, err, providerErr)
		assert.Zero(t, calls)
	})

	t.Run("weighted round robin does not invoke backend", func(t *testing.T) {
		calls := 0
		lb, err := NewShardKeyWeightedRoundRobin(
			[]BackendItem{{Name: "svc-a", Weight: 1, Engine: octollm.EngineFunc(func(_ *octollm.Request) (*octollm.Response, error) {
				calls++
				return &octollm.Response{StatusCode: 200}, nil
			})}},
			time.Second,
			1,
			affinityProviderFunc(func(*octollm.Request) ([]*PrioritizedBackend, AffinityCommitFunc, error) {
				return nil, nil, providerErr
			}),
		)
		require.NoError(t, err)

		_, err = lb.Process(testhelper.CreateTestRequest())
		require.ErrorIs(t, err, providerErr)
		assert.Zero(t, calls)
	})
}

func TestShardKeyLoadBalancers_AffinityCommitErrorDoesNotFailRequest(t *testing.T) {
	commitErr := errors.New("commit failed")
	provider := affinityProviderFunc(func(*octollm.Request) ([]*PrioritizedBackend, AffinityCommitFunc, error) {
		return nil, func(string) error { return commitErr }, nil
	})

	t.Run("concurrency", func(t *testing.T) {
		mr := miniredis.RunT(t)
		defer mr.Close()
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		lb, err := NewShardKeyConcurrency(
			[]BackendItem{{Name: "svc-a", Weight: 1, Engine: octollm.EngineFunc(func(_ *octollm.Request) (*octollm.Response, error) {
				return &octollm.Response{StatusCode: 200}, nil
			})}},
			time.Second,
			1,
			provider,
			rd,
			func(_ *octollm.Request, backendName string) string { return "concurrency:" + backendName },
		)
		require.NoError(t, err)

		resp, err := lb.Process(testhelper.CreateTestRequest())
		require.NoError(t, err)
		assert.Equal(t, 200, resp.StatusCode)
	})

	t.Run("weighted round robin", func(t *testing.T) {
		lb, err := NewShardKeyWeightedRoundRobin(
			[]BackendItem{{Name: "svc-a", Weight: 1, Engine: octollm.EngineFunc(func(_ *octollm.Request) (*octollm.Response, error) {
				return &octollm.Response{StatusCode: 200}, nil
			})}},
			time.Second,
			1,
			provider,
		)
		require.NoError(t, err)

		resp, err := lb.Process(testhelper.CreateTestRequest())
		require.NoError(t, err)
		assert.Equal(t, 200, resp.StatusCode)
	})
}

func TestShardKeyAffinityProvider_SinglePrimaryStrategy(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	const keyPrefix = "legacy"
	ctx := context.Background()
	require.NoError(t, rd.ZAdd(ctx, keyPrefix+":k1", redis.Z{Score: 1, Member: "svc1"}).Err())

	provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"k1"} },
			IsPrimary:          true,
		}},
		RedisClient:  rd,
		KeyPrefix:    keyPrefix,
		ShardKeyTTL:  time.Minute,
		BackendNames: []string{"svc1"},
	})
	require.NoError(t, err)

	prioritized, commit, err := provider.Resolve(testhelper.CreateTestRequest())
	require.NoError(t, err)
	require.Len(t, prioritized, 1)
	assert.Equal(t, "svc1", prioritized[0].Name)

	require.NoError(t, commit("svc1"))
	members, err := rd.ZRange(ctx, keyPrefix+":k1", 0, -1).Result()
	require.NoError(t, err)
	assert.Contains(t, members, "svc1")
}
