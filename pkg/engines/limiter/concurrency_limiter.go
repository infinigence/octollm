package limiter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/octollm"
)

type ConcurrencyLimiterEngine struct {
	redisClient   *redis.Client
	key           string
	concurrency   int
	timeout       time.Duration
	acquireScript *redis.Script
	releaseScript *redis.Script
	renewScript   *redis.Script
	next          octollm.Engine
}

var _ octollm.Engine = (*ConcurrencyLimiterEngine)(nil)

var ErrConcurrencyLimitReached = errors.New("concurrency limit reached")

// NewConcurrencyLimiterEngine creates a simple concurrency limiter engine
// redisClient: Redis client
// key: Redis key for storing concurrency count
// concurrency: Maximum concurrency, must be greater than 0
// timeout: Timeout duration, must be greater than 0
// next: Next engine
func NewConcurrencyLimiterEngine(redisClient *redis.Client, key string, concurrency int, timeout time.Duration, next octollm.Engine) (*ConcurrencyLimiterEngine, error) {
	if next == nil {
		return nil, fmt.Errorf("next engine must not be nil")
	}

	if concurrency <= 0 {
		return &ConcurrencyLimiterEngine{
			redisClient:   redisClient,
			key:           key,
			concurrency:   0,
			timeout:       0,
			acquireScript: nil,
			releaseScript: nil,
			renewScript:   nil,
			next:          next,
		}, nil
	}

	if timeout <= 0 {
		return nil, fmt.Errorf("timeout must be positive")
	}

	acquireScript := redis.NewScript(simpleAcquireLuaScript)
	releaseScript := redis.NewScript(simpleReleaseLuaScript)
	renewScript := redis.NewScript(simpleRenewLuaScript)

	return &ConcurrencyLimiterEngine{
		redisClient:   redisClient,
		key:           key,
		concurrency:   concurrency,
		timeout:       timeout,
		acquireScript: acquireScript,
		releaseScript: releaseScript,
		renewScript:   renewScript,
		next:          next,
	}, nil
}

// allow attempts to allow the request to pass through, performing concurrency limiting check
func (e *ConcurrencyLimiterEngine) allow(ctx context.Context) (done func(), err error) {
	// If concurrency is 0 or script is nil, directly pass through
	if e.concurrency <= 0 || e.acquireScript == nil {
		return func() {}, nil
	}

	nowUnix := time.Now().Unix()
	expireBefore := nowUnix - int64(e.timeout.Seconds())
	memberID := uuid.New().String()

	result, err := e.acquireScript.Run(ctx, e.redisClient, []string{e.key},
		e.concurrency, nowUnix, expireBefore, memberID).Result()
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("acquire script error: %v, key: %s", err, e.key))
		return func() {}, fmt.Errorf("acquire script error: %w", err)
	}

	results, ok := result.([]interface{})
	if !ok || len(results) != 2 {
		slog.ErrorContext(ctx, fmt.Sprintf("unexpected script result format, key: %s", e.key))
		return func() {}, fmt.Errorf("unexpected script result format")
	}

	acquiredInt, _ := results[0].(int64)
	currentCount, _ := results[1].(int64)

	if acquiredInt == 0 {
		slog.WarnContext(ctx, fmt.Sprintf("concurrency limit %d reached, current: %d, key: %s", e.concurrency, currentCount, e.key))
		return func() {}, ErrConcurrencyLimitReached
	}

	// Start renewal goroutine
	renewCtx, renewCancel := context.WithCancel(context.WithoutCancel(ctx))
	renewDone := make(chan struct{})
	go e.renewMember(renewCtx, memberID, renewDone)

	done = func() {
		// Stop renewal goroutine
		renewCancel()
		<-renewDone

		// Release member
		if e.releaseScript != nil {
			c1 := context.WithoutCancel(ctx)
			_, err := e.releaseScript.Run(c1, e.redisClient, []string{e.key}, memberID).Result()
			if err != nil {
				slog.ErrorContext(ctx, fmt.Sprintf("failed to release member from set: %v, key: %s", err, e.key))
			}
		}
	}

	slog.DebugContext(ctx, fmt.Sprintf("concurrency allow: count=%d/%d, key=%s", currentCount, e.concurrency, e.key))

	return done, nil
}

func (e *ConcurrencyLimiterEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	ctx := req.Context()

	// Use allow method to perform rate limiting
	done, err := e.allow(ctx)
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("concurrency limiter error: %v, key: %s", err, e.key))
		return nil, err
	}

	// Process request
	resp, err := e.next.Process(req)

	// Call done to cleanup regardless of success or failure
	done()

	return resp, err
}

// renewMember periodically renews the member's score
func (e *ConcurrencyLimiterEngine) renewMember(ctx context.Context, memberID string, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nowUnix := time.Now().Unix()
			result, err := e.renewScript.Run(ctx, e.redisClient, []string{e.key}, nowUnix, memberID).Result()
			if err != nil {
				slog.ErrorContext(ctx, fmt.Sprintf("failed to renew member: %v, key: %s, memberID: %s", err, e.key, memberID))
				continue
			}
			results, ok := result.([]interface{})
			if !ok || len(results) != 1 {
				slog.ErrorContext(ctx, fmt.Sprintf("unexpected renew script result format, key: %s, memberID: %s", e.key, memberID))
				continue
			}
			renewed, _ := results[0].(int64)
			if renewed == 0 {
				slog.WarnContext(ctx, fmt.Sprintf("member not found for renewal, key: %s, memberID: %s", e.key, memberID))
				return
			}
			slog.DebugContext(ctx, fmt.Sprintf("renewed member: key=%s, memberID=%s", e.key, memberID))
		}
	}
}

const simpleAcquireLuaScript = `
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

const simpleReleaseLuaScript = `
local key = KEYS[1]
local memberID = ARGV[1]

redis.call('ZREM', key, memberID)

return {1}
`

const simpleRenewLuaScript = `
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
