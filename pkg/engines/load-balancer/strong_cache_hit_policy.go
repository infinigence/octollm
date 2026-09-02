package loadbalancer

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

const (
	StrongHitPolicyLastTwo = "last_two"
	StrongHitPolicyLeaf    = "leaf"
)

// StrongCacheHitPolicy reads shard-key affinity snapshots. It does not persist
// success learning; that stays on AffinityCommitFunc.
type StrongCacheHitPolicy interface {
	Name() string
	ResolveShardKeyAffinity(
		ctx context.Context,
		scope StrongHitScope,
		shardKeys []string,
	) ([]ShardKeyAffinitySnapshot, error)
}

// ShardKeyAffinitySnapshot is one hash's mapping members (newest first) and
// whether this hash qualifies as a strong cache hit under the active policy.
type ShardKeyAffinitySnapshot struct {
	ShardKey       string
	BackendNames   []string
	StrongCacheHit bool
}

const leafAffinityReadLua = `
-- KEYS[1]: mapping ZSET; KEYS[2]: marker STRING
local members = redis.call('ZREVRANGE', KEYS[1], 0, -1)
local exists = redis.call('EXISTS', KEYS[2])
return {members, exists}
`

func newStrongCacheHitPolicy(name string, redisClient *redis.Client) (StrongCacheHitPolicy, error) {
	if redisClient == nil {
		return nil, fmt.Errorf("redisClient must be provided")
	}
	switch name {
	case StrongHitPolicyLastTwo:
		return &lastTwoStrongCacheHitPolicy{redis: redisClient}, nil
	case StrongHitPolicyLeaf:
		return &leafStrongCacheHitPolicy{redis: redisClient}, nil
	default:
		return nil, fmt.Errorf("unsupported strong_hit_policy %q", name)
	}
}

func validShardKeys(shardKeys []string) []string {
	valid := make([]string, 0, len(shardKeys))
	for _, k := range shardKeys {
		if k != "" {
			valid = append(valid, k)
		}
	}
	return valid
}

type lastTwoStrongCacheHitPolicy struct {
	redis *redis.Client
}

func (p *lastTwoStrongCacheHitPolicy) Name() string {
	return StrongHitPolicyLastTwo
}

func (p *lastTwoStrongCacheHitPolicy) ResolveShardKeyAffinity(
	ctx context.Context,
	scope StrongHitScope,
	shardKeys []string,
) ([]ShardKeyAffinitySnapshot, error) {
	if len(shardKeys) == 0 {
		return nil, nil
	}

	pipe := p.redis.Pipeline()
	cmds := make([]*redis.StringSliceCmd, len(shardKeys))
	queued := false
	for i, hash := range shardKeys {
		if hash == "" {
			continue
		}
		cmds[i] = pipe.ZRevRange(ctx, redisKeyForStrategy(scope.KeyPrefix, scope.Namespace, hash), 0, -1)
		queued = true
	}
	if queued {
		if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
			return nil, err
		}
	}

	// Match resolvePrimaryBackends: empty strings keep their index and can occupy
	// a last-two slot without producing a snapshot.
	strongFrom := len(shardKeys) - 2
	var out []ShardKeyAffinitySnapshot
	for i, hash := range shardKeys {
		if cmds[i] == nil {
			continue
		}
		names, err := cmds[i].Result()
		if err != nil && err != redis.Nil {
			return nil, err
		}
		out = append(out, ShardKeyAffinitySnapshot{
			ShardKey:       hash,
			BackendNames:   names,
			StrongCacheHit: i >= strongFrom,
		})
	}
	return out, nil
}

type leafStrongCacheHitPolicy struct {
	redis *redis.Client
}

func (p *leafStrongCacheHitPolicy) Name() string {
	return StrongHitPolicyLeaf
}

func (p *leafStrongCacheHitPolicy) ResolveShardKeyAffinity(
	ctx context.Context,
	scope StrongHitScope,
	shardKeys []string,
) ([]ShardKeyAffinitySnapshot, error) {
	valid := validShardKeys(shardKeys)
	if len(valid) == 0 {
		return nil, nil
	}

	pipe := p.redis.Pipeline()
	cmds := make([]*redis.Cmd, len(valid))
	for i, hash := range valid {
		cmds[i] = pipe.Eval(ctx, leafAffinityReadLua, []string{
			redisKeyForStrategy(scope.KeyPrefix, scope.Namespace, hash),
			redisKeyForStrategy(scope.KeyPrefix, scope.Namespace, "is-leaf:"+hash),
		})
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, err
	}

	out := make([]ShardKeyAffinitySnapshot, len(valid))
	for i, hash := range valid {
		raw, err := cmds[i].Result()
		if err != nil && err != redis.Nil {
			return nil, err
		}
		names, markerExists, err := parseLeafAffinityRead(raw)
		if err != nil {
			return nil, err
		}
		out[i] = ShardKeyAffinitySnapshot{
			ShardKey:       hash,
			BackendNames:   names,
			StrongCacheHit: len(names) > 0 && markerExists,
		}
	}
	return out, nil
}

func parseLeafAffinityRead(result any) ([]string, bool, error) {
	pair, ok := result.([]interface{})
	if !ok || len(pair) != 2 {
		return nil, false, fmt.Errorf("unexpected leaf affinity lua result: %T", result)
	}

	names, err := parseLuaStringSlice(pair[0])
	if err != nil {
		return nil, false, err
	}

	exists, err := parseLuaIntBool(pair[1])
	if err != nil {
		return nil, false, err
	}
	return names, exists, nil
}

func parseLuaStringSlice(v any) ([]string, error) {
	switch members := v.(type) {
	case nil:
		return nil, nil
	case []string:
		return members, nil
	case []interface{}:
		out := make([]string, 0, len(members))
		for _, m := range members {
			s, ok := m.(string)
			if !ok {
				return nil, fmt.Errorf("unexpected mapping member type %T", m)
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unexpected mapping members type %T", v)
	}
}

func parseLuaIntBool(v any) (bool, error) {
	switch n := v.(type) {
	case int64:
		return n == 1, nil
	case int:
		return n == 1, nil
	default:
		return false, fmt.Errorf("unexpected marker exists type %T", v)
	}
}
