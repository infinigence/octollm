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

type ConcurrencyColorLimiterEngine struct {
	redisClient         *redis.Client
	key                 string
	concurrencyRates    []int
	timeout             time.Duration
	nameSpace           string
	acquireSingleScript *redis.Script
	acquireDualScript   *redis.Script
	releaseSingleScript *redis.Script
	releaseDualScript   *redis.Script
	renewSingleScript   *redis.Script
	renewDualScript     *redis.Script
	next                octollm.Engine
}

var _ octollm.Engine = (*ConcurrencyColorLimiterEngine)(nil)

// NewConcurrencyColorLimiterEngine creates a concurrency color limiter engine with priority-based limits
// redisClient: Redis client
// key: Redis key prefix for storing concurrency counts (each tier uses key:tier_N)
// rates: Concurrency limit array for each priority tier (must be non-increasing), e.g., [200, 100, 50]
//
//	means tier 0 (priority 2): 200 concurrent, tier 1 (priority 1): 100 concurrent, tier 2 (priority 0): 50 concurrent
//
// timeout: Timeout duration for member expiration, must be greater than 0
// nameSpace: Namespace for isolating priority across different namespaces, marker and limiter within the same nameSpace can communicate
// next: Next engine
//
// Working principle:
//   - Priority mapping: priority = len(rates) - 1 - tierIdx (same as marker)
//   - Max priority requests (tier 0): only check tier 0 concurrency
//   - Lower priority requests: atomically check own tier and tier 0 with reservation
//   - Reservation mechanism: reserve N slots in tier 0 where N = priority difference (tierIdx)
//     This ensures higher priority requests always have slots available
//
// Example: rates=[200, 100, 50] (supports priority 2, 1, 0)
// - Tier 0: 200 concurrent → Priority 2 (highest)
// - Tier 1: 100 concurrent → Priority 1 (medium)
// - Tier 2: 50 concurrent → Priority 0 (lowest)
// - Priority 2: only check tier 0 < 200
// - Priority 1: check tier 1 < 100 AND tier 0 < (200-1)=199
// - Priority 0: check tier 2 < 50 AND tier 0 < (200-2)=198
func NewConcurrencyColorLimiterEngine(redisClient *redis.Client, key string, rates []int, timeout time.Duration, nameSpace string, next octollm.Engine) (*ConcurrencyColorLimiterEngine, error) {
	// If rates is empty, return an engine that directly passes through
	if len(rates) == 0 {
		return &ConcurrencyColorLimiterEngine{
			redisClient:         redisClient,
			key:                 key,
			concurrencyRates:    nil,
			timeout:             timeout,
			nameSpace:           nameSpace,
			acquireSingleScript: nil,
			acquireDualScript:   nil,
			releaseSingleScript: nil,
			releaseDualScript:   nil,
			renewSingleScript:   nil,
			renewDualScript:     nil,
			next:                next,
		}, nil
	}

	if timeout <= 0 {
		return nil, fmt.Errorf("timeout must be positive")
	}
	filteredRates, filtered := filterDecreasingRates(rates)
	if filtered {
		slog.Warn(fmt.Sprintf("concurrency_rates must be non-increasing, filtered from %v to %v (removed %d invalid suffix values)", rates, filteredRates, len(rates)-len(filteredRates)))
	}

	return &ConcurrencyColorLimiterEngine{
		redisClient:         redisClient,
		key:                 key,
		concurrencyRates:    filteredRates,
		timeout:             timeout,
		nameSpace:           nameSpace,
		acquireSingleScript: redis.NewScript(acquireSingleLuaScript),
		acquireDualScript:   redis.NewScript(acquireLuaScript),
		releaseSingleScript: redis.NewScript(releaseSingleLuaScript),
		releaseDualScript:   redis.NewScript(releaseLuaScript),
		renewSingleScript:   redis.NewScript(renewSingleLuaScript),
		renewDualScript:     redis.NewScript(renewLuaScript),
		next:                next,
	}, nil
}

