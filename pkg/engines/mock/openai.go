package mock

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
)

type MockEndpoint struct {
	OutputString string //  the output string to return
	TTFT         time.Duration
	TPOT         time.Duration
}

var _ octollm.Engine = (*MockEndpoint)(nil)

func NewWithFixedOutput(outputString string, ttft, tpot time.Duration) *MockEndpoint {
	return &MockEndpoint{
		OutputString: outputString,
		TTFT:         ttft,
		TPOT:         tpot,
	}
}

func (e *MockEndpoint) Process(req *octollm.Request) (*octollm.Response, error) {
	reqBody, err := req.Body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("failed to parse request body: %w", err)
	}

	switch v := reqBody.(type) {
	case *openai.ChatCompletionRequest:
		if v.Stream != nil && *v.Stream {
			return e.openAIStreamResponse(req, v)
		}
		return e.openAINonStreamResponse(req, v)
	default:
		return nil, fmt.Errorf("unexpected request body type: %T", reqBody)
	}
}

func (e *MockEndpoint) openAINonStreamResponse(req *octollm.Request, v *openai.ChatCompletionRequest) (*octollm.Response, error) {
	rOutput := []rune(e.OutputString)
	finishReason := "stop"
	if v.MaxTokens != nil && len(rOutput) > *v.MaxTokens {
		rOutput = rOutput[:*v.MaxTokens]
		finishReason = "length"
	}
	time.Sleep(e.TTFT + e.TPOT*time.Duration(len(rOutput)))
	bodyVal := &openai.ChatCompletionResponse{
		ID:      "mock-id",
		Object:  "chat.completion",
		Created: int(time.Now().Unix()),
		Model:   v.Model,
		Choices: []*openai.ChatCompletionChoice{
			{
				Index: 0,
				Message: &openai.Message{
					Role:    "assistant",
					Content: openai.MessageContentString(string(rOutput)),
				},
				FinishReason: finishReason,
			},
		},
		Usage: &openai.Usage{
			PromptTokens:     1,
			CompletionTokens: 100,
			TotalTokens:      101,
		},
	}
	body := octollm.NewBodyFromParsed(bodyVal, &octollm.JSONParser[openai.ChatCompletionResponse]{})
	resp := octollm.NewNonStreamResponse(200, http.Header{"Content-Type": {"application/json"}}, body)
	return resp, nil
}

func (e *MockEndpoint) openAIStreamResponse(req *octollm.Request, v *openai.ChatCompletionRequest) (*octollm.Response, error) {
	rOutput := []rune(e.OutputString)
	finishReason := "stop"
	if v.MaxTokens != nil && len(rOutput) > *v.MaxTokens {
		rOutput = rOutput[:*v.MaxTokens]
		finishReason = "length"
	}

	ch := make(chan *octollm.StreamChunk)
	ctx, cancel := context.WithCancel(req.Context())

	octollm.SafeGo(req, func() {
		defer close(ch)
		time.Sleep(e.TTFT)
		for _, c := range rOutput {
			bodyVal := &openai.ChatCompletionStreamChunk{
				ID:      "mock-id",
				Object:  "chat.completion.chunk",
				Created: int(time.Now().Unix()),
				Model:   v.Model,
				Choices: []*openai.ChatCompletionStreamChoice{
					{
						Index: 0,
						Delta: &openai.Message{
							Role:    "assistant",
							Content: openai.MessageContentString(string(c)),
						},
					},
				},
			}
			select {
			case ch <- &octollm.StreamChunk{
				Body: octollm.NewBodyFromParsed(bodyVal, &octollm.JSONParser[openai.ChatCompletionStreamChunk]{}),
			}:
			case <-ctx.Done():
				slog.InfoContext(ctx, fmt.Sprintf("[http-endpoint] context canceled during stream response: %v", ctx.Err()))
				return
			}
			time.Sleep(e.TPOT)
		}
		bodyVal := &openai.ChatCompletionStreamChunk{
			ID:      "mock-id",
			Object:  "chat.completion.chunk",
			Created: int(time.Now().Unix()),
			Model:   v.Model,
			Choices: []*openai.ChatCompletionStreamChoice{
				{
					Index:        0,
					FinishReason: finishReason,
				},
			},
			Usage: &openai.Usage{
				PromptTokens:     1,
				CompletionTokens: 100,
				TotalTokens:      101,
			},
		}
		select {
		case ch <- &octollm.StreamChunk{
			Body: octollm.NewBodyFromParsed(bodyVal, &octollm.JSONParser[openai.ChatCompletionStreamChunk]{}),
		}:
		case <-ctx.Done():
			slog.InfoContext(ctx, fmt.Sprintf("[http-endpoint] context canceled during stream response: %v", ctx.Err()))
			return
		}

		select {
		case ch <- &octollm.StreamChunk{
			Body: octollm.NewBodyFromBytes([]byte("[DONE]"), &octollm.JSONParser[openai.ChatCompletionStreamChunk]{}),
		}:
		case <-ctx.Done():
			slog.InfoContext(ctx, fmt.Sprintf("[http-endpoint] context canceled during stream response: %v", ctx.Err()))
			return
		}
	})

	streamChan := octollm.NewStreamChan(ch, cancel)
	resp := octollm.NewStreamResponse(200, http.Header{"Content-Type": {"text/event-stream"}}, streamChan)
	return resp, nil
}
