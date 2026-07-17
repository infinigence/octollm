package loadbalancer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/octollm"
)

// PrioritizedBackend pairs a shard-key-resolved backend name with whether it is a strong
// (high-confidence) cache hit. LB implementations use Name to look up their internal backend.
type PrioritizedBackend struct {
	Name           string
	StrongCacheHit bool
}

// AffinityCommitFunc writes primary and shadow shard-key mappings after a successful request.
// It is only invoked when eng.Process returns err == nil.
type AffinityCommitFunc func(ctx context.Context, selectedBackend string) error

// AffinityProvider resolves shard-key affinity for routing and deferred Redis writes.
//
// Resolve may return a non-nil error for hard failures; LBs then fail the request
// without selecting a backend. The built-in ShardKeyAffinityProvider never does this
// for Redis miss/unavailable: it returns an empty prioritized list and err == nil so
// the LB falls back to normal selection (same soft-fail as the pre-extraction logic).
type AffinityProvider interface {
	Resolve(ctx context.Context, req *octollm.Request) ([]*PrioritizedBackend, AffinityCommitFunc, error)
}

// ShardKeyStrategySpec describes one shard-key strategy. maas-gateway compiles expr into
// ShardKeyListGetter before constructing the provider.
type ShardKeyStrategySpec struct {
	ShardKeyListGetter func(req *octollm.Request) []string
	CacheKeyNamespace  string
	IsPrimary          bool
}

// ShardKeyAffinityProviderConfig configures a multi-strategy shard-key affinity provider.
type ShardKeyAffinityProviderConfig struct {
	Strategies   []ShardKeyStrategySpec
	RedisClient  *redis.Client
	KeyPrefix    string
	ShardKeyTTL  time.Duration
	BackendNames []string
}

type shardKeyStrategyRuntime struct {
	getter    func(req *octollm.Request) []string
	namespace string
	isPrimary bool
}

type shardKeyAffinityProvider struct {
	primary      shardKeyStrategyRuntime
	shadows      []shardKeyStrategyRuntime
	redisClient  *redis.Client
	keyPrefix    string
	shardKeyTTL  time.Duration
	backendNames map[string]struct{}
}

var _ AffinityProvider = (*shardKeyAffinityProvider)(nil)

// NewShardKeyAffinityProvider creates a provider with one primary strategy (Redis read + write)
// and zero or more shadow strategies (write-only on success).
func NewShardKeyAffinityProvider(cfg ShardKeyAffinityProviderConfig) (AffinityProvider, error) {
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

	return &shardKeyAffinityProvider{
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

func (p *shardKeyAffinityProvider) Resolve(
	ctx context.Context,
	req *octollm.Request,
) ([]*PrioritizedBackend, AffinityCommitFunc, error) {
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

	commit := func(commitCtx context.Context, selectedBackend string) error {
		if selectedBackend == "" || p.shardKeyTTL <= 0 {
			return nil
		}
		pipe := p.redisClient.Pipeline()
		writeShardKeyMappings(pipe, commitCtx, p.keyPrefix, p.primary.namespace, primarySnapshot, selectedBackend, p.shardKeyTTL)
		for _, shadow := range shadowSnapshots {
			writeShardKeyMappings(pipe, commitCtx, p.keyPrefix, shadow.namespace, shadow.keys, selectedBackend, p.shardKeyTTL)
		}
		if _, err := pipe.Exec(commitCtx); err != nil && err != redis.Nil {
			slog.WarnContext(commitCtx, fmt.Sprintf("[ShardKey affinity provider] failed to update shard key mapping in Redis: %v", err))
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
func (p *shardKeyAffinityProvider) resolvePrimaryBackends(
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
			stop := int64(len(backendNames) - 4)
			if stop >= 0 {
				trimPipe.ZRemRangeByRank(ctx, redisKeyForStrategy(p.keyPrefix, p.primary.namespace, shardKeyList[i]), 0, stop)
			}
		}
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
