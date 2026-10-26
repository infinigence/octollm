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
// A request is counted in the slice it was admitted in: admission reserves an
// estimate of its tokens there, atomically with the cap check, so concurrent
// requests see one another before any has finished. Once the actual usage is
// known, the difference is settled into that same slice, so the reservation and
// its correction roll out together. Should the slice have rolled out already (a
// request over a minute long), only a positive difference is added, to the
// current slice: a late refund never drives the window below what it holds.
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

-- windowSum returns the TPM of a rolled key.
local function windowSum(key)
    local vals = redis.call('HMGET', key, unpack(bucketFields))
    local sum = 0
    for _, v in ipairs(vals) do
        sum = sum + (tonumber(v) or 0)
    end
    return sum
end

-- add adds tokens to the slice of slot and keeps the key alive.
local function add(key, slot, tokens, effective, ttl)
    redis.call('HINCRBY', key, bucketField(slot), tokens)
    redis.call('HSET', key, 'last_sec', effective)
    redis.call('EXPIRE', key, ttl)
end
`

// tpmReadLuaScript returns the window TPM of every key.
// KEYS: TPM window keys
// Returns: {tpm_1, ..., tpm_n}
const tpmReadLuaScript = tpmWindowLuaPrelude + `
local sums = {}
for i = 1, #KEYS do
    roll(KEYS[i], now)
    sums[i] = windowSum(KEYS[i])
end
return sums
`

// tpmReserveLuaScript admits a request when the window TPM of every key is
// below its cap, and then adds the reserved tokens to the current slice of
// every key; all or nothing.
// KEYS: TPM window keys
// ARGV[1]: tokens to reserve; nothing is written when not positive
// ARGV[2]: ttl in seconds
// ARGV[3..]: cap of each key, in key order
// Returns: {admitted (1 or 0), tpm_1, ..., tpm_n, slot_1, ..., slot_n}, where
// tpm is the window TPM before the reservation and slot is the absolute index
// of the current slice.
const tpmReserveLuaScript = tpmWindowLuaPrelude + `
local tokens = tonumber(ARGV[1])
local ttl = tonumber(ARGV[2])
local n = #KEYS
local admitted = 1
local effectives, sums, slots = {}, {}, {}
for i = 1, n do
    effectives[i] = roll(KEYS[i], now)
    slots[i] = math.floor(effectives[i] / BUCKET_SECONDS)
    sums[i] = windowSum(KEYS[i])
    if sums[i] >= tonumber(ARGV[2 + i]) then
        admitted = 0
    end
end
if admitted == 1 and tokens > 0 then
    for i = 1, n do
        add(KEYS[i], slots[i], tokens, effectives[i], ttl)
    end
end
local result = {admitted}
for i = 1, n do
    result[1 + i] = sums[i]
    result[1 + n + i] = slots[i]
end
return result
`

// tpmSettleLuaScript adds delta tokens to every key, into the slice a request
// was admitted in while it is still in the window, and otherwise into the
// current slice when delta is positive. A negative delta never takes a slice
// below zero.
// KEYS: TPM window keys
// ARGV[1]: delta tokens
// ARGV[2]: ttl in seconds
// ARGV[3..]: admission slot of each key, in key order; none for keys without one
const tpmSettleLuaScript = tpmWindowLuaPrelude + `
local delta = tonumber(ARGV[1])
local ttl = tonumber(ARGV[2])
for i = 1, #KEYS do
    local key = KEYS[i]
    local effective = roll(key, now)
    local nowSlot = math.floor(effective / BUCKET_SECONDS)
    local slot = tonumber(ARGV[2 + i])
    local target = nil
    local tokens = delta
    if slot ~= nil and slot <= nowSlot and nowSlot - slot < BUCKET_COUNT then
        target = slot
        if tokens < 0 then
            local held = tonumber(redis.call('HGET', key, bucketField(slot))) or 0
            tokens = math.max(tokens, -held)
        end
    elseif tokens > 0 then
        target = nowSlot
    end
    if target ~= nil and tokens ~= 0 then
        add(key, target, tokens, effective, ttl)
    end
