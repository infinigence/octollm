package composer

import (
	"fmt"
	"time"

	"github.com/infinigence/octollm/pkg/engines/limiter"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/redis/go-redis/v9"
)

func NewTenantRpmLimiterMarker(redisClient *redis.Client, tenantId string, rates []int, next octollm.Engine) (octollm.Engine, error) {
	composerConfig := &limiter.RpmMarkerConfig{
		Key:      fmt.Sprintf("tenant:%s:rpm_marker", tenantId),
		Rates:    rates,
		NeedMark: true,
	}
	return limiter.NewRpmMarkerEngine(redisClient, composerConfig, next)
}

func NewTenantRpmLimiterMarkerWithoutMark(redisClient *redis.Client, tenantId string, rates []int, next octollm.Engine) (octollm.Engine, error) {
	composerConfig := &limiter.RpmMarkerConfig{
		Key:      fmt.Sprintf("tenant:%s:rpm_marker", tenantId),
		Rates:    rates,
		NeedMark: false,
	}
	return limiter.NewRpmMarkerEngine(redisClient, composerConfig, next)
}

func NewApiKeyRpmLimiterMarker(redisClient *redis.Client, apiKey string, rates []int, next octollm.Engine) (octollm.Engine, error) {
	composerConfig := &limiter.RpmMarkerConfig{
		Key:      fmt.Sprintf("api_key:%s:rpm_marker", apiKey),
		Rates:    rates,
		NeedMark: true,
	}
	return limiter.NewRpmMarkerEngine(redisClient, composerConfig, next)
}

func NewApiKeyRpmLimiterMarkerWithoutMark(redisClient *redis.Client, apiKey string, rates []int, next octollm.Engine) (octollm.Engine, error) {
	composerConfig := &limiter.RpmMarkerConfig{
		Key:      fmt.Sprintf("api_key:%s:rpm_marker", apiKey),
		Rates:    rates,
		NeedMark: false,
	}
	return limiter.NewRpmMarkerEngine(redisClient, composerConfig, next)
}

func NewRuleConcurrencyMarker(redisClient *redis.Client, ruleName string, rates []int, timeout time.Duration, next octollm.Engine) (octollm.Engine, error) {
	composerConfig := &limiter.ConcurrencyMarkerConfig{
		Key:      fmt.Sprintf("rule:%s:concurrency_marker", ruleName),
		Rates:    rates,
		NeedMark: true,
		Timeout:  timeout,
	}
	return limiter.NewConcurrencyMarkerEngine(redisClient, composerConfig, next)
}

func NewBackendConcurrencyLimiter(redisClient *redis.Client, backendName string, rates []int, timeout time.Duration, next octollm.Engine) (octollm.Engine, error) {
	composerConfig := &limiter.ConcurrencyLimiterConfig{
		Key:     fmt.Sprintf("backend:%s:concurrency_limiter", backendName),
		Rates:   rates,
		Timeout: timeout,
	}
	return limiter.NewConcurrencyLimiterEngine(redisClient, composerConfig, next)
}

func NewBackendRpmLimiter(redisClient *redis.Client, backendName string, rates []int, next octollm.Engine) (octollm.Engine, error) {
	composerConfig := &limiter.RpmLimiterConfig{
		Key:   fmt.Sprintf("backend:%s:rpm_limiter", backendName),
		Rates: rates,
	}
	return limiter.NewRpmLimiterEngine(redisClient, composerConfig, next)
}
