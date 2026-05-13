package limiter

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/octollm"
)

// RequestColorMarkerEngine is a multi-tier token bucket-based request rate limiter with priority marking.
// Uses multiple token buckets for different priority levels, trying from highest to lowest priority.
type RequestColorMarkerEngine struct {
	redisClient       *redis.Client
	keyPrefix         string        // Redis key prefix for storing token bucket states
	limits            []int         // Per-priority request budget per window (independent tiers; see NewRequestColorMarkerEngine)
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
// limits: Per-priority request budget within the window, highest priority first. Each positive entry is an
// independent token-bucket burst; total steady burst capacity is the sum of positive limits.
// Negative values are treated as 0. Only an empty limits slice disables the marker (pass-through).
// A zero entry at any index means that band has no quota (skipped); e.g. limits=[0] rejects all traffic.
//
//	Priority is: len(limits) - 1 - tierIdx (tier 0 = highest priority).
//
// window: Time window (supports seconds, minutes, etc.), must be greater than 0
// nameSpace: Namespace for isolating priority across different namespaces, marker and limiter within the same nameSpace can communicate
// next: Next engine
//
// Working principle:
// - Each tier has its own token bucket: rate = limit / window.Seconds(), burst = limit
// - A request tries tier 0, then tier 1, …; the first tier with burst>0 and at least one token wins
// - Only that tier’s bucket is decremented
//
// Example: limits=[3, 2, 1], window=1m → up to 3+2+1 marked requests per minute at priorities 2, 1, 0 respectively
// Example: limits=[3, 0, 0] → three highest-priority tokens per window; lower bands empty
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

	normalizedLimits, normalized := normalizeConcurrencyMarkerRates(limits)
	if normalized {
		slog.Warn(fmt.Sprintf("request_color_marker_limits normalized from %v to %v", limits, normalizedLimits))
	}
	if len(normalizedLimits) == 0 {
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

	if window <= 0 {
		return nil, fmt.Errorf("window must be positive")
	}

	// Calculate rate and TTL for each tier
	rates := make([]float64, len(normalizedLimits))
	ttls := make([]int64, len(normalizedLimits))
	for i, limit := range normalizedLimits {
		if limit <= 0 {
			rates[i] = 0
			ttls[i] = 1
			continue
		}
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
		limits:            normalizedLimits,
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
		return ctx, func() {}, ErrRateLimitReached
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
		slog.ErrorContext(ctx, fmt.Sprintf("[RequestColorMarkerEngine] marker error: %v, keyPrefix: %s", err, e.keyPrefix))
		return nil, err
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

-- Phase 1: First tier with burst>=1 and at least one token wins; consume only that bucket
local foundTierIdx = nil
local foundTokens = nil

for tierIdx = 1, numTiers do
    if bursts[tierIdx] >= 1 then
        local tokens = getBucketTokens(tierIdx)
        if tokens >= 1 then
            foundTierIdx = tierIdx
            foundTokens = tokens
            break
        end
    end
end

if foundTierIdx == nil then
    return {0, ""}
end

local priority = numTiers - foundTierIdx + 1
updateBucket(foundTierIdx, foundTokens - 1)

local acquiredKeysStr = tostring(foundTierIdx - 1)
return {priority, acquiredKeysStr}
`
