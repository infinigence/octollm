package limiter

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/redis/go-redis/v9"
)

// RequestColorMarkerEngine is a multi-tier token bucket-based request rate limiter with priority marking.
// Uses multiple token buckets for different priority levels, trying from highest to lowest priority.
type RequestColorMarkerEngine struct {
	redisClient       *redis.Client
	keyPrefix         string        // Redis key prefix for storing token bucket states
	limits            []int         // Request limits for each priority tier (must be strictly increasing)
	rates             []float64     // Token refill rates for each priority tier (calculated from limits and window)
	ttls              []int64       // Redis key TTL in seconds per tier; when expired, bucket is guaranteed full
	window            time.Duration // Time window
	nameSpace         string        // Namespace for priority context isolation
	tokenBucketScript *redis.Script
	next              octollm.Engine
}

var _ octollm.Engine = (*RequestColorMarkerEngine)(nil)

// NewRequestColorMarkerEngine creates a multi-tier token bucket-based request rate limiter with priority marking
// redisClient: Redis client
// keyPrefix: Redis key prefix for storing token bucket states (each tier uses keyPrefix:tier_N)
// limits: Request limit array for each priority tier (must be strictly increasing), e.g., [10, 50, 100]
//
//	means tier 0 allows 10 requests, tier 1 allows 50 requests, tier 2 allows 100 requests within the window
//	Priority is calculated as: len(limits) - 1 - tierIdx
//	So tier 0 → priority 2 (highest), tier 1 → priority 1, tier 2 → priority 0 (lowest)
//
// window: Time window (supports seconds, minutes, etc.), must be greater than 0
// nameSpace: Namespace for isolating priority across different namespaces, marker and limiter within the same nameSpace can communicate
// next: Next engine
//
// Working principle:
// - Each tier has its own token bucket with a specific limit and refill rate
// - Priority number: larger = higher priority (priority 2 > priority 1 > priority 0)
// - When a request comes in, it tries to consume a token from tier 0 (smallest limit, highest priority) first
// - Occupancy rule:
//   - If acquired from tier 0 (highest priority): occupies tier 0 and last tier (lowest priority tier)
//   - If acquired from tier 1: occupies tier 1 and last tier
//   - If acquired from last tier (lowest priority): only occupies last tier
//
// - If tier 0 fails, it tries tier 1, and so on
// - If all tiers fail, the request is rejected
// - The rate for each tier is calculated as: rate = limit / window.Seconds()
//
// Example: limits=[10, 50, 100], window=1*time.Minute
// - Tier 0: 10 requests/min, rate=10/60≈0.167 tokens/sec → Priority 2 (highest)
// - Tier 1: 50 requests/min, rate=50/60≈0.833 tokens/sec → Priority 1 (medium)
// - Tier 2: 100 requests/min, rate=100/60≈1.667 tokens/sec → Priority 0 (lowest)
// - Request tries tier 0 first: if available, occupies tier 0 and tier 2 → Priority 2
// - If tier 0 full, tries tier 1: if available, occupies tier 1 and tier 2 → Priority 1
// - If tier 1 full, tries tier 2: if available, occupies tier 2 only → Priority 0
func NewRequestColorMarkerEngine(redisClient *redis.Client, keyPrefix string, limits []int, window time.Duration, nameSpace string, next octollm.Engine) (*RequestColorMarkerEngine, error) {
	if next == nil {
		return nil, fmt.Errorf("next engine must not be nil")
	}

	// If limits is empty, disable rate limiting (pass through)
	if len(limits) == 0 {
		return &RequestColorMarkerEngine{
			redisClient:       redisClient,
			keyPrefix:         keyPrefix,
			limits:            nil,
			rates:             nil,
			ttls:              nil,
			window:            0,
			nameSpace:         nameSpace,
			tokenBucketScript: nil,
			next:              next,
		}, nil
	}

	// Validate and filter limits to ensure strictly increasing order
	filteredLimits, filtered := filterIncreasingRates(limits)
	if filtered {
		slog.Warn(fmt.Sprintf("request_color_marker_limits must be strictly increasing, filtered from %v to %v (removed %d non-increasing values)", limits, filteredLimits, len(limits)-len(filteredLimits)))
	}

	if window <= 0 {
		return nil, fmt.Errorf("window must be positive")
	}

	// Calculate rate and TTL for each tier
	rates := make([]float64, len(filteredLimits))
	ttls := make([]int64, len(filteredLimits))
	for i, limit := range filteredLimits {
		rates[i] = float64(limit) / window.Seconds()
		ttlSec := int64(math.Ceil(float64(limit)/rates[i])) + 1
		if ttlSec < 1 {
			ttlSec = 1
		}
		ttls[i] = ttlSec
	}

	return &RequestColorMarkerEngine{
		redisClient:       redisClient,
		keyPrefix:         keyPrefix,
		limits:            filteredLimits,
		rates:             rates,
		ttls:              ttls,
		window:            window,
		nameSpace:         nameSpace,
		tokenBucketScript: redis.NewScript(tokenBucketColorMarkerAquireLuaScript),
		next:              next,
	}, nil
}

