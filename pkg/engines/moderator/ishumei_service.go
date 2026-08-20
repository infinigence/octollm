package moderator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

const (
	// ModerationServiceNameIshumei 数美文本审核服务名，用于 metrics 打标与加权路由。
	ModerationServiceNameIshumei = "Ishumei"

	// ishumeiCodeSuccess 数美 V4 接口「成功」业务返回码，非 1100 视为 API 失败。
	ishumeiCodeSuccess = 1100

	// 数美 riskLevel 处置建议取值。
	ishumeiRiskLevelPass   = "PASS"   // 正常，放行
	ishumeiRiskLevelReview = "REVIEW" // 可疑，建议人工审核
	ishumeiRiskLevelReject = "REJECT" // 违规，拦截
)

// IshumeiModeratorConfig 数美「智能文本识别」V4 同步接口配置。
// 参见 https://help.ishumei.com/docs/tj/text/versionV4/sync/developDoc/
type IshumeiModeratorConfig struct {
	AccessKey  string        // 接口认证密钥，由数美提供
	AppID      string        // 应用标识，联系数美开通
	EventID    string        // 事件标识，联系数美开通（如 input/output）
	Type       string        // 检测的风险类型，默认 TEXTRISK（常规风险检测）
	APIURL     string        // 请求地址，如 http://api-text-bj.fengkongcloud.com/text/v4
	MaxRuneLen int           // 单次送审最大 rune 数，<=0 用默认 10000（数美上限）
	Timeout    time.Duration // HTTP 客户端超时，<=0 用默认 3s
	// BlockReview 为 true 时，REVIEW（可疑）也拦截；默认 false（仅 REJECT 拦截，
	// 与 netease「可疑不拦」口径一致）。
	BlockReview bool
	Metrics     ModeratorMetrics
}

// DefaultIshumeiModeratorConfig returns default configuration for Ishumei moderation service.
func DefaultIshumeiModeratorConfig() *IshumeiModeratorConfig {
	return &IshumeiModeratorConfig{
		Type:       "TEXTRISK",
		MaxRuneLen: 10000, // 数美单次请求文本上限 1 万字
		Timeout:    3 * time.Second,
	}
}

// IshumeiModeratorService implements TextModeratorService against the Ishumei V4 sync API.
type IshumeiModeratorService struct {
	accessKey   string
	appID       string
	eventID     string
	typ         string
	apiURL      string
	maxRuneLen  int
	blockReview bool
	client      *http.Client
	metrics     ModeratorMetrics
}

var _ TextModeratorService = (*IshumeiModeratorService)(nil)

// NewIshumeiModeratorService creates an Ishumei moderation service with configuration.
func NewIshumeiModeratorService(config *IshumeiModeratorConfig) *IshumeiModeratorService {
	if config == nil {
		config = DefaultIshumeiModeratorConfig()
	}

	maxRuneLen := config.MaxRuneLen
	if maxRuneLen <= 0 {
		maxRuneLen = DefaultIshumeiModeratorConfig().MaxRuneLen
	}
	typ := config.Type
	if typ == "" {
		typ = DefaultIshumeiModeratorConfig().Type
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = DefaultIshumeiModeratorConfig().Timeout
	}

	return &IshumeiModeratorService{
		accessKey:   config.AccessKey,
		appID:       config.AppID,
		eventID:     config.EventID,
		typ:         typ,
		apiURL:      config.APIURL,
		maxRuneLen:  maxRuneLen,
		blockReview: config.BlockReview,
		client:      &http.Client{Timeout: timeout},
		metrics:     config.Metrics,
	}
}

// ishumeiRequest 数美 V4 文本检测请求体（字段名与数美文档一致）。
type ishumeiRequest struct {
	AccessKey string             `json:"accessKey"`
	AppID     string             `json:"appId"`
	EventID   string             `json:"eventId"`
	Type      string             `json:"type"`
	Data      ishumeiRequestData `json:"data"`
}

type ishumeiRequestData struct {
	Text    string `json:"text"`
	TokenID string `json:"tokenId"`
}

