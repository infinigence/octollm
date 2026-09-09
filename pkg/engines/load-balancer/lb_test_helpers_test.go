package loadbalancer

import (
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/octollm"
)

func testConcurrencyKey(backendName string) string {
	return "concurrency:" + backendName + ":tier_0"
}

func gpt4ConcurrencyKey(backendName string) string {
	return "concurrency_rate:service:gpt-4:" + backendName + ":tier_0"
}

func withConcurrencyKeys(items []BackendItem, keyFor func(string) string) []BackendItem {
	out := make([]BackendItem, len(items))
	copy(out, items)
	for i := range out {
		if out[i].ConcurrencyKeyFn != nil {
			continue
		}
		name := out[i].Name
		out[i].ConcurrencyKeyFn = func(*octollm.Request) string { return keyFor(name) }
	}
	return out
}

func withTestConcurrencyKeys(items []BackendItem) []BackendItem {
	return withConcurrencyKeys(items, testConcurrencyKey)
}

func withGPT4ConcurrencyKeys(items []BackendItem) []BackendItem {
	return withConcurrencyKeys(items, gpt4ConcurrencyKey)
}

func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	return redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
}

func backendNamesFromItems(items []BackendItem) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		if item.Name != "" {
			names = append(names, item.Name)
		}
	}
	return names
}

func newPrimaryShardKeyProvider(
	t *testing.T,
	getter func(req *octollm.Request) []string,
	rd *redis.Client,
	keyPrefix string,
	ttl time.Duration,
	items []BackendItem,
) AffinityProvider {
	t.Helper()
	return newPrimaryShardKeyProviderWithPolicy(t, getter, rd, keyPrefix, ttl, items, StrongHitPolicyLastTwo)
}

func newPrimaryShardKeyProviderWithPolicy(
	t *testing.T,
	getter func(req *octollm.Request) []string,
	rd *redis.Client,
	keyPrefix string,
	ttl time.Duration,
	items []BackendItem,
	policy string,
) AffinityProvider {
	t.Helper()
	provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: getter,
			IsPrimary:          true,
			StrongHitPolicy:    policy,
		}},
		RedisClient:  rd,
		KeyPrefix:    keyPrefix,
		ShardKeyTTL:  ttl,
		BackendNames: backendNamesFromItems(items),
	})
	require.NoError(t, err)
	return provider
}