// allow attempts to consume tokens from token buckets, trying from highest to lowest priority tier
// Returns the achieved priority level and a done function
func (e *RequestColorMarkerEngine) allow(ctx context.Context) (newCtx context.Context, done func(), err error) {
	// If configuration is invalid, directly pass through
	if len(e.limits) == 0 || e.redisClient == nil || e.tokenBucketScript == nil {
		return ctx, func() {}, nil
	}

	now := time.Now()
	nowUnix := now.Unix()

	// Build all tier keys
	numTiers := len(e.limits)
	keys := make([]string, numTiers)
	for i := 0; i < numTiers; i++ {
		keys[i] = fmt.Sprintf("%s:tier_%d", e.keyPrefix, i)
	}

	// Build ARGV: numTiers, burst1, rate1, burst2, rate2, ..., nowUnix, ttl1, ttl2, ...
	args := make([]interface{}, 0, 1+numTiers*2+1+numTiers)
	args = append(args, numTiers)
	for i := 0; i < numTiers; i++ {
		args = append(args, e.limits[i], e.rates[i])
	}
	args = append(args, nowUnix)
	for i := 0; i < numTiers; i++ {
		args = append(args, e.ttls[i])
	}

	// Call Lua script
	result, err := e.tokenBucketScript.Run(ctx, e.redisClient, keys, args...).Result()
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("[RequestColorMarkerEngine] token bucket script error: %v, keyPrefix: %s", err, e.keyPrefix))
		return ctx, func() {}, fmt.Errorf("token bucket script error: %w", err)
	}

	results, ok := result.([]interface{})
	if !ok || len(results) < 2 {
		slog.ErrorContext(ctx, fmt.Sprintf("[RequestColorMarkerEngine] unexpected script result format, keyPrefix: %s", e.keyPrefix))
		return ctx, func() {}, fmt.Errorf("unexpected script result format")
	}

	acquired, _ := results[0].(int64)
	if acquired == 0 {
		// All tiers exhausted
		slog.WarnContext(ctx, fmt.Sprintf("[RequestColorMarkerEngine] all tiers exhausted, keyPrefix: %s", e.keyPrefix))
		return ctx, func() {}, errRateLimitReached
	}

	// acquired > 0: priority (1-based in Lua, convert to 0-based)
	priority := int(acquired) - 1
	acquiredTierIdx := numTiers - 1 - priority

	// Set priority to context
	newCtx = e.setPriorityToContext(ctx, priority)

	slog.InfoContext(ctx, fmt.Sprintf("[RequestColorMarkerEngine] request allowed with priority %d (tier %d)", priority, acquiredTierIdx))
	done = func() {}
	return newCtx, done, nil
}

func (e *RequestColorMarkerEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	ctx := req.Context()

	// Use allow method to perform rate limiting and priority marking
	newCtx, done, err := e.allow(ctx)
	if err != nil {
		if err == errRateLimitReached {
			slog.WarnContext(ctx, fmt.Sprintf("[RequestColorMarkerEngine] rate limit reached, keyPrefix: %s", e.keyPrefix))
			return nil, &errutils.UpstreamRespError{
				StatusCode: 429,
				Body:       []byte("rate limit reached"),
			}
		}
		slog.ErrorContext(ctx, fmt.Sprintf("[RequestColorMarkerEngine] marker error: %v, keyPrefix: %s", err, e.keyPrefix))
		return nil, &errutils.UpstreamRespError{
			StatusCode: 500,
			Body:       []byte("internal server error"),
		}
	}

	// Use WithContext method to set new context with priority
	req = req.WithContext(newCtx)

	// Process request
	resp, err := e.next.Process(req)

	// Cleanup
	done()

	return resp, err
}

