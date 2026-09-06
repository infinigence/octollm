package loadbalancer

import (
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
	// StrongHitPolicy is last_two or leaf. Empty is invalid; callers must pass a concrete name.
	StrongHitPolicy string
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

// ShardKeyAffinityProvider is the built-in Redis-backed affinity provider. It reads routing
// affinity from the primary strategy and writes successful selections to the primary and all
// shadow strategies. Redis misses and read errors do not cause Resolve to return an error; when no
// candidates are resolved, load balancers fall back to their normal selection algorithm.
type ShardKeyAffinityProvider struct {
	primary      shardKeyStrategyRuntime
	shadows      []shardKeyStrategyRuntime
	redisClient  *redis.Client
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
		if s.StrongHitPolicy != StrongHitPolicyLastTwo && s.StrongHitPolicy != StrongHitPolicyLeaf {
			return nil, fmt.Errorf("strategy %d namespace %q: unsupported strong_hit_policy %q", i, s.CacheKeyNamespace, s.StrongHitPolicy)
		}
		rt := shardKeyStrategyRuntime{
			getShardKeys:    s.ShardKeyListGetter,
			keyPrefix:       cfg.KeyPrefix,
			namespace:       s.CacheKeyNamespace,
			strongHitPolicy: s.StrongHitPolicy,
			redis:           cfg.RedisClient,
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

func lastNonEmptyShardKey(shardKeys []string) string {
	for i := len(shardKeys) - 1; i >= 0; i-- {
		if shardKeys[i] != "" {
			return shardKeys[i]
		}
	}
	return ""
}

func countNonEmptyShardKeys(shardKeys []string) int {
	n := 0
	for _, k := range shardKeys {
		if k != "" {
			n++
		}
	}
	return n
}

// Resolve reads primary affinity, captures this request's primary and shadow keys, and returns a
// callback for deferred persistence after backend success.
func (p *ShardKeyAffinityProvider) Resolve(req *octollm.Request) ([]*PrioritizedBackend, AffinityCommitFunc, error) {
	ctx := req.Context()
	primaryKeys := p.primary.getShardKeys(req)

	prioritized, err := p.primary.lookupAffinity(ctx, primaryKeys)
	if err != nil {
		slog.WarnContext(ctx, fmt.Sprintf("[ShardKey affinity provider] failed to resolve shard key affinity: %v", err))
		prioritized = nil
	} else {
		prioritized = filterKnownBackends(prioritized, p.backendNames)
	}

	shadowKeys := make([][]string, len(p.shadows))
	for i := range p.shadows {
		shadowKeys[i] = p.shadows[i].getShardKeys(req)
	}

	commit := func(selectedBackend string) error {
		if selectedBackend == "" || p.shardKeyTTL <= 0 {
			return nil
		}
		pipe := p.redisClient.Pipeline()
		p.primary.enqueueSuccessfulPathLearning(pipe, ctx, primaryKeys, selectedBackend, p.shardKeyTTL)
		for i := range p.shadows {
			p.shadows[i].enqueueSuccessfulPathLearning(pipe, ctx, shadowKeys[i], selectedBackend, p.shardKeyTTL)
		}
		if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
			slog.WarnContext(ctx, fmt.Sprintf("[ShardKey affinity provider] failed to update shard key mapping in Redis: %v", err))
			return err
		}
		if leaf := lastNonEmptyShardKey(primaryKeys); leaf != "" {
			slog.DebugContext(ctx, "[ShardKey affinity provider] path learning committed",
				"backend_name", selectedBackend,
				"namespace", p.primary.keyspace().Namespace,
				"mapping_key", p.primary.keyspace().mappingKey(leaf),
				"marker_key", p.primary.keyspace().markerKey(leaf),
				"hash_count", countNonEmptyShardKeys(primaryKeys),
				"shadow_count", len(shadowKeys),
			)
		}
		return nil
	}

	return prioritized, commit, nil
}

// filterKnownBackends drops candidates not in known. An empty known set means
// no allowlist is configured and all candidates are kept.
func filterKnownBackends(prioritized []*PrioritizedBackend, known map[string]struct{}) []*PrioritizedBackend {
	if len(known) == 0 {
		return prioritized
	}
	out := make([]*PrioritizedBackend, 0, len(prioritized))
	for _, b := range prioritized {
		if _, ok := known[b.Name]; ok {
			out = append(out, b)
		}
	}
	return out
}
