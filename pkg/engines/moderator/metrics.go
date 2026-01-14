package moderator

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ModeratorMetrics 定义审核服务的metrics接口
type ModeratorMetrics interface {
	RecordLatency(serviceName string, duration time.Duration)
	RecordContentLength(serviceName string, length int)
	RecordResult(serviceName, result, status string)
}

// ModeratorMetricsConfig 定义metrics配置
type ModeratorMetricsConfig struct {
	LatencyBuckets       []float64
	ContentLengthBuckets []float64
}

// DefaultModeratorMetricsConfig 返回默认的metrics配置
func DefaultModeratorMetricsConfig() *ModeratorMetricsConfig {
	return &ModeratorMetricsConfig{
		LatencyBuckets:       prometheus.DefBuckets,
		ContentLengthBuckets: []float64{10, 50, 100, 500, 1000, 5000, 10000, 20000},
	}
}

// validateBuckets 验证histogram bucket配置是否有效
func validateBuckets(buckets []float64) error {
	if len(buckets) == 0 {
		return fmt.Errorf("buckets cannot be empty")
	}

	for i, bucket := range buckets {
		if bucket < 0 {
			return fmt.Errorf("bucket at index %d must be non-negative, got %f", i, bucket)
		}
		if i > 0 && bucket <= buckets[i-1] {
			return fmt.Errorf("buckets must be strictly increasing, got %f after %f", bucket, buckets[i-1])
		}
	}

	return nil
}

// PrometheusModeratorMetrics Prometheus metrics实现
type PrometheusModeratorMetrics struct {
	Latency           *prometheus.HistogramVec
	ContentRuneLength *prometheus.HistogramVec
	ResultCounter     *prometheus.CounterVec
}

// NewPrometheusModeratorMetrics 创建Prometheus metrics实例
func NewPrometheusModeratorMetrics() *PrometheusModeratorMetrics {
	return NewPrometheusModeratorMetricsWithConfig(DefaultModeratorMetricsConfig())
}

// NewPrometheusModeratorMetricsWithConfig 使用自定义配置创建Prometheus metrics实例
func NewPrometheusModeratorMetricsWithConfig(config *ModeratorMetricsConfig) *PrometheusModeratorMetrics {
	if config == nil {
		config = DefaultModeratorMetricsConfig()
	}

	// 验证bucket配置
	if err := validateBuckets(config.LatencyBuckets); err != nil {
		panic(fmt.Sprintf("invalid latency buckets: %v", err))
	}
	if err := validateBuckets(config.ContentLengthBuckets); err != nil {
		panic(fmt.Sprintf("invalid content length buckets: %v", err))
	}

	return &PrometheusModeratorMetrics{
		Latency: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "octollm_moderator_request_duration_seconds",
				Help:    "Time spent processing moderation requests",
				Buckets: config.LatencyBuckets,
			},
			[]string{"service"},
		),
		ContentRuneLength: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "octollm_moderator_content_rune_length",
				Help:    "Length of content being moderated in runes",
				Buckets: config.ContentLengthBuckets,
			},
			[]string{"service"},
		),
		ResultCounter: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "octollm_moderator_requests_total",
				Help: "Total number of moderation requests",
			},
			[]string{"service", "result", "status"},
		),
	}
}

// RecordLatency 记录请求延迟
func (m *PrometheusModeratorMetrics) RecordLatency(serviceName string, duration time.Duration) {
	if m.Latency != nil {
		m.Latency.WithLabelValues(serviceName).Observe(duration.Seconds())
	}
}

// RecordContentLength 记录内容长度
func (m *PrometheusModeratorMetrics) RecordContentLength(serviceName string, length int) {
	if m.ContentRuneLength != nil {
		m.ContentRuneLength.WithLabelValues(serviceName).Observe(float64(length))
	}
}

// RecordResult 记录审核结果
func (m *PrometheusModeratorMetrics) RecordResult(serviceName, result, status string) {
	if m.ResultCounter != nil {
		m.ResultCounter.WithLabelValues(serviceName, result, status).Inc()
	}
}
