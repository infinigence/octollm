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

func TestLastNonEmptyShardKey(t *testing.T) {
	assert.Equal(t, "", lastNonEmptyShardKey(nil))
	assert.Equal(t, "", lastNonEmptyShardKey([]string{"", ""}))
	assert.Equal(t, "H3", lastNonEmptyShardKey([]string{"H1", "H2", "H3"}))
	assert.Equal(t, "H3", lastNonEmptyShardKey([]string{"H1", "", "H3", ""}))
	assert.Equal(t, 2, countNonEmptyShardKeys([]string{"H1", "", "H3", ""}))
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
			StrongHitPolicy:    StrongHitPolicyLastTwo,
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
			StrongHitPolicy:    StrongHitPolicyLastTwo,
		}},
	})
	assert.Error(t, err)

	_, err = NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		RedisClient: rd,
		Strategies: []ShardKeyStrategySpec{
			{ShardKeyListGetter: getter, IsPrimary: true, StrongHitPolicy: StrongHitPolicyLastTwo},
			{ShardKeyListGetter: getter, IsPrimary: true, StrongHitPolicy: StrongHitPolicyLastTwo},
		},
	})
	assert.Error(t, err)

	_, err = NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		RedisClient: rd,
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: getter,
			IsPrimary:          false,
			StrongHitPolicy:    StrongHitPolicyLastTwo,
		}},
	})
	assert.Error(t, err)

	_, err = NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		RedisClient: rd,
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: getter,
			IsPrimary:          true,
			StrongHitPolicy:    "not-a-policy",
		}},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not-a-policy")

	_, err = NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		RedisClient: rd,
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: getter,
			IsPrimary:          true,
			StrongHitPolicy:    "",
		}},
	})
	assert.Error(t, err)

	_, err = NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		RedisClient: rd,
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: getter,
			IsPrimary:          true,
			StrongHitPolicy:    StrongHitPolicyLeaf,
		}},
	})
	assert.NoError(t, err)

	_, err = NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		RedisClient: rd,
		Strategies: []ShardKeyStrategySpec{
			{ShardKeyListGetter: getter, IsPrimary: true, StrongHitPolicy: StrongHitPolicyLastTwo},
			{ShardKeyListGetter: getter, CacheKeyNamespace: "shadow", StrongHitPolicy: "not-a-policy"},
		},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not-a-policy")
}

func TestShardKeyAffinityProvider_EmptyShardKeyList(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: func(_ *octollm.Request) []string { return nil },
			IsPrimary:          true,
			StrongHitPolicy:    StrongHitPolicyLastTwo,
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
			StrongHitPolicy:    StrongHitPolicyLastTwo,
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
			StrongHitPolicy:    StrongHitPolicyLastTwo,
		}},
		RedisClient:  rd,
		KeyPrefix:    "pfx-down",
		ShardKeyTTL:  time.Minute,
		BackendNames: []string{"b1"},
	})
	require.NoError(t, err)

	mr.Close()

	prioritized, commit, err := provider.Resolve(testhelper.CreateTestRequest())
	require.NoError(t, err)
	assert.Empty(t, prioritized)
	assert.NotNil(t, commit)
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
				IsPrimary:       true,
				StrongHitPolicy: StrongHitPolicyLastTwo,
			},
			{
				ShardKeyListGetter: func(_ *octollm.Request) []string {
					return []string{"shadow-key"}
				},
				CacheKeyNamespace: "shadow-ns",
				StrongHitPolicy:   StrongHitPolicyLastTwo,
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
			{ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"primary"} }, IsPrimary: true, StrongHitPolicy: StrongHitPolicyLastTwo},
			{ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"shadow-a"} }, CacheKeyNamespace: "a:v1", StrongHitPolicy: StrongHitPolicyLastTwo},
			{ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"shadow-b"} }, CacheKeyNamespace: "b:v1", StrongHitPolicy: StrongHitPolicyLastTwo},
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
	require.NoError(t, rd.Set(ctx, keyPrefix+":is-leaf:sk0", "1", time.Minute).Err())

	provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: func(_ *octollm.Request) []string {
				return []string{"sk0", "sk1", "sk2"}
			},
			IsPrimary:       true,
			StrongHitPolicy: StrongHitPolicyLastTwo,
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
			StrongHitPolicy:    StrongHitPolicyLastTwo,
		}},
		RedisClient:  rd,
		KeyPrefix:    keyPrefix,
		ShardKeyTTL:  time.Minute,
		BackendNames: []string{"b0", "b1", "b2", "b3", "b4"},
	})
	require.NoError(t, err)

	prioritized, _, err := provider.Resolve(testhelper.CreateTestRequest())
	require.NoError(t, err)
	require.Len(t, prioritized, 5)
	assert.Equal(t, []string{"b4", "b3", "b2", "b1", "b0"}, []string{
		prioritized[0].Name, prioritized[1].Name, prioritized[2].Name,
		prioritized[3].Name, prioritized[4].Name,
	})

	card, err := rd.ZCard(ctx, keyPrefix+":k1").Result()
	require.NoError(t, err)
	assert.Equal(t, int64(3), card)
}

