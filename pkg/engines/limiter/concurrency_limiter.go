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

type ConcurrencyLimiterEngine struct {
	redisClient      *redis.Client
	key              string
	concurrencyRates []int
	timeout          time.Duration
	acquireScript    *redis.Script
	releaseScript    *redis.Script
	renewScript      *redis.Script
	next             octollm.Engine
}

var _ octollm.Engine = (*ConcurrencyLimiterEngine)(nil)

func NewConcurrencyLimiterEngine(redisClient *redis.Client, config *ConcurrencyLimiterConfig, next octollm.Engine) (*ConcurrencyLimiterEngine, error) {
	// 如果配置不存在或 rates 为空，返回一个直接放过的 engine
	if config == nil || len(config.Rates) == 0 {
		key := ""
		if config != nil {
			key = config.Key
		}
		return &ConcurrencyLimiterEngine{
			redisClient:      redisClient,
			key:              key,
			concurrencyRates: nil,
			timeout:          config.Timeout,
			acquireScript:    nil,
			releaseScript:    nil,
			renewScript:      nil,
			next:             next,
		}, nil
	}

	concurrencyRates := config.Rates
	if timeout <= 0 {
		return nil, fmt.Errorf("timeout must be positive")
	}
	filteredRates, filtered := filterDecreasingRates(concurrencyRates)
	if filtered {
		logrus.Warnf("concurrency_rates must be strictly decreasing, filtered from %v to %v (removed %d non-decreasing values)", concurrencyRates, filteredRates, len(concurrencyRates)-len(filteredRates))
	}
	acquireScript := redis.NewScript(acquireLuaScript)
	releaseScript := redis.NewScript(releaseLuaScript)
	renewScript := redis.NewScript(renewLuaScript)

	return &ConcurrencyLimiterEngine{
		redisClient:      redisClient,
		key:              config.Key,
		concurrencyRates: filteredRates,
		timeout:          timeout,
		acquireScript:    acquireScript,
		releaseScript:    releaseScript,
		renewScript:      renewScript,
		next:             next,
	}, nil
}

// allow 尝试允许请求通过，进行限流检查
func (e *ConcurrencyLimiterEngine) allow(ctx context.Context) (done func(), err error) {
	// 如果 concurrencyRates 为空，直接放过
	if len(e.concurrencyRates) == 0 || e.acquireScript == nil {
		return func() {}, nil
	}

	priority := 0
	if p, ok := getPriorityFromContext(ctx); ok {
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

	// 启动续期协程
	renewCtx, renewCancel := context.WithCancel(context.WithoutCancel(ctx))
	renewDone := make(chan struct{})
	go e.renewMember(renewCtx, priorityKey, totalKey, memberID, renewDone)

	done = func() {
		// 停止续期协程
		renewCancel()
		<-renewDone

		// 释放 member
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

func (e *ConcurrencyLimiterEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	ctx := req.Context()

	// 使用 allow 方法进行限流
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

	// 处理请求
	resp, err := e.next.Process(req)

	// 无论成功还是失败，都需要调用 done 清理
	done()

	return resp, err
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

// renewMember 定期续期 member 的 score
func (e *ConcurrencyLimiterEngine) renewMember(ctx context.Context, priorityKey, totalKey, memberID string, done chan struct{}) {
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
