package moderator

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	v5 "github.com/yidun/yidun-golang-sdk/yidun/service/antispam/text"
	"github.com/yidun/yidun-golang-sdk/yidun/service/antispam/text/v5/check/sync/single"
)

const (
	NeteaseSuggestionRisk    = 2 // Risky
	NeteaseSuggestionSuspect = 1 // Suspect
	NeteaseSuggestionSafe    = 0 // Safe

	ModerationServiceNameNetease = "Netease"
)

// NeteaseModeratorConfig configuration for Netease moderation service
type NeteaseModeratorConfig struct {
	APIKey      string
	APISecret   string
	BusinessID  string
	CheckLabels []string
	MaxRuneLen  int
	Metrics     ModeratorMetrics
}

// DefaultNeteaseModeratorConfig returns default configuration for Netease moderation service
func DefaultNeteaseModeratorConfig() *NeteaseModeratorConfig {
	return &NeteaseModeratorConfig{
		MaxRuneLen: 10000, // Default length limit for Netease Yidun text moderation
	}
}

type NeteaseModeratorService struct {
	client      *v5.TextClient
	businessID  string
	apiKey      string
	apiSecret   string
	checkLabels []string
	maxRuneLen  int
	metrics     ModeratorMetrics
}

var _ TextModeratorService = (*NeteaseModeratorService)(nil)

// NewNeteaseModeratorService creates Netease moderation service with configuration
func NewNeteaseModeratorService(config *NeteaseModeratorConfig) *NeteaseModeratorService {
	if config == nil {
		config = DefaultNeteaseModeratorConfig()
	}

	// Set default length limit if not provided
	maxRuneLen := config.MaxRuneLen
	if maxRuneLen <= 0 {
		maxRuneLen = DefaultNeteaseModeratorConfig().MaxRuneLen
	}

	return &NeteaseModeratorService{
		apiKey:      config.APIKey,
		apiSecret:   config.APISecret,
		businessID:  config.BusinessID,
		checkLabels: config.CheckLabels,
		maxRuneLen:  maxRuneLen,
		metrics:     config.Metrics,
	}
}

func (s *NeteaseModeratorService) Allow(ctx context.Context, text []rune) error {
	beginTime := time.Now()
	runeContentLength := len(text)

	// Record content length
	if s.metrics != nil {
		s.metrics.RecordContentLength(ModerationServiceNameNetease, runeContentLength)
	}

	var result, status string
	defer func() {
		// Record latency and results
		if s.metrics != nil {
			s.metrics.RecordLatency(ModerationServiceNameNetease, time.Since(beginTime))
			s.metrics.RecordResult(ModerationServiceNameNetease, result, status)
		}
	}()

	textStr := string(text)

	// Lazy initialize client
	if s.client == nil {
		s.client = v5.NewTextClientWithAccessKey(s.apiKey, s.apiSecret)
	}

	// Create moderation request
	request := single.NewTextCheckRequest(s.businessID)

	// Generate request ID (using timestamp)
	requestID := fmt.Sprintf("octollm-%d", time.Now().UnixNano())
	request.SetDataID(requestID)
	request.SetContent(textStr)

	// Set check labels
	if len(s.checkLabels) > 0 {
		checkLabelsStr := strings.Join(s.checkLabels, ",")
		request.SetCheckLabels(checkLabelsStr)
	}

	// Call Netease moderation API
	apiResult, err := s.client.SyncCheckText(request)
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("[NeteaseModeratorService.Allow] SyncCheckText error: %v", err))
		result, status = ModerationResultNil, ModerationRequestFailed
		return fmt.Errorf("netease moderation API call failed: %w", err)
	}

	// Check HTTP status code
	if apiResult.Code != http.StatusOK {
		slog.WarnContext(ctx, fmt.Sprintf("[NeteaseModeratorService.Allow] netease API returned error code: %d, msg: %s", apiResult.Code, apiResult.Msg))
		result, status = ModerationResultNil, ModerationRequestFailed
		return fmt.Errorf("netease API returned error code: %d", apiResult.Code)
	}

	if apiResult.Result != nil && apiResult.Result.Antispam != nil && apiResult.Result.Antispam.Suggestion != nil {
		suggestion := *apiResult.Result.Antispam.Suggestion

		// Block risky content
		if suggestion == NeteaseSuggestionRisk {
			slog.InfoContext(ctx, fmt.Sprintf("[NeteaseModeratorService.Allow] content blocked by Netease moderator, suggestion: %d", suggestion))
			result, status = ModerationResultBlocked, ModerationRequestSuccess
			return fmt.Errorf("content blocked by Netease moderator: risk content detected")
		}

		// Allow content to pass in other cases
		result, status = ModerationResultAllowed, ModerationRequestSuccess
		return nil
	}

	// Log warning but allow content if no moderation result
	slog.WarnContext(ctx, fmt.Sprintf("[NeteaseModeratorService.Allow] no moderation result from Netease API"))
	result, status = ModerationResultAllowed, ModerationRequestSuccess
	return nil
}

func (s *NeteaseModeratorService) MaxRuneLen() int {
	return s.maxRuneLen
}
