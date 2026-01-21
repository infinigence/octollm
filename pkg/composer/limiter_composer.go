package composer

import (
	"fmt"
	"time"

	"github.com/infinigence/octollm/pkg/engines/limiter"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/redis/go-redis/v9"
)

// LimiterComposerConfig 限流器组合器配置
type LimiterComposerConfig struct {
	RPMMarkerKey   string `json:"rpm_marker_key" yaml:"rpm_marker_key"`
	RpmMarkerRates []int  `json:"rpm_marker_rates" yaml:"rpm_marker_rates"`
	RpmNeedMark    bool   `json:"rpm_need_mark" yaml:"rpm_need_mark"`

	RPMLimiterKey   string `json:"rpm_limiter_key" yaml:"rpm_limiter_key"`
	RpmLimiterRates []int  `json:"rpm_limiter_rates" yaml:"rpm_limiter_rates"`

	ConcurrencyMarkerKey   string `json:"concurrency_marker_key" yaml:"concurrency_marker_key"`
	ConcurrencyMarkerRates []int  `json:"concurrency_marker_rates" yaml:"concurrency_marker_rates"`
	ConcurrencyNeedMark    bool   `json:"concurrency_need_mark" yaml:"concurrency_need_mark"`

	ConcurrencyLimiterKey   string `json:"concurrency_limiter_key" yaml:"concurrency_limiter_key"`
	ConcurrencyLimiterRates []int  `json:"concurrency_limiter_rates" yaml:"concurrency_limiter_rates"`

	ConcurrencyMarkerTimeout  time.Duration `json:"concurrency_marker_timeout" yaml:"concurrency_marker_timeout"`
	ConcurrencyLimiterTimeout time.Duration `json:"concurrency_limiter_timeout" yaml:"concurrency_limiter_timeout"`
}

// NewLimiterComposer 根据配置创建限流器组合器，并链式组合引擎
// redisClient: Redis 客户端
// composerConfig: 限流器组合器配置
// next: 下一个 engine
// 返回链式组合后的 engine: RPM Marker -> Concurrency Marker -> RPM Limiter -> Concurrency Limiter -> next
func NewLimiterComposer(redisClient *redis.Client, composerConfig *LimiterComposerConfig, next octollm.Engine) (octollm.Engine, error) {
	if redisClient == nil {
		return nil, fmt.Errorf("redis client must not be nil")
	}
	if next == nil {
		return nil, fmt.Errorf("next engine must not be nil")
	}

	current := next

	// 构造 limiter 配置
	rpmMarkerConfig := &limiter.RpmMarkerConfig{
		Rates:    composerConfig.RpmMarkerRates,
		NeedMark: composerConfig.RpmNeedMark,
	}
	concurrencyMarkerConfig := &limiter.ConcurrencyMarkerConfig{
		Rates:    composerConfig.ConcurrencyMarkerRates,
		NeedMark: composerConfig.ConcurrencyNeedMark,
	}
	concurrencyLimiterConfig := &limiter.ConcurrencyLimiterConfig{
		Rates: composerConfig.ConcurrencyLimiterRates,
	}
	rpmLimiterConfig := &limiter.RpmLimiterConfig{
		Rates: composerConfig.RpmLimiterRates,
	}

	// 从内到外链式组合：
	// 1. ConcurrencyLimiterEngine (最内层)
	if composerConfig.ConcurrencyLimiterKey != "" {
		concurrencyLimiter, err := limiter.NewConcurrencyLimiterEngine(redisClient, concurrencyLimiterConfig, composerConfig.ConcurrencyLimiterKey, composerConfig.ConcurrencyLimiterTimeout, current)
		if err != nil {
			return nil, fmt.Errorf("failed to create concurrency limiter engine: %w", err)
		}
		current = concurrencyLimiter
	}

	// 2. RPM Limiter
	if composerConfig.RPMLimiterKey != "" {
		rpmLimiter, err := limiter.NewRpmLimiterEngine(redisClient, rpmLimiterConfig, composerConfig.RPMLimiterKey, current)
		if err != nil {
			return nil, fmt.Errorf("failed to create rpm limiter engine: %w", err)
		}
		current = rpmLimiter
	}

	// 3. ConcurrencyMarkerEngine
	if composerConfig.ConcurrencyMarkerKey != "" {
		concurrencyMarker, err := limiter.NewConcurrencyMarkerEngine(redisClient, concurrencyMarkerConfig, composerConfig.ConcurrencyMarkerKey, composerConfig.ConcurrencyMarkerTimeout, current)
		if err != nil {
			return nil, fmt.Errorf("failed to create concurrency marker engine: %w", err)
		}
		current = concurrencyMarker
	}

	// 4. RPM Marker (最外层)
	if composerConfig.RPMMarkerKey != "" {
		rpmMarker, err := limiter.NewRpmMarkerEngine(redisClient, rpmMarkerConfig, composerConfig.RPMMarkerKey, current)
		if err != nil {
			return nil, fmt.Errorf("failed to create rpm marker engine: %w", err)
		}
		current = rpmMarker
	}

	return current, nil
}
