package repeat_detector

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/engines/moderator"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
)

// MockEngine 用于测试的 mock engine
type mockEngine struct {
	response *octollm.Response
	err      error
}

func (m *mockEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	return m.response, m.err
}

// 辅助函数：创建使用 TextModeratorEngine 的重复检测器
func newRepeatDetectorEngine(config *RepeatDetectorConfig, modelName string, svcName string, next octollm.Engine) *moderator.TextModeratorEngine {
	service := NewRepeatDetectorService(config, modelName, svcName)
	return &moderator.TextModeratorEngine{
		ModeratorService:     service,
		TextModeratorAdapter: moderator.NewUniversalAdapter(), // 使用通用 adapter
		ModerateInput:        false,
		ModerateOutput:       true,
		ModerateStreamEvery:  10,
		Next:                 next,
	}
}

func TestRepeatDetector_NonStream_WithRepetition(t *testing.T) {
	// 创建一个包含重复内容的响应
	repeatedText := strings.Repeat("这是一个测试文本。", 5) // 重复 5 次
	resp := &openai.ChatCompletionResponse{
		ID:    "test-123",
		Model: "gpt-4",
		Choices: []*openai.ChatCompletionChoice{
			{
				Message: &openai.Message{
					Content: openai.MessageContentString(repeatedText),
				},
			},
		},
	}

	mockResp := &octollm.Response{
		Body: octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionResponse]{}),
	}
	mockResp.Body.SetParsed(resp)

	mockEngine := &mockEngine{response: mockResp}

	detector := newRepeatDetectorEngine(
		&RepeatDetectorConfig{
			MinRepeatLen:    5,
			MaxRepeatLen:    50,
			RepeatThreshold: 3,
			BlockOnDetect:   false, // 不拦截，只记录日志
		},
		"gpt-4",
		"default:1",
		mockEngine,
	)

	httpReq, _ := http.NewRequestWithContext(context.Background(), "POST", "http://localhost/v1/chat/completions", nil)
	httpReq.URL, _ = url.Parse("http://localhost/v1/chat/completions")
	req := octollm.NewRequest(httpReq, octollm.APIFormatChatCompletions)
	req.Body = octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionRequest]{})
	req.Body.SetParsed(&openai.ChatCompletionRequest{Model: "gpt-4"})

	result, err := detector.Process(req)
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.NotNil(t, result.Body)
}

func TestRepeatDetector_NonStream_NoRepetition(t *testing.T) {
	// 创建一个不包含重复的响应
	normalText := "这是一段正常的文本，没有任何重复的内容。"
	resp := &openai.ChatCompletionResponse{
		ID:    "test-456",
		Model: "gpt-4",
		Choices: []*openai.ChatCompletionChoice{
			{
				Message: &openai.Message{
					Content: openai.MessageContentString(normalText),
				},
			},
		},
	}

	mockResp := &octollm.Response{
		Body: octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionResponse]{}),
	}
	mockResp.Body.SetParsed(resp)

	mockEngine := &mockEngine{response: mockResp}

	detector := newRepeatDetectorEngine(
		&RepeatDetectorConfig{
			MinRepeatLen:    5,
			MaxRepeatLen:    50,
			RepeatThreshold: 3,
			BlockOnDetect:   false, // 不拦截，只记录日志
		},
		"gpt-4",
		"default:1",
		mockEngine,
	)

	httpReq, _ := http.NewRequestWithContext(context.Background(), "POST", "http://localhost/v1/chat/completions", nil)
	httpReq.URL, _ = url.Parse("http://localhost/v1/chat/completions")
	req := octollm.NewRequest(httpReq, octollm.APIFormatChatCompletions)
	req.Body = octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionRequest]{})
	req.Body.SetParsed(&openai.ChatCompletionRequest{Model: "gpt-4"})

	result, err := detector.Process(req)
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.NotNil(t, result.Body)
}

func TestRepeatDetector_Stream_WithRepetition(t *testing.T) {
	// 创建流式响应的 mock
	chunks := make(chan *octollm.StreamChunk, 10)

	// 模拟流式响应：发送重复的文本
	go func() {
		defer close(chunks)
		repeatedText := "重复"
		for i := 0; i < 60; i++ { // 发送 60 次
			chunk := &openai.ChatCompletionStreamChunk{
				ID:    "test-stream",
				Model: "gpt-4",
				Choices: []*openai.ChatCompletionStreamChoice{
					{
						Delta: &openai.Message{
							Content: openai.MessageContentString(repeatedText),
						},
					},
				},
			}
			body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionStreamChunk]{})
			body.SetParsed(chunk)
			chunks <- &octollm.StreamChunk{Body: body}
		}
	}()

	mockResp := &octollm.Response{
		Stream: octollm.NewStreamChan(chunks, func() {}),
	}

	mockEngine := &mockEngine{response: mockResp}

	detector := newRepeatDetectorEngine(
		&RepeatDetectorConfig{
			MinRepeatLen:    1,
			MaxRepeatLen:    5,
			RepeatThreshold: 50,
			BlockOnDetect:   false, // 不拦截，只记录日志
		},
		"gpt-4",
		"default:1",
		mockEngine,
	)

	httpReq, _ := http.NewRequestWithContext(context.Background(), "POST", "http://localhost/v1/chat/completions", nil)
	httpReq.URL, _ = url.Parse("http://localhost/v1/chat/completions")
	req := octollm.NewRequest(httpReq, octollm.APIFormatChatCompletions)
	req.Body = octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionRequest]{})
	req.Body.SetParsed(&openai.ChatCompletionRequest{Model: "gpt-4"})

	result, err := detector.Process(req)
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.NotNil(t, result.Stream)

	// 消费所有 chunks
	count := 0
	for range result.Stream.Chan() {
		count++
	}
	assert.Equal(t, 60, count)
}

