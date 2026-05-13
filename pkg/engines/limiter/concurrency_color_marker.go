package limiter

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/octollm"
)

type ConcurrencyColorMarkerEngine struct {
	redisClient   *redis.Client
	key           string
	rates         []int
	timeout       time.Duration
	nameSpace     string
	acquireScript *redis.Script
	releaseScript *redis.Script
	renewScript   *redis.Script
	next          octollm.Engine
}

var _ octollm.Engine = (*ConcurrencyColorMarkerEngine)(nil)

// NewConcurrencyColorMarkerEngine creates a multi-tier concurrency-based marker with priority assignment
// redisClient: Redis client
// key: Redis key prefix for storing concurrency counts (each tier uses key:tier_N)
// rates: Per-priority concurrency slots, highest priority first. Each element is its own cap; the sum is total concurrency.
// Negative values are treated as 0. Only an empty rates slice disables the marker (pass-through).
// A zero entry at any index means that priority band has no slots (skipped); e.g. rates=[0] rejects all traffic.
//
// Parameters:
//   - redisClient: Redis client.
//   - key: Redis key prefix for storing concurrency counts (each tier uses key:tier_N).
//   - rates: Per-tier concurrency caps, highest priority first. Each element caps that tier independently;
//     total concurrency is the sum. Negative values are clamped to 0. Leading zeros are trimmed (which
//     shrinks the priority range, since priority numbers are derived from the post-trim length). Zeros
//     after the first positive entry keep the tier in place but with no slots (always skipped).
//     Priority assigned to a request: len(rates) - 1 - tierIdx, so tier 0 is the highest priority.
//   - timeout: Member expiration duration; must be positive.
//   - nameSpace: Namespace for isolating priority between marker/limiter pairs. Marker and limiter sharing
//     a namespace can communicate via context.
//   - next: Next engine in the chain.
//
// Working principle:
//   - Larger priority number = higher priority (priority 2 > priority 1 > priority 0).
//   - A request scans tier 0, tier 1, ...; the first tier with limit>0 and available capacity wins.
//   - Acquisition occupies exactly one slot in the winning tier; lower tiers are not consulted further.
//
// Example: rates=[3, 2, 1]
//   - Slots 1-3 -> priority 2; 4-5 -> priority 1; 6th -> priority 0; 7th rejected.
//
// Example: rates=[3, 0, 0]
//   - Three slots, all priority 2; lower bands are empty (limit 0).
func NewConcurrencyColorMarkerEngine(redisClient *redis.Client, key string, rates []int, timeout time.Duration, nameSpace string, next octollm.Engine) (*ConcurrencyColorMarkerEngine, error) {
	// If rates is empty, return an engine that directly passes through
	if len(rates) == 0 {
		return &ConcurrencyColorMarkerEngine{
			redisClient:   redisClient,
			key:           key,
			rates:         nil,
			timeout:       timeout,
			nameSpace:     nameSpace,
			acquireScript: nil,
			releaseScript: nil,
			renewScript:   nil,
			next:          next,
		}, nil
	}

	if timeout <= 0 {
		return nil, fmt.Errorf("timeout must be positive")
	}

	normalizedRates, normalized := normalizeConcurrencyMarkerRates(rates)
	if normalized {
		slog.Warn(fmt.Sprintf("concurrency marker rates normalized from %v to %v", rates, normalizedRates))
	}
	if len(normalizedRates) == 0 {
		return &ConcurrencyColorMarkerEngine{
			redisClient:   redisClient,
			key:           key,
			rates:         nil,
			timeout:       timeout,
			nameSpace:     nameSpace,
			acquireScript: nil,
			releaseScript: nil,
			renewScript:   nil,
			next:          next,
		}, nil
	}

	return &ConcurrencyColorMarkerEngine{
		redisClient:   redisClient,
		key:           key,
		rates:         normalizedRates,
		timeout:       timeout,
		nameSpace:     nameSpace,
		acquireScript: redis.NewScript(concurrencyColorMarkerAquireLuaScript),
		releaseScript: redis.NewScript(concurrencyColorMarkerReleaseLuaScript),
		renewScript:   redis.NewScript(concurrencyColorMarkerRenewLuaScript),
		next:          next,
	}, nil
}

