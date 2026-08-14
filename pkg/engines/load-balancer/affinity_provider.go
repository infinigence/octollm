package loadbalancer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/octollm"
)

// PrioritizedBackend pairs an affinity-resolved backend name with whether it is a strong
// (high-confidence) cache hit. LB implementations use Name to look up their internal backend.
type PrioritizedBackend struct {
	Name string
	// StrongCacheHit marks high-confidence affinity; ShardKeyConcurrency lets such hits bypass
	// cache-miss headroom limits.
	StrongCacheHit bool
}

// AffinityCommitFunc persists affinity for a selected backend. Load balancers invoke it only after
// eng.Process succeeds; implementations capture any required request context when creating it.
type AffinityCommitFunc func(selectedBackend string) error

// AffinityProvider resolves backend affinity and optionally returns a deferred commit callback.
//
// Resolve returns candidates in priority order. A non-nil error prevents backend selection;
// implementations obtain cancellation and request-scoped values from req.Context().
type AffinityProvider interface {
	Resolve(req *octollm.Request) ([]*PrioritizedBackend, AffinityCommitFunc, error)
}

// ShardKeyStrategySpec describes how one strategy derives and namespaces shard keys.
type ShardKeyStrategySpec struct {
	// ShardKeyListGetter extracts keys in ascending priority order; later keys have higher priority.
	ShardKeyListGetter func(req *octollm.Request) []string
	// CacheKeyNamespace isolates this strategy's Redis keys from other strategies.
	CacheKeyNamespace string
	// IsPrimary enables Redis reads for routing. Exactly one strategy must be primary.
	IsPrimary bool
}

// ShardKeyAffinityProviderConfig configures a multi-strategy shard-key affinity provider.
type ShardKeyAffinityProviderConfig struct {
	// Strategies must contain exactly one primary strategy. Other strategies are write-only shadows.
	Strategies []ShardKeyStrategySpec
	// RedisClient is required for affinity reads and deferred writes.
	RedisClient *redis.Client
	// KeyPrefix is prepended to every strategy's Redis keys when non-empty.
	KeyPrefix string
	// ShardKeyTTL controls mapping expiration; values <= 0 disable deferred writes.
	ShardKeyTTL time.Duration
	// BackendNames optionally restricts resolved mappings to known backends. An empty list allows all.
	BackendNames []string
}

type shardKeyStrategyRuntime struct {
	getter    func(req *octollm.Request) []string
	namespace string
	isPrimary bool
}

// ShardKeyAffinityProvider is the built-in Redis-backed affinity provider. It reads routing
// affinity from the primary strategy and writes successful selections to the primary and all
// shadow strategies. Redis misses and read errors do not cause Resolve to return an error; when no
// candidates are resolved, load balancers fall back to their normal selection algorithm.
type ShardKeyAffinityProvider struct {
	primary      shardKeyStrategyRuntime
	shadows      []shardKeyStrategyRuntime
	redisClient  *redis.Client
	keyPrefix    string
	shardKeyTTL  time.Duration
	backendNames map[string]struct{}
}

var _ AffinityProvider = (*ShardKeyAffinityProvider)(nil)

// NewShardKeyAffinityProvider creates a provider with one primary strategy (Redis read + write)
// and zero or more shadow strategies (write-only on success).
func NewShardKeyAffinityProvider(cfg ShardKeyAffinityProviderConfig) (*ShardKeyAffinityProvider, error) {
	if cfg.RedisClient == nil {
		return nil, fmt.Errorf("redisClient must be provided")
	}
	if len(cfg.Strategies) == 0 {
		return nil, fmt.Errorf("strategies must have at least one item")
	}

	var primary *shardKeyStrategyRuntime
	shadows := make([]shardKeyStrategyRuntime, 0, len(cfg.Strategies))
	for i, s := range cfg.Strategies {
		if s.ShardKeyListGetter == nil {
			return nil, fmt.Errorf("strategy %d: ShardKeyListGetter must be provided", i)
		}
		rt := shardKeyStrategyRuntime{
			getter:    s.ShardKeyListGetter,
			namespace: s.CacheKeyNamespace,
			isPrimary: s.IsPrimary,
		}
		if s.IsPrimary {
			if primary != nil {
				return nil, fmt.Errorf("multiple primary strategies are not allowed")
			}
			primary = &rt
		} else {
			shadows = append(shadows, rt)
		}
	}
	if primary == nil {
		return nil, fmt.Errorf("exactly one primary strategy is required")
	}

	backendNames := make(map[string]struct{}, len(cfg.BackendNames))
	for _, name := range cfg.BackendNames {
		if name != "" {
			backendNames[name] = struct{}{}
		}
	}

	return &ShardKeyAffinityProvider{
		primary:      *primary,
		shadows:      shadows,
		redisClient:  cfg.RedisClient,
		keyPrefix:    cfg.KeyPrefix,
		shardKeyTTL:  cfg.ShardKeyTTL,
		backendNames: backendNames,
	}, nil
}

func redisKeyForStrategy(keyPrefix, namespace, shardKey string) string {
	if keyPrefix == "" {
		if namespace == "" {
			return shardKey
		}
		return namespace + ":" + shardKey
	}
	if namespace == "" {
		return keyPrefix + ":" + shardKey
	}
	return keyPrefix + ":" + namespace + ":" + shardKey
}

