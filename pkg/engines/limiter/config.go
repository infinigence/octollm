package limiter

import "time"

type contextKey string

const (
	ContextKeyDefaultPriority     contextKey = "default_priority_key"
	ContextKeyRpmPriority         contextKey = ContextKeyDefaultPriority
	ContextKeyConcurrencyPriority contextKey = ContextKeyDefaultPriority
)

const (
	ContextValuePrefixDefaultPriority     string = "default_priority_value_prefix"
	ContextValuePrefixRpmPriority         string = ContextValuePrefixDefaultPriority
	ContextValuePrefixConcurrencyPriority string = ContextValuePrefixDefaultPriority
)

// ConcurrencyMarkerConfig 并发染色配置（不依赖外部包）
type ConcurrencyMarkerConfig struct {
	// Rates: strictly increasing
	Rates    []int
	NeedMark bool // 是否需要在 context 中写入优先级，默认 false
	Key      string
	Timeout  time.Duration // 超时时间，默认 10 秒
}

// ConcurrencyLimiterConfig 并发限流配置（不依赖外部包）
type ConcurrencyLimiterConfig struct {
	// Rates: strictly decreasing
	Rates   []int
	Key     string
	Timeout time.Duration // 超时时间，默认 10 秒
}

// RpmMarkerConfig 请求速率染色配置
type RpmMarkerConfig struct {
	// Rates: strictly increasing
	Rates    []int
	NeedMark bool // 是否需要在 context 中写入优先级，默认 false
	Key      string
}

// RpmLimiterConfig 请求速率限流配置
type RpmLimiterConfig struct {
	// Rates: strictly decreasing
	Rates []int
	Key   string
}
