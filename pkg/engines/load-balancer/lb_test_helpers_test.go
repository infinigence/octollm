package loadbalancer

import (
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/octollm"
)

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
	provider, err := NewShardKeyAffinityProvider(ShardKeyAffinityProviderConfig{
		Strategies: []ShardKeyStrategySpec{{
			ShardKeyListGetter: getter,
			IsPrimary:          true,
		}},
		RedisClient:  rd,
		KeyPrefix:    keyPrefix,
		ShardKeyTTL:  ttl,
		BackendNames: backendNamesFromItems(items),
	})
	require.NoError(t, err)
	return provider
}