// allow attempts to allow the request to pass through, performing coloring and counting
func (e *ConcurrencyColorMarkerEngine) allow(ctx context.Context) (newCtx context.Context, done func(), err error) {
	// If rates is empty, directly pass through
	if len(e.rates) == 0 || e.acquireScript == nil {
		return ctx, func() {}, nil
	}

	nowUnix := time.Now().Unix()
	expireBefore := nowUnix - int64(e.timeout.Seconds())
	memberID := uuid.New().String()

	// Build all tier keys
	numTiers := len(e.rates)
	keys := make([]string, numTiers)
	for i := 0; i < numTiers; i++ {
		keys[i] = fmt.Sprintf("%s:tier_%d", e.key, i)
	}

	// Build ARGV: numTiers, limit1, limit2, ..., nowUnix, expireBefore, memberID
	args := make([]any, 0, 1+numTiers+3)
	args = append(args, numTiers)
	for _, limit := range e.rates {
		args = append(args, limit)
	}
	args = append(args, nowUnix, expireBefore, memberID)

	// Call Lua script
	result, err := e.acquireScript.Run(ctx, e.redisClient, keys, args...).Result()
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("acquire script error: %v, key: %s", err, e.key))
		return ctx, func() {}, fmt.Errorf("acquire script error: %w", err)
	}

	results, ok := result.([]any)
	if !ok || len(results) < 2 {
		slog.ErrorContext(ctx, fmt.Sprintf("unexpected script result format, key: %s", e.key))
		return ctx, func() {}, fmt.Errorf("unexpected script result format")
	}

	acquired, _ := results[0].(int64)
	countsRaw, _ := results[1].([]any)
	counts := make([]int64, len(countsRaw))
	for i, v := range countsRaw {
		c, _ := v.(int64)
		counts[i] = c
	}

	if acquired == 0 {
		// All tiers exhausted
		slog.WarnContext(ctx, fmt.Sprintf("all tiers exhausted, key: %s, usage: %s", e.key, formatTierUsage(counts, e.rates)))
		return ctx, func() {}, ErrRateLimitReached
	}

	// acquired > 0: priority (1-based in Lua, convert to 0-based)
	priority := int(acquired) - 1
	acquiredTierIdx := numTiers - 1 - priority
	acquiredKeys := []string{keys[acquiredTierIdx]}

	// Set priority to context
	newCtx = e.setPriorityToContext(ctx, priority)

	// Start renewal goroutine
	renewCtx, renewCancel := context.WithCancel(context.WithoutCancel(ctx))
	renewDone := make(chan struct{})
	go e.renewMember(renewCtx, acquiredKeys, memberID, renewDone)

	done = func() {
		renewCancel()
		<-renewDone
		c1 := context.WithoutCancel(ctx)
		_, err := e.releaseScript.Run(c1, e.redisClient, acquiredKeys, memberID).Result()
		if err != nil {
			slog.ErrorContext(ctx, fmt.Sprintf("failed to release member: %v, keys: %v", err, acquiredKeys))
		}
	}

	slog.InfoContext(ctx, fmt.Sprintf("acquired priority %d from tier %d, acquiredKeys: %v, usage: %s", priority, acquiredTierIdx, acquiredKeys, formatTierUsage(counts, e.rates)))
	return newCtx, done, nil
}

func (e *ConcurrencyColorMarkerEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	ctx := req.Context()

	// Use allow method to perform coloring
	newCtx, done, err := e.allow(ctx)
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("concurrency marker error: %v, key: %s", err, e.key))
		return nil, err
	}

	// Use WithContext method to set new context
	req = req.WithContext(newCtx)

	// Process request
	resp, err := e.next.Process(req)

	// Call done to cleanup regardless of success or failure
	if err != nil || resp == nil {
		done()
		return resp, err
	}
	if resp.Stream != nil {
		resp.Stream.OnClose(done)
	} else if resp.Body != nil {
		resp.Body.OnClose(done)
	} else {
		done()
	}

	return resp, err
}

func (e *ConcurrencyColorMarkerEngine) setPriorityToContext(ctx context.Context, priority int) context.Context {
	priorityStr := fmt.Sprintf("%s%d", ContextValuePrefixPriority, priority)
	contextKey := contextKey(fmt.Sprintf("%s:%s", e.nameSpace, ContextKeyPriority))
	return context.WithValue(ctx, contextKey, priorityStr)
}

// formatTierUsage formats per-tier "count/limit" usage as "(2/2 1/3 0/5)".
// Assumes counts and limits have the same length; truncates to the shorter if not.
func formatTierUsage(counts []int64, limits []int) string {
	n := min(len(counts), len(limits))
	parts := make([]string, n)
	for i := range n {
		parts[i] = fmt.Sprintf("%d/%d", counts[i], limits[i])
	}
	return "(" + strings.Join(parts, " ") + ")"
}

// normalizeConcurrencyMarkerRates clamps negatives to 0, trims leading zeros, and returns nil if no positive band remains.
// normalizeConcurrencyMarkerRates clamps negatives to 0 and returns a copy of the same length as rates.
func normalizeConcurrencyMarkerRates(rates []int) ([]int, bool) {
	if len(rates) == 0 {
		return nil, false
	}
	altered := false
	out := make([]int, len(rates))
	for i, v := range rates {
		if v < 0 {
			altered = true
			v = 0
		}
		out[i] = v
	}
	return out, altered
}

