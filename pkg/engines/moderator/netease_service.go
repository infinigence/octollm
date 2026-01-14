package moderator

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	v5 "github.com/yidun/yidun-golang-sdk/yidun/service/antispam/text"
	"github.com/yidun/yidun-golang-sdk/yidun/service/antispam/text/v5/check/sync/single"
)

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

func NewNeteaseModeratorService(apiKey, apiSecret, businessID string, checkLabels []string, maxRuneLen int, metrics ModeratorMetrics) *NeteaseModeratorService {
	// 设置默认长度限制
	if maxRuneLen <= 0 {
		maxRuneLen = 10000 // 网易易盾文本审核的默认长度限制
	}

	return &NeteaseModeratorService{
		apiKey:      apiKey,
		apiSecret:   apiSecret,
		businessID:  businessID,
		checkLabels: checkLabels,
		maxRuneLen:  maxRuneLen,
		metrics:     metrics,
	}
}

func (s *NeteaseModeratorService) Allow(ctx context.Context, text []rune) error {
	beginTime := time.Now()
	runeContentLength := len(text)

	// 记录内容长度
	if s.metrics != nil {
		s.metrics.RecordContentLength(SpamServiceNameNetease, runeContentLength)
	}

	var result, status string
	defer func() {
		// 记录延迟和结果
		if s.metrics != nil {
			s.metrics.RecordLatency(SpamServiceNameNetease, time.Since(beginTime))
			s.metrics.RecordResult(SpamServiceNameNetease, result, status)
		}
	}()

	textStr := string(text)

	// 延迟初始化客户端
	if s.client == nil {
		s.client = v5.NewTextClientWithAccessKey(s.apiKey, s.apiSecret)
	}

	// 创建审核请求
	request := single.NewTextCheckRequest(s.businessID)

	// 生成请求ID（使用时间戳）
	requestID := fmt.Sprintf("octollm-%d", time.Now().UnixNano())
	request.SetDataID(requestID)
	request.SetContent(textStr)

	// 设置检查标签
	if len(s.checkLabels) > 0 {
		checkLabelsStr := strings.Join(s.checkLabels, ",")
		request.SetCheckLabels(checkLabelsStr)
	}

	// 调用网易审核API
	apiResult, err := s.client.SyncCheckText(request)
	if err != nil {
		logrus.WithContext(ctx).Errorf("[NeteaseModeratorService.Allow] SyncCheckText error: %v", err)
		result, status = SpamLabelResultNil, SpamLabelRequestFailed
		return fmt.Errorf("netease moderation API call failed: %w", err)
	}

	// 检查HTTP状态码
	if apiResult.Code != http.StatusOK {
		logrus.WithContext(ctx).Warnf("[NeteaseModeratorService.Allow] netease API returned error code: %d, msg: %s", apiResult.Code, apiResult.Msg)
		result, status = SpamLabelResultNil, SpamLabelRequestFailed
		return fmt.Errorf("netease API returned error code: %d", apiResult.Code)
	}

	if apiResult.Result != nil && apiResult.Result.Antispam != nil && apiResult.Result.Antispam.Suggestion != nil {
		suggestion := *apiResult.Result.Antispam.Suggestion

		// 如果是风险内容，拦截
		if suggestion == SpamNeteaseSuggestionRisk {
			logrus.WithContext(ctx).Infof("[NeteaseModeratorService.Allow] content blocked by Netease moderator, suggestion: %s", suggestion)
			result, status = SpamLabelResultIsSpam, SpamLabelRequestSuccess
			return fmt.Errorf("content blocked by Netease moderator: risk content detected")
		}

		// pass或其他情况允许通过
		logrus.WithContext(ctx).Debugf("[NeteaseModeratorService.Allow] content passed moderation, suggestion: %s", suggestion)
		result, status = SpamLabelResultIsNotSpam, SpamLabelRequestSuccess
		return nil
	}

	// 如果没有审核结果，记录警告但允许通过
	logrus.WithContext(ctx).Warnf("[NeteaseModeratorService.Allow] no moderation result from Netease API")
	result, status = SpamLabelResultIsNotSpam, SpamLabelRequestSuccess
	return nil
}

func (s *NeteaseModeratorService) MaxRuneLen() int {
	return s.maxRuneLen
}
