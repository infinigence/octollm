package limiter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"runtime/debug"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/octollm"
)

// QoSLimits is the dual-bucket configuration for one request.
// AvailableTotalMax[i] is the own-bucket cap and PriorityLevelMax[i] is the public-bucket cap.
// The limiter maps priority and cache-hit kind onto those caps.
type QoSLimits struct {
	AvailableTotalMax []int
	PriorityLevelMax  []int
	HeadroomRatio     float64
}

// LimitsFunc resolves per-request QoSLimits for the dual-bucket Lua scripts.
// A non-nil result with both arrays empty (or a nil result) pass-through that request.
// Errors fail the request.
type LimitsFunc func(req *octollm.Request) (*QoSLimits, error)

type ConcurrencyColorLimiterEngine struct {
	redisClient   *redis.Client
	key           string
	limitsFunc    LimitsFunc
	timeout       time.Duration
	nameSpace     string
	acquireScript *redis.Script
	releaseScript *redis.Script
	renewScript   *redis.Script
	next          octollm.Engine
}

var _ octollm.Engine = (*ConcurrencyColorLimiterEngine)(nil)

// NewConcurrencyColorLimiterEngine creates a limiter with static QoSLimits.
// Empty AvailableTotalMax and PriorityLevelMax disable the limiter (pass-through).
func NewConcurrencyColorLimiterEngine(redisClient *redis.Client, key string, limits QoSLimits, timeout time.Duration, nameSpace string, next octollm.Engine) (*ConcurrencyColorLimiterEngine, error) {
	if len(limits.AvailableTotalMax) == 0 && len(limits.PriorityLevelMax) == 0 {
		return NewConcurrencyColorLimiterEngineWithLimitsFunc(redisClient, key, timeout, nameSpace, nil, next)
	}
	if err := validateQoSLimits(limits); err != nil {
		return nil, err
	}
	cloned := cloneQoSLimits(limits)
	return NewConcurrencyColorLimiterEngineWithLimitsFunc(redisClient, key, timeout, nameSpace, func(*octollm.Request) (*QoSLimits, error) {
		return &cloned, nil
	}, next)
}

