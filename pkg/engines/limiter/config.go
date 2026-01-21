package limiter

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
}

// ConcurrencyLimiterConfig 并发限流配置（不依赖外部包）
type ConcurrencyLimiterConfig struct {
	// Rates: strictly decreasing
	Rates []int
}

// RpmMarkerConfig 请求速率染色配置
type RpmMarkerConfig struct {
	// Rates: strictly increasing
	Rates    []int
	NeedMark bool // 是否需要在 context 中写入优先级，默认 false
}

// RpmLimiterConfig 请求速率限流配置
type RpmLimiterConfig struct {
	// Rates: strictly decreasing
	Rates []int
}