func filterIncreasingRates(rates []int) ([]int, bool) {
	if len(rates) == 0 {
		return rates, false
	}

	filteredRates := make([]int, 0, len(rates))
	filteredRates = append(filteredRates, rates[0])

	for i := 1; i < len(rates); i++ {
		if rates[i] >= rates[i-1] {
			filteredRates = append(filteredRates, rates[i])
		} else {
			break
		}
	}

	filtered := len(filteredRates) < len(rates)
	return filteredRates, filtered
}

// Unified acquire script supporting N tiers
// KEYS: all tier keys (tier_0, tier_1, ..., tier_N-1)
// ARGV[1]: numTiers
// ARGV[2..numTiers+1]: limits for each tier
// ARGV[numTiers+2]: nowUnix
// ARGV[numTiers+3]: expireBefore
// ARGV[numTiers+4]: memberID
//
// Returns: {priority, counts}
// - priority: 0 means rejected, >0 means acquired with priority (1-based, convert to 0-based in Go)
// - counts: per-tier ZCARD after acquisition (length = numTiers)
const concurrencyColorMarkerAquireLuaScript = `
local numTiers = tonumber(ARGV[1])
local limits = {}
for i = 1, numTiers do
    limits[i] = tonumber(ARGV[1 + i])
end
local nowUnix = tonumber(ARGV[numTiers + 2])
local expireBefore = tonumber(ARGV[numTiers + 3])
local memberID = ARGV[numTiers + 4]

-- Clean up expired members and snapshot counts for all tiers in one pass
local counts = {}
for i = 1, numTiers do
    redis.call('ZREMRANGEBYSCORE', KEYS[i], '0', expireBefore)
    counts[i] = redis.call('ZCARD', KEYS[i])
end

-- Find first tier (highest priority) with limit>0 and free capacity
local foundTierIdx = nil
for tierIdx = 1, numTiers do
    if limits[tierIdx] > 0 and counts[tierIdx] < limits[tierIdx] then
        foundTierIdx = tierIdx
        break
    end
end

if foundTierIdx == nil then
    return {0, counts}
end

redis.call('ZADD', KEYS[foundTierIdx], nowUnix, memberID)
redis.call('EXPIRE', KEYS[foundTierIdx], 3600)
counts[foundTierIdx] = counts[foundTierIdx] + 1

local priority = numTiers - foundTierIdx + 1
return {priority, counts}
`

// Unified release script supporting N keys
// KEYS: variable number of tier keys
// ARGV[1]: memberID
const concurrencyColorMarkerReleaseLuaScript = `
local memberID = ARGV[1]

for i = 1, #KEYS do
    redis.call('ZREM', KEYS[i], memberID)
end

return {1}
`

// Unified renew script supporting N keys
// KEYS: variable number of tier keys
// ARGV[1]: nowUnix
// ARGV[2]: memberID
const concurrencyColorMarkerRenewLuaScript = `
local nowUnix = tonumber(ARGV[1])
local memberID = ARGV[2]

-- Check if member exists in all keys
for i = 1, #KEYS do
    if redis.call('ZSCORE', KEYS[i], memberID) == false then
        return {0}
    end
end

-- Renew member in all keys
for i = 1, #KEYS do
    redis.call('ZADD', KEYS[i], nowUnix, memberID)
    redis.call('EXPIRE', KEYS[i], 3600)
end

return {1}
`

// renewMember periodically renews the member's score for N keys
func (e *ConcurrencyColorMarkerEngine) renewMember(ctx context.Context, keys []string, memberID string, done chan struct{}) {
	defer close(done)
	defer func() {
		if err := recover(); err != nil {
			slog.ErrorContext(ctx, "panic in renewal goroutine", "err", err, "stack", string(debug.Stack()))
		}
	}()
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nowUnix := time.Now().Unix()
			result, err := e.renewScript.Run(ctx, e.redisClient, keys, nowUnix, memberID).Result()
			if err != nil {
				slog.ErrorContext(ctx, fmt.Sprintf("failed to renew member: %v, keys: %v, memberID: %s", err, keys, memberID))
				continue
			}
			results, ok := result.([]any)
			if !ok || len(results) != 1 {
				slog.ErrorContext(ctx, fmt.Sprintf("unexpected renew script result format, keys: %v, memberID: %s", keys, memberID))
				continue
			}
			renewed, _ := results[0].(int64)
			if renewed == 0 {
				slog.WarnContext(ctx, fmt.Sprintf("member not found for renewal, keys: %v, memberID: %s", keys, memberID))
				return
			}
			slog.DebugContext(ctx, fmt.Sprintf("renewed member: keys=%v, memberID=%s", keys, memberID))
		}
	}
}