// NewConcurrencyColorLimiterEngineWithLimitsFunc creates a limiter that resolves QoSLimits per request.
// A nil limitsFunc constructs a disabled pass-through limiter.
func NewConcurrencyColorLimiterEngineWithLimitsFunc(redisClient *redis.Client, key string, timeout time.Duration, nameSpace string, limitsFunc LimitsFunc, next octollm.Engine) (*ConcurrencyColorLimiterEngine, error) {
	if limitsFunc == nil {
		return &ConcurrencyColorLimiterEngine{
			redisClient:   redisClient,
			key:           key,
			limitsFunc:    nil,
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
	if redisClient == nil {
		return nil, fmt.Errorf("redisClient must be provided")
	}

	return &ConcurrencyColorLimiterEngine{
		redisClient:   redisClient,
		key:           key,
		limitsFunc:    limitsFunc,
		timeout:       timeout,
		nameSpace:     nameSpace,
		acquireScript: redis.NewScript(acquireLuaScript),
		releaseScript: redis.NewScript(releaseLuaScript),
		renewScript:   redis.NewScript(renewLuaScript),
		next:          next,
	}, nil
}

func (e *ConcurrencyColorLimiterEngine) allow(req *octollm.Request) (done func(), priority int, err error) {
	ctx := req.Context()
	if e.limitsFunc == nil {
		return func() {}, 0, nil
	}
	limits, err := e.limitsFunc(req)
	if err != nil {
		return func() {}, 0, err
	}
	if limits == nil || (len(limits.AvailableTotalMax) == 0 && len(limits.PriorityLevelMax) == 0) || e.acquireScript == nil {
		return func() {}, 0, nil
	}
	if err := validateQoSLimits(*limits); err != nil {
		return func() {}, 0, err
	}

	priority = 0
	if p, ok := e.getPriorityFromContext(ctx); ok {
		priority = p
	}
	i := qosIndex(priority, len(limits.AvailableTotalMax))
	kind := cacheHitKindFromRequest(req)
	ownBase := limits.AvailableTotalMax[i]
	totalBase := limits.PriorityLevelMax[i]
	effectiveOwn, effectiveTotal := effectiveOwnAndTotalLimits(ownBase, totalBase, limits.HeadroomRatio, kind)

	nowUnix := time.Now().Unix()
	expireBefore := nowUnix - int64(e.timeout.Seconds())
	memberID := uuid.New().String()
	ownKey := fmt.Sprintf("%s:own_%d", e.key, i+1)
	publicKey := e.key + ":public"

	result, err := e.acquireScript.Run(ctx, e.redisClient, []string{ownKey, publicKey},
		effectiveOwn, effectiveTotal, nowUnix, expireBefore, memberID).Result()
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("dual acquire script error: %v, ownKey: %s, publicKey: %s", err, ownKey, publicKey))
		return func() {}, 0, fmt.Errorf("acquire script error: %w", err)
	}

	results, ok := result.([]interface{})
	if !ok || len(results) != 3 {
		slog.ErrorContext(ctx, fmt.Sprintf("unexpected script result format, ownKey: %s, publicKey: %s", ownKey, publicKey))
		return func() {}, 0, fmt.Errorf("unexpected script result format")
	}

	acquiredInt, _ := results[0].(int64)
	ownCount, _ := results[1].(int64)
	publicCount, _ := results[2].(int64)

	if acquiredInt == 0 {
		slog.WarnContext(ctx, fmt.Sprintf("concurrency limit reached for priority %d (qosIndex %d), ownCount: %d/%d, publicCount: %d/%d, hitKind: %s",
			priority, i, ownCount, effectiveOwn, publicCount, effectiveTotal, kind))
		return func() {}, priority, ErrRateLimitReached
	}

	renewCtx, renewCancel := context.WithCancel(context.WithoutCancel(ctx))
	renewDone := make(chan struct{})
	go e.renewMember(renewCtx, ownKey, publicKey, memberID, renewDone)

	done = func() {
		renewCancel()
		<-renewDone
		c1 := context.WithoutCancel(ctx)
		_, err := e.releaseScript.Run(c1, e.redisClient, []string{ownKey, publicKey}, memberID).Result()
		if err != nil {
			slog.ErrorContext(ctx, fmt.Sprintf("failed to release member: %v, ownKey: %s, publicKey: %s", err, ownKey, publicKey))
		}
	}

	slog.InfoContext(ctx, fmt.Sprintf("concurrency allowed for priority %d (qosIndex %d), ownCount: %d/%d, publicCount: %d/%d, hitKind: %s",
		priority, i, ownCount, effectiveOwn, publicCount, effectiveTotal, kind))
	return done, priority, nil
}

func (e *ConcurrencyColorLimiterEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	ctx := req.Context()

	done, priority, err := e.allow(req)
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("concurrency rate limiter error: %v, key: %s", err, e.key))
		if errors.Is(err, ErrRateLimitReached) {
			recordLimiterDeny(req, priority)
		}
		return nil, err
	}
	recordLimiterAllow(req, priority)

	resp, err := e.next.Process(req)

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

func cacheHitKindFromRequest(req *octollm.Request) octollm.CacheHitKind {
	kind, ok := octollm.GetCtxValue[octollm.CacheHitKind](req, octollm.ContextKeyCacheHitKind)
	if !ok {
		return octollm.CacheMiss
	}
	switch kind {
	case octollm.CacheHitStrong, octollm.CacheHitWeak, octollm.CacheMiss:
		return kind
	default:
		return octollm.CacheMiss
	}
}

// qosIndex maps a request priority onto the high-to-low QoS arrays.
// Missing or negative priority uses the lowest band; priority >= n-1 uses the highest.
func qosIndex(priority, n int) int {
	if n <= 0 {
		return 0
	}
	if priority < 0 {
		return n - 1
	}
	if priority >= n-1 {
		return 0
	}
	return n - 1 - priority
}

