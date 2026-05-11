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
	"github.com/infinigence/octollm/pkg/types/anthropic"
)

type ClaudeMockEndpoint struct {
	OutputString string
	TTFT         time.Duration
	TPOT         time.Duration
}

var _ octollm.Engine = (*ClaudeMockEndpoint)(nil)

func NewClaudeWithFixedOutput(outputString string, ttft, tpot time.Duration) *ClaudeMockEndpoint {
	return &ClaudeMockEndpoint{
		OutputString: outputString,
		TTFT:         ttft,
		TPOT:         tpot,
	}
}

// See mockParams doc comment on the OpenAI MockEndpoint for parameter details.
// Claude uses stop_reason "end_turn" / "max_tokens" instead of "stop" / "length",
// and Usage uses InputTokens / OutputTokens / CacheReadInputTokens.
type claudeMockParams struct {
	rawParams    string
	httpStatus   int
	ttft         time.Duration
	tpot         time.Duration
	output       string
	outputRunes  []rune
	stopReason   string
	usage        *anthropic.Usage
}

func (e *ClaudeMockEndpoint) newClaudeMockParams(v *anthropic.ClaudeMessagesRequest) *claudeMockParams {
	p := &claudeMockParams{
		ttft:       e.TTFT,
		tpot:       e.TPOT,
		httpStatus: 200,
	}

	outputLen := len([]rune(e.OutputString))
	cachedLen := 0
	inputLen := 0

	totalRunes := 0
	if len(v.Messages) > 0 {
		lastMsg := v.Messages[len(v.Messages)-1]
		firstContent := extractClaudeText(lastMsg.Content)
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

		for _, m := range v.Messages[:len(v.Messages)-1] {
			totalRunes += len([]rune(extractClaudeText(m.Content)))
		}
	}

	if v.System != nil {
		switch s := v.System.(type) {
		case anthropic.SystemString:
			totalRunes += len([]rune(string(s)))
		case anthropic.SystemBlocks:
			for _, b := range s {
				totalRunes += len([]rune(b.Text))
			}
		}
	}

	if inputLen == 0 {
		inputLen = totalRunes
	}

	if outputLen == 0 {
		p.output = ""
		p.outputRunes = nil
		p.stopReason = "end_turn"
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

		p.stopReason = "end_turn"
		if int(v.MaxTokens) < outputLen {
			output = output[:v.MaxTokens]
			p.stopReason = "max_tokens"
		}

		p.output = string(output)
		p.outputRunes = output
	}

	p.usage = &anthropic.Usage{
		InputTokens:  int64(inputLen),
		OutputTokens: int64(len(p.outputRunes)),
	}
	if cachedLen > 0 {
		cached := int64(cachedLen)
		p.usage.CacheReadInputTokens = &cached
	}

	return p
}

func (p *claudeMockParams) String() string {
	return p.rawParams
}

func extractClaudeText(content []anthropic.MessageContent) string {
	var sb strings.Builder
	for _, c := range content {
		sb.WriteString(c.ExtractText())
	}
	return sb.String()
}

func (e *ClaudeMockEndpoint) Process(req *octollm.Request) (*octollm.Response, error) {
	reqBody, err := req.Body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("failed to parse request body: %w", err)
	}

	switch v := reqBody.(type) {
	case *anthropic.ClaudeMessagesRequest:
		p := e.newClaudeMockParams(v)
		slog.InfoContext(req.Context(), "[claude-mock-endpoint] params: "+p.String())
		if p.httpStatus != 200 {
			time.Sleep(p.ttft)
			return nil, &errutils.UpstreamRespError{
				StatusCode: p.httpStatus,
				Header: http.Header{
					"Content-Type":  {"application/json"},
					"X-Mock-Params": {p.String()},
				},
				Body: []byte(fmt.Sprintf(`{"type":"error","error":{"type":"api_error","message":"mock error (status %d)"}}`, p.httpStatus)),
			}
		}
		if v.Stream != nil && *v.Stream {
			return e.claudeStreamResponse(req, v, p)
		}
		return e.claudeNonStreamResponse(req, v, p)
	default:
		return nil, fmt.Errorf("unexpected request body type: %T", reqBody)
	}
}

func (e *ClaudeMockEndpoint) claudeNonStreamResponse(req *octollm.Request, v *anthropic.ClaudeMessagesRequest, p *claudeMockParams) (*octollm.Response, error) {
	time.Sleep(p.ttft + p.tpot*time.Duration(len(p.outputRunes)))

	text := p.output
	bodyVal := &anthropic.ClaudeMessagesResponse{
		ID:   "msg_mock-id",
		Type: "message",
		Role: "assistant",
		Content: []*anthropic.MessageContentBlock{
			{
				Type: "text",
				Text: &text,
			},
		},
		Model:      v.Model,
		StopReason: p.stopReason,
		Usage:      p.usage,
	}
	body := octollm.NewBodyFromParsed(bodyVal, &octollm.JSONParser[anthropic.ClaudeMessagesResponse]{})
	hdr := http.Header{
		"Content-Type":  {"application/json"},
		"X-Mock-Params": {p.String()},
	}
	resp := octollm.NewNonStreamResponse(200, hdr, body)
	return resp, nil
}

