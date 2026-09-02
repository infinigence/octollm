package loadbalancer

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// StrongHitScope identifies the Redis key space for one strategy's mapping and leaf markers.
type StrongHitScope struct {
	KeyPrefix string
	Namespace string
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

func enqueueSuccessfulPathLearning(
	pipe redis.Pipeliner,
	ctx context.Context,
	scope StrongHitScope,
	shardKeys []string,
	selectedBackend string,
	ttl time.Duration,
) {
	if selectedBackend == "" || ttl <= 0 {
		return
	}
	valid := make([]string, 0, len(shardKeys))
	for _, k := range shardKeys {
		if k != "" {
			valid = append(valid, k)
		}
	}
	if len(valid) == 0 {
		return
	}

	keys := make([]string, 0, len(valid)*2)
	for _, hash := range valid {
		keys = append(keys,
			redisKeyForStrategy(scope.KeyPrefix, scope.Namespace, hash),
			redisKeyForStrategy(scope.KeyPrefix, scope.Namespace, "is-leaf:"+hash),
		)
	}
	pipe.Eval(ctx, successfulPathLearningLua, keys, selectedBackend, ttl.Milliseconds())
}
