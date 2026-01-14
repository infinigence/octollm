package ratelimiter

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

type contextKey string

const priorityColorKey contextKey = "priority_color"
const priorityPrefix = "priority_"

var ErrRateLimitReached = fmt.Errorf("rate limit reached")

// ColorLimiter 负责对请求进行染色，基于 ZSet 实现计数
type ColorLimiter struct {
	RedisClient   *redis.Client
	Key           string
	Rates         []int
	Timeout       time.Duration
	acquireScript *redis.Script
	releaseScript *redis.Script
}

// NewColorLimiter 创建新的染色器
// rates: 染色配置，例如 [100,200,300] 表示 default+2:100, default+1:200, default:300, drop>300
// rates 必须是递增的，如果不是递增的，只取递增的部分，丢弃其他部分
func NewColorLimiter(redisClient *redis.Client, key string, rates []int, timeout time.Duration) (*ColorLimiter, error) {
	if len(rates) == 0 {
		return nil, fmt.Errorf("rates must not be empty")
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

	return &ColorLimiter{
		RedisClient:   redisClient,
		Key:           key,
		Rates:         filteredRates,
		Timeout:       timeout,
		acquireScript: acquireScript,
		releaseScript: releaseScript,
	}, nil
}

// Allow 尝试允许请求通过，进行染色并计数
func (c *ColorLimiter) Allow(ctx context.Context) (newCtx context.Context, done func(), err error) {
	nowUnix := time.Now().Unix()
	expireBefore := nowUnix - int64(c.Timeout.Seconds())
	memberID := uuid.New().String()

	result, err := c.acquireScript.Run(ctx, c.RedisClient, []string{c.Key},
		c.Rates[len(c.Rates)-1], nowUnix, expireBefore, memberID).Result()
	if err != nil {
		logrus.WithContext(ctx).Errorf("acquire script error: %v, key: %s", err, c.Key)
		return ctx, func() {}, fmt.Errorf("acquire script error: %w", err)
	}

	results, ok := result.([]interface{})
	if !ok || len(results) != 2 {
		logrus.WithContext(ctx).Errorf("unexpected script result format, key: %s", c.Key)
		return ctx, func() {}, fmt.Errorf("unexpected script result format")
	}

	acquiredInt, _ := results[0].(int64)
	currentValue, _ := results[1].(int64)

	if acquiredInt == 0 {
		logrus.WithContext(ctx).Errorf("value %d exceeds max rate %d, key: %s", currentValue, c.Rates[len(c.Rates)-1], c.Key)
		return ctx, func() {}, ErrRateLimitReached
	}
	priority := CalculatePriority(int(currentValue), c.Rates)
	newCtx = SetPriorityToContext(ctx, priority)
	done = func() {
		c1 := context.WithoutCancel(ctx)
		_, err := c.releaseScript.Run(c1, c.RedisClient, []string{c.Key}, memberID).Result()
		if err != nil {
			logrus.WithContext(ctx).Errorf("failed to remove member from set: %v, key: %s", err, c.Key)
		}
	}
	logrus.WithContext(ctx).Debugf("current concurrency: key=%s, val=%d, priority=%d", c.Key, currentValue, priority)
	return newCtx, done, nil
}

// RateLimiter 基于 ZSet 实现的优先级限流器
type RateLimiter struct {
	RedisClient      *redis.Client
	Key              string
	ConcurrencyRates []int
	Timeout          time.Duration
	acquireScript    *redis.Script
	releaseScript    *redis.Script
}

// NewRateLimiter 创建新的限流器
// concurrencyRates: 并发限制配置，例如 [300,200,100,50] 第一个数是最大值，最后一个数是优先级0的值
// concurrencyRates 必须是递减的，如果不是也丢弃后面不递减的部分，只保留前面的
func NewRateLimiter(redisClient *redis.Client, key string, concurrencyRates []int, timeout time.Duration) (*RateLimiter, error) {
	if len(concurrencyRates) == 0 {
		return nil, fmt.Errorf("concurrency_rates must not be empty")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("timeout must be positive")
	}
	filteredRates, filtered := filterDecreasingRates(concurrencyRates)
	if filtered {
		logrus.Warnf("concurrency_rates must be strictly decreasing, filtered from %v to %v (removed %d non-decreasing values)", concurrencyRates, filteredRates, len(concurrencyRates)-len(filteredRates))
	}
	acquireScript := redis.NewScript(acquireLuaScript)
	releaseScript := redis.NewScript(releaseLuaScript)

	return &RateLimiter{
		RedisClient:      redisClient,
		Key:              key,
		ConcurrencyRates: filteredRates,
		Timeout:          timeout,
		acquireScript:    acquireScript,
		releaseScript:    releaseScript,
	}, nil
}

// Allow 尝试允许请求通过，进行限流检查
func (r *RateLimiter) Allow(ctx context.Context) (done func(), err error) {
	priority := 0
	if p, ok := GetPriorityFromContext(ctx); ok {
		priority = p
	}
	if priority < 0 {
		logrus.WithContext(ctx).Errorf("invalid priority: %d, must be in range [0, %d)", priority, len(r.ConcurrencyRates))
		return func() {}, fmt.Errorf("invalid priority: %d, must be in range [0, %d)", priority, len(r.ConcurrencyRates))
	}
	var priorityLimit int
	if priority >= len(r.ConcurrencyRates) {
		priority = len(r.ConcurrencyRates) - 1
		priorityLimit = r.ConcurrencyRates[0]
	} else {
		priorityLimit = r.ConcurrencyRates[len(r.ConcurrencyRates)-1-priority]
	}
	maxConcurrency := r.ConcurrencyRates[0]
	gap := len(r.ConcurrencyRates) - 1 - priority
	if gap < 0 {
		gap = 0
	}
	totalLimit := maxConcurrency - gap
	priorityKey := fmt.Sprintf("%s:priority:%d", r.Key, priority)
	totalKey := fmt.Sprintf("%s:total", r.Key)
	nowUnix := time.Now().Unix()
	expireBefore := nowUnix - int64(r.Timeout.Seconds())
	memberID := uuid.New().String()
	result, err := r.acquireScript.Run(ctx, r.RedisClient, []string{priorityKey, totalKey},
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
			return func() {}, ErrRateLimitReached
		}
		logrus.WithContext(ctx).Errorf("total concurrency limit %d reached, current: %d,key: %s,priority: %d", totalLimit, totalCount, totalKey, priority)
		return func() {}, ErrRateLimitReached
	}
	done = func() {
		c1 := context.WithoutCancel(ctx)
		_, err := r.releaseScript.Run(c1, r.RedisClient, []string{priorityKey, totalKey}, memberID).Result()
		if err != nil {
			logrus.WithContext(ctx).Errorf("failed to release member from sets: %v,key: %s,%s", err, priorityKey, totalKey)
		}
	}

	logrus.WithContext(ctx).Debugf("rate limit allow: priority=%d, priorityCount=%d/%d, totalCount=%d/%d,key: %s,%s",
		priority, priorityCount, priorityLimit, totalCount, totalLimit, priorityKey, totalKey)

	return done, nil
}

func CalculatePriority(value int, rates []int) int {
	for i := 0; i < len(rates); i++ {
		if value <= rates[i] {
			return len(rates) - 1 - i
		}
	}
	return 0
}

func SetPriorityToContext(ctx context.Context, priority int) context.Context {
	priorityStr := fmt.Sprintf("%s%d", priorityPrefix, priority)
	return context.WithValue(ctx, priorityColorKey, priorityStr)
}

func GetPriorityFromContext(ctx context.Context) (int, bool) {
	priorityStr, ok := ctx.Value(priorityColorKey).(string)
	if !ok {
		return 0, false
	}
	var priority int
	_, err := fmt.Sscanf(priorityStr, priorityPrefix+"%d", &priority)
	if err != nil {
		return 0, false
	}

	return priority, true
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
