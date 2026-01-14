package moderator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	aliOpenapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	green20220302 "github.com/alibabacloud-go/green-20220302/v2/client"
	aliUtil "github.com/alibabacloud-go/tea-utils/v2/service"
	"github.com/alibabacloud-go/tea/tea"
	"github.com/sirupsen/logrus"
)

// Threshold 审核阈值配置
type Threshold struct {
	Label string  `json:"label"`
	Value float32 `json:"value"`
}

type AliModeratorService struct {
	client      *green20220302.Client
	runtime     *aliUtil.RuntimeOptions
	serviceCode string
	thresholds  map[string]Threshold
	maxRuneLen  int
	metrics     ModeratorMetrics
}

// 客户端缓存，避免重复创建相同的客户端
var clientCache = make(map[string]*green20220302.Client)
var clientCacheMu sync.RWMutex

// ClientCacheKey 生成客户端缓存的key
func ClientCacheKey(accessKeyId, endpoint, regionId string) string {
	return fmt.Sprintf("%s:%s:%s", accessKeyId, endpoint, regionId)
}

var _ TextModeratorService = (*AliModeratorService)(nil)

// NewAliModeratorService 创建阿里云审核服务
func NewAliModeratorService(accessKeyId, accessKeySecret, endpoint, regionId, serviceCode string, thresholds map[string]Threshold, maxRuneLen int, connectTimeout, readTimeout *int, metrics ModeratorMetrics) (*AliModeratorService, error) {
	client, err := getOrCreateAliyunClient(accessKeyId, accessKeySecret, endpoint, regionId, connectTimeout, readTimeout)
	if err != nil {
		return nil, fmt.Errorf("failed to create aliyun client: %w", err)
	}

	runtime := &aliUtil.RuntimeOptions{}

	// 设置默认阈值
	if thresholds == nil {
		thresholds = map[string]Threshold{
			"spam":       {Label: "spam", Value: 80.0},
			"ad":         {Label: "ad", Value: 80.0},
			"politics":   {Label: "politics", Value: 90.0},
			"terrorism":  {Label: "terrorism", Value: 90.0},
			"abuse":      {Label: "abuse", Value: 80.0},
			"porn":       {Label: "porn", Value: 90.0},
			"contraband": {Label: "contraband", Value: 80.0},
		}
	}

	// 设置默认长度限制
	if maxRuneLen <= 0 {
		maxRuneLen = 20000 // 阿里云文本审核PLUS的默认长度限制
	}

	return &AliModeratorService{
		client:      client,
		runtime:     runtime,
		serviceCode: serviceCode,
		thresholds:  thresholds,
		maxRuneLen:  maxRuneLen,
		metrics:     metrics,
	}, nil
}

func (s *AliModeratorService) Allow(ctx context.Context, text []rune) error {
	beginTime := time.Now()
	runeContentLength := len(text)

	// 记录内容长度
	if s.metrics != nil {
		s.metrics.RecordContentLength(SpamServiceNameAliyun, runeContentLength)
	}

	var result, status string
	defer func() {
		// 记录延迟和结果
		if s.metrics != nil {
			s.metrics.RecordLatency(SpamServiceNameAliyun, time.Since(beginTime))
			s.metrics.RecordResult(SpamServiceNameAliyun, result, status)
		}
	}()

	textStr := string(text)

	// 构建服务参数
	serviceParameters, err := json.Marshal(map[string]interface{}{
		"content": textStr,
	})
	if err != nil {
		logrus.WithContext(ctx).Errorf("[AliModeratorService.Allow] failed to marshal service parameters: %v", err)
		result, status = SpamLabelResultNil, SpamLabelRequestFailed
		return fmt.Errorf("failed to marshal service parameters: %w", err)
	}

	// 创建审核请求
	request := green20220302.TextModerationPlusRequest{
		Service:           tea.String(s.serviceCode),
		ServiceParameters: tea.String(string(serviceParameters)),
	}

	// 调用阿里云API
	apiResult, err := s.client.TextModerationPlusWithOptions(&request, s.runtime)
	if err != nil {
		logrus.WithContext(ctx).Errorf("[AliModeratorService.Allow] TextModerationPlusWithOptions error: %v", err)
		result, status = SpamLabelResultNil, SpamLabelRequestFailed
		return fmt.Errorf("aliyun moderation API call failed: %w", err)
	}

	// 检查HTTP状态码
	if *apiResult.StatusCode != http.StatusOK {
		logrus.WithContext(ctx).Warnf("[AliModeratorService.Allow] aliyun API returned error status: %d", *apiResult.StatusCode)
		result, status = SpamLabelResultNil, SpamLabelRequestFailed
		return fmt.Errorf("aliyun API returned error status: %d", *apiResult.StatusCode)
	}

	// 检查业务状态码
	body := apiResult.Body
	if *body.Code != http.StatusOK {
		logrus.WithContext(ctx).Warnf("[AliModeratorService.Allow] aliyun API returned error code: %d", *body.Code)
		result, status = SpamLabelResultNil, SpamLabelRequestFailed
		return fmt.Errorf("aliyun API returned error code: %d", *body.Code)
	}

	// 解析审核结果
	data := *body.Data
	for _, info := range data.Result {
		label := *info.Label

		// 跳过非标签结果
		if label == "nonLabel" {
			continue
		}

		// 获取阈值配置
		threshold, ok := s.thresholds[label]
		if !ok {
			logrus.WithContext(ctx).Warnf("[AliModeratorService.Allow] label %s not configured in thresholds, skipping", label)
			continue
		}

		// 检查是否超过阈值
		confidence := *info.Confidence
		if confidence > threshold.Value {
			logrus.WithContext(ctx).Infof("[AliModeratorService.Allow] content blocked, label: %s, confidence: %.2f, threshold: %.2f",
				label, confidence, threshold.Value)
			result, status = SpamLabelResultIsSpam, SpamLabelRequestSuccess
			return fmt.Errorf("content blocked by Ali moderator: %s (confidence: %.2f)", label, confidence)
		}
	}

	logrus.WithContext(ctx).Debugf("[AliModeratorService.Allow] content passed moderation")
	result, status = SpamLabelResultIsNotSpam, SpamLabelRequestSuccess
	return nil
}

func (s *AliModeratorService) MaxRuneLen() int {
	return s.maxRuneLen
}

// getOrCreateAliyunClient 获取或创建阿里云客户端（带缓存）
func getOrCreateAliyunClient(accessKeyId, accessKeySecret, endpoint, regionId string, connectTimeout, readTimeout *int) (*green20220302.Client, error) {
	cacheKey := ClientCacheKey(accessKeyId, endpoint, regionId)

	// 先尝试从缓存获取
	clientCacheMu.RLock()
	if client, exists := clientCache[cacheKey]; exists {
		clientCacheMu.RUnlock()
		return client, nil
	}
	clientCacheMu.RUnlock()

	// 缓存未命中，创建新客户端
	clientCacheMu.Lock()
	defer clientCacheMu.Unlock()

	// 再次检查（避免并发创建）
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

// createAliyunClient 创建阿里云客户端
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
