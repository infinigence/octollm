package limiter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

// RpmLimiterEngine 基于 RPM 的优先级限流。
// 根据优先级和配置的 rates 进行限流检查。
type RpmLimiterEngine struct {
	redisClient *redis.Client
	key         string
	rates       []int
	next        octollm.Engine
}

var _ octollm.Engine = (*RpmLimiterEngine)(nil)

var errRPMRateLimitReached = fmt.Errorf("rpm rate limit reached")

func NewRpmLimiterEngine(redisClient *redis.Client, config *RpmLimiterConfig, key string, next octollm.Engine) (*RpmLimiterEngine, error) {
	if next == nil {
		return nil, fmt.Errorf("next engine must not be nil")
	}

	if config == nil || len(config.Rates) == 0 {
		return &RpmLimiterEngine{
			redisClient: redisClient,
			key:         key,
			rates:       nil,
			next:        next,
		}, nil
	}

	filteredRates, filtered := filterDecreasingRates(config.Rates)
	if filtered {
		logrus.Warnf("rpm_limiter_rates must be strictly decreasing, filtered from %v to %v (removed %d non-decreasing values)", config.Rates, filteredRates, len(config.Rates)-len(filteredRates))
	}

	return &RpmLimiterEngine{
		redisClient: redisClient,
		key:         key,
		rates:       filteredRates,
		next:        next,
	}, nil
}

// allow 尝试允许请求通过，进行 RPM 限流检查
func (e *RpmLimiterEngine) allow(ctx context.Context) (done func(), err error) {
	// 如果 rates 为空，直接放过
	if len(e.rates) == 0 || e.redisClient == nil {
		return func() {}, nil
	}

	// 获取优先级
	priority := 0
	if p, ok := e.getPriorityFromContext(ctx); ok {
		priority = p
	}
	if priority < 0 {
		logrus.WithContext(ctx).Errorf("[RpmLimiterEngine] invalid priority: %d, must be in range [0, %d)", priority, len(e.rates))
		return func() {}, fmt.Errorf("invalid priority: %d, must be in range [0, %d)", priority, len(e.rates))
	}

	var priorityLimit int
	if priority >= len(e.rates) {
		priority = len(e.rates) - 1
		priorityLimit = e.rates[0]
	} else {
		priorityLimit = e.rates[len(e.rates)-1-priority]
	}
	maxRPM := e.rates[0]
	gap := len(e.rates) - 1 - priority
	if gap < 0 {
		gap = 0
	}
	totalLimit := maxRPM - gap

	// 检查当前 RPM 使用情况
	val, err := e.redisClient.Get(ctx, e.key).Int()
	if err != nil && err != redis.Nil {
		logrus.WithContext(ctx).Errorf("[RpmLimiterEngine] failed to get RPM value for %s: %v", e.key, err)
		return func() {}, fmt.Errorf("failed to get RPM value: %w", err)
	}

	currentRemaining := priorityLimit
	if err != redis.Nil {
		currentRemaining = val
	}

	if currentRemaining <= 0 {
		logrus.WithContext(ctx).Errorf("[RpmLimiterEngine] RPM limit %d reached, current: %d, key: %s, priority: %d", priorityLimit, currentRemaining, e.key, priority)
		return func() {}, errRPMRateLimitReached
	}

	// 检查是否超过总限制（简单检查）
	if totalLimit > 0 && (priorityLimit-currentRemaining+1) > totalLimit {
		logrus.WithContext(ctx).Errorf("[RpmLimiterEngine] total RPM limit %d reached, key: %s, priority: %d", totalLimit, e.key, priority)
		return func() {}, errRPMRateLimitReached
	}

	logrus.WithContext(ctx).Debugf("[RpmLimiterEngine] RPM allow: priority=%d, remaining=%d/%d, key=%s", priority, currentRemaining, priorityLimit, e.key)

	done = func() {
		// 扣除 RPM
		if e.redisClient != nil {
			c1 := context.WithoutCancel(ctx)
			pip := e.redisClient.Pipeline()
			pip.DecrBy(c1, e.key, 1)
			ttl := pip.TTL(c1, e.key)
			_, err := pip.Exec(c1)
			if err != nil && !errors.Is(err, redis.Nil) {
				logrus.WithContext(ctx).Errorf("[RpmLimiterEngine] failed to deduct RPM for %s: %v", e.key, err)
				return
			}

			// 如果 TTL 不存在，设置初始值
			if ttl.Val() <= 0 {
				err := e.redisClient.SetEx(c1, e.key, priorityLimit-1, 120*time.Second).Err()
				if err != nil {
					logrus.WithContext(ctx).Errorf("[RpmLimiterEngine] failed to set RPM TTL for %s: %v", e.key, err)
				}
			}
		}
	}

	return done, nil
}

func (e *RpmLimiterEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	ctx := req.Context()

	// 使用 allow 方法进行限流
	done, err := e.allow(ctx)
	if err != nil {
		if err == errRPMRateLimitReached {
			logrus.WithContext(ctx).Warnf("[RpmLimiterEngine] RPM rate limit reached, key: %s", e.key)
			return nil, &errutils.UpstreamRespError{
				StatusCode: 429,
				Body:       []byte("RPM rate limit reached"),
			}
		}
		logrus.WithContext(ctx).Errorf("[RpmLimiterEngine] RPM limiter error: %v, key: %s", err, e.key)
		return nil, &errutils.UpstreamRespError{
			StatusCode: 500,
			Body:       []byte("internal server error"),
		}
	}

	// 处理请求
	resp, err := e.next.Process(req)

	// 无论成功还是失败，都需要调用 done 清理（扣除配额）
	done()

	return resp, err
}

func (e *RpmLimiterEngine) getPriorityFromContext(ctx context.Context) (int, bool) {
	priorityStr, ok := ctx.Value(ContextKeyRpmPriority).(string)
	if !ok {
		return 0, false
	}
	var priority int
	_, err := fmt.Sscanf(priorityStr, ContextValuePrefixRpmPriority+"%d", &priority)
	if err != nil {
		return 0, false
	}

	return priority, true
}