// allow attempts to allow the request to pass through, performing rate limiting check
func (e *ConcurrencyColorLimiterEngine) allow(ctx context.Context) (done func(), err error) {
	// If concurrencyRates is empty, directly pass through
	if len(e.concurrencyRates) == 0 || e.acquireSingleScript == nil {
		return func() {}, nil
	}

	priority := 0
	if p, ok := e.getPriorityFromContext(ctx); ok {
		priority = p
	}

	nowUnix := time.Now().Unix()
	expireBefore := nowUnix - int64(e.timeout.Seconds())
	memberID := uuid.New().String()

	// Calculate max supported priority: len(concurrencyRates) - 1
	maxSupportedPriority := len(e.concurrencyRates) - 1

	// Determine which tier corresponds to this priority
	// Priority mapping: priority = len(concurrencyRates) - 1 - tierIdx
	// So tierIdx = len(concurrencyRates) - 1 - priority
	var tierIdx int
	if priority >= maxSupportedPriority {
		// High priority (>= max supported): use tier 0
		tierIdx = 0
	} else if priority >= 0 {
		// Valid priority: use corresponding tier
		tierIdx = len(e.concurrencyRates) - 1 - priority
	} else {
		// Invalid priority (< 0): use last tier (lowest priority)
		tierIdx = len(e.concurrencyRates) - 1
	}

	if tierIdx == 0 {
		// Max priority: only check tier 0 using single key script
		tier0Key := fmt.Sprintf("%s:tier_0", e.key)
		tier0Limit := e.concurrencyRates[0]

		result, err := e.acquireSingleScript.Run(ctx, e.redisClient, []string{tier0Key},
			tier0Limit, nowUnix, expireBefore, memberID).Result()
		if err != nil {
			slog.ErrorContext(ctx, fmt.Sprintf("acquire script error for tier 0: %v, key: %s", err, tier0Key))
			return func() {}, fmt.Errorf("acquire script error: %w", err)
		}

		results, ok := result.([]interface{})
		if !ok || len(results) != 2 {
			slog.ErrorContext(ctx, fmt.Sprintf("unexpected script result format for tier 0, key: %s", tier0Key))
			return func() {}, fmt.Errorf("unexpected script result format")
		}

		acquiredInt, _ := results[0].(int64)
		tier0Count, _ := results[1].(int64)

		if acquiredInt == 0 {
			slog.WarnContext(ctx, fmt.Sprintf("tier 0 concurrency limit %d reached, current: %d, key: %s", tier0Limit, tier0Count, tier0Key))
			return func() {}, ErrRateLimitReached
		}

		// Start renewal goroutine
		renewCtx, renewCancel := context.WithCancel(context.WithoutCancel(ctx))
		renewDone := make(chan struct{})
		go e.renewMemberSingle(renewCtx, tier0Key, memberID, renewDone)

		done = func() {
			renewCancel()
			<-renewDone
			c1 := context.WithoutCancel(ctx)
			_, err := e.releaseSingleScript.Run(c1, e.redisClient, []string{tier0Key}, memberID).Result()
			if err != nil {
				slog.ErrorContext(ctx, fmt.Sprintf("failed to release member: %v, key: %s", err, tier0Key))
			}
		}

		slog.InfoContext(ctx, fmt.Sprintf("max priority concurrency allowed, tier 0 count: %d/%d, key: %s", tier0Count, tier0Limit, tier0Key))
		return done, nil
	} else {
		// Non-max priority: atomically check own tier and tier 0 with reservation
		ownKey := fmt.Sprintf("%s:tier_%d", e.key, tierIdx)
		tier0Key := fmt.Sprintf("%s:tier_0", e.key)

		ownLimit := e.concurrencyRates[tierIdx]
		tier0Limit := e.concurrencyRates[0]

		// Calculate reservation (priority difference)
		reservedSlots := int64(tierIdx)

		result, err := e.acquireDualScript.Run(ctx, e.redisClient, []string{ownKey, tier0Key},
			ownLimit, tier0Limit, nowUnix, expireBefore, memberID, reservedSlots).Result()
		if err != nil {
			slog.ErrorContext(ctx, fmt.Sprintf("dual acquire script error: %v, ownKey: %s, tier0Key: %s", err, ownKey, tier0Key))
			return func() {}, fmt.Errorf("acquire script error: %w", err)
		}

		results, ok := result.([]interface{})
		if !ok || len(results) != 3 {
			slog.ErrorContext(ctx, fmt.Sprintf("unexpected script result format, ownKey: %s, tier0Key: %s", ownKey, tier0Key))
			return func() {}, fmt.Errorf("unexpected script result format")
		}

		acquiredInt, _ := results[0].(int64)
		ownCount, _ := results[1].(int64)
		tier0Count, _ := results[2].(int64)

		if acquiredInt == 0 {
			slog.WarnContext(ctx, fmt.Sprintf("concurrency limit reached for priority %d (tier %d), ownCount: %d/%d, tier0Count: %d/%d (reserved: %d)",
				priority, tierIdx, ownCount, ownLimit, tier0Count, tier0Limit, reservedSlots))
			return func() {}, ErrRateLimitReached
		}

		// Start renewal goroutine
		renewCtx, renewCancel := context.WithCancel(context.WithoutCancel(ctx))
		renewDone := make(chan struct{})
		go e.renewMemberDual(renewCtx, ownKey, tier0Key, memberID, renewDone)

		done = func() {
			renewCancel()
			<-renewDone
			c1 := context.WithoutCancel(ctx)
			_, err := e.releaseDualScript.Run(c1, e.redisClient, []string{ownKey, tier0Key}, memberID).Result()
			if err != nil {
				slog.ErrorContext(ctx, fmt.Sprintf("failed to release member: %v, ownKey: %s, tier0Key: %s", err, ownKey, tier0Key))
			}
		}

		slog.InfoContext(ctx, fmt.Sprintf("concurrency allowed for priority %d (tier %d), ownCount: %d/%d, tier0Count: %d/%d (reserved: %d)",
			priority, tierIdx, ownCount, ownLimit, tier0Count, tier0Limit, reservedSlots))
		return done, nil
	}
}

func (e *ConcurrencyColorLimiterEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	ctx := req.Context()

	// Use allow method to perform rate limiting
	done, err := e.allow(ctx)
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("concurrency rate limiter error: %v, key: %s", err, e.key))
		return nil, err
	}

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

func (e *ConcurrencyColorLimiterEngine) getPriorityFromContext(ctx context.Context) (int, bool) {
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

func filterDecreasingRates(rates []int) ([]int, bool) {
	if len(rates) == 0 {
		return rates, false
	}

	filteredRates := make([]int, 0, len(rates))
	filteredRates = append(filteredRates, rates[0])

	for i := 1; i < len(rates); i++ {
		if rates[i] <= rates[i-1] {
			filteredRates = append(filteredRates, rates[i])
		} else {
			break
		}
	}

	filtered := len(filteredRates) < len(rates)
	return filteredRates, filtered
}

// Single key acquire script (for max priority)
const acquireSingleLuaScript = `
local key = KEYS[1]
local concurrency = tonumber(ARGV[1])
local nowUnix = tonumber(ARGV[2])
local expireBefore = tonumber(ARGV[3])
local memberID = ARGV[4]

redis.call('ZREMRANGEBYSCORE', key, '0', expireBefore)

local currentCount = redis.call('ZCARD', key)

local acquired = 0
if currentCount < concurrency then
    redis.call('ZADD', key, nowUnix, memberID)
    redis.call('EXPIRE', key, 3600)
    currentCount = currentCount + 1
    acquired = 1
end

return {acquired, currentCount}
`

// Dual key acquire script with reservation (for non-max priority)
// KEYS[1]: own tier key
// KEYS[2]: tier 0 key
// ARGV[1]: ownLimit
// ARGV[2]: tier0Limit
// ARGV[3]: nowUnix
// ARGV[4]: expireBefore
// ARGV[5]: memberID
// ARGV[6]: reservedSlots (number of slots to reserve in tier 0)
const acquireLuaScript = `
local ownKey = KEYS[1]
local tier0Key = KEYS[2]
local ownLimit = tonumber(ARGV[1])
local tier0Limit = tonumber(ARGV[2])
local nowUnix = tonumber(ARGV[3])
local expireBefore = tonumber(ARGV[4])
local memberID = ARGV[5]
local reservedSlots = tonumber(ARGV[6])

-- Clean up expired members
redis.call('ZREMRANGEBYSCORE', ownKey, '0', expireBefore)
redis.call('ZREMRANGEBYSCORE', tier0Key, '0', expireBefore)

local ownCount = redis.call('ZCARD', ownKey)
local tier0Count = redis.call('ZCARD', tier0Key)

-- Check if both conditions are met: ownCount < ownLimit AND tier0Count < (tier0Limit - reservedSlots)
local acquired = 0
if ownCount < ownLimit and tier0Count < (tier0Limit - reservedSlots) then
    redis.call('ZADD', ownKey, nowUnix, memberID)
    redis.call('ZADD', tier0Key, nowUnix, memberID)
    redis.call('EXPIRE', ownKey, 3600)
    redis.call('EXPIRE', tier0Key, 3600)
    ownCount = ownCount + 1
    tier0Count = tier0Count + 1
    acquired = 1
end

return {acquired, ownCount, tier0Count}
`

// Single key release script
const releaseSingleLuaScript = `
local key = KEYS[1]
local memberID = ARGV[1]

redis.call('ZREM', key, memberID)

return {1}
`

// Dual key release script
const releaseLuaScript = `
local ownKey = KEYS[1]
local tier0Key = KEYS[2]
local memberID = ARGV[1]

redis.call('ZREM', ownKey, memberID)
redis.call('ZREM', tier0Key, memberID)

return {1}
`

// Single key renew script
const renewSingleLuaScript = `
local key = KEYS[1]
local nowUnix = tonumber(ARGV[1])
local memberID = ARGV[2]

if redis.call('ZSCORE', key, memberID) ~= false then
    redis.call('ZADD', key, nowUnix, memberID)
    redis.call('EXPIRE', key, 3600)
    return {1}
else
    return {0}
end
`

// Dual key renew script
const renewLuaScript = `
local ownKey = KEYS[1]
local tier0Key = KEYS[2]
local nowUnix = tonumber(ARGV[1])
local memberID = ARGV[2]

local ownExists = redis.call('ZSCORE', ownKey, memberID) ~= false
local tier0Exists = redis.call('ZSCORE', tier0Key, memberID) ~= false

if ownExists and tier0Exists then
    redis.call('ZADD', ownKey, nowUnix, memberID)
    redis.call('ZADD', tier0Key, nowUnix, memberID)
    redis.call('EXPIRE', ownKey, 3600)
    redis.call('EXPIRE', tier0Key, 3600)
    return {1}
else
    return {0}
end
`

// renewMemberSingle periodically renews the member's score for single key
func (e *ConcurrencyColorLimiterEngine) renewMemberSingle(ctx context.Context, key, memberID string, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nowUnix := time.Now().Unix()
			result, err := e.renewSingleScript.Run(ctx, e.redisClient, []string{key}, nowUnix, memberID).Result()
			if err != nil {
				slog.ErrorContext(ctx, fmt.Sprintf("failed to renew member: %v, key: %s, memberID: %s", err, key, memberID))
				continue
			}
			results, ok := result.([]interface{})
			if !ok || len(results) != 1 {
				slog.ErrorContext(ctx, fmt.Sprintf("unexpected renew script result format, key: %s, memberID: %s", key, memberID))
				continue
			}
			renewed, _ := results[0].(int64)
			if renewed == 0 {
				slog.WarnContext(ctx, fmt.Sprintf("member not found for renewal, key: %s, memberID: %s", key, memberID))
				return
			}
			slog.DebugContext(ctx, fmt.Sprintf("renewed member: key=%s, memberID=%s", key, memberID))
		}
	}
}