func (e *RequestColorMarkerEngine) setPriorityToContext(ctx context.Context, priority int) context.Context {
	priorityStr := fmt.Sprintf("%s%d", ContextValuePrefixPriority, priority)
	contextKey := contextKey(fmt.Sprintf("%s:%s", e.nameSpace, ContextKeyPriority))
	return context.WithValue(ctx, contextKey, priorityStr)
}

// Unified token bucket script supporting N tiers
// KEYS: all tier keys (tier_0, tier_1, ..., tier_N-1)
// ARGV[1]: numTiers
// ARGV[2..2*numTiers+1]: burst1, rate1, burst2, rate2, ...
// ARGV[2*numTiers+2]: nowUnix
// ARGV[2*numTiers+3..2*numTiers+2+numTiers]: ttl1, ttl2, ...
//
// Returns: {priority, acquiredKeysIndices}
// - priority: 0 means rejected, >0 means acquired with priority (1-based, convert to 0-based in Go)
// - acquiredKeysIndices: string of key indices that were consumed (e.g., "02" means keys[0] and keys[2])
const tokenBucketColorMarkerAquireLuaScript = `
local numTiers = tonumber(ARGV[1])
local bursts = {}
local rates = {}
local ttls = {}
for i = 1, numTiers do
    bursts[i] = tonumber(ARGV[1 + (i-1)*2 + 1])
    rates[i] = tonumber(ARGV[1 + (i-1)*2 + 2])
end
local nowUnix = tonumber(ARGV[2*numTiers + 2])
for i = 1, numTiers do
    ttls[i] = tonumber(ARGV[2*numTiers + 2 + i])
end

local lastTierIdx = numTiers

-- Helper function to get bucket tokens
local function getBucketTokens(keyIdx)
    local bucket = redis.call('HMGET', KEYS[keyIdx], 'tokens', 'lastRefill')
    local tokens = tonumber(bucket[1]) or bursts[keyIdx]
    local lastRefill = tonumber(bucket[2]) or nowUnix
    
    local elapsed = nowUnix - lastRefill
    local tokensToAdd = math.floor(elapsed * rates[keyIdx])
    tokens = math.min(bursts[keyIdx], tokens + tokensToAdd)
    
    return tokens
end

-- Helper function to update bucket
local function updateBucket(keyIdx, tokens)
    redis.call('HMSET', KEYS[keyIdx], 'tokens', tokens, 'lastRefill', nowUnix)
    redis.call('EXPIRE', KEYS[keyIdx], ttls[keyIdx])
end

-- Phase 1: Find first available tier from tier 0 (highest priority) to last tier
local foundTierIdx = nil
local tiersTokens = {}

for tierIdx = 1, numTiers do
    local tokens = getBucketTokens(tierIdx)
    tiersTokens[tierIdx] = tokens
    
    if tokens >= 1 then
        foundTierIdx = tierIdx
        break
    end
end

-- If no tier is available, reject without updating (don't update lastRefill when not consuming)
if foundTierIdx == nil then
    return {0, ""}
end

-- Phase 2: Determine which keys to consume
local keysToConsume = {}
local priority = numTiers - foundTierIdx + 1

if foundTierIdx == lastTierIdx then
    -- Found tier is the last tier: only consume last tier
    keysToConsume[1] = {keyIdx = lastTierIdx, tokens = tiersTokens[lastTierIdx]}
else
    -- Found tier is not the last tier: check if last tier also has tokens
    local lastTierTokens = tiersTokens[lastTierIdx]
    if lastTierTokens == nil then
        lastTierTokens = getBucketTokens(lastTierIdx)
        tiersTokens[lastTierIdx] = lastTierTokens
    end
    
    if lastTierTokens >= 1 then
        -- Both available: consume both found tier and last tier
        keysToConsume[1] = {keyIdx = foundTierIdx, tokens = tiersTokens[foundTierIdx]}
        keysToConsume[2] = {keyIdx = lastTierIdx, tokens = lastTierTokens}
    else
        -- Last tier not available: reject without updating (don't update lastRefill when not consuming)
        return {0, ""}
    end
end

-- Phase 3: Consume tokens from required buckets (only update lastRefill when consuming)
for i = 1, #keysToConsume do
    local keyIdx = keysToConsume[i].keyIdx
    local tokens = keysToConsume[i].tokens - 1
    updateBucket(keyIdx, tokens)
end

-- Build acquired keys indices string
local acquiredKeysStr = ""
for i = 1, #keysToConsume do
    acquiredKeysStr = acquiredKeysStr .. tostring(keysToConsume[i].keyIdx - 1)
end

return {priority, acquiredKeysStr}
`
