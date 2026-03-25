package repeat_detector

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/infinigence/octollm/pkg/engines/moderator"
)

type RepeatDetectorConfig struct {
	MinRepeatLen    int
	MaxRepeatLen    int
	RepeatThreshold int
	BlockOnDetect   bool
	BlockMessage    string
}

func DefaultRepeatDetectorConfig() *RepeatDetectorConfig {
	return &RepeatDetectorConfig{
		MinRepeatLen:    1,
		MaxRepeatLen:    5,
		RepeatThreshold: 50,
		BlockOnDetect:   false,
		BlockMessage:    "Repeated content detected, please adjust and try again. ",
	}
}

// RepeatDetectorService implement TextModeratorService interface
// used to integrate into TextModeratorEngine, reuse streaming processing logic
type RepeatDetectorService struct {
	config    *RepeatDetectorConfig
	modelName string
	svcName   string
}

var _ moderator.TextModeratorService = (*RepeatDetectorService)(nil)

// NewRepeatDetectorService create repeat detection service
func NewRepeatDetectorService(
	config *RepeatDetectorConfig,
	modelName string,
	svcName string,
) *RepeatDetectorService {
	if config == nil {
		config = DefaultRepeatDetectorConfig()
	}
	return &RepeatDetectorService{
		config:    config,
		modelName: modelName,
		svcName:   svcName,
	}
}

func (s *RepeatDetectorService) Allow(ctx context.Context, text []rune) error {
	startTime := time.Now()

	pattern, repeatCount, found := ExtractRepeatPattern(
		string(text),
		s.config.MinRepeatLen,
		s.config.MaxRepeatLen,
		s.config.RepeatThreshold,
	)

	detectionTime := time.Since(startTime)

	if found {
		s.logRepeatDetection(ctx, string(text), pattern, repeatCount, detectionTime)

		if s.config.BlockOnDetect {
			return fmt.Errorf("Content filter: pattern '%s' repeated %d times", pattern, repeatCount)
		}
	}

	return nil
}

// MaxRuneLen return the maximum detection text length
func (s *RepeatDetectorService) MaxRuneLen() int {
	return 2000
}

// record repeat detection result
func (s *RepeatDetectorService) logRepeatDetection(ctx context.Context, content, pattern string, repeatCount int, detectionTime time.Duration) {
	// truncate pattern for logging (avoid too long)
	patternPreview := pattern
	if len(patternPreview) > 100 {
		patternPreview = patternPreview[:100] + "..."
	}

	traceID := extractTraceID(ctx)

	slog.WarnContext(ctx, "[RepeatDetector] Repeated pattern detected",
		"model", s.modelName,
		"svc_name", s.svcName,
		"trace_id", traceID,
		"detection_time", detectionTime,
		"content_length", len([]rune(content)),
		"repeat_pattern", patternPreview,
		"repeat_count", repeatCount,
		"pattern_length", len(pattern))

	// record metrics to Grafana
	moderator.RepeatDetectionCounter.WithLabelValues(s.svcName, s.modelName).Inc()
}

// extract trace_id from context
func extractTraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}

	if traceID, ok := ctx.Value("trace_id").(string); ok {
		return traceID
	}
	return ""
}
