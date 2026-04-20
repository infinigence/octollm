package limiter

import (
	"context"
	"fmt"
	"log/slog"
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
// rates: Concurrency limit array for each priority tier (must be non-decreasing), e.g., [10, 50, 100]
//
//	means tier 0 allows 10 concurrent, tier 1 allows 50 concurrent, tier 2 allows 100 concurrent
//	Priority is calculated as: len(rates) - 1 - tierIdx
//	So tier 0 → priority 2 (highest), tier 1 → priority 1, tier 2 → priority 0 (lowest)
//
// timeout: Timeout duration for member expiration, must be greater than 0
// nameSpace: Namespace for isolating priority across different namespaces, marker and limiter within the same nameSpace can communicate
// next: Next engine
//
// Working principle:
// - Each tier has its own concurrency limit (non-decreasing)
// - Priority number: larger = higher priority (priority 2 > priority 1 > priority 0)
// - When a request comes in, it tries to acquire from tier 0 (smallest limit, highest priority) first
// - Occupancy rule:
//   - If acquired from tier 0 (highest priority): occupies tier 0 and last tier (lowest priority tier)
//   - If acquired from tier 1: occupies tier 1 and last tier
//   - If acquired from last tier (lowest priority): only occupies last tier
//
// - If tier 0 fails, it tries tier 1, and so on
// - If all tiers fail, the request is rejected
//
// Example: rates=[10, 50, 100]
// - Tier 0: 10 concurrent → Priority 2 (highest)
// - Tier 1: 50 concurrent → Priority 1 (medium)
// - Tier 2: 100 concurrent → Priority 0 (lowest)
// - Request tries tier 0 first: if available, occupies tier 0 and tier 2 → Priority 2
// - If tier 0 full, tries tier 1: if available, occupies tier 1 and tier 2 → Priority 1
// - If tier 1 full, tries tier 2: if available, occupies tier 2 only → Priority 0
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

	filteredRates, filtered := filterIncreasingRates(rates)
	if filtered {
		slog.Warn(fmt.Sprintf("rates must be non-decreasing, filtered from %v to %v (removed %d invalid suffix values)", rates, filteredRates, len(rates)-len(filteredRates)))
	}

	return &ConcurrencyColorMarkerEngine{
		redisClient:   redisClient,
		key:           key,
		rates:         filteredRates,
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
	args := make([]interface{}, 0, 1+numTiers+3)
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

	results, ok := result.([]interface{})
	if !ok || len(results) < 2 {
		slog.ErrorContext(ctx, fmt.Sprintf("unexpected script result format, key: %s", e.key))
		return ctx, func() {}, fmt.Errorf("unexpected script result format")
	}

	acquired, _ := results[0].(int64)
	if acquired == 0 {
		// All tiers exhausted
		slog.WarnContext(ctx, fmt.Sprintf("all tiers exhausted, key: %s", e.key))
		return ctx, func() {}, ErrRateLimitReached
	}

	// acquired > 0: priority (1-based in Lua, convert to 0-based)
	priority := int(acquired) - 1
	acquiredTierIdx := numTiers - 1 - priority

	// Parse acquired keys indices from results[1]
	acquiredKeysStr, _ := results[1].(string)
	var acquiredKeys []string
	if acquiredKeysStr != "" {
		for _, idxStr := range []byte(acquiredKeysStr) {
			idx := int(idxStr - '0')
			if idx >= 0 && idx < numTiers {
				acquiredKeys = append(acquiredKeys, keys[idx])
			}
		}
	}

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

	slog.InfoContext(ctx, fmt.Sprintf("acquired priority %d from tier %d, acquiredKeys: %v", priority, acquiredTierIdx, acquiredKeys))
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
// Returns: {priority, acquiredKeysIndices}
// - priority: 0 means rejected, >0 means acquired with priority (1-based, convert to 0-based in Go)
// - acquiredKeysIndices: string of key indices that were acquired (e.g., "02" means keys[0] and keys[2])
const concurrencyColorMarkerAquireLuaScript = `
local numTiers = tonumber(ARGV[1])
local limits = {}
for i = 1, numTiers do
    limits[i] = tonumber(ARGV[1 + i])
end
local nowUnix = tonumber(ARGV[numTiers + 2])
local expireBefore = tonumber(ARGV[numTiers + 3])
local memberID = ARGV[numTiers + 4]

local lastTierIdx = numTiers

-- Clean up expired members from all tiers
for i = 1, numTiers do
    redis.call('ZREMRANGEBYSCORE', KEYS[i], '0', expireBefore)
end

-- Phase 1: Find first available tier from tier 0 (highest priority) to last tier
local foundTierIdx = nil
for tierIdx = 1, numTiers do
    local count = redis.call('ZCARD', KEYS[tierIdx])
    if count < limits[tierIdx] then
        foundTierIdx = tierIdx
        break
    end
end

-- If no tier is available, reject
if foundTierIdx == nil then
    return {0, ""}
end

-- Phase 2: Determine which keys to acquire
local keysToAcquire = {}
local priority = numTiers - foundTierIdx + 1

if foundTierIdx == lastTierIdx then
    -- Found tier is the last tier: only acquire last tier
    keysToAcquire[1] = lastTierIdx
else
    -- Found tier is not the last tier: check if last tier is also available
    local lastTierCount = redis.call('ZCARD', KEYS[lastTierIdx])
    if lastTierCount < limits[lastTierIdx] then
        -- Both available: acquire both found tier and last tier
        keysToAcquire[1] = foundTierIdx
        keysToAcquire[2] = lastTierIdx
    else
        -- Last tier not available: reject
        return {0, ""}
    end
end

-- Phase 3: Acquire all required keys
for i = 1, #keysToAcquire do
    local keyIdx = keysToAcquire[i]
    redis.call('ZADD', KEYS[keyIdx], nowUnix, memberID)
    redis.call('EXPIRE', KEYS[keyIdx], 3600)
end

-- Build acquired keys indices string
local acquiredKeysStr = ""
for i = 1, #keysToAcquire do
    acquiredKeysStr = acquiredKeysStr .. tostring(keysToAcquire[i] - 1)
end

return {priority, acquiredKeysStr}
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
			results, ok := result.([]interface{})
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
