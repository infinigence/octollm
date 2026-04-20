package limiter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/octollm"
)

// RequestColorLimiterEngine is a multi-tier token bucket-based priority rate limiter.
// Performs rate limiting checks based on priority marked by RequestColorMarkerEngine.
type RequestColorLimiterEngine struct {
	redisClient           *redis.Client
	keyPrefix             string        // Redis key prefix for storing token bucket states
	limits                []int         // Request limits for each supported priority tier (must be non-increasing)
	rates                 []float64     // Token refill rates for each priority tier (calculated from limits and window)
	ttls                  []int64       // Redis key TTL in seconds per tier; when expired, bucket is guaranteed full
	window                time.Duration // Time window
	nameSpace             string        // Namespace for priority context isolation
	tokenBucketScript     *redis.Script
	dualTokenBucketScript *redis.Script
	next                  octollm.Engine
}

var _ octollm.Engine = (*RequestColorLimiterEngine)(nil)

var ErrRequestLimitReached = errors.New("request limit reached")

// NewRequestColorLimiterEngine creates a multi-tier token bucket-based priority rate limiter
// redisClient: Redis client
// keyPrefix: Redis key prefix for storing token bucket states (each tier uses keyPrefix:tier_N)
// limits: Request limit array for each supported priority tier (must be non-increasing), e.g., [200, 100, 50]
//
//	means this limiter supports priority 2, 1, 0
//	tier 0 (priority 2): 200 requests, tier 1 (priority 1): 100 requests, tier 2 (priority 0): 50 requests within the window
//
// window: Time window (supports seconds, minutes, etc.), must be greater than 0
// nameSpace: Namespace for isolating priority across different namespaces, marker and limiter within the same nameSpace can communicate
// next: Next engine
//
// Working principle:
// - The number of limits determines how many priority levels are supported
// - Priority mapping: priority = len(limits) - 1 - tierIdx (same as marker)
// - Token consumption logic:
//   - Max priority requests (tier 0): only consume from tier 0
//   - Lower priority requests: atomically consume from own tier and tier 0 with reservation
//   - Reservation mechanism: when consuming tier 0 tokens, reserve N tokens where N = priority difference
//     This ensures higher priority requests always have tokens available
//   - Atomic operation: uses a single Lua script to check both buckets and consume from both or neither
//     No rollback needed as the operation is atomic
//
// Example 1: limits=[200, 100], window=1*time.Minute (supports priority 1, 0)
// - Tier 0: 200 req/min → Priority 1 (highest supported)
// - Tier 1: 100 req/min → Priority 0 (lowest)
// - Priority 1 request: only consume from tier 0
// - Priority 0 request: atomically check tier 1 ≥1 AND tier 0 >1, then consume from both
//   - If tier 0 has ≤1 tokens, priority 0 is rejected (reserved for priority 1)
//
// Example 2: limits=[200, 100, 50], window=1*time.Minute (supports priority 2, 1, 0)
// - Tier 0: 200 req/min → Priority 2 (highest supported)
// - Tier 1: 100 req/min → Priority 1 (medium)
// - Tier 2: 50 req/min → Priority 0 (lowest)
// - Priority 2 request: only consume from tier 0
// - Priority 1 request: atomically check tier 1 ≥1 AND tier 0 >1, then consume from both
//   - If tier 0 has ≤1 tokens, priority 1 is rejected
//
// - Priority 0 request: atomically check tier 2 ≥1 AND tier 0 >2, then consume from both
//   - If tier 0 has ≤2 tokens, priority 0 is rejected
func NewRequestColorLimiterEngine(redisClient *redis.Client, keyPrefix string, limits []int, window time.Duration, nameSpace string, next octollm.Engine) (*RequestColorLimiterEngine, error) {
	if next == nil {
		return nil, fmt.Errorf("next engine must not be nil")
	}

	// If limits is empty, disable rate limiting (pass through)
	if len(limits) == 0 {
		return &RequestColorLimiterEngine{
			redisClient:           redisClient,
			keyPrefix:             keyPrefix,
			limits:                nil,
			rates:                 nil,
			ttls:                  nil,
			window:                0,
			nameSpace:             nameSpace,
			tokenBucketScript:     nil,
			dualTokenBucketScript: nil,
			next:                  next,
		}, nil
	}

	// Validate and filter limits to ensure non-increasing order
	filteredLimits, filtered := filterDecreasingRates(limits)
	if filtered {
		slog.Warn(fmt.Sprintf("request_color_limiter_limits must be non-increasing, filtered from %v to %v (removed %d invalid suffix values)", limits, filteredLimits, len(limits)-len(filteredLimits)))
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

	return &RequestColorLimiterEngine{
		redisClient:           redisClient,
		keyPrefix:             keyPrefix,
		limits:                filteredLimits,
		rates:                 rates,
		ttls:                  ttls,
		window:                window,
		nameSpace:             nameSpace,
		tokenBucketScript:     redis.NewScript(tokenBucketLuaScript),
		dualTokenBucketScript: redis.NewScript(dualTokenBucketLuaScript),
		next:                  next,
	}, nil
}

// allow attempts to consume tokens from token buckets based on request priority
func (e *RequestColorLimiterEngine) allow(ctx context.Context) (done func(), err error) {
	// If configuration is invalid, directly pass through
	if len(e.limits) == 0 || e.redisClient == nil || e.tokenBucketScript == nil {
		return func() {}, nil
	}

	// Get priority from context (set by RequestColorMarkerEngine)
	priority := 0
	if p, ok := e.getPriorityFromContext(ctx); ok {
		priority = p
	}

	now := time.Now()
	nowUnix := now.Unix()

	// Calculate max supported priority: len(limits) - 1
	maxSupportedPriority := len(e.limits) - 1

	// Determine which tier corresponds to this priority
	// Priority mapping: priority = len(limits) - 1 - tierIdx
	// So tierIdx = len(limits) - 1 - priority
	var tierIdx int
	if priority >= maxSupportedPriority {
		// High priority (>= max supported): use tier 0
		tierIdx = 0
	} else if priority >= 0 {
		// Valid priority: use corresponding tier
		tierIdx = len(e.limits) - 1 - priority
	} else {
		// Invalid priority (< 0): use last tier (lowest priority)
		tierIdx = len(e.limits) - 1
	}

	if tierIdx == 0 {
		// Max priority: only consume from tier 0 using single key script
		key := fmt.Sprintf("%s:tier_%d", e.keyPrefix, 0)
		burst := e.limits[0]
		rate := e.rates[0]

		result, err := e.tokenBucketScript.Run(ctx, e.redisClient, []string{key},
			burst, rate, nowUnix, e.ttls[0]).Result()
		if err != nil {
			slog.ErrorContext(ctx, fmt.Sprintf("[RequestColorLimiterEngine] token bucket script error for tier 0: %v, key: %s", err, key))
			return func() {}, fmt.Errorf("token bucket script error: %w", err)
		}

		results, ok := result.([]interface{})
		if !ok || len(results) != 2 {
			slog.ErrorContext(ctx, fmt.Sprintf("[RequestColorLimiterEngine] unexpected script result format for tier 0, key: %s", key))
			return func() {}, fmt.Errorf("unexpected script result format")
		}

		allowed, _ := results[0].(int64)
		tokens, _ := results[1].(int64)

		if allowed == 1 {
			slog.InfoContext(ctx, fmt.Sprintf("[RequestColorLimiterEngine] max priority request allowed, consumed from tier 0, tokens: %d/%d", tokens, burst))
		} else {
			slog.WarnContext(ctx, fmt.Sprintf("[RequestColorLimiterEngine] tier 0 rejected, tokens: %d/%d, key: %s", tokens, burst, key))
			return func() {}, ErrRateLimitReached
		}
	} else {
		// Non-max priority: atomically consume from both own tier and tier 0 with reservation
		ownKey := fmt.Sprintf("%s:tier_%d", e.keyPrefix, tierIdx)
		tier0Key := fmt.Sprintf("%s:tier_%d", e.keyPrefix, 0)

		ownBurst := e.limits[tierIdx]
		ownRate := e.rates[tierIdx]
		tier0Burst := e.limits[0]
		tier0Rate := e.rates[0]

		// Calculate priority difference (how many tokens to reserve in tier 0)
		reservedTokens := int64(tierIdx)

		// Use atomic dual-key Lua script: check both, consume both or consume neither
		result, err := e.dualTokenBucketScript.Run(ctx, e.redisClient, []string{ownKey, tier0Key},
			ownBurst, ownRate, tier0Burst, tier0Rate, nowUnix, e.ttls[tierIdx], e.ttls[0], reservedTokens).Result()
		if err != nil {
			slog.ErrorContext(ctx, fmt.Sprintf("[RequestColorLimiterEngine] dual token bucket script error: %v, ownKey: %s, tier0Key: %s", err, ownKey, tier0Key))
			return func() {}, fmt.Errorf("token bucket script error: %w", err)
		}

		results, ok := result.([]interface{})
		if !ok || len(results) != 3 {
			slog.ErrorContext(ctx, fmt.Sprintf("[RequestColorLimiterEngine] unexpected script result format, ownKey: %s, tier0Key: %s", ownKey, tier0Key))
			return func() {}, fmt.Errorf("unexpected script result format")
		}

		allowed, _ := results[0].(int64)
		ownTokens, _ := results[1].(int64)
		tier0Tokens, _ := results[2].(int64)

		if allowed == 1 {
			slog.InfoContext(ctx, fmt.Sprintf("[RequestColorLimiterEngine] request allowed with priority %d (tier %d), consumed from tier %d and tier 0, ownTokens: %d/%d, tier0Tokens: %d/%d (reserved: %d)",
				priority, tierIdx, tierIdx, ownTokens, ownBurst, tier0Tokens, tier0Burst, reservedTokens))
		} else {
			slog.WarnContext(ctx, fmt.Sprintf("[RequestColorLimiterEngine] request rejected with priority %d (tier %d), ownTokens: %d/%d, tier0Tokens: %d/%d (reserved: %d)",
				priority, tierIdx, ownTokens, ownBurst, tier0Tokens, tier0Burst, reservedTokens))
			return func() {}, ErrRateLimitReached
		}
	}

	// done function doesn't need to do anything, as tokens are already consumed
	done = func() {}
	return done, nil
}

func (e *RequestColorLimiterEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	ctx := req.Context()

	// Use allow method to perform rate limiting
	done, err := e.allow(ctx)
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("[RequestColorLimiterEngine] request rate limiter error: %v, keyPrefix: %s", err, e.keyPrefix))
		return nil, err
	}

	// Process request
	resp, err := e.next.Process(req)

	// Call done (although currently an empty function, maintains interface consistency)
	done()

	return resp, err
}

