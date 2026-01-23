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

type ConcurrencyColorLimiterEngine struct {
	redisClient      *redis.Client
	key              string
	concurrencyRates []int
	timeout          time.Duration
	nameSpace        string
	acquireScript    *redis.Script
	releaseScript    *redis.Script
	renewScript      *redis.Script
	next             octollm.Engine
}

var _ octollm.Engine = (*ConcurrencyColorLimiterEngine)(nil)

// NewConcurrencyColorLimiterEngine creates a concurrency color limiter engine
// redisClient: Redis client
// key: Redis key for storing concurrency count
// rates: Concurrency rate threshold array, must be strictly decreasing
// timeout: Timeout duration, must be greater than 0
// nameSpace: Namespace for isolating priority across different namespaces, marker and limiter within the same nameSpace can communicate
// next: Next engine
func NewConcurrencyColorLimiterEngine(redisClient *redis.Client, key string, rates []int, timeout time.Duration, nameSpace string, next octollm.Engine) (*ConcurrencyColorLimiterEngine, error) {
	// If rates is empty, return an engine that directly passes through
	if len(rates) == 0 {
		return &ConcurrencyColorLimiterEngine{
			redisClient:      redisClient,
			key:              key,
			concurrencyRates: nil,
			timeout:          timeout,
			nameSpace:        nameSpace,
			acquireScript:    nil,
			releaseScript:    nil,
			renewScript:      nil,
			next:             next,
		}, nil
	}

	if timeout <= 0 {
		return nil, fmt.Errorf("timeout must be positive")
	}
	filteredRates, filtered := filterDecreasingRates(rates)
	if filtered {
		logrus.Warnf("concurrency_rates must be strictly decreasing, filtered from %v to %v (removed %d non-decreasing values)", rates, filteredRates, len(rates)-len(filteredRates))
	}
	acquireScript := redis.NewScript(acquireLuaScript)
	releaseScript := redis.NewScript(releaseLuaScript)
	renewScript := redis.NewScript(renewLuaScript)

	return &ConcurrencyColorLimiterEngine{
		redisClient:      redisClient,
		key:              key,
		concurrencyRates: filteredRates,
		timeout:          timeout,
		nameSpace:        nameSpace,
		acquireScript:    acquireScript,
		releaseScript:    releaseScript,
		renewScript:      renewScript,
		next:             next,
	}, nil
}

// allow attempts to allow the request to pass through, performing rate limiting check
func (e *ConcurrencyColorLimiterEngine) allow(ctx context.Context) (done func(), err error) {
	// If concurrencyRates is empty, directly pass through
	if len(e.concurrencyRates) == 0 || e.acquireScript == nil {
		return func() {}, nil
	}

	priority := 0
	if p, ok := e.getPriorityFromContext(ctx); ok {
		priority = p
	}
	if priority < 0 {
		logrus.WithContext(ctx).Errorf("invalid priority: %d, must be in range [0, %d)", priority, len(e.concurrencyRates))
		return func() {}, fmt.Errorf("invalid priority: %d, must be in range [0, %d)", priority, len(e.concurrencyRates))
	}
	var priorityLimit int
	if priority >= len(e.concurrencyRates) {
		priority = len(e.concurrencyRates) - 1
		priorityLimit = e.concurrencyRates[0]
	} else {
		priorityLimit = e.concurrencyRates[len(e.concurrencyRates)-1-priority]
	}
	maxConcurrency := e.concurrencyRates[0]
	gap := len(e.concurrencyRates) - 1 - priority
	if gap < 0 {
		gap = 0
	}
	totalLimit := maxConcurrency - gap
	priorityKey := fmt.Sprintf("%s:priority:%d", e.key, priority)
	totalKey := fmt.Sprintf("%s:total", e.key)
	nowUnix := time.Now().Unix()
	expireBefore := nowUnix - int64(e.timeout.Seconds())
	memberID := uuid.New().String()
	result, err := e.acquireScript.Run(ctx, e.redisClient, []string{priorityKey, totalKey},
		priorityLimit, totalLimit, nowUnix, expireBefore, memberID).Result()
	if err != nil {
		logrus.WithContext(ctx).Errorf("acquire script error: %v, key: %s,%s", err, priorityKey, totalKey)
		return func() {}, fmt.Errorf("acquire script error: %w", err)
	}
	results, ok := result.([]interface{})
	if !ok || len(results) != 3 {
		logrus.WithContext(ctx).Errorf("unexpected script result format, key: %s,%s", priorityKey, totalKey)
		return func() {}, fmt.Errorf("unexpected script result format")
	}

	acquiredInt, _ := results[0].(int64)
	priorityCount, _ := results[1].(int64)
	totalCount, _ := results[2].(int64)

	if acquiredInt == 0 {
		if int(priorityCount) >= priorityLimit {
			logrus.WithContext(ctx).Errorf("priority %d concurrency limit %d reached, current: %d,key: %s,priority: %d", priority, priorityLimit, priorityCount, priorityKey, priority)
			return func() {}, errRateLimitReached
		}
		logrus.WithContext(ctx).Errorf("total concurrency limit %d reached, current: %d,key: %s,priority: %d", totalLimit, totalCount, totalKey, priority)
		return func() {}, errRateLimitReached
	}

	// Start renewal goroutine
	renewCtx, renewCancel := context.WithCancel(context.WithoutCancel(ctx))
	renewDone := make(chan struct{})
	go e.renewMember(renewCtx, priorityKey, totalKey, memberID, renewDone)

	done = func() {
		// Stop renewal goroutine
		renewCancel()
		<-renewDone

		// Release member
		if e.releaseScript != nil {
			c1 := context.WithoutCancel(ctx)
			_, err := e.releaseScript.Run(c1, e.redisClient, []string{priorityKey, totalKey}, memberID).Result()
			if err != nil {
				logrus.WithContext(ctx).Errorf("failed to release member from sets: %v,key: %s,%s", err, priorityKey, totalKey)
			}
		}
	}

	logrus.WithContext(ctx).Debugf("rate limit allow: priority=%d, priorityCount=%d/%d, totalCount=%d/%d,key: %s,%s",
		priority, priorityCount, priorityLimit, totalCount, totalLimit, priorityKey, totalKey)

	return done, nil
}

func (e *ConcurrencyColorLimiterEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	ctx := req.Context()

	// Use allow method to perform rate limiting
	done, err := e.allow(ctx)
	if err != nil {
		if err == errRateLimitReached {
			logrus.WithContext(ctx).Warnf("concurrency rate limiter: rate limit reached, key: %s", e.key)
			return nil, &errutils.UpstreamRespError{
				StatusCode: 429,
				Body:       []byte("rate limit reached"),
			}
		}
		logrus.WithContext(ctx).Errorf("concurrency rate limiter error: %v, key: %s", err, e.key)
		return nil, &errutils.UpstreamRespError{
			StatusCode: 500,
			Body:       []byte("internal server error"),
		}
	}

	// Process request
	resp, err := e.next.Process(req)

	// Call done to cleanup regardless of success or failure
	done()

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
		if rates[i] < rates[i-1] {
			filteredRates = append(filteredRates, rates[i])
		} else {
			break
		}
	}

	filtered := len(filteredRates) < len(rates)
	return filteredRates, filtered
}

const acquireLuaScript = `
local priorityKey = KEYS[1]
local totalKey = KEYS[2]
local priorityLimit = tonumber(ARGV[1])
local totalLimit = tonumber(ARGV[2])
local nowUnix = tonumber(ARGV[3])
local expireBefore = tonumber(ARGV[4])
local memberID = ARGV[5]

redis.call('ZREMRANGEBYSCORE', priorityKey, '0', expireBefore)
redis.call('ZREMRANGEBYSCORE', totalKey, '0', expireBefore)

local priorityCount = redis.call('ZCARD', priorityKey)
local totalCount = redis.call('ZCARD', totalKey)

local acquired = 0
if priorityCount < priorityLimit and totalCount < totalLimit then
    redis.call('ZADD', priorityKey, nowUnix, memberID)
    redis.call('ZADD', totalKey, nowUnix, memberID)
    redis.call('EXPIRE', priorityKey, 3600)
    redis.call('EXPIRE', totalKey, 3600)
    priorityCount = priorityCount + 1
    totalCount = totalCount + 1
    acquired = 1
end

return {acquired, priorityCount, totalCount}
`

const releaseLuaScript = `
local priorityKey = KEYS[1]
local totalKey = KEYS[2]
local memberID = ARGV[1]

redis.call('ZREM', priorityKey, memberID)
redis.call('ZREM', totalKey, memberID)

return {1}
`

const renewLuaScript = `
local priorityKey = KEYS[1]
local totalKey = KEYS[2]
local nowUnix = tonumber(ARGV[1])
local memberID = ARGV[2]

local priorityExists = redis.call('ZSCORE', priorityKey, memberID) ~= false
local totalExists = redis.call('ZSCORE', totalKey, memberID) ~= false

if priorityExists and totalExists then
    redis.call('ZADD', priorityKey, nowUnix, memberID)
    redis.call('ZADD', totalKey, nowUnix, memberID)
    redis.call('EXPIRE', priorityKey, 3600)
    redis.call('EXPIRE', totalKey, 3600)
    return {1}
else
    return {0}
end
`

// renewMember periodically renews the member's score
func (e *ConcurrencyColorLimiterEngine) renewMember(ctx context.Context, priorityKey, totalKey, memberID string, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nowUnix := time.Now().Unix()
			result, err := e.renewScript.Run(ctx, e.redisClient, []string{priorityKey, totalKey}, nowUnix, memberID).Result()
			if err != nil {
				logrus.WithContext(ctx).Errorf("failed to renew member: %v, priorityKey: %s, totalKey: %s, memberID: %s", err, priorityKey, totalKey, memberID)
				continue
			}
			results, ok := result.([]interface{})
			if !ok || len(results) != 1 {
				logrus.WithContext(ctx).Errorf("unexpected renew script result format, priorityKey: %s, totalKey: %s, memberID: %s", priorityKey, totalKey, memberID)
				continue
			}
			renewed, _ := results[0].(int64)
			if renewed == 0 {
				logrus.WithContext(ctx).Warnf("member not found for renewal, priorityKey: %s, totalKey: %s, memberID: %s", priorityKey, totalKey, memberID)
				return
			}
			logrus.WithContext(ctx).Debugf("renewed member: priorityKey=%s, totalKey=%s, memberID=%s", priorityKey, totalKey, memberID)
		}
	}
}
