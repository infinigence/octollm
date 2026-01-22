package limiter

import (
	"context"
	"fmt"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

// RpmMarkerEngine 基于 RPM 的染色逻辑扩展。
// 根据当前 RPM 使用情况计算优先级并设置到 context 中。
type RpmMarkerEngine struct {
	redisClient *redis.Client
	key         string
	rates       []int
	needMarker  bool
	next        octollm.Engine
}

var _ octollm.Engine = (*RpmMarkerEngine)(nil)

func NewRpmMarkerEngine(redisClient *redis.Client, config *RpmMarkerConfig, next octollm.Engine) (*RpmMarkerEngine, error) {
	if next == nil {
		return nil, fmt.Errorf("next engine must not be nil")
	}

	if config == nil || len(config.Rates) == 0 {
		key := ""
		if config != nil {
			key = config.Key
		}
		return &RpmMarkerEngine{
			redisClient: redisClient,
			key:         key,
			rates:       nil,
			needMarker:  false,
			next:        next,
		}, nil
	}

	filteredRates, filtered := filterIncreasingRates(config.Rates)
	if filtered {
		logrus.Warnf("rpm_marker_rates must be strictly increasing, filtered from %v to %v (removed %d non-increasing values)", config.Rates, filteredRates, len(config.Rates)-len(filteredRates))
	}

	return &RpmMarkerEngine{
		redisClient: redisClient,
		key:         config.Key,
		rates:       filteredRates,
		needMarker:  config.NeedMark,
		next:        next,
	}, nil
}

// allow 尝试计算 RPM 优先级并设置到 context 中
func (e *RpmMarkerEngine) allow(ctx context.Context) (newCtx context.Context, done func(), err error) {
	// 如果 rates 为空，直接放过
	if len(e.rates) == 0 || e.redisClient == nil {
		return ctx, func() {}, nil
	}

	// 获取当前 RPM 使用情况
	currentRPM, err := e.getCurrentRPM(ctx)
	if err != nil {
		logrus.WithContext(ctx).Errorf("[RpmMarkerEngine] failed to get current RPM: %v", err)
		return ctx, func() {}, nil // 出错时不阻断，继续处理
	}

	// 限流为必选逻辑：达到最高档位直接拒绝
	if len(e.rates) > 0 && currentRPM >= e.rates[len(e.rates)-1] {
		logrus.WithContext(ctx).Warnf("[RpmMarkerEngine] rpm limit reached at max tier, key=%s, currentRPM=%d, maxRate=%d", e.key, currentRPM, e.rates[len(e.rates)-1])
		return ctx, func() {}, errRateLimitReached
	}

	// 计算优先级
	priority := e.calculatePriority(currentRPM)
	if e.needMarker {
		newCtx = e.setPriorityToContext(ctx, priority)
	} else {
		newCtx = ctx
	}

	if e.needMarker {
		logrus.WithContext(ctx).Debugf("[RpmMarkerEngine] key=%s, currentRPM=%d, priority=%d (marker on)", e.key, currentRPM, priority)
	} else {
		logrus.WithContext(ctx).Debugf("[RpmMarkerEngine] key=%s, currentRPM=%d (marker off)", e.key, currentRPM)
	}

	return newCtx, func() {}, nil
}

func (e *RpmMarkerEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	ctx := req.Context()

	// 使用 allow 方法进行染色
	newCtx, done, err := e.allow(ctx)
	if err != nil {
		logrus.WithContext(ctx).Errorf("[RpmMarkerEngine] marker error: %v", err)
		return nil, err
	}

	// 使用 SetContext 方法设置新的 context
	req.SetContext(newCtx)

	// 处理请求
	resp, err := e.next.Process(req)

	// 清理
	done()

	return resp, err
}

func (e *RpmMarkerEngine) getCurrentRPM(ctx context.Context) (int, error) {
	val, err := e.redisClient.Get(ctx, e.key).Int()
	if err != nil && err != redis.Nil {
		return 0, err
	}

	// 如果 key 不存在，返回 0（表示当前没有使用）
	if err == redis.Nil {
		return 0, nil
	}

	// Redis 中存储的是剩余配额，这里我们需要计算已使用的
	// 假设 burst 是 rates 的最大值（第一个值，因为rates是递减的）
	maxRPM := e.rates[0]
	usedRPM := maxRPM - val
	if usedRPM < 0 {
		usedRPM = 0
	}

	return usedRPM, nil
}

func (e *RpmMarkerEngine) calculatePriority(currentRPM int) int {
	for i := 0; i < len(e.rates); i++ {
		if currentRPM <= e.rates[i] {
			return len(e.rates) - 1 - i
		}
	}
	return 0
}

func (e *RpmMarkerEngine) setPriorityToContext(ctx context.Context, priority int) context.Context {
	priorityStr := fmt.Sprintf("%s%d", ContextValuePrefixRpmPriority, priority)
	return context.WithValue(ctx, ContextKeyRpmPriority, priorityStr)
}