func effectiveOwnAndTotalLimits(ownBase, totalBase int, headroomRatio float64, kind octollm.CacheHitKind) (int, int) {
	if kind != octollm.CacheHitStrong || headroomRatio <= 0 {
		return ownBase, totalBase
	}
	factor := 1 + headroomRatio
	return int(math.Floor(float64(ownBase) * factor)), int(math.Floor(float64(totalBase) * factor))
}

func validateQoSLimits(limits QoSLimits) error {
	if len(limits.AvailableTotalMax) != len(limits.PriorityLevelMax) {
		return fmt.Errorf("available_total_max and priority_level_max must have the same length")
	}
	if len(limits.AvailableTotalMax) == 0 {
		return fmt.Errorf("qos limits must not be empty")
	}
	if limits.HeadroomRatio < 0 {
		return fmt.Errorf("headroom_ratio must be non-negative")
	}
	return nil
}

func cloneQoSLimits(in QoSLimits) QoSLimits {
	return QoSLimits{
		AvailableTotalMax: append([]int(nil), in.AvailableTotalMax...),
		PriorityLevelMax:  append([]int(nil), in.PriorityLevelMax...),
		HeadroomRatio:     in.HeadroomRatio,
	}
}

// Dual key acquire script.
// KEYS[1]: current QoS own-bucket key (own_{i+1})
// KEYS[2]: public bucket key
// ARGV[1]: effectiveOwnLimit
// ARGV[2]: effectiveTier0Limit
// ARGV[3]: nowUnix
// ARGV[4]: expireBefore
// ARGV[5]: memberID
const acquireLuaScript = `
local ownKey = KEYS[1]
local tier0Key = KEYS[2]
local ownLimit = tonumber(ARGV[1])
local tier0Limit = tonumber(ARGV[2])
local nowUnix = tonumber(ARGV[3])
local expireBefore = tonumber(ARGV[4])
local memberID = ARGV[5]

redis.call('ZREMRANGEBYSCORE', ownKey, '0', expireBefore)
redis.call('ZREMRANGEBYSCORE', tier0Key, '0', expireBefore)

local ownCount = redis.call('ZCARD', ownKey)
local tier0Count = redis.call('ZCARD', tier0Key)

local acquired = 0
if ownCount < ownLimit and tier0Count < tier0Limit then
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

const releaseLuaScript = `
local ownKey = KEYS[1]
local tier0Key = KEYS[2]
local memberID = ARGV[1]

redis.call('ZREM', ownKey, memberID)
redis.call('ZREM', tier0Key, memberID)

return {1}
`

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

func (e *ConcurrencyColorLimiterEngine) renewMember(ctx context.Context, ownKey, publicKey, memberID string, done chan struct{}) {
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
			result, err := e.renewScript.Run(ctx, e.redisClient, []string{ownKey, publicKey}, nowUnix, memberID).Result()
			if err != nil {
				slog.ErrorContext(ctx, fmt.Sprintf("failed to renew member: %v, ownKey: %s, publicKey: %s, memberID: %s", err, ownKey, publicKey, memberID))
				continue
			}
			results, ok := result.([]interface{})
			if !ok || len(results) != 1 {
				slog.ErrorContext(ctx, fmt.Sprintf("unexpected renew script result format, ownKey: %s, publicKey: %s, memberID: %s", ownKey, publicKey, memberID))
				continue
			}
			renewed, _ := results[0].(int64)
			if renewed == 0 {
				slog.WarnContext(ctx, fmt.Sprintf("member not found for renewal, ownKey: %s, publicKey: %s, memberID: %s", ownKey, publicKey, memberID))
				return
			}
			slog.DebugContext(ctx, fmt.Sprintf("renewed member: ownKey=%s, publicKey=%s, memberID=%s", ownKey, publicKey, memberID))
		}
	}
}