func TestRepeatDetector_Stream_NoRepetition(t *testing.T) {
	// 创建流式响应的 mock
	chunks := make(chan *octollm.StreamChunk, 10)

	// 模拟流式响应：发送不重复的文本
	go func() {
		defer close(chunks)
		texts := []string{"这", "是", "一", "段", "正", "常", "的", "文", "本"}
		for _, text := range texts {
			chunk := &openai.ChatCompletionStreamChunk{
				ID:    "test-stream-normal",
				Model: "gpt-4",
				Choices: []*openai.ChatCompletionStreamChoice{
					{
						Delta: &openai.Message{
							Content: openai.MessageContentString(text),
						},
					},
				},
			}
			body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionStreamChunk]{})
			body.SetParsed(chunk)
			chunks <- &octollm.StreamChunk{Body: body}
		}
	}()

	mockResp := &octollm.Response{
		Stream: octollm.NewStreamChan(chunks, func() {}),
	}

	mockEngine := &mockEngine{response: mockResp}

	detector := newRepeatDetectorEngine(
		&RepeatDetectorConfig{
			MinRepeatLen:    1,
			MaxRepeatLen:    5,
			RepeatThreshold: 50,
			BlockOnDetect:   false, // 不拦截，只记录日志
		},
		"gpt-4",
		"default:1",
		mockEngine,
	)

	httpReq, _ := http.NewRequestWithContext(context.Background(), "POST", "http://localhost/v1/chat/completions", nil)
	httpReq.URL, _ = url.Parse("http://localhost/v1/chat/completions")
	req := octollm.NewRequest(httpReq, octollm.APIFormatChatCompletions)
	req.Body = octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionRequest]{})
	req.Body.SetParsed(&openai.ChatCompletionRequest{Model: "gpt-4"})

	result, err := detector.Process(req)
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.NotNil(t, result.Stream)

	// 消费所有 chunks
	count := 0
	for range result.Stream.Chan() {
		count++
	}
	assert.Equal(t, 9, count)
}

func TestRepeatDetector_EmptyResponse(t *testing.T) {
	resp := &openai.ChatCompletionResponse{
		ID:    "test-empty",
		Model: "gpt-4",
		Choices: []*openai.ChatCompletionChoice{
			{
				Message: &openai.Message{
					Content: openai.MessageContentString(""),
				},
			},
		},
	}

	mockResp := &octollm.Response{
		Body: octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionResponse]{}),
	}
	mockResp.Body.SetParsed(resp)

	mockEngine := &mockEngine{response: mockResp}

	detector := newRepeatDetectorEngine(
		&RepeatDetectorConfig{
			MinRepeatLen:    1,
			MaxRepeatLen:    5,
			RepeatThreshold: 50,
			BlockOnDetect:   false, // 不拦截，只记录日志
		},
		"gpt-4",
		"default:1",
		mockEngine,
	)

	httpReq, _ := http.NewRequestWithContext(context.Background(), "POST", "http://localhost/v1/chat/completions", nil)
	httpReq.URL, _ = url.Parse("http://localhost/v1/chat/completions")
	req := octollm.NewRequest(httpReq, octollm.APIFormatChatCompletions)
	req.Body = octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionRequest]{})
	req.Body.SetParsed(&openai.ChatCompletionRequest{Model: "gpt-4"})

	result, err := detector.Process(req)
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.NotNil(t, result.Body)
}