end
return 1
`

var (
	tpmReadScript    = redis.NewScript(tpmReadLuaScript)
	tpmReserveScript = redis.NewScript(tpmReserveLuaScript)
	tpmSettleScript  = redis.NewScript(tpmSettleLuaScript)
)

// readTPM returns the TPM of the last 60 seconds for each key, in key order.
func readTPM(ctx context.Context, client *redis.Client, keys []string) ([]int64, error) {
	result, err := tpmReadScript.Run(ctx, client, keys).Result()
	if err != nil {
		return nil, fmt.Errorf("tpm read script error: %w", err)
	}
	sums, err := int64sFromScript(result, len(keys))
	if err != nil {
		return nil, fmt.Errorf("unexpected tpm read script result format: %w", err)
	}
	return sums, nil
}

// recordTPM adds tokens to the current 5-second slice of each key.
func recordTPM(ctx context.Context, client *redis.Client, keys []string, tokens int64) error {
	return settleTPM(ctx, client, keys, nil, tokens)
}

// settleTPM adds delta tokens to each key, into slots[i] while that slice is
// still in the window, and otherwise into the current slice when delta is
// positive. slots may be nil.
func settleTPM(ctx context.Context, client *redis.Client, keys []string, slots []int64, delta int64) error {
	args := make([]any, 0, 2+len(slots))
	args = append(args, delta, int64(tpmKeyTTL.Seconds()))
	for _, slot := range slots {
		args = append(args, slot)
	}
	if _, err := tpmSettleScript.Run(ctx, client, keys, args...).Result(); err != nil {
		return fmt.Errorf("tpm settle script error: %w", err)
	}
	return nil
}

// TokenEstimateFunc estimates the tokens a request will use before it is sent
// upstream, e.g. from its input text. A TPM marker or limiter reserves the
// estimate on admission; an error or a non-positive estimate reserves nothing.
type TokenEstimateFunc func(req *octollm.Request) (int64, error)

// estimateTokens returns fn's estimate for req, or 0 when there is none.
func estimateTokens(req *octollm.Request, fn TokenEstimateFunc, logPrefix string) int64 {
	if fn == nil {
		return 0
	}
	tokens, err := fn(req)
	if err != nil {
		slog.DebugContext(req.Context(), fmt.Sprintf("%s no token estimate, reserving nothing: %v", logPrefix, err))
		return 0
	}
	return max(tokens, 0)
}

// tpmReservation is the estimate a request reserved in TPM windows on
// admission, to be settled to its actual usage once that is known.
type tpmReservation struct {
	redisClient *redis.Client
	keys        []string
	slots       []int64
	tokens      int64
	logPrefix   string
}

// reserveTPM admits a request when the TPM of every key is below its cap,
// caps[i] for keys[i], and then reserves tokens in every key. It returns the
// TPM of each key before the reservation, and a nil reservation when the
// request is not admitted.
func reserveTPM(ctx context.Context, client *redis.Client, keys []string, caps []int64, tokens int64, logPrefix string) (*tpmReservation, []int64, error) {
	args := make([]any, 0, 2+len(caps))
	args = append(args, tokens, int64(tpmKeyTTL.Seconds()))
	for _, c := range caps {
		args = append(args, c)
	}
	result, err := tpmReserveScript.Run(ctx, client, keys, args...).Result()
	if err != nil {
		return nil, nil, fmt.Errorf("tpm reserve script error: %w", err)
	}
	values, err := int64sFromScript(result, 1+2*len(keys))
	if err != nil {
		return nil, nil, fmt.Errorf("unexpected tpm reserve script result format: %w", err)
	}
	tpm := values[1 : 1+len(keys)]
	if values[0] != 1 {
		return nil, tpm, nil
	}
	return &tpmReservation{
		redisClient: client,
		keys:        keys,
		slots:       values[1+len(keys):],
		tokens:      tokens,
		logPrefix:   logPrefix,
	}, tpm, nil
}

// settle replaces the reserved estimate with actual tokens. It runs on a
// context detached from req's, so it still takes effect when the client has
// disconnected.
func (r *tpmReservation) settle(req *octollm.Request, actual int64) {
	delta := max(actual, 0) - r.tokens
	if delta == 0 {
		return
	}
	ctx := context.WithoutCancel(req.Context())
	if err := settleTPM(ctx, r.redisClient, r.keys, r.slots, delta); err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("%s failed to settle tpm: %v, keys: %v, reserved: %d, actual: %d", r.logPrefix, err, r.keys, r.tokens, actual))
	}
}

// settleUsage settles to the total tokens a usage.CollectorEngine recorded on
// req, keeping the estimate when it recorded none.
func (r *tpmReservation) settleUsage(req *octollm.Request) {
	u := usage.Get(req)
	if u == nil {
		slog.WarnContext(req.Context(), fmt.Sprintf("%s no usage recorded, keeping the reserved %d tokens, keys: %v", r.logPrefix, r.tokens, r.keys))
		return
	}
	r.settle(req, u.GetTotalTokens())
}

// processReserved runs next and settles res: refunded in full when next fails,
// and otherwise to the recorded usage once the response is closed.
func processReserved(req *octollm.Request, next octollm.Engine, res *tpmReservation) (*octollm.Response, error) {
	resp, err := next.Process(req)
	if err != nil || resp == nil {
		res.settle(req, 0)
		return resp, err
	}
	onResponseClose(resp, func() {
		res.settleUsage(req)
	})
	return resp, nil
}

// int64sFromScript converts a script's array reply of n integers.
func int64sFromScript(result any, n int) ([]int64, error) {
	raw, ok := result.([]any)
	if !ok || len(raw) != n {
		return nil, fmt.Errorf("want %d integers, got %v", n, result)
	}
	values := make([]int64, n)
	for i, v := range raw {
		values[i], ok = v.(int64)
		if !ok {
			return nil, errors.New("non-integer element")
		}
	}
	return values, nil
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
