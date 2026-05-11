package mock

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
)

type MockEndpoint struct {
	OutputString string
	TTFT         time.Duration
	TPOT         time.Duration
}

var _ octollm.Engine = (*MockEndpoint)(nil)

func NewOpenAIWithFixedOutput(outputString string, ttft, tpot time.Duration) *MockEndpoint {
	return &MockEndpoint{
		OutputString: outputString,
		TTFT:         ttft,
		TPOT:         tpot,
	}
}

// Mock behavior is controlled via the first line of the first message in URL
// query-string format. Lines after the first newline are treated as normal
// message content. Supported parameters:
//
//	ttft       - Time to first token (Go duration, e.g. "2.3s", "30ms"). Default: MockEndpoint.TTFT.
//	tpot       - Time per output token (Go duration, e.g. "30ms"). Default: MockEndpoint.TPOT.
//	output_len - Number of output runes. The init OutputString is repeated to fill this length.
//	             Default: len(OutputString). If max_tokens < output_len, output is truncated to
//	             max_tokens and finish_reason becomes "length"; otherwise finish_reason is "stop".
//	             completion_tokens in usage equals the actual number of runes output.
//	input_len  - Number of prompt tokens. Default: total rune count of all messages
//	             (with the first-line control params stripped). Affects prompt_tokens in usage.
//	cached_len - Number of cached prompt tokens. Default: 0. Affects cached_tokens in usage.prompt_tokens_details.
//	http_status - HTTP status code to return. Default: 200. If set to a non-200 value, the request
//	              fails after sleeping for ttft duration.
//
// Example first message:
//
//	ttft=2.3s&tpot=30ms&output_len=200&input_len=100&cached_len=50
//	The rest of the message content goes here.
type mockParams struct {
	rawParams    string
	httpStatus   int
	ttft         time.Duration
	tpot         time.Duration
	output       string
	outputRunes  []rune
	finishReason string
	usage        *openai.Usage
}

func (e *MockEndpoint) newMockParams(v *openai.ChatCompletionRequest) *mockParams {
	messages := v.Messages
	maxTokens := v.MaxTokens
	p := &mockParams{
		ttft:       e.TTFT,
		tpot:       e.TPOT,
		httpStatus: 200,
	}

	outputLen := len([]rune(e.OutputString))
	cachedLen := 0
	inputLen := 0

	totalRunes := 0
	if len(messages) > 0 {
		firstContent := messages[len(messages)-1].Content.ExtractText()
		lines := strings.SplitN(firstContent, "\n", 2)
		firstLine := strings.TrimSpace(lines[0])

		if strings.Contains(firstLine, "=") || strings.Contains(firstLine, "&") {
			p.rawParams = firstLine
			vals, err := url.ParseQuery(firstLine)
			if err == nil {
				if v := vals.Get("ttft"); v != "" {
					if d, err := time.ParseDuration(v); err == nil {
						p.ttft = d
					}
				}
				if v := vals.Get("tpot"); v != "" {
					if d, err := time.ParseDuration(v); err == nil {
						p.tpot = d
					}
				}
				if v := vals.Get("output_len"); v != "" {
					if n, err := strconv.Atoi(v); err == nil {
						outputLen = n
					}
				}
				if v := vals.Get("cached_len"); v != "" {
					if n, err := strconv.Atoi(v); err == nil {
						cachedLen = n
					}
				}
				if v := vals.Get("input_len"); v != "" {
					if n, err := strconv.Atoi(v); err == nil {
						inputLen = n
					}
				}
				if v := vals.Get("http_status"); v != "" {
					if n, err := strconv.Atoi(v); err == nil {
						p.httpStatus = n
					}
				}
			}
			if len(lines) > 1 {
				totalRunes += len([]rune(lines[1]))
			}
		} else {
			totalRunes += len([]rune(firstContent))
		}

		for _, m := range messages[1:] {
			totalRunes += len([]rune(m.Content.ExtractText()))
		}
	}

	if inputLen == 0 {
		inputLen = totalRunes
	}

	if outputLen == 0 {
		p.output = ""
		p.outputRunes = nil
		p.finishReason = "stop"
	} else {
		runes := []rune(e.OutputString)
		if len(runes) == 0 {
			runes = []rune(" ")
		}

		output := make([]rune, 0, outputLen)
		for len(output) < outputLen {
			remaining := outputLen - len(output)
			if remaining >= len(runes) {
				output = append(output, runes...)
			} else {
				output = append(output, runes[:remaining]...)
			}
		}

		p.finishReason = "stop"
		if maxTokens != nil && *maxTokens < outputLen {
			output = output[:*maxTokens]
			p.finishReason = "length"
		}

		p.output = string(output)
		p.outputRunes = output
	}

	p.usage = &openai.Usage{
		PromptTokens:     inputLen,
		CompletionTokens: len(p.outputRunes),
		TotalTokens:      inputLen + len(p.outputRunes),
	}
	if cachedLen > 0 {
		p.usage.PromptTokensDetails = &openai.PromptTokensDetails{
			CachedTokens: cachedLen,
		}
	}

	return p
}