// Resolve reads primary affinity, snapshots this request's primary and shadow keys, and returns a
// callback for deferred persistence after backend success.
func (p *ShardKeyAffinityProvider) Resolve(req *octollm.Request) ([]*PrioritizedBackend, AffinityCommitFunc, error) {
	ctx := req.Context()
	primaryKeys := p.primary.getter(req)
	prioritized := p.resolvePrimaryBackends(ctx, primaryKeys)

	// Capture each request's keys now; commit runs only after a backend succeeds.
	primarySnapshot := append([]string(nil), primaryKeys...)
	shadowSnapshots := make([]struct {
		namespace string
		keys      []string
	}, len(p.shadows))
	for i, shadow := range p.shadows {
		keys := shadow.getter(req)
		shadowSnapshots[i] = struct {
			namespace string
			keys      []string
		}{
			namespace: shadow.namespace,
			keys:      append([]string(nil), keys...),
		}
	}

	commit := func(selectedBackend string) error {
		if selectedBackend == "" || p.shardKeyTTL <= 0 {
			return nil
		}
		pipe := p.redisClient.Pipeline()
		writeShardKeyMappings(pipe, ctx, p.keyPrefix, p.primary.namespace, primarySnapshot, selectedBackend, p.shardKeyTTL)
		for _, shadow := range shadowSnapshots {
			writeShardKeyMappings(pipe, ctx, p.keyPrefix, shadow.namespace, shadow.keys, selectedBackend, p.shardKeyTTL)
		}
		if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
			slog.WarnContext(ctx, fmt.Sprintf("[ShardKey affinity provider] failed to update shard key mapping in Redis: %v", err))
			return err
		}
		return nil
	}

	return prioritized, commit, nil
}

func writeShardKeyMappings(
	pipe redis.Pipeliner,
	ctx context.Context,
	keyPrefix, namespace string,
	shardKeyList []string,
	selectedBackend string,
	ttl time.Duration,
) {
	for _, shardKey := range shardKeyList {
		if shardKey == "" {
			continue
		}
		redisKey := redisKeyForStrategy(keyPrefix, namespace, shardKey)
		pipe.ZAdd(ctx, redisKey, redis.Z{
			Score:  float64(time.Now().Unix()),
			Member: selectedBackend,
		})
		pipe.Expire(ctx, redisKey, ttl)
	}
}

// resolvePrimaryBackends scans shard keys from last to first, so later keys have higher priority.
// Only primary mappings affect routing; Redis ZSETs are trimmed to their three newest backends.
// A hit on either of the last two shard keys is strong; this threshold is fixed and not configurable.
func (p *ShardKeyAffinityProvider) resolvePrimaryBackends(
	ctx context.Context,
	shardKeyList []string,
) []*PrioritizedBackend {
	if len(shardKeyList) == 0 {
		return nil
	}

	readPipe := p.redisClient.Pipeline()
	cmds := make([]*redis.StringSliceCmd, len(shardKeyList))
	for i, shardKey := range shardKeyList {
		if shardKey == "" {
			continue
		}
		cmds[i] = readPipe.ZRevRange(ctx, redisKeyForStrategy(p.keyPrefix, p.primary.namespace, shardKey), 0, -1)
	}
	if _, err := readPipe.Exec(ctx); err != nil && err != redis.Nil {
		slog.WarnContext(ctx, fmt.Sprintf("[ShardKey affinity provider] failed to exec Redis pipeline for shard keys: %v", err))
	}

	trimPipe := p.redisClient.Pipeline()
	seen := make(map[string]bool)
	var prioritized []*PrioritizedBackend
	shardKeyLen := len(shardKeyList)

	for i := len(shardKeyList) - 1; i >= 0; i-- {
		cmd := cmds[i]
		if cmd == nil {
			continue
		}
		backendNames, err := cmd.Result()
		if err != nil && err != redis.Nil {
			slog.DebugContext(ctx, fmt.Sprintf("[ShardKey affinity provider] Redis ZSET error for shard key %s: %v", shardKeyList[i], err))
			continue
		}
		if len(backendNames) > 3 {
			// ZRemRangeByRank uses ascending score order; remove the oldest ranks and retain
			// the three newest backend mappings.
			stop := int64(len(backendNames) - 4)
			if stop >= 0 {
				trimPipe.ZRemRangeByRank(ctx, redisKeyForStrategy(p.keyPrefix, p.primary.namespace, shardKeyList[i]), 0, stop)
			}
		}
		// ZRevRange already orders mappings from newest to oldest; preserve that order.
		for _, name := range backendNames {
			if name == "" || seen[name] {
				continue
			}
			if len(p.backendNames) > 0 {
				if _, ok := p.backendNames[name]; !ok {
					continue
				}
			}
			prioritized = append(prioritized, &PrioritizedBackend{
				Name:           name,
				StrongCacheHit: i >= shardKeyLen-2,
			})
			seen[name] = true
		}
	}

	if _, err := trimPipe.Exec(ctx); err != nil && err != redis.Nil {
		slog.WarnContext(ctx, fmt.Sprintf("[ShardKey affinity provider] failed to trim Redis ZSET for shard keys: %v", err))
	}

	return prioritized
}
