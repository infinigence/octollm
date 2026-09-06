package loadbalancer

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testRuntimeScope() affinityKeyspace {
	return affinityKeyspace{
		KeyPrefix: "maas:cache_aware:model-a",
		Namespace: "message5:v1",
	}
}

func testRuntimeRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

func mappingRedisKey(scope affinityKeyspace, hash string) string {
	return scope.mappingKey(hash)
}

func markerRedisKey(scope affinityKeyspace, hash string) string {
	return scope.markerKey(hash)
}

func execPathLearning(t *testing.T, rd *redis.Client, scope affinityKeyspace, hashes []string, backend string, ttl time.Duration) {
	t.Helper()
	ctx := context.Background()
	pipe := rd.Pipeline()
	runtime := &shardKeyStrategyRuntime{
		keyPrefix: scope.KeyPrefix,
		namespace: scope.Namespace,
	}
	runtime.enqueueSuccessfulPathLearning(pipe, ctx, hashes, backend, ttl)
	_, err := pipe.Exec(ctx)
	if err != nil && err != redis.Nil {
		require.NoError(t, err)
	}
}

func requireMappingBackend(t *testing.T, rd *redis.Client, scope affinityKeyspace, hash, backend string) {
	t.Helper()
	members, err := rd.ZRevRange(context.Background(), mappingRedisKey(scope, hash), 0, -1).Result()
	require.NoError(t, err)
	require.Contains(t, members, backend)
}

func requireMarker(t *testing.T, rd *redis.Client, scope affinityKeyspace, hash string, wantPresent bool) {
	t.Helper()
	val, err := rd.Get(context.Background(), markerRedisKey(scope, hash)).Result()
	if !wantPresent {
		assert.ErrorIs(t, err, redis.Nil)
		return
	}
	require.NoError(t, err)
	assert.Equal(t, "1", val)
}

func seedMapping(t *testing.T, rd *redis.Client, scope affinityKeyspace, hash string, backends ...string) {
	t.Helper()
	if len(backends) == 0 {
		return
	}
	members := make([]redis.Z, len(backends))
	now := time.Now().Unix()
	for i, name := range backends {
		members[i] = redis.Z{
			Score:  float64(now + int64(i)),
			Member: name,
		}
	}
	require.NoError(t, rd.ZAdd(context.Background(), mappingRedisKey(scope, hash), members...).Err())
}

func seedMarker(t *testing.T, rd *redis.Client, scope affinityKeyspace, hash string) {
	t.Helper()
	require.NoError(t, rd.Set(context.Background(), markerRedisKey(scope, hash), "1", time.Minute).Err())
}

func testRuntime(policy string, rd *redis.Client, scope affinityKeyspace) *shardKeyStrategyRuntime {
	return &shardKeyStrategyRuntime{
		keyPrefix:       scope.KeyPrefix,
		namespace:       scope.Namespace,
		strongHitPolicy: policy,
		redis:           rd,
	}
}

func requirePrioritized(t *testing.T, got []*PrioritizedBackend, names []string, strong []bool) {
	t.Helper()
	require.Len(t, got, len(names))
	for i := range names {
		assert.Equal(t, names[i], got[i].Name)
		assert.Equal(t, strong[i], got[i].StrongCacheHit)
	}
}

func TestLastTwoIgnoresMarkersAndUsesLastTwoValidHashes(t *testing.T) {
	rd := testRuntimeRedis(t)
	scope := testRuntimeScope()
	rt := testRuntime(StrongHitPolicyLastTwo, rd, scope)

	seedMapping(t, rd, scope, "H1", "old", "svc-h1")
	seedMapping(t, rd, scope, "H2", "svc-h2")
	seedMapping(t, rd, scope, "H3", "svc-h3")
	seedMarker(t, rd, scope, "H1")

	got, err := rt.lookupAffinity(context.Background(), []string{"H1", "H2", "H3"})
	require.NoError(t, err)
	requirePrioritized(t, got,
		[]string{"svc-h3", "svc-h2", "svc-h1", "old"},
		[]bool{true, true, false, false},
	)
}