func (p *mockParams) String() string {
	return p.rawParams
}

func (e *MockEndpoint) Process(req *octollm.Request) (*octollm.Response, error) {
	reqBody, err := req.Body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("failed to parse request body: %w", err)
	}

	switch v := reqBody.(type) {
	case *openai.ChatCompletionRequest:
		p := e.newMockParams(v)
		slog.InfoContext(req.Context(), "[mock-endpoint] params: "+p.String())
		if p.httpStatus != 200 {
			time.Sleep(p.ttft)
			return nil, &errutils.UpstreamRespError{
				StatusCode: p.httpStatus,
				Header: http.Header{
					"Content-Type":  {"application/json"},
					"X-Mock-Params": {p.String()},
				},
				Body: []byte(fmt.Sprintf(`{"error":{"message":"mock error","type":"mock_error","code":%d}}`, p.httpStatus)),
			}
		}
		if v.Stream != nil && *v.Stream {
			return e.openAIStreamResponse(req, v, p)
		}
		return e.openAINonStreamResponse(req, v, p)
	default:
		return nil, fmt.Errorf("unexpected request body type: %T", reqBody)
	}
}

func (e *MockEndpoint) openAINonStreamResponse(req *octollm.Request, v *openai.ChatCompletionRequest, p *mockParams) (*octollm.Response, error) {
	time.Sleep(p.ttft + p.tpot*time.Duration(len(p.outputRunes)))

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
					Content: openai.MessageContentString(p.output),
				},
				FinishReason: p.finishReason,
			},
		},
		Usage: p.usage,
	}
	body := octollm.NewBodyFromParsed(bodyVal, &octollm.JSONParser[openai.ChatCompletionResponse]{})
	hdr := http.Header{
		"Content-Type":  {"application/json"},
		"X-Mock-Params": {p.String()},
	}
	resp := octollm.NewNonStreamResponse(200, hdr, body)
	return resp, nil
}

func (e *MockEndpoint) openAIStreamResponse(req *octollm.Request, v *openai.ChatCompletionRequest, p *mockParams) (*octollm.Response, error) {
	ch := make(chan *octollm.StreamChunk)
	ctx, cancel := context.WithCancel(req.Context())

	octollm.SafeGo(req, func() {
		defer close(ch)
		time.Sleep(p.ttft)
		for _, c := range p.outputRunes {
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
			time.Sleep(p.tpot)
		}

		bodyVal := &openai.ChatCompletionStreamChunk{
			ID:      "mock-id",
			Object:  "chat.completion.chunk",
			Created: int(time.Now().Unix()),
			Model:   v.Model,
			Choices: []*openai.ChatCompletionStreamChoice{
				{
					Index:        0,
					FinishReason: p.finishReason,
				},
			},
			Usage: p.usage,
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
	hdr := http.Header{
		"Content-Type":  {"text/event-stream"},
		"X-Mock-Params": {p.String()},
	}
	resp := octollm.NewStreamResponse(200, hdr, streamChan)
	return resp, nil
}