func TestShardKeyAffinityProvider_LeafTrimToThreeMembers(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	const keyPrefix = "trim-leaf"
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
			StrongHitPolicy:    StrongHitPolicyLeaf,
		}},
		RedisClient:  rd,
		KeyPrefix:    keyPrefix,
		ShardKeyTTL:  time.Minute,
		BackendNames: []string{"b0", "b1", "b2", "b3", "b4"},
	})
	require.NoError(t, err)

	prioritized, _, err := provider.Resolve(testhelper.CreateTestRequest())
	require.NoError(t, err)
	require.Len(t, prioritized, 5)
	assert.Equal(t, []string{"b4", "b3", "b2", "b1", "b0"}, []string{
		prioritized[0].Name, prioritized[1].Name, prioritized[2].Name,
		prioritized[3].Name, prioritized[4].Name,
	})

	card, err := rd.ZCard(ctx, keyPrefix+":k1").Result()
	require.NoError(t, err)
	assert.Equal(t, int64(3), card)
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
			StrongHitPolicy:    StrongHitPolicyLastTwo,
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
				StrongHitPolicy:    StrongHitPolicyLastTwo,
			},
			{
				ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"sk-shadow"} },
				CacheKeyNamespace:  "trace:v1",
				StrongHitPolicy:    StrongHitPolicyLastTwo,
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
		nil,
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
			{ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"primary"} }, IsPrimary: true, StrongHitPolicy: StrongHitPolicyLastTwo},
			{ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"shadow"} }, CacheKeyNamespace: "trace:v1", StrongHitPolicy: StrongHitPolicyLastTwo},
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
		nil,
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
				StrongHitPolicy:    StrongHitPolicyLastTwo,
			},
			{
				ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"sk-shadow"} },
				CacheKeyNamespace:  "ns1",
				StrongHitPolicy:    StrongHitPolicyLastTwo,
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
		nil,
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
			{ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"primary"} }, IsPrimary: true, StrongHitPolicy: StrongHitPolicyLastTwo},
			{ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"shadow"} }, CacheKeyNamespace: "trace:v1", StrongHitPolicy: StrongHitPolicyLastTwo},
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
		nil,
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
			nil,
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
			nil,
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
			nil,
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
			nil,
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
			StrongHitPolicy:    StrongHitPolicyLastTwo,
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

func TestShardKeyAffinityProvider_LeafPolicyResolve(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	const keyPrefix = "leaf-resolve"

	require.NoError(t, rd.ZAdd(ctx, keyPrefix+":both", redis.Z{Score: 1, Member: "svc-both"}).Err())
	require.NoError(t, rd.Set(ctx, keyPrefix+":is-leaf:both", "1", time.Minute).Err())
	require.NoError(t, rd.ZAdd(ctx, keyPrefix+":map-only", redis.Z{Score: 1, Member: "svc-map"}).Err())
	require.NoError(t, rd.Set(ctx, keyPrefix+":is-leaf:marker-only", "1", time.Minute).Err())

	provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: func(_ *octollm.Request) []string {
				return []string{"both", "map-only", "marker-only", "miss"}
			},
			IsPrimary:       true,
			StrongHitPolicy: StrongHitPolicyLeaf,
		}},
		RedisClient: rd,
		KeyPrefix:   keyPrefix,
		ShardKeyTTL: time.Minute,
	})
	require.NoError(t, err)

	prioritized, _, err := provider.Resolve(testhelper.CreateTestRequest())
	require.NoError(t, err)
	require.Len(t, prioritized, 2)
	assert.Equal(t, "svc-map", prioritized[0].Name)
	assert.False(t, prioritized[0].StrongCacheHit)
	assert.Equal(t, "svc-both", prioritized[1].Name)
	assert.True(t, prioritized[1].StrongCacheHit)
}