// ishumeiResponse 数美 V4 文本检测响应体（只取判定所需字段）。
type ishumeiResponse struct {
	Code            int    `json:"code"`
	Message         string `json:"message"`
	RequestID       string `json:"requestId"`
	RiskLevel       string `json:"riskLevel"`
	RiskLabel1      string `json:"riskLabel1"`
	RiskLabel2      string `json:"riskLabel2"`
	RiskLabel3      string `json:"riskLabel3"`
	RiskDescription string `json:"riskDescription"`
}

func (s *IshumeiModeratorService) Allow(ctx context.Context, text []rune) error {
	beginTime := time.Now()
	runeContentLength := len(text)

	if s.metrics != nil {
		s.metrics.RecordContentLength(ModerationServiceNameIshumei, runeContentLength)
	}

	var result, status string
	defer func() {
		if s.metrics != nil {
			s.metrics.RecordLatency(ModerationServiceNameIshumei, time.Since(beginTime))
			s.metrics.RecordResult(ModerationServiceNameIshumei, result, status)
		}
	}()

	payload, err := json.Marshal(ishumeiRequest{
		AccessKey: s.accessKey,
		AppID:     s.appID,
		EventID:   s.eventID,
		Type:      s.typ,
		Data: ishumeiRequestData{
			Text:    string(text),
			TokenID: uuid.NewString(),
		},
	})
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("[IshumeiModeratorService.Allow] marshal request: %v", err))
		result, status = ModerationResultNil, ModerationRequestFailed
		return fmt.Errorf("%w: marshal request: %v", ErrModerationAPIFailed, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL, bytes.NewReader(payload))
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("[IshumeiModeratorService.Allow] build request: %v", err))
		result, status = ModerationResultNil, ModerationRequestFailed
		return fmt.Errorf("%w: build request: %v", ErrModerationAPIFailed, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(httpReq)
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("[IshumeiModeratorService.Allow] call ishumei API: %v", err))
		result, status = ModerationResultNil, ModerationRequestFailed
		return fmt.Errorf("%w: call ishumei API: %v", ErrModerationAPIFailed, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.WarnContext(ctx, fmt.Sprintf("[IshumeiModeratorService.Allow] ishumei API returned status: %d", resp.StatusCode))
		result, status = ModerationResultNil, ModerationRequestFailed
		return fmt.Errorf("%w: ishumei API returned status: %d", ErrModerationAPIFailed, resp.StatusCode)
	}

	var ishumeiResp ishumeiResponse
	if err := json.NewDecoder(resp.Body).Decode(&ishumeiResp); err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("[IshumeiModeratorService.Allow] decode response: %v", err))
		result, status = ModerationResultNil, ModerationRequestFailed
		return fmt.Errorf("%w: decode response: %v", ErrModerationAPIFailed, err)
	}

	if ishumeiResp.Code != ishumeiCodeSuccess {
		slog.WarnContext(ctx, fmt.Sprintf("[IshumeiModeratorService.Allow] ishumei API returned code: %d, message: %s", ishumeiResp.Code, ishumeiResp.Message))
		result, status = ModerationResultNil, ModerationRequestFailed
		return fmt.Errorf("%w: ishumei API returned code: %d, message: %s", ErrModerationAPIFailed, ishumeiResp.Code, ishumeiResp.Message)
	}

	block := ishumeiResp.RiskLevel == ishumeiRiskLevelReject ||
		(ishumeiResp.RiskLevel == ishumeiRiskLevelReview && s.blockReview)
	if block {
		slog.InfoContext(ctx, fmt.Sprintf("[IshumeiModeratorService.Allow] content blocked, riskLevel: %s, riskLabel1: %s, riskLabel2: %s, riskLabel3: %s",
			ishumeiResp.RiskLevel, ishumeiResp.RiskLabel1, ishumeiResp.RiskLabel2, ishumeiResp.RiskLabel3))
		result, status = ModerationResultBlocked, ModerationRequestSuccess
		return fmt.Errorf("%w: Ishumei riskLevel=%s label1=%s label2=%s label3=%s",
			ErrModerationBlocked, ishumeiResp.RiskLevel, ishumeiResp.RiskLabel1, ishumeiResp.RiskLabel2, ishumeiResp.RiskLabel3)
	}

	result, status = ModerationResultAllowed, ModerationRequestSuccess
	return nil
}

func (s *IshumeiModeratorService) MaxRuneLen() int {
	return s.maxRuneLen
}
