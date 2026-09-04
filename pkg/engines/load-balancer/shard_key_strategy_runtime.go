package loadbalancer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/octollm"
)

const (
	StrongHitPolicyLastTwo = "last_two"
	StrongHitPolicyLeaf    = "leaf"
)

// shardKeyStrategyRuntime is one strategy's key space: extract keys, resolve mappings, and
// enqueue successful path learning. Primary vs shadow is a provider composition role, not a runtime flag.
type shardKeyStrategyRuntime struct {
	getShardKeys    func(req *octollm.Request) []string
	keyPrefix       string
	namespace       string
	strongHitPolicy string
	redis           *redis.Client
}

// affinityKeyspace identifies the Redis key space for one strategy's mappings and leaf markers.
type affinityKeyspace struct {
	KeyPrefix string
	Namespace string
}

func (s *shardKeyStrategyRuntime) keyspace() affinityKeyspace {
	return affinityKeyspace{KeyPrefix: s.keyPrefix, Namespace: s.namespace}
}

func (s affinityKeyspace) mappingKey(shardKey string) string {
	return redisKeyForStrategy(s.KeyPrefix, s.Namespace, shardKey)
}

func (s affinityKeyspace) markerKey(shardKey string) string {
	return redisKeyForStrategy(s.KeyPrefix, s.Namespace, "is-leaf:"+shardKey)
}

func nonEmptyShardKeys(shardKeys []string) []string {
	valid := make([]string, 0, len(shardKeys))
	for _, k := range shardKeys {
		if k != "" {
			valid = append(valid, k)
		}
	}
	return valid
}

// appendPrioritizedMapping appends unseen backends from names. If names has more
// than three members, it queues a ZSET trim; this request still uses the full list.
func appendPrioritizedMapping(
	ctx context.Context,
	trimPipe redis.Pipeliner,
	keyspace affinityKeyspace,
	shardKey string,
	names []string,
	strongCacheHit bool,
	seen map[string]bool,
	prioritized *[]*PrioritizedBackend,
	queuedTrim *bool,
) {
	if shardKey == "" {
		return
	}
	if len(names) > 3 {
		stop := int64(len(names) - 4)
		if stop >= 0 {
			trimPipe.ZRemRangeByRank(ctx, keyspace.mappingKey(shardKey), 0, stop)
			*queuedTrim = true
		}
	}
	for _, name := range names {
		if name == "" || seen[name] {
			continue
		}
		*prioritized = append(*prioritized, &PrioritizedBackend{
			Name:           name,
			StrongCacheHit: strongCacheHit,
		})
		seen[name] = true
	}
}

// lookupAffinityForLastTwoPolicy reads each non-empty shard key's mapping ZSET,
// then collects candidates from last to first so later keys have higher priority.
// StrongCacheHit uses original indices: empty strings still occupy a last-two
// slot but do not contribute backends.
func lookupAffinityForLastTwoPolicy(
	ctx context.Context,
	rd *redis.Client,
	keyspace affinityKeyspace,
	shardKeys []string,
) ([]*PrioritizedBackend, error) {
	if len(shardKeys) == 0 {
		return nil, nil
	}

	pipe := rd.Pipeline()
	cmds := make([]*redis.StringSliceCmd, len(shardKeys))
	queued := false
	for i, shardKey := range shardKeys {
		if shardKey == "" {
			continue
		}
		cmds[i] = pipe.ZRevRange(ctx, keyspace.mappingKey(shardKey), 0, -1)
		queued = true
	}
	if queued {
		if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
			slog.WarnContext(ctx, fmt.Sprintf("[ShardKey affinity provider] failed to exec Redis pipeline for shard keys: %v", err))
		}
	}

	trimPipe := rd.Pipeline()
	seen := make(map[string]bool)
	var prioritized []*PrioritizedBackend
	queuedTrim := false
	strongFrom := len(shardKeys) - 2
	for i := len(shardKeys) - 1; i >= 0; i-- {
		if cmds[i] == nil {
			continue
		}
		names, err := cmds[i].Result()
		if err != nil && err != redis.Nil {
			slog.DebugContext(ctx, fmt.Sprintf("[ShardKey affinity provider] Redis ZSET error for shard key %s: %v", shardKeys[i], err))
			continue
		}
		appendPrioritizedMapping(ctx, trimPipe, keyspace, shardKeys[i], names, i >= strongFrom, seen, &prioritized, &queuedTrim)
	}
	if queuedTrim {
		if _, err := trimPipe.Exec(ctx); err != nil && err != redis.Nil {
			slog.WarnContext(ctx, fmt.Sprintf("[ShardKey affinity provider] failed to trim Redis ZSET for shard keys: %v", err))
		}
	}
	return prioritized, nil
}

