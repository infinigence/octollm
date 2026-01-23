package limiter

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

// Use unified priority context key

var errRateLimitReached = fmt.Errorf("rate limit reached")

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

// NewConcurrencyColorMarkerEngine creates a concurrency color marker engine
// redisClient: Redis client
// key: Redis key for storing concurrency count
// rates: Concurrency rate threshold array, must be strictly increasing
// timeout: Timeout duration, must be greater than 0
// nameSpace: Namespace for isolating priority across different namespaces, marker and limiter within the same nameSpace can communicate
// next: Next engine
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
		logrus.Warnf("rates must be strictly increasing, filtered from %v to %v (removed %d non-increasing values)", rates, filteredRates, len(rates)-len(filteredRates))
	}
	acquireScript := redis.NewScript(colorAcquireLuaScript)
	releaseScript := redis.NewScript(colorReleaseLuaScript)
	renewScript := redis.NewScript(colorRenewLuaScript)

	return &ConcurrencyColorMarkerEngine{
		redisClient:   redisClient,
		key:           key,
		rates:         filteredRates,
		timeout:       timeout,
		nameSpace:     nameSpace,
		acquireScript: acquireScript,
		releaseScript: releaseScript,
		renewScript:   renewScript,
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

	result, err := e.acquireScript.Run(ctx, e.redisClient, []string{e.key},
		e.rates[len(e.rates)-1], nowUnix, expireBefore, memberID).Result()
	if err != nil {
		logrus.WithContext(ctx).Errorf("acquire script error: %v, key: %s", err, e.key)
		return ctx, func() {}, fmt.Errorf("acquire script error: %w", err)
	}

	results, ok := result.([]interface{})
	if !ok || len(results) != 2 {
		logrus.WithContext(ctx).Errorf("unexpected script result format, key: %s", e.key)
		return ctx, func() {}, fmt.Errorf("unexpected script result format")
	}

	acquiredInt, _ := results[0].(int64)
	currentValue, _ := results[1].(int64)

	if acquiredInt == 0 {
		logrus.WithContext(ctx).Errorf("value %d exceeds max rate %d, key: %s", currentValue, e.rates[len(e.rates)-1], e.key)
		return ctx, func() {}, errRateLimitReached
	}
	priority := calculatePriority(int(currentValue), e.rates)
	newCtx = e.setPriorityToContext(ctx, priority)

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
				logrus.WithContext(ctx).Errorf("failed to remove member from set: %v, key: %s", err, e.key)
			}
		}
	}
	logrus.WithContext(ctx).Debugf("current concurrency: key=%s, val=%d, priority=%d", e.key, currentValue, priority)
	return newCtx, done, nil
}

func (e *ConcurrencyColorMarkerEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	ctx := req.Context()

	// Use allow method to perform coloring
	newCtx, done, err := e.allow(ctx)
	if err != nil {
		if err == errRateLimitReached {
			logrus.WithContext(ctx).Warnf("concurrency marker: rate limit reached, key: %s", e.key)
			return nil, &errutils.UpstreamRespError{
				StatusCode: 429,
				Body:       []byte("rate limit reached"),
			}
		}
		logrus.WithContext(ctx).Errorf("concurrency marker error: %v, key: %s", err, e.key)
		return nil, &errutils.UpstreamRespError{
			StatusCode: 500,
			Body:       []byte("internal server error"),
		}
	}

	// Use SetContext method to set new context
	req.SetContext(newCtx)

	// Process request
	resp, err := e.next.Process(req)

	// Call done to cleanup regardless of success or failure
	done()

	return resp, err
}

func calculatePriority(value int, rates []int) int {
	for i := 0; i < len(rates); i++ {
		if value <= rates[i] {
			return len(rates) - 1 - i
		}
	}
	return 0
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
		if rates[i] > rates[i-1] {
			filteredRates = append(filteredRates, rates[i])
		} else {
			break
		}
	}

	filtered := len(filteredRates) < len(rates)
	return filteredRates, filtered
}

const colorAcquireLuaScript = `
local key = KEYS[1]
local lastRate = tonumber(ARGV[1])
local nowUnix = tonumber(ARGV[2])
local expireBefore = tonumber(ARGV[3])
local memberID = ARGV[4]

redis.call('ZREMRANGEBYSCORE', key, '0', expireBefore)

local currentValue = redis.call('ZCARD', key)

local acquired = 0
if currentValue <= lastRate then
    redis.call('ZADD', key, nowUnix, memberID)
    redis.call('EXPIRE', key, 3600)
    acquired = 1
end

return {acquired, currentValue}
`

const colorReleaseLuaScript = `
local key = KEYS[1]
local memberID = ARGV[1]

redis.call('ZREM', key, memberID)

return {1}
`

const colorRenewLuaScript = `
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

// renewMember periodically renews the member's score
func (e *ConcurrencyColorMarkerEngine) renewMember(ctx context.Context, memberID string, done chan struct{}) {
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
				logrus.WithContext(ctx).Errorf("failed to renew member: %v, key: %s, memberID: %s", err, e.key, memberID)
				continue
			}
			results, ok := result.([]interface{})
			if !ok || len(results) != 1 {
				logrus.WithContext(ctx).Errorf("unexpected renew script result format, key: %s, memberID: %s", e.key, memberID)
				continue
			}
			renewed, _ := results[0].(int64)
			if renewed == 0 {
				logrus.WithContext(ctx).Warnf("member not found for renewal, key: %s, memberID: %s", e.key, memberID)
				return
			}
			logrus.WithContext(ctx).Debugf("renewed member: key=%s, memberID=%s", e.key, memberID)
		}
	}
}