func TestShardKeyAffinityProvider_SwitchLastTwoAndLeaf(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	const keyPrefix = "policy-switch"
	hash := "only"

	require.NoError(t, rd.ZAdd(ctx, keyPrefix+":"+hash, redis.Z{Score: 1, Member: "svc-a"}).Err())

	build := func(policy string) AffinityProvider {
		t.Helper()
		provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
			Strategies: []ShardKeyStrategySpec{{
				ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{hash} },
				IsPrimary:          true,
				StrongHitPolicy:    policy,
			}},
			RedisClient:  rd,
			KeyPrefix:    keyPrefix,
			ShardKeyTTL:  time.Minute,
			BackendNames: []string{"svc-a"},
		})
		require.NoError(t, err)
		return provider
	}

	resolveHit := func(provider AffinityProvider) *PrioritizedBackend {
		t.Helper()
		prioritized, _, err := provider.Resolve(testhelper.CreateTestRequest())
		require.NoError(t, err)
		require.Len(t, prioritized, 1)
		return prioritized[0]
	}

	hit := resolveHit(build(StrongHitPolicyLastTwo))
	assert.Equal(t, "svc-a", hit.Name)
	assert.True(t, hit.StrongCacheHit)

	hit = resolveHit(build(StrongHitPolicyLeaf))
	assert.Equal(t, "svc-a", hit.Name)
	assert.False(t, hit.StrongCacheHit)

	hit = resolveHit(build(StrongHitPolicyLastTwo))
	assert.True(t, hit.StrongCacheHit)
}

func TestShardKeyAffinityProvider_AllowlistAndDedup(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	const keyPrefix = "allow-dedup"

	require.NoError(t, rd.ZAdd(ctx, keyPrefix+":sk-early",
		redis.Z{Score: 1, Member: "unknown"},
		redis.Z{Score: 2, Member: "hot"},
	).Err())
	require.NoError(t, rd.ZAdd(ctx, keyPrefix+":sk-late", redis.Z{Score: 1, Member: "hot"}).Err())

	provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: func(_ *octollm.Request) []string {
				return []string{"sk-early", "sk-late"}
			},
			IsPrimary:       true,
			StrongHitPolicy: StrongHitPolicyLastTwo,
		}},
		RedisClient:  rd,
		KeyPrefix:    keyPrefix,
		ShardKeyTTL:  time.Minute,
		BackendNames: []string{"hot"},
	})
	require.NoError(t, err)

	prioritized, _, err := provider.Resolve(testhelper.CreateTestRequest())
	require.NoError(t, err)
	require.Len(t, prioritized, 1)
	assert.Equal(t, "hot", prioritized[0].Name)
	assert.True(t, prioritized[0].StrongCacheHit)
}

func TestShardKeyAffinityProvider_AllowlistFiltersAll(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	const keyPrefix = "allow-none"

	require.NoError(t, rd.ZAdd(ctx, keyPrefix+":k1", redis.Z{Score: 1, Member: "unknown"}).Err())

	provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"k1"} },
			IsPrimary:          true,
			StrongHitPolicy:    StrongHitPolicyLastTwo,
		}},
		RedisClient:  rd,
		KeyPrefix:    keyPrefix,
		ShardKeyTTL:  time.Minute,
		BackendNames: []string{"known"},
	})
	require.NoError(t, err)

	prioritized, commit, err := provider.Resolve(testhelper.CreateTestRequest())
	require.NoError(t, err)
	assert.Empty(t, prioritized)
	assert.NotNil(t, commit)
}

func TestFilterKnownBackends(t *testing.T) {
	in := []*PrioritizedBackend{{Name: "a"}, {Name: "b"}}

	assert.Equal(t, in, filterKnownBackends(in, nil))
	assert.Equal(t, in, filterKnownBackends(in, map[string]struct{}{}))

	got := filterKnownBackends(in, map[string]struct{}{"b": {}})
	require.Len(t, got, 1)
	assert.Equal(t, "b", got[0].Name)
}

func TestShardKeyWeightedRoundRobin_AllZeroSkipsAffinityCommit(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	const keyPrefix = "wrr-all-zero"

	provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: func(_ *octollm.Request) []string { return []string{"k1"} },
			IsPrimary:          true,
			StrongHitPolicy:    StrongHitPolicyLastTwo,
		}},
		RedisClient: rd,
		KeyPrefix:   keyPrefix,
		ShardKeyTTL: time.Minute,
	})
	require.NoError(t, err)

	okEngine := octollm.EngineFunc(func(_ *octollm.Request) (*octollm.Response, error) {
		return &octollm.Response{StatusCode: 200}, nil
	})
	lb := &ShardKeyWeightedRoundRobin{
		backends: []*wrrBackend{
			{name: "svc-a", weight: 0, engine: okEngine},
		},
		affinityProvider: provider,
		retryTimeout:     time.Second,
		retryMaxCount:    1,
	}

	resp, err := lb.Process(testhelper.CreateTestRequest())
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 200, resp.StatusCode)

	card, err := rd.ZCard(context.Background(), keyPrefix+":k1").Result()
	require.NoError(t, err)
	assert.Zero(t, card)
	assert.Equal(t, int64(0), rd.Exists(context.Background(), keyPrefix+":is-leaf:k1").Val())
}