func TestLastTwoEmptyKeysOccupyOriginalIndices(t *testing.T) {
	rd := testRuntimeRedis(t)
	scope := testRuntimeScope()
	rt := testRuntime(StrongHitPolicyLastTwo, rd, scope)

	seedMapping(t, rd, scope, "A", "svc-a")
	seedMapping(t, rd, scope, "B", "svc-b")
	seedMapping(t, rd, scope, "C", "svc-c")

	got, err := rt.lookupAffinity(context.Background(), []string{"A", "B", "", "C"})
	require.NoError(t, err)
	requirePrioritized(t, got,
		[]string{"svc-c", "svc-b", "svc-a"},
		[]bool{true, false, false},
	)
}

func TestLastTwoSingleHashIsStrong(t *testing.T) {
	rd := testRuntimeRedis(t)
	scope := testRuntimeScope()
	rt := testRuntime(StrongHitPolicyLastTwo, rd, scope)
	seedMapping(t, rd, scope, "only", "svc-only")

	got, err := rt.lookupAffinity(context.Background(), []string{"only"})
	require.NoError(t, err)
	requirePrioritized(t, got, []string{"svc-only"}, []bool{true})
}

func TestLastTwoNoValidKeys(t *testing.T) {
	rd := testRuntimeRedis(t)
	rt := testRuntime(StrongHitPolicyLastTwo, rd, testRuntimeScope())

	got, err := rt.lookupAffinity(context.Background(), []string{"", ""})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestLeafMappingMarkerCombinations(t *testing.T) {
	rd := testRuntimeRedis(t)
	scope := testRuntimeScope()
	rt := testRuntime(StrongHitPolicyLeaf, rd, scope)

	seedMapping(t, rd, scope, "both", "svc-both")
	seedMarker(t, rd, scope, "both")

	seedMapping(t, rd, scope, "map-only", "svc-map")

	seedMarker(t, rd, scope, "marker-only")

	got, err := rt.lookupAffinity(context.Background(), []string{
		"both", "map-only", "marker-only", "miss",
	})
	require.NoError(t, err)
	requirePrioritized(t, got,
		[]string{"svc-map", "svc-both"},
		[]bool{false, true},
	)
}

func TestLeafEmptyKeysDropped(t *testing.T) {
	rd := testRuntimeRedis(t)
	scope := testRuntimeScope()
	rt := testRuntime(StrongHitPolicyLeaf, rd, scope)

	seedMapping(t, rd, scope, "H", "svc-h")
	seedMarker(t, rd, scope, "H")

	got, err := rt.lookupAffinity(context.Background(), []string{"", "H", ""})
	require.NoError(t, err)
	requirePrioritized(t, got, []string{"svc-h"}, []bool{true})
}

func TestLeafNoValidKeys(t *testing.T) {
	rd := testRuntimeRedis(t)
	rt := testRuntime(StrongHitPolicyLeaf, rd, testRuntimeScope())

	t.Run("all empty strings", func(t *testing.T) {
		got, err := rt.lookupAffinity(context.Background(), []string{"", ""})
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("nil list", func(t *testing.T) {
		got, err := rt.lookupAffinity(context.Background(), nil)
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

func TestLeafLearnThenResolve(t *testing.T) {
	rd := testRuntimeRedis(t)
	scope := testRuntimeScope()
	execPathLearning(t, rd, scope, []string{"A", "B", "C"}, "svc-1", time.Minute)

	rt := testRuntime(StrongHitPolicyLeaf, rd, scope)
	got, err := rt.lookupAffinity(context.Background(), []string{"A", "B", "C"})
	require.NoError(t, err)
	requirePrioritized(t, got, []string{"svc-1"}, []bool{true})
}

func TestEnqueueSuccessfulPathLearning_ABC(t *testing.T) {
	rd := testRuntimeRedis(t)
	scope := testRuntimeScope()

	execPathLearning(t, rd, scope, []string{"A", "B", "C"}, "svc-1", time.Minute)

	requireMappingBackend(t, rd, scope, "A", "svc-1")
	requireMappingBackend(t, rd, scope, "B", "svc-1")
	requireMappingBackend(t, rd, scope, "C", "svc-1")
	requireMarker(t, rd, scope, "A", false)
	requireMarker(t, rd, scope, "B", false)
	requireMarker(t, rd, scope, "C", true)
}

func TestEnqueueSuccessfulPathLearning_ABCThenABCD(t *testing.T) {
	rd := testRuntimeRedis(t)
	scope := testRuntimeScope()

	execPathLearning(t, rd, scope, []string{"A", "B", "C"}, "svc-1", time.Minute)
	execPathLearning(t, rd, scope, []string{"A", "B", "C", "D"}, "svc-2", time.Minute)

	requireMappingBackend(t, rd, scope, "A", "svc-2")
	requireMappingBackend(t, rd, scope, "B", "svc-2")
	requireMappingBackend(t, rd, scope, "C", "svc-2")
	requireMappingBackend(t, rd, scope, "D", "svc-2")
	requireMarker(t, rd, scope, "A", false)
	requireMarker(t, rd, scope, "B", false)
	requireMarker(t, rd, scope, "C", false)
	requireMarker(t, rd, scope, "D", true)
}

func TestEnqueueSuccessfulPathLearning_FiveHashes(t *testing.T) {
	rd := testRuntimeRedis(t)
	scope := testRuntimeScope()
	hashes := []string{"H1", "H2", "H3", "H4", "H5"}

	execPathLearning(t, rd, scope, hashes, "svc-1", time.Minute)

	for i, hash := range hashes {
		requireMappingBackend(t, rd, scope, hash, "svc-1")
		requireMarker(t, rd, scope, hash, i == len(hashes)-1)
	}
}

func TestEnqueueSuccessfulPathLearning_DuplicateHashLastWins(t *testing.T) {
	rd := testRuntimeRedis(t)
	scope := testRuntimeScope()

	execPathLearning(t, rd, scope, []string{"A", "B", "A"}, "svc-1", time.Minute)

	requireMappingBackend(t, rd, scope, "A", "svc-1")
	requireMappingBackend(t, rd, scope, "B", "svc-1")
	requireMarker(t, rd, scope, "A", true)
	requireMarker(t, rd, scope, "B", false)
}

func TestEnqueueSuccessfulPathLearning_SameExpireAt(t *testing.T) {
	rd := testRuntimeRedis(t)
	scope := testRuntimeScope()
	ctx := context.Background()

	execPathLearning(t, rd, scope, []string{"A", "B"}, "svc-1", time.Minute)

	mappingTTL, err := rd.PTTL(ctx, mappingRedisKey(scope, "B")).Result()
	require.NoError(t, err)
	markerTTL, err := rd.PTTL(ctx, markerRedisKey(scope, "B")).Result()
	require.NoError(t, err)
	assert.InDelta(t, mappingTTL.Milliseconds(), markerTTL.Milliseconds(), 5)
	assert.Greater(t, mappingTTL, 50*time.Second)
}

func TestEnqueueSuccessfulPathLearning_Skips(t *testing.T) {
	rd := testRuntimeRedis(t)
	scope := testRuntimeScope()
	ctx := context.Background()

	t.Run("empty backend", func(t *testing.T) {
		execPathLearning(t, rd, scope, []string{"A"}, "", time.Minute)
		assert.Equal(t, int64(0), rd.Exists(ctx, mappingRedisKey(scope, "A")).Val())
		assert.Equal(t, int64(0), rd.Exists(ctx, markerRedisKey(scope, "A")).Val())
	})

	t.Run("non-positive ttl", func(t *testing.T) {
		execPathLearning(t, rd, scope, []string{"B"}, "svc-1", 0)
		assert.Equal(t, int64(0), rd.Exists(ctx, mappingRedisKey(scope, "B")).Val())
		assert.Equal(t, int64(0), rd.Exists(ctx, markerRedisKey(scope, "B")).Val())
	})

	t.Run("no valid hashes", func(t *testing.T) {
		execPathLearning(t, rd, scope, []string{"", ""}, "svc-1", time.Minute)
		keys, err := rd.Keys(ctx, "*").Result()
		require.NoError(t, err)
		assert.Empty(t, keys)
	})
}

func TestEnqueueSuccessfulPathLearning_EmptyHashesDropped(t *testing.T) {
	rd := testRuntimeRedis(t)
	scope := testRuntimeScope()

	execPathLearning(t, rd, scope, []string{"A", "", "B"}, "svc-1", time.Minute)

	requireMappingBackend(t, rd, scope, "A", "svc-1")
	requireMappingBackend(t, rd, scope, "B", "svc-1")
	requireMarker(t, rd, scope, "A", false)
	requireMarker(t, rd, scope, "B", true)
}

func TestLeafMarkerRedisKeyFormula(t *testing.T) {
	scope := testRuntimeScope()
	assert.Equal(t, "maas:cache_aware:model-a:message5:v1:C", mappingRedisKey(scope, "C"))
	assert.Equal(t, "maas:cache_aware:model-a:message5:v1:is-leaf:C", markerRedisKey(scope, "C"))
}

func TestEnqueueSuccessfulPathLearning_ExistingMappingGetsLeafMarker(t *testing.T) {
	rd := testRuntimeRedis(t)
	scope := testRuntimeScope()
	ctx := context.Background()

	require.NoError(t, rd.ZAdd(ctx, mappingRedisKey(scope, "C"), redis.Z{Score: 1, Member: "svc-old"}).Err())
	requireMarker(t, rd, scope, "C", false)

	execPathLearning(t, rd, scope, []string{"A", "B", "C"}, "svc-new", time.Minute)

	requireMappingBackend(t, rd, scope, "C", "svc-new")
	requireMarker(t, rd, scope, "A", false)
	requireMarker(t, rd, scope, "B", false)
	requireMarker(t, rd, scope, "C", true)
}

func TestEnqueueSuccessfulPathLearning_LastWriterWinsOnOverlappingLeaf(t *testing.T) {
	scope := testRuntimeScope()

	t.Run("ABC then AB", func(t *testing.T) {
		rd := testRuntimeRedis(t)
		execPathLearning(t, rd, scope, []string{"A", "B", "C"}, "svc-long", time.Minute)
		execPathLearning(t, rd, scope, []string{"A", "B"}, "svc-short", time.Minute)

		requireMarker(t, rd, scope, "A", false)
		requireMarker(t, rd, scope, "B", true)
		requireMarker(t, rd, scope, "C", true)
	})

	t.Run("AB then ABC", func(t *testing.T) {
		rd := testRuntimeRedis(t)
		execPathLearning(t, rd, scope, []string{"A", "B"}, "svc-short", time.Minute)
		execPathLearning(t, rd, scope, []string{"A", "B", "C"}, "svc-long", time.Minute)

		requireMarker(t, rd, scope, "A", false)
		requireMarker(t, rd, scope, "B", false)
		requireMarker(t, rd, scope, "C", true)
	})
}

func TestLookupAffinityForLastTwoPolicy_NilAndEmpty(t *testing.T) {
	rd := testRuntimeRedis(t)
	scope := testRuntimeScope()

	got, err := lookupAffinityForLastTwoPolicy(context.Background(), rd, scope, nil)
	require.NoError(t, err)
	assert.Empty(t, got)

	got, err = lookupAffinityForLastTwoPolicy(context.Background(), rd, scope, []string{})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestLookupAffinity_UnsupportedPolicy(t *testing.T) {
	rt := &shardKeyStrategyRuntime{strongHitPolicy: "not-a-policy"}
	_, err := rt.lookupAffinity(context.Background(), []string{"k"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported strong_hit_policy")
}