func (e *RequestColorLimiterEngine) getPriorityFromContext(ctx context.Context) (int, bool) {
	contextKey := contextKey(fmt.Sprintf("%s:%s", e.nameSpace, ContextKeyPriority))
	priorityStr, ok := ctx.Value(contextKey).(string)
	if !ok {
		return 0, false
	}
	var priority int
	_, err := fmt.Sscanf(priorityStr, ContextValuePrefixPriority+"%d", &priority)
	if err != nil {
		return 0, false
	}

	return priority, true
}

// dualTokenBucketLuaScript atomically checks and consumes tokens from two buckets
// KEYS[1]: own tier bucket key
// KEYS[2]: tier 0 bucket key
// ARGV[1]: ownBurst (maximum tokens for own tier)
// ARGV[2]: ownRate (refill rate for own tier)
// ARGV[3]: tier0Burst (maximum tokens for tier 0)
// ARGV[4]: tier0Rate (refill rate for tier 0)
// ARGV[5]: nowUnix (current timestamp)
// ARGV[6]: ownTtl (Redis key TTL in seconds for own tier)
// ARGV[7]: tier0Ttl (Redis key TTL in seconds for tier 0)
// ARGV[8]: reservedTokens (tokens to reserve in tier 0 for higher priority)
// Returns: {allowed, ownTokens, tier0Tokens}
const dualTokenBucketLuaScript = `
local ownKey = KEYS[1]
local tier0Key = KEYS[2]
local ownBurst = tonumber(ARGV[1])
local ownRate = tonumber(ARGV[2])
local tier0Burst = tonumber(ARGV[3])
local tier0Rate = tonumber(ARGV[4])
local nowUnix = tonumber(ARGV[5])
local ownTtl = tonumber(ARGV[6])
local tier0Ttl = tonumber(ARGV[7])
local reservedTokens = tonumber(ARGV[8])

-- Get own tier bucket state
local ownBucket = redis.call('HMGET', ownKey, 'tokens', 'lastRefill')
local ownTokens = tonumber(ownBucket[1]) or ownBurst
local ownLastRefill = tonumber(ownBucket[2]) or nowUnix

-- Calculate tokens to add for own tier
local ownElapsed = nowUnix - ownLastRefill
local ownTokensToAdd = math.floor(ownElapsed * ownRate)
ownTokens = math.min(ownBurst, ownTokens + ownTokensToAdd)

-- Get tier 0 bucket state
local tier0Bucket = redis.call('HMGET', tier0Key, 'tokens', 'lastRefill')
local tier0Tokens = tonumber(tier0Bucket[1]) or tier0Burst
local tier0LastRefill = tonumber(tier0Bucket[2]) or nowUnix

-- Calculate tokens to add for tier 0
local tier0Elapsed = nowUnix - tier0LastRefill
local tier0TokensToAdd = math.floor(tier0Elapsed * tier0Rate)
tier0Tokens = math.min(tier0Burst, tier0Tokens + tier0TokensToAdd)

-- Check if both buckets have sufficient tokens
local allowed = 0
if ownTokens >= 1 and tier0Tokens > reservedTokens then
    -- Both conditions met, consume from both
    ownTokens = ownTokens - 1
    tier0Tokens = tier0Tokens - 1
    allowed = 1
    
    -- Update both buckets
    redis.call('HMSET', ownKey, 'tokens', ownTokens, 'lastRefill', nowUnix)
    redis.call('HMSET', tier0Key, 'tokens', tier0Tokens, 'lastRefill', nowUnix)
	redis.call('EXPIRE', ownKey, ownTtl)
    redis.call('EXPIRE', tier0Key, tier0Ttl)
end

return {allowed, ownTokens, tier0Tokens}
`