func (e *ClaudeMockEndpoint) claudeStreamResponse(req *octollm.Request, v *anthropic.ClaudeMessagesRequest, p *claudeMockParams) (*octollm.Response, error) {
	ch := make(chan *octollm.StreamChunk)
	ctx, cancel := context.WithCancel(req.Context())

	msgID := "msg_mock-id"

	octollm.SafeGo(req, func() {
		defer close(ch)

		startEvent := &anthropic.ClaudeMessagesStreamEvent{
			Type: "message_start",
			Message: &anthropic.ClaudeMessagesResponse{
				ID:    msgID,
				Type:  "message",
				Role:  "assistant",
				Model: v.Model,
				Usage: &anthropic.Usage{
					InputTokens:  p.usage.InputTokens,
					OutputTokens: 0,
				},
			},
		}
		select {
		case ch <- &octollm.StreamChunk{
			Body:     octollm.NewBodyFromParsed(startEvent, &octollm.JSONParser[anthropic.ClaudeMessagesStreamEvent]{}),
			Metadata: map[string]string{"event": "message_start"},
		}:
		case <-ctx.Done():
			return
		}

		time.Sleep(p.ttft)

		blockStart := &anthropic.ClaudeMessagesStreamEvent{
			Type:  "content_block_start",
			Index: intPtr(0),
			ContentBlock: &anthropic.MessageContentBlock{
				Type: "text",
				Text: strPtr(""),
			},
		}
		select {
		case ch <- &octollm.StreamChunk{
			Body:     octollm.NewBodyFromParsed(blockStart, &octollm.JSONParser[anthropic.ClaudeMessagesStreamEvent]{}),
			Metadata: map[string]string{"event": "content_block_start"},
		}:
		case <-ctx.Done():
			return
		}

		for _, c := range p.outputRunes {
			deltaText := string(c)
			deltaRaw := fmt.Sprintf(`{"type":"text_delta","text":%q}`, deltaText)
			deltaEvent := &anthropic.ClaudeMessagesStreamEvent{
				Type:     "content_block_delta",
				Index:    intPtr(0),
				DeltaRaw: []byte(deltaRaw),
			}
			select {
			case ch <- &octollm.StreamChunk{
				Body:     octollm.NewBodyFromParsed(deltaEvent, &octollm.JSONParser[anthropic.ClaudeMessagesStreamEvent]{}),
				Metadata: map[string]string{"event": "content_block_delta"},
			}:
			case <-ctx.Done():
				slog.InfoContext(ctx, fmt.Sprintf("[claude-mock-endpoint] context canceled during stream response: %v", ctx.Err()))
				return
			}
			time.Sleep(p.tpot)
		}

		blockStop := &anthropic.ClaudeMessagesStreamEvent{
			Type:  "content_block_stop",
			Index: intPtr(0),
		}
		select {
		case ch <- &octollm.StreamChunk{
			Body:     octollm.NewBodyFromParsed(blockStop, &octollm.JSONParser[anthropic.ClaudeMessagesStreamEvent]{}),
			Metadata: map[string]string{"event": "content_block_stop"},
		}:
		case <-ctx.Done():
			return
		}

		deltaStopReason := p.stopReason
		msgDelta := &anthropic.ClaudeMessagesStreamEvent{
			Type: "message_delta",
			DeltaRaw: fmt.Appendf(nil, `{"stop_reason":%q}`, deltaStopReason),
			Usage: &anthropic.Usage{
				OutputTokens: int64(len(p.outputRunes)),
			},
		}
		select {
		case ch <- &octollm.StreamChunk{
			Body:     octollm.NewBodyFromParsed(msgDelta, &octollm.JSONParser[anthropic.ClaudeMessagesStreamEvent]{}),
			Metadata: map[string]string{"event": "message_delta"},
		}:
		case <-ctx.Done():
			return
		}

		msgStop := &anthropic.ClaudeMessagesStreamEvent{
			Type: "message_stop",
		}
		select {
		case ch <- &octollm.StreamChunk{
			Body:     octollm.NewBodyFromParsed(msgStop, &octollm.JSONParser[anthropic.ClaudeMessagesStreamEvent]{}),
			Metadata: map[string]string{"event": "message_stop"},
		}:
		case <-ctx.Done():
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

func intPtr(i int) *int       { return &i }
func strPtr(s string) *string { return &s }