// renewMemberDual periodically renews the member's score for dual keys
func (e *ConcurrencyColorLimiterEngine) renewMemberDual(ctx context.Context, ownKey, tier0Key, memberID string, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nowUnix := time.Now().Unix()
			result, err := e.renewDualScript.Run(ctx, e.redisClient, []string{ownKey, tier0Key}, nowUnix, memberID).Result()
			if err != nil {
				slog.ErrorContext(ctx, fmt.Sprintf("failed to renew member: %v, ownKey: %s, tier0Key: %s, memberID: %s", err, ownKey, tier0Key, memberID))
				continue
			}
			results, ok := result.([]interface{})
			if !ok || len(results) != 1 {
				slog.ErrorContext(ctx, fmt.Sprintf("unexpected renew script result format, ownKey: %s, tier0Key: %s, memberID: %s", ownKey, tier0Key, memberID))
				continue
			}
			renewed, _ := results[0].(int64)
			if renewed == 0 {
				slog.WarnContext(ctx, fmt.Sprintf("member not found for renewal, ownKey: %s, tier0Key: %s, memberID: %s", ownKey, tier0Key, memberID))
				return
			}
			slog.DebugContext(ctx, fmt.Sprintf("renewed member: ownKey=%s, tier0Key=%s, memberID=%s", ownKey, tier0Key, memberID))
		}
	}
}
