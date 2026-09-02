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

func testPolicyRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

func seedMapping(t *testing.T, rd *redis.Client, scope StrongHitScope, hash string, backends ...string) {
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

func seedMarker(t *testing.T, rd *redis.Client, scope StrongHitScope, hash string) {
	t.Helper()
	require.NoError(t, rd.Set(context.Background(), markerRedisKey(scope, hash), "1", time.Minute).Err())
}

func TestNewStrongCacheHitPolicy(t *testing.T) {
	rd := testPolicyRedis(t)

	t.Run("empty is unsupported", func(t *testing.T) {
		p, err := newStrongCacheHitPolicy("", rd)
		require.Error(t, err)
		assert.Nil(t, p)
	})

	t.Run("last_two", func(t *testing.T) {
		p, err := newStrongCacheHitPolicy(StrongHitPolicyLastTwo, rd)
		require.NoError(t, err)
		assert.Equal(t, StrongHitPolicyLastTwo, p.Name())
	})

	t.Run("leaf", func(t *testing.T) {
		p, err := newStrongCacheHitPolicy(StrongHitPolicyLeaf, rd)
		require.NoError(t, err)
		assert.Equal(t, StrongHitPolicyLeaf, p.Name())
	})

	t.Run("unsupported", func(t *testing.T) {
		p, err := newStrongCacheHitPolicy("unknown", rd)
		require.Error(t, err)
		assert.Nil(t, p)
		assert.Contains(t, err.Error(), "unknown")
	})

	t.Run("nil redis", func(t *testing.T) {
		p, err := newStrongCacheHitPolicy(StrongHitPolicyLastTwo, nil)
		require.Error(t, err)
		assert.Nil(t, p)
	})
}

func TestLastTwoIgnoresMarkersAndUsesLastTwoValidHashes(t *testing.T) {
	rd := testPolicyRedis(t)
	scope := testPathLearningScope()
	p, err := newStrongCacheHitPolicy(StrongHitPolicyLastTwo, rd)
	require.NoError(t, err)

	seedMapping(t, rd, scope, "H1", "old", "svc-h1")
	seedMapping(t, rd, scope, "H2", "svc-h2")
	seedMapping(t, rd, scope, "H3", "svc-h3")
	seedMarker(t, rd, scope, "H1")

	got, err := p.ResolveShardKeyAffinity(context.Background(), scope, []string{"H1", "H2", "H3"})
	require.NoError(t, err)
	require.Len(t, got, 3)

	assert.Equal(t, "H1", got[0].ShardKey)
	assert.Equal(t, []string{"svc-h1", "old"}, got[0].BackendNames)
	assert.False(t, got[0].StrongCacheHit)

	assert.Equal(t, "H2", got[1].ShardKey)
	assert.Equal(t, []string{"svc-h2"}, got[1].BackendNames)
	assert.True(t, got[1].StrongCacheHit)

	assert.Equal(t, "H3", got[2].ShardKey)
	assert.Equal(t, []string{"svc-h3"}, got[2].BackendNames)
	assert.True(t, got[2].StrongCacheHit)
}

func TestLastTwoEmptyKeysOccupyOriginalIndices(t *testing.T) {
	rd := testPolicyRedis(t)
	scope := testPathLearningScope()
	p, err := newStrongCacheHitPolicy(StrongHitPolicyLastTwo, rd)
	require.NoError(t, err)

	seedMapping(t, rd, scope, "A", "svc-a")
	seedMapping(t, rd, scope, "B", "svc-b")
	seedMapping(t, rd, scope, "C", "svc-c")

	got, err := p.ResolveShardKeyAffinity(context.Background(), scope, []string{"A", "B", "", "C"})
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "A", got[0].ShardKey)
	assert.False(t, got[0].StrongCacheHit)
	assert.Equal(t, "B", got[1].ShardKey)
	assert.False(t, got[1].StrongCacheHit)
	assert.Equal(t, "C", got[2].ShardKey)
	assert.True(t, got[2].StrongCacheHit)
}

