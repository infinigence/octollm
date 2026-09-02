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

func testPathLearningScope() StrongHitScope {
	return StrongHitScope{
		KeyPrefix: "maas:cache_aware:model-a",
		Namespace: "message5:v1",
	}
}

func mappingRedisKey(scope StrongHitScope, hash string) string {
	return redisKeyForStrategy(scope.KeyPrefix, scope.Namespace, hash)
}

func markerRedisKey(scope StrongHitScope, hash string) string {
	return redisKeyForStrategy(scope.KeyPrefix, scope.Namespace, "is-leaf:"+hash)
}

func execPathLearning(t *testing.T, rd *redis.Client, scope StrongHitScope, hashes []string, backend string, ttl time.Duration) {
	t.Helper()
	ctx := context.Background()
	pipe := rd.Pipeline()
	enqueueSuccessfulPathLearning(pipe, ctx, scope, hashes, backend, ttl)
	_, err := pipe.Exec(ctx)
	if err != nil && err != redis.Nil {
		require.NoError(t, err)
	}
}

func requireMappingBackend(t *testing.T, rd *redis.Client, scope StrongHitScope, hash, backend string) {
	t.Helper()
	members, err := rd.ZRevRange(context.Background(), mappingRedisKey(scope, hash), 0, -1).Result()
	require.NoError(t, err)
	require.Contains(t, members, backend)
}

func requireMarker(t *testing.T, rd *redis.Client, scope StrongHitScope, hash string, wantPresent bool) {
	t.Helper()
	val, err := rd.Get(context.Background(), markerRedisKey(scope, hash)).Result()
	if !wantPresent {
		assert.ErrorIs(t, err, redis.Nil)
		return
	}
	require.NoError(t, err)
	assert.Equal(t, "1", val)
}

func TestEnqueueSuccessfulPathLearning_ABC(t *testing.T) {
	mr := miniredis.RunT(t)
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	scope := testPathLearningScope()

	execPathLearning(t, rd, scope, []string{"A", "B", "C"}, "svc-1", time.Minute)

	requireMappingBackend(t, rd, scope, "A", "svc-1")
	requireMappingBackend(t, rd, scope, "B", "svc-1")
	requireMappingBackend(t, rd, scope, "C", "svc-1")
	requireMarker(t, rd, scope, "A", false)
	requireMarker(t, rd, scope, "B", false)
	requireMarker(t, rd, scope, "C", true)
}

func TestEnqueueSuccessfulPathLearning_ABCThenABCD(t *testing.T) {
	mr := miniredis.RunT(t)
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	scope := testPathLearningScope()

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
	mr := miniredis.RunT(t)
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	scope := testPathLearningScope()
	hashes := []string{"H1", "H2", "H3", "H4", "H5"}

	execPathLearning(t, rd, scope, hashes, "svc-1", time.Minute)

	for i, hash := range hashes {
		requireMappingBackend(t, rd, scope, hash, "svc-1")
		requireMarker(t, rd, scope, hash, i == len(hashes)-1)
	}
}

func TestEnqueueSuccessfulPathLearning_DuplicateHashLastWins(t *testing.T) {
	mr := miniredis.RunT(t)
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	scope := testPathLearningScope()

	execPathLearning(t, rd, scope, []string{"A", "B", "A"}, "svc-1", time.Minute)

	requireMappingBackend(t, rd, scope, "A", "svc-1")
	requireMappingBackend(t, rd, scope, "B", "svc-1")
	requireMarker(t, rd, scope, "A", true)
	requireMarker(t, rd, scope, "B", false)
}

func TestEnqueueSuccessfulPathLearning_SameExpireAt(t *testing.T) {
	mr := miniredis.RunT(t)
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	scope := testPathLearningScope()
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
	mr := miniredis.RunT(t)
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	scope := testPathLearningScope()
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
	mr := miniredis.RunT(t)
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	scope := testPathLearningScope()

	execPathLearning(t, rd, scope, []string{"A", "", "B"}, "svc-1", time.Minute)

	requireMappingBackend(t, rd, scope, "A", "svc-1")
	requireMappingBackend(t, rd, scope, "B", "svc-1")
	requireMarker(t, rd, scope, "A", false)
	requireMarker(t, rd, scope, "B", true)
}

func TestLeafMarkerRedisKeyFormula(t *testing.T) {
	scope := testPathLearningScope()
	assert.Equal(t, "maas:cache_aware:model-a:message5:v1:C", mappingRedisKey(scope, "C"))
	assert.Equal(t, "maas:cache_aware:model-a:message5:v1:is-leaf:C", markerRedisKey(scope, "C"))
}

func TestEnqueueSuccessfulPathLearning_ExistingMappingGetsLeafMarker(t *testing.T) {
	mr := miniredis.RunT(t)
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	scope := testPathLearningScope()
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
	scope := testPathLearningScope()

	t.Run("ABC then AB", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		execPathLearning(t, rd, scope, []string{"A", "B", "C"}, "svc-long", time.Minute)
		execPathLearning(t, rd, scope, []string{"A", "B"}, "svc-short", time.Minute)

		requireMarker(t, rd, scope, "A", false)
		requireMarker(t, rd, scope, "B", true)
		requireMarker(t, rd, scope, "C", true)
	})

	t.Run("AB then ABC", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		execPathLearning(t, rd, scope, []string{"A", "B"}, "svc-short", time.Minute)
		execPathLearning(t, rd, scope, []string{"A", "B", "C"}, "svc-long", time.Minute)

		requireMarker(t, rd, scope, "A", false)
		requireMarker(t, rd, scope, "B", false)
		requireMarker(t, rd, scope, "C", true)
	})
}
