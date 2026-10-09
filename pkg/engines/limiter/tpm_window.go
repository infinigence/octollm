package limiter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/engines/usage"
	"github.com/infinigence/octollm/pkg/octollm"
)

// ErrTPMLimitReached is returned by the TPM marker and limiter. It wraps
// ErrRateLimitReached, so load balancers fail over on it like on any other
// limiter reject, while callers can still tell a TPM reject apart.
var ErrTPMLimitReached = fmt.Errorf("tpm limit reached: %w", ErrRateLimitReached)

// tpmKeyTTL expires a TPM window key after two idle windows; by then every
// bucket it holds is stale anyway.
const tpmKeyTTL = 2 * time.Minute

// The TPM window is a Redis hash per key: "last_sec" is the latest Unix second
// that touched it, and "bucket:00" .. "bucket:55" are 12 five-second slices of
// the last 60 seconds, named by the slice's start second within the minute.
// Every read or write first clears the slices that rolled out of the window
// since last_sec, so the 12 buckets only hold tokens from the last 60 seconds
// and the window TPM is their sum.
//
// "now" is the Redis server's clock, not the gateway's: every gateway pod then
// rolls a key by the same clock, so a pod whose clock runs ahead cannot clear
// slices early for all the others. Should the server clock step back (e.g. a
// failover to a replica that lags), last_sec is used instead, so a slice that
// has already been reused is never written into.
const tpmWindowLuaPrelude = `
-- TIME is non-deterministic; before Redis 5 a script may only write after it
-- with effects replication. A no-op on Redis 7.
redis.replicate_commands()

local BUCKET_SECONDS = 5
local BUCKET_COUNT = 12
local now = tonumber(redis.call('TIME')[1])

local function bucketField(slot)
    return string.format('bucket:%02d', (slot % BUCKET_COUNT) * BUCKET_SECONDS)
end

local bucketFields = {}
for slot = 0, BUCKET_COUNT - 1 do
    bucketFields[slot + 1] = bucketField(slot)
end

-- roll clears the slices that entered the window since last_sec and returns
-- the effective now, max(now, last_sec). A key without last_sec holds no
-- usable state and is dropped.
local function roll(key, now)
    local last = tonumber(redis.call('HGET', key, 'last_sec'))
    if last == nil then
        redis.call('DEL', key)
        return now
    end
    if now <= last then
        return last
    end
    local lastSlot = math.floor(last / BUCKET_SECONDS)
    local nowSlot = math.floor(now / BUCKET_SECONDS)
    if nowSlot > lastSlot then
        local stale = math.min(nowSlot - lastSlot, BUCKET_COUNT)
        for s = 1, stale do
            redis.call('HDEL', key, bucketField(lastSlot + s))
        end
    end
    redis.call('HSET', key, 'last_sec', now)
    return now
end
`

// tpmReadLuaScript returns the window TPM of every key.
// KEYS: TPM window keys
// Returns: {tpm_1, ..., tpm_n}
const tpmReadLuaScript = tpmWindowLuaPrelude + `
local sums = {}
for i = 1, #KEYS do
    roll(KEYS[i], now)
    local vals = redis.call('HMGET', KEYS[i], unpack(bucketFields))
    local sum = 0
    for _, v in ipairs(vals) do
        sum = sum + (tonumber(v) or 0)
    end
    sums[i] = sum
end
return sums
`

// tpmRecordLuaScript adds tokens to the current slice of every key.
// KEYS: TPM window keys
// ARGV[1]: tokens
// ARGV[2]: ttl in seconds
const tpmRecordLuaScript = tpmWindowLuaPrelude + `
local tokens = tonumber(ARGV[1])
local ttl = tonumber(ARGV[2])
for i = 1, #KEYS do
    local effective = roll(KEYS[i], now)
    redis.call('HINCRBY', KEYS[i], bucketField(math.floor(effective / BUCKET_SECONDS)), tokens)
    redis.call('HSET', KEYS[i], 'last_sec', effective)
    redis.call('EXPIRE', KEYS[i], ttl)
end
return 1
`

var (
	tpmReadScript   = redis.NewScript(tpmReadLuaScript)
	tpmRecordScript = redis.NewScript(tpmRecordLuaScript)
)

// readTPM returns the TPM of the last 60 seconds for each key, in key order.
func readTPM(ctx context.Context, client *redis.Client, keys []string) ([]int64, error) {
	result, err := tpmReadScript.Run(ctx, client, keys).Result()
	if err != nil {
		return nil, fmt.Errorf("tpm read script error: %w", err)
	}
	raw, ok := result.([]any)
	if !ok || len(raw) != len(keys) {
		return nil, errors.New("unexpected tpm read script result format")
	}
	sums := make([]int64, len(raw))
	for i, v := range raw {
		sums[i], _ = v.(int64)
	}
	return sums, nil
}

// recordTPM adds tokens to the current 5-second slice of each key.
func recordTPM(ctx context.Context, client *redis.Client, keys []string, tokens int64) error {
	_, err := tpmRecordScript.Run(ctx, client, keys, tokens, int64(tpmKeyTTL.Seconds())).Result()
	if err != nil {
		return fmt.Errorf("tpm record script error: %w", err)
	}
	return nil
}

// recordTPMUsage adds the total tokens a usage.CollectorEngine recorded on req
// to every key. It runs on a context detached from req's, so the usage is still
// persisted when the client has disconnected.
func recordTPMUsage(req *octollm.Request, client *redis.Client, keys []string, logPrefix string) {
	ctx := context.WithoutCancel(req.Context())
	u := usage.Get(req)
	if u == nil {
		slog.WarnContext(ctx, fmt.Sprintf("%s no usage recorded, skipping tpm record, keys: %v", logPrefix, keys))
		return
	}
	tokens := u.GetTotalTokens()
	if tokens <= 0 {
		return
	}
	if err := recordTPM(ctx, client, keys, tokens); err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("%s failed to record tpm: %v, keys: %v, tokens: %d", logPrefix, err, keys, tokens))
	}
}

// onResponseClose runs fn once resp is fully consumed, or right away when resp
// has nothing to consume.
func onResponseClose(resp *octollm.Response, fn func()) {
	switch {
	case resp.Stream != nil:
		resp.Stream.OnClose(fn)
	case resp.Body != nil:
		resp.Body.OnClose(fn)
	default:
		fn()
	}
}