func TestRepeatDetector_BlockOnDetect(t *testing.T) {
	// 创建一个包含重复内容的响应
	repeatedText := strings.Repeat("ABC", 60) // 重复 60 次
	resp := &openai.ChatCompletionResponse{
		ID:    "test-block",
		Model: "gpt-4",
		Choices: []*openai.ChatCompletionChoice{
			{
				Message: &openai.Message{
					Content: openai.MessageContentString(repeatedText),
				},
			},
		},
	}

	mockResp := &octollm.Response{
		Body: octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionResponse]{}),
	}
	mockResp.Body.SetParsed(resp)

	mockEngine := &mockEngine{response: mockResp}

	// 测试拦截功能
	blockMessage := "内容被拦截：检测到重复"
	detector := &moderator.TextModeratorEngine{
		ModeratorService: NewRepeatDetectorService(
			&RepeatDetectorConfig{
				MinRepeatLen:    1,
				MaxRepeatLen:    5,
				RepeatThreshold: 50,
				BlockOnDetect:   true, // 启用拦截
				BlockMessage:    blockMessage,
			},
			"gpt-4",
			"default:1",
		),
		TextModeratorAdapter: moderator.NewUniversalAdapterWithConfig(
			blockMessage,                // 流式拦截消息
			blockMessage,                // 非流式拦截消息
			"repeated_content_detected", // finish_reason
		),
		ModerateInput:       false,
		ModerateOutput:      true,
		ModerateStreamEvery: 10,
		Next:                mockEngine,
	}

	httpReq, _ := http.NewRequestWithContext(context.Background(), "POST", "http://localhost/v1/chat/completions", nil)
	httpReq.URL, _ = url.Parse("http://localhost/v1/chat/completions")
	req := octollm.NewRequest(httpReq, octollm.APIFormatChatCompletions)
	req.Body = octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionRequest]{})
	req.Body.SetParsed(&openai.ChatCompletionRequest{Model: "gpt-4"})

	result, err := detector.Process(req)
	// 应该返回成功，但内容被替换为拦截消息
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Body)

	// 验证返回的是替换后的内容
	parsedResp, err := result.Body.Parsed()
	require.NoError(t, err)

	openaiResp, ok := parsedResp.(*openai.ChatCompletionResponse)
	require.True(t, ok)
	require.Len(t, openaiResp.Choices, 1)
	assert.Equal(t, blockMessage, openaiResp.Choices[0].Message.Content.ExtractText())
	assert.Equal(t, "repeated_content_detected", openaiResp.Choices[0].FinishReason)
}

func TestRepeatDetector_BlockOnDetect_Stream(t *testing.T) {
	// 创建流式响应的 mock
	chunks := make(chan *octollm.StreamChunk, 70)

	// 模拟流式响应：发送重复的文本
	go func() {
		defer close(chunks)
		repeatedText := "重复"
		for i := 0; i < 60; i++ { // 发送 60 次，会触发拦截
			chunk := &openai.ChatCompletionStreamChunk{
				ID:    "test-stream",
				Model: "gpt-4",
				Choices: []*openai.ChatCompletionStreamChoice{
					{
						Delta: &openai.Message{
							Content: openai.MessageContentString(repeatedText),
						},
					},
				},
			}
			body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionStreamChunk]{})
			body.SetParsed(chunk)
			chunks <- &octollm.StreamChunk{Body: body}
		}
	}()

	mockResp := &octollm.Response{
		Stream: octollm.NewStreamChan(chunks, func() {}),
	}

	mockEngine := &mockEngine{response: mockResp}

	blockMessage := "流式内容被拦截：检测到重复"
	detector := &moderator.TextModeratorEngine{
		ModeratorService: NewRepeatDetectorService(
			&RepeatDetectorConfig{
				MinRepeatLen:    1,
				MaxRepeatLen:    5,
				RepeatThreshold: 50,
				BlockOnDetect:   true, // 启用拦截
				BlockMessage:    blockMessage,
			},
			"gpt-4",
			"default:1",
		),
		TextModeratorAdapter: moderator.NewUniversalAdapterWithConfig(
			blockMessage,                // 流式拦截消息
			blockMessage,                // 非流式拦截消息
			"repeated_content_detected", // finish_reason
		),
		ModerateInput:       false,
		ModerateOutput:      true,
		ModerateStreamEvery: 10, // 每 10 个 chunk 检测一次
		Next:                mockEngine,
	}

	httpReq, _ := http.NewRequestWithContext(context.Background(), "POST", "http://localhost/v1/chat/completions", nil)
	httpReq.URL, _ = url.Parse("http://localhost/v1/chat/completions")
	req := octollm.NewRequest(httpReq, octollm.APIFormatChatCompletions)
	req.Body = octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionRequest]{})
	streamTrue := true
	req.Body.SetParsed(&openai.ChatCompletionRequest{Model: "gpt-4", Stream: &streamTrue})

	result, err := detector.Process(req)
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.NotNil(t, result.Stream)

	// 消费所有 chunks，并验证最后收到拦截消息
	var lastChunk *octollm.StreamChunk
	count := 0
	for chunk := range result.Stream.Chan() {
		lastChunk = chunk
		count++
	}

	// 应该在检测到重复后停止，并发送拦截消息
	t.Logf("Received %d chunks", count)

	// 验证最后一个 chunk 是拦截消息
	if lastChunk != nil {
		parsedChunk, err := lastChunk.Body.Parsed()
		require.NoError(t, err)

		streamChunk, ok := parsedChunk.(*openai.ChatCompletionStreamChunk)
		require.True(t, ok)

		if len(streamChunk.Choices) > 0 && streamChunk.Choices[0].Delta != nil {
			// 验证包含拦截消息
			content := streamChunk.Choices[0].Delta.Content.ExtractText()
			if content != "" {
				assert.Equal(t, blockMessage, content)
			}
		}
	}
}
