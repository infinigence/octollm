package moderator

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	ModerationResultBlocked = "blocked"
	ModerationResultAllowed = "allowed"
	ModerationResultNil     = "nil"

	ModerationRequestFailed  = "failed"
	ModerationRequestSuccess = "success"
)

// ModeratorMetrics defines the metrics interface for moderation services
type ModeratorMetrics interface {
	RecordLatency(serviceName string, duration time.Duration)
	RecordContentLength(serviceName string, length int)
	RecordResult(serviceName, result, status string)
}

var (
	// Request duration histogram
	moderatorRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "octollm_moderator_request_duration_seconds",
			Help:    "Time spent processing moderation requests",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"service_name"},
	)

	// Content length histogram
	moderatorContentLength = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "octollm_moderator_content_rune_length",
			Help:    "Length of content being moderated in runes",
			Buckets: []float64{5, 20, 50, 100, 200, 500, 1000},
		},
		[]string{"service_name"},
	)

	// Request counter
	moderatorRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "octollm_moderator_requests_total",
			Help: "Total number of moderation requests",
		},
		[]string{"service_name", "result", "status"},
	)

	// Duplication detection counter
	RepeatDetectionCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "octollm_repeat_detection_total",
			Help: "Total number of repeat pattern detections",
		},
		[]string{"backend_name", "model_name"},
	)
)

func init() {
	// Register metrics
	prometheus.MustRegister(moderatorRequestDuration)
	prometheus.MustRegister(moderatorContentLength)
	prometheus.MustRegister(moderatorRequestsTotal)
	prometheus.MustRegister(RepeatDetectionCounter)
}

// PrometheusModeratorMetrics implements Prometheus metrics
type PrometheusModeratorMetrics struct{}

// NewPrometheusModeratorMetrics creates a Prometheus metrics instance
func NewPrometheusModeratorMetrics() *PrometheusModeratorMetrics {
	return &PrometheusModeratorMetrics{}
}

// RecordLatency records the request latency
func (m *PrometheusModeratorMetrics) RecordLatency(serviceName string, duration time.Duration) {
	moderatorRequestDuration.WithLabelValues(serviceName).Observe(duration.Seconds())
}

// RecordContentLength records the content length
func (m *PrometheusModeratorMetrics) RecordContentLength(serviceName string, length int) {
	moderatorContentLength.WithLabelValues(serviceName).Observe(float64(length))
}

// RecordResult records the moderation result
func (m *PrometheusModeratorMetrics) RecordResult(serviceName, result, status string) {
	moderatorRequestsTotal.WithLabelValues(serviceName, result, status).Inc()
}