// lookupAffinityForLeafPolicy reads mappings and leaf markers for non-empty shard
// keys only, then collects candidates from last to first. StrongCacheHit is true
// only when the mapping has members and the leaf marker exists.
func lookupAffinityForLeafPolicy(
	ctx context.Context,
	rd *redis.Client,
	keyspace affinityKeyspace,
	shardKeys []string,
) ([]*PrioritizedBackend, error) {
	valid := nonEmptyShardKeys(shardKeys)
	if len(valid) == 0 {
		return nil, nil
	}

	pipe := rd.Pipeline()
	rangeCmds := make([]*redis.StringSliceCmd, len(valid))
	existsCmds := make([]*redis.IntCmd, len(valid))
	for i, shardKey := range valid {
		rangeCmds[i] = pipe.ZRevRange(ctx, keyspace.mappingKey(shardKey), 0, -1)
		existsCmds[i] = pipe.Exists(ctx, keyspace.markerKey(shardKey))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		slog.WarnContext(ctx, fmt.Sprintf("[ShardKey affinity provider] failed to exec Redis pipeline for shard keys: %v", err))
	}

	trimPipe := rd.Pipeline()
	seen := make(map[string]bool)
	var prioritized []*PrioritizedBackend
	queuedTrim := false
	for i := len(valid) - 1; i >= 0; i-- {
		names, err := rangeCmds[i].Result()
		if err != nil && err != redis.Nil {
			slog.DebugContext(ctx, fmt.Sprintf("[ShardKey affinity provider] Redis ZSET error for shard key %s: %v", valid[i], err))
			continue
		}
		exists, err := existsCmds[i].Result()
		if err != nil && err != redis.Nil {
			slog.DebugContext(ctx, fmt.Sprintf("[ShardKey affinity provider] Redis marker error for shard key %s: %v", valid[i], err))
			continue
		}
		appendPrioritizedMapping(
			ctx, trimPipe, keyspace, valid[i], names,
			len(names) > 0 && exists == 1,
			seen, &prioritized, &queuedTrim,
		)
	}
	if queuedTrim {
		if _, err := trimPipe.Exec(ctx); err != nil && err != redis.Nil {
			slog.WarnContext(ctx, fmt.Sprintf("[ShardKey affinity provider] failed to trim Redis ZSET for shard keys: %v", err))
		}
	}
	return prioritized, nil
}

// lookupAffinity returns routing candidates for this strategy. It reads Redis
// under strongHitPolicy, then scans from last to first so later keys have
// higher priority. Mappings with more than three members are trimmed in Redis
// after this request has already used the full member list. BackendNames
// allowlisting is applied by the caller, not here.
func (s *shardKeyStrategyRuntime) lookupAffinity(
	ctx context.Context,
	shardKeys []string,
) ([]*PrioritizedBackend, error) {
	keyspace := s.keyspace()
	switch s.strongHitPolicy {
	case StrongHitPolicyLastTwo:
		return lookupAffinityForLastTwoPolicy(ctx, s.redis, keyspace, shardKeys)
	case StrongHitPolicyLeaf:
		return lookupAffinityForLeafPolicy(ctx, s.redis, keyspace, shardKeys)
	default:
		return nil, fmt.Errorf("unsupported strong_hit_policy %q", s.strongHitPolicy)
	}
}

const successfulPathLearningLua = `
-- KEYS: mappingKey1, markerKey1, ..., mappingKeyN, markerKeyN
-- ARGV[1]: selectedBackend; ARGV[2]: ttlMs
local t = redis.call('TIME')
local score = tonumber(t[1])
local ttlSec = math.floor(tonumber(ARGV[2]) / 1000)
local n = #KEYS / 2

for i = 1, n do
    local mappingKey = KEYS[(i - 1) * 2 + 1]
    local markerKey = KEYS[(i - 1) * 2 + 2]

    redis.call('ZADD', mappingKey, score, ARGV[1])
    redis.call('EXPIRE', mappingKey, ttlSec)

    if i < n then
        redis.call('DEL', markerKey)
    else
        redis.call('SET', markerKey, '1')
        redis.call('EXPIRE', markerKey, ttlSec)
    end
end

return 1
`

// enqueueSuccessfulPathLearning queues a Lua eval that ZADDs selectedBackend
// onto each non-empty shard key's mapping and sets the leaf marker on the last
// key. The caller must Exec the pipeline.
func (s *shardKeyStrategyRuntime) enqueueSuccessfulPathLearning(
	pipe redis.Pipeliner,
	ctx context.Context,
	shardKeys []string,
	selectedBackend string,
	ttl time.Duration,
) {
	if selectedBackend == "" || ttl <= 0 {
		return
	}
	valid := nonEmptyShardKeys(shardKeys)
	if len(valid) == 0 {
		return
	}

	keyspace := s.keyspace()
	keys := make([]string, 0, len(valid)*2)
	for _, shardKey := range valid {
		keys = append(keys, keyspace.mappingKey(shardKey), keyspace.markerKey(shardKey))
	}
	pipe.Eval(ctx, successfulPathLearningLua, keys, selectedBackend, ttl.Milliseconds())
}
