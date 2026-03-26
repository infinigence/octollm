package moderator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	aliOpenapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	green20220302 "github.com/alibabacloud-go/green-20220302/v2/client"
	aliUtil "github.com/alibabacloud-go/tea-utils/v2/service"
	"github.com/alibabacloud-go/tea/tea"
)

const (
	ModerationServiceNameAliyun = "Aliyun"
)

// Client cache to avoid creating duplicate clients
var clientCache = make(map[string]*green20220302.Client)
var clientCacheMu sync.RWMutex

// Moderation threshold configuration
type Threshold struct {
	Label string  `json:"label"`
	Value float32 `json:"value"`
}

// AliModeratorConfig configuration for Alibaba Cloud moderation service
type AliModeratorConfig struct {
	AccessKeyId     string
	AccessKeySecret string
	Endpoint        string
	RegionId        string
	ServiceCode     string
	Thresholds      map[string]Threshold
	MaxRuneLen      int
	ConnectTimeout  *int
	ReadTimeout     *int
	Metrics         ModeratorMetrics
}

// DefaultAliModeratorConfig returns default configuration for Alibaba Cloud moderation service
func DefaultAliModeratorConfig() *AliModeratorConfig {
	return &AliModeratorConfig{
		ServiceCode: "llm_response_moderation",
		Thresholds: map[string]Threshold{
			// Sexual content
			"pornographic_adult": {Label: "pornographic_adult", Value: 95.0},
			"sexual_terms":       {Label: "sexual_terms", Value: 95.0},
			"sexual_suggestive":  {Label: "sexual_suggestive", Value: 95.0},
			"sexual_prompts":     {Label: "sexual_prompts", Value: 95.0},
			// Political content
			"political_figure":  {Label: "political_figure", Value: 95.0},
			"political_entity":  {Label: "political_entity", Value: 95.0},
			"political_n":       {Label: "political_n", Value: 95.0},
			"political_p":       {Label: "political_p", Value: 95.0},
			"political_prompts": {Label: "political_prompts", Value: 95.0},
			"political_a":       {Label: "political_a", Value: 95.0},
			// Violent content
			"violent_extremists": {Label: "violent_extremists", Value: 95.0},
			"violent_prompts":    {Label: "violent_prompts", Value: 95.0},
			// Contraband content
			"contraband_drug":     {Label: "contraband_drug", Value: 95.0},
			"contraband_gambling": {Label: "contraband_gambling", Value: 95.0},
			"contraband_act":      {Label: "contraband_act", Value: 95.0},
			"contraband_entity":   {Label: "contraband_entity", Value: 95.0},
			// Inappropriate content
			"inappropriate_ethics":    {Label: "inappropriate_ethics", Value: 95.0},
			"inappropriate_profanity": {Label: "inappropriate_profanity", Value: 95.0},
			// Religious content
			"religion_b": {Label: "religion_b", Value: 95.0},
			"religion_t": {Label: "religion_t", Value: 95.0},
			"religion_c": {Label: "religion_c", Value: 95.0},
			"religion_i": {Label: "religion_i", Value: 95.0},
			"religion_h": {Label: "religion_h", Value: 95.0},
			// Promotion/advertising
			"pt_to_sites":       {Label: "pt_to_sites", Value: 95.0},
			"pt_by_recruitment": {Label: "pt_by_recruitment", Value: 95.0},
			"pt_to_contact":     {Label: "pt_to_contact", Value: 95.0},
			// Custom dictionary
			"customized": {Label: "customized", Value: 95.0},
		},
		MaxRuneLen: 20000, // Default length limit for Alibaba Cloud Text Moderation PLUS
	}
}

type AliModeratorService struct {
	client      *green20220302.Client
	runtime     *aliUtil.RuntimeOptions
	serviceCode string
	thresholds  map[string]Threshold
	maxRuneLen  int
	metrics     ModeratorMetrics
}

// ClientCacheKey generates the cache key for clients
func ClientCacheKey(accessKeyId, endpoint, regionId string) string {
	return fmt.Sprintf("%s:%s:%s", accessKeyId, endpoint, regionId)
}

var _ TextModeratorService = (*AliModeratorService)(nil)

// NewAliModeratorService creates Alibaba Cloud moderation service with configuration
func NewAliModeratorService(config *AliModeratorConfig) (*AliModeratorService, error) {
	if config == nil {
		config = DefaultAliModeratorConfig()
	}

	client, err := getOrCreateAliyunClient(config.AccessKeyId, config.AccessKeySecret, config.Endpoint, config.RegionId, config.ConnectTimeout, config.ReadTimeout)
	if err != nil {
		return nil, fmt.Errorf("failed to create aliyun client: %w", err)
	}

	runtime := &aliUtil.RuntimeOptions{}

	// Set default thresholds if not provided
	thresholds := config.Thresholds
	if thresholds == nil {
		thresholds = DefaultAliModeratorConfig().Thresholds
	}

	// Set default length limit if not provided
	maxRuneLen := config.MaxRuneLen
	if maxRuneLen <= 0 {
		maxRuneLen = DefaultAliModeratorConfig().MaxRuneLen
	}

	return &AliModeratorService{
		client:      client,
		runtime:     runtime,
		serviceCode: config.ServiceCode,
		thresholds:  thresholds,
		maxRuneLen:  maxRuneLen,
		metrics:     config.Metrics,
	}, nil
}