func TestLastTwoSingleHashIsStrong(t *testing.T) {
	rd := testPolicyRedis(t)
	scope := testPathLearningScope()
	p, err := newStrongCacheHitPolicy(StrongHitPolicyLastTwo, rd)
	require.NoError(t, err)

	got, err := p.ResolveShardKeyAffinity(context.Background(), scope, []string{"only"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "only", got[0].ShardKey)
	assert.Empty(t, got[0].BackendNames)
	assert.True(t, got[0].StrongCacheHit)
}

func TestLastTwoNoValidKeys(t *testing.T) {
	rd := testPolicyRedis(t)
	p, err := newStrongCacheHitPolicy(StrongHitPolicyLastTwo, rd)
	require.NoError(t, err)

	got, err := p.ResolveShardKeyAffinity(context.Background(), testPathLearningScope(), []string{"", ""})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestLeafMappingMarkerCombinations(t *testing.T) {
	rd := testPolicyRedis(t)
	scope := testPathLearningScope()
	p, err := newStrongCacheHitPolicy(StrongHitPolicyLeaf, rd)
	require.NoError(t, err)

	seedMapping(t, rd, scope, "both", "svc-both")
	seedMarker(t, rd, scope, "both")

	seedMapping(t, rd, scope, "map-only", "svc-map")

	seedMarker(t, rd, scope, "marker-only")

	got, err := p.ResolveShardKeyAffinity(context.Background(), scope, []string{
		"both", "map-only", "marker-only", "miss",
	})
	require.NoError(t, err)
	require.Len(t, got, 4)

	assert.Equal(t, "both", got[0].ShardKey)
	assert.Equal(t, []string{"svc-both"}, got[0].BackendNames)
	assert.True(t, got[0].StrongCacheHit)

	assert.Equal(t, "map-only", got[1].ShardKey)
	assert.Equal(t, []string{"svc-map"}, got[1].BackendNames)
	assert.False(t, got[1].StrongCacheHit)

	assert.Equal(t, "marker-only", got[2].ShardKey)
	assert.Empty(t, got[2].BackendNames)
	assert.False(t, got[2].StrongCacheHit)

	assert.Equal(t, "miss", got[3].ShardKey)
	assert.Empty(t, got[3].BackendNames)
	assert.False(t, got[3].StrongCacheHit)
}

func TestLeafEmptyKeysDropped(t *testing.T) {
	rd := testPolicyRedis(t)
	scope := testPathLearningScope()
	p, err := newStrongCacheHitPolicy(StrongHitPolicyLeaf, rd)
	require.NoError(t, err)

	seedMapping(t, rd, scope, "H", "svc-h")
	seedMarker(t, rd, scope, "H")

	got, err := p.ResolveShardKeyAffinity(context.Background(), scope, []string{"", "H", ""})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "H", got[0].ShardKey)
	assert.True(t, got[0].StrongCacheHit)
}

func TestLeafNoValidKeys(t *testing.T) {
	rd := testPolicyRedis(t)
	p, err := newStrongCacheHitPolicy(StrongHitPolicyLeaf, rd)
	require.NoError(t, err)
	scope := testPathLearningScope()

	t.Run("all empty strings", func(t *testing.T) {
		got, err := p.ResolveShardKeyAffinity(context.Background(), scope, []string{"", ""})
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("nil list", func(t *testing.T) {
		got, err := p.ResolveShardKeyAffinity(context.Background(), scope, nil)
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

func TestLeafLearnThenResolve(t *testing.T) {
	rd := testPolicyRedis(t)
	scope := testPathLearningScope()
	execPathLearning(t, rd, scope, []string{"A", "B", "C"}, "svc-1", time.Minute)

	p, err := newStrongCacheHitPolicy(StrongHitPolicyLeaf, rd)
	require.NoError(t, err)
	got, err := p.ResolveShardKeyAffinity(context.Background(), scope, []string{"A", "B", "C"})
	require.NoError(t, err)
	require.Len(t, got, 3)

	assert.Equal(t, "A", got[0].ShardKey)
	assert.Equal(t, []string{"svc-1"}, got[0].BackendNames)
	assert.False(t, got[0].StrongCacheHit)

	assert.Equal(t, "B", got[1].ShardKey)
	assert.Equal(t, []string{"svc-1"}, got[1].BackendNames)
	assert.False(t, got[1].StrongCacheHit)

	assert.Equal(t, "C", got[2].ShardKey)
	assert.Equal(t, []string{"svc-1"}, got[2].BackendNames)
	assert.True(t, got[2].StrongCacheHit)
}