func (s *AliModeratorService) Allow(ctx context.Context, text []rune) error {
	beginTime := time.Now()
	runeContentLength := len(text)

	// Record content length
	if s.metrics != nil {
		s.metrics.RecordContentLength(ModerationServiceNameAliyun, runeContentLength)
	}

	var result, status string
	defer func() {
		// Record latency and results
		if s.metrics != nil {
			s.metrics.RecordLatency(ModerationServiceNameAliyun, time.Since(beginTime))
			s.metrics.RecordResult(ModerationServiceNameAliyun, result, status)
		}
	}()

	textStr := string(text)

	// Build service parameters
	serviceParameters, err := json.Marshal(map[string]interface{}{
		"content": textStr,
	})
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("[AliModeratorService.Allow] failed to marshal service parameters: %v", err))
		result, status = ModerationResultNil, ModerationRequestFailed
		return fmt.Errorf("failed to marshal service parameters: %w", err)
	}

	// Create moderation request
	request := green20220302.TextModerationPlusRequest{
		Service:           tea.String(s.serviceCode),
		ServiceParameters: tea.String(string(serviceParameters)),
	}

	// Call Alibaba Cloud API
	apiResult, err := s.client.TextModerationPlusWithOptions(&request, s.runtime)
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("[AliModeratorService.Allow] TextModerationPlusWithOptions error: %v", err))
		result, status = ModerationResultNil, ModerationRequestFailed
		return fmt.Errorf("aliyun moderation API call failed: %w", err)
	}

	// Check HTTP status code
	if *apiResult.StatusCode != http.StatusOK {
		slog.WarnContext(ctx, fmt.Sprintf("[AliModeratorService.Allow] aliyun API returned error status: %d, serviceParameters: %s", *apiResult.StatusCode, string(serviceParameters)))
		result, status = ModerationResultNil, ModerationRequestFailed
		return fmt.Errorf("aliyun API returned error status: %d", *apiResult.StatusCode)
	}

	// Check business status code
	body := apiResult.Body
	if *body.Code != http.StatusOK {
		var msg string
		if body.Message != nil {
			msg = *body.Message
		}
		slog.WarnContext(ctx, fmt.Sprintf("[AliModeratorService.Allow] aliyun API returned error code: %d, message: %s, serviceParameters: %s", *body.Code, msg, string(serviceParameters)))
		result, status = ModerationResultNil, ModerationRequestFailed
		return fmt.Errorf("aliyun API returned error code: %d", *body.Code)
	}

	// Parse moderation result
	data := *body.Data
	for _, info := range data.Result {
		label := *info.Label

		// Skip non-label results
		if label == "nonLabel" {
			continue
		}

		// Get threshold configuration
		threshold, ok := s.thresholds[label]
		if !ok {
			slog.WarnContext(ctx, fmt.Sprintf("[AliModeratorService.Allow] label %s not configured in thresholds, skipping", label))
			continue
		}

		// Check if threshold is exceeded
		confidence := *info.Confidence
		if confidence > threshold.Value {
			slog.InfoContext(ctx, fmt.Sprintf("[AliModeratorService.Allow] content blocked, label: %s, confidence: %.2f, threshold: %.2f",
				label, confidence, threshold.Value))
			result, status = ModerationResultBlocked, ModerationRequestSuccess
			return fmt.Errorf("content blocked by Ali moderator: %s (confidence: %.2f)", label, confidence)
		}
	}

	slog.DebugContext(ctx, fmt.Sprintf("[AliModeratorService.Allow] content passed moderation"))
	result, status = ModerationResultAllowed, ModerationRequestSuccess
	return nil
}

func (s *AliModeratorService) MaxRuneLen() int {
	return s.maxRuneLen
}

// getOrCreateAliyunClient gets or creates an Alibaba Cloud client (with caching)
func getOrCreateAliyunClient(accessKeyId, accessKeySecret, endpoint, regionId string, connectTimeout, readTimeout *int) (*green20220302.Client, error) {
	cacheKey := ClientCacheKey(accessKeyId, endpoint, regionId)

	// Try to get from cache first
	clientCacheMu.RLock()
	if client, exists := clientCache[cacheKey]; exists {
		clientCacheMu.RUnlock()
		return client, nil
	}
	clientCacheMu.RUnlock()

	// Cache miss, create new client
	clientCacheMu.Lock()
	defer clientCacheMu.Unlock()

	// Double-check (to avoid concurrent creation)
	if client, exists := clientCache[cacheKey]; exists {
		return client, nil
	}

	client, err := createAliyunClient(accessKeyId, accessKeySecret, endpoint, regionId, connectTimeout, readTimeout)
	if err != nil {
		return nil, err
	}

	clientCache[cacheKey] = client
	return client, nil
}

// createAliyunClient creates an Alibaba Cloud client
func createAliyunClient(accessKeyId, accessKeySecret, endpoint, regionId string, connectTimeout, readTimeout *int) (*green20220302.Client, error) {
	config := &aliOpenapi.Config{
		AccessKeyId:     tea.String(accessKeyId),
		AccessKeySecret: tea.String(accessKeySecret),
		Endpoint:        tea.String(endpoint),
	}

	if regionId != "" {
		config.RegionId = tea.String(regionId)
	}

	if connectTimeout != nil {
		config.ConnectTimeout = tea.Int(*connectTimeout)
	}

	if readTimeout != nil {
		config.ReadTimeout = tea.Int(*readTimeout)
	}

	client, err := green20220302.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create aliyun green client: %w", err)
	}

	return client, nil
}
