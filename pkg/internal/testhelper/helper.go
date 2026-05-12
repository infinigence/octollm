package testhelper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/infinigence/octollm/pkg/exprenv"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
)

type reqOptions struct {
	ctx        context.Context
	HttpMethod string
	URL        string
	Body       io.Reader
	ReqParser  octollm.Parser

	recvHeader http.Header
	features   map[string]exprenv.FeatureExtractor
}

type reqOptFunc func(opts *reqOptions)

func defaultReqOpts() *reqOptions {
	stream := false
	req := openai.ChatCompletionRequest{
		Model: "glm-4.7",
		Messages: []*openai.Message{
			{
				Role:    "user",
				Content: openai.MessageContentString("who are you?"),
			},
		},
		Stream: &stream,
	}
	bodyJSON, _ := json.Marshal(req)
	return &reqOptions{
		HttpMethod: http.MethodPost,
		URL:        "http://localhost:8000/v1/chat/completions",
		ReqParser:  &octollm.JSONParser[openai.ChatCompletionRequest]{},
		Body:       bytes.NewReader(bodyJSON),
	}
}

func CreateTestRequest(opts ...reqOptFunc) *octollm.Request {
	o := defaultReqOpts()
	for _, opt := range opts {
		opt(o)
	}

	var r *http.Request
	if o.ctx == nil {
		r, _ = http.NewRequest(o.HttpMethod, o.URL, o.Body)
	} else {
		r, _ = http.NewRequestWithContext(o.ctx, o.HttpMethod, o.URL, o.Body)
	}

	// inject recvHeader before creating the octollm Request so that the
	// exprenv's req reference already sees ContextKeyReceivedHeader
	if o.recvHeader != nil {
		rctx := context.WithValue(r.Context(), octollm.ContextKeyReceivedHeader, o.recvHeader)
		r = r.WithContext(rctx)
	}

	parser := o.ReqParser
	req := octollm.NewRequest(r, octollm.APIFormatChatCompletions)
	req.Body.SetParser(parser)

	// When custom features are set, register them so Get(req) will include them (no context storage).
	if o.features != nil {
		for name, extractor := range o.features {
			exprenv.RegisterDefaultExtractor(name, extractor)
		}
	}
	return req
}

func WithContext(ctx context.Context) reqOptFunc {
	return func(opts *reqOptions) {
		opts.ctx = ctx
	}
}

func WithBody(body any) reqOptFunc {
	return func(opts *reqOptions) {
		switch v := body.(type) {
		case io.Reader:
			opts.Body = v
		case string:
			opts.Body = bytes.NewReader([]byte(v))
		case []byte:
			opts.Body = bytes.NewReader(v)
		case anthropic.ClaudeMessagesRequest:
			opts.ReqParser = &octollm.JSONParser[anthropic.ClaudeMessagesRequest]{}
			buffer, _ := json.Marshal(v)
			opts.Body = bytes.NewReader(buffer)
		default:
			buffer, _ := json.Marshal(v)
			opts.Body = bytes.NewReader(buffer)
		}
	}
}

func WithRecvHeader(key, value string) reqOptFunc {
	return func(opts *reqOptions) {
		if opts.recvHeader == nil {
			opts.recvHeader = make(http.Header)
		}
		opts.recvHeader.Add(key, value)
	}
}

func WithFeature(name string, extractor exprenv.FeatureExtractor) reqOptFunc {
	return func(opts *reqOptions) {
		if opts.features == nil {
			opts.features = make(map[string]exprenv.FeatureExtractor)
		}
		opts.features[name] = extractor
	}
}

// CloseCountingEngine wraps an Engine and, via OnClose on the returned
// stream or body, counts how many times the upstream's closeFunc fires.
// t.Cleanup asserts the count is exactly one — any test using this
// constructor automatically gets a once-and-only-once invariant.
type CloseCountingEngine struct {
	t          *testing.T
	inner      octollm.Engine
	closeCount *int32
}

func NewCloseCountingEngine(t *testing.T, inner octollm.Engine) *CloseCountingEngine {
	e := &CloseCountingEngine{
		t:          t,
		inner:      inner,
		closeCount: new(int32),
	}
	t.Cleanup(func() {
		count := atomic.LoadInt32(e.closeCount)
		if count > 1 {
			t.Errorf("upstream closed %d times, expected exactly once", count)
		} else if count == 0 {
			t.Errorf("upstream was not closed, expected exactly once")
		}
	})
	return e
}

func (e *CloseCountingEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	resp, err := e.inner.Process(req)
	if err != nil {
		return nil, err
	}
	closeFunc := func() {
		atomic.AddInt32(e.closeCount, 1)
	}
	if resp.Stream != nil {
		resp.Stream.OnClose(closeFunc)
	} else if resp.Body != nil {
		resp.Body.OnClose(closeFunc)
	}
	return resp, nil
}

// CollectSSEStream drains a stream response and returns its SSE wire bytes,
// mirroring the encoding done by octollm.httpSSEHandler.
// Returns an error if the timeout elapses or any chunk body cannot be read.
func CollectSSEStream(stream *octollm.StreamChan, timeout time.Duration) ([]byte, error) {
	defer stream.Close()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var buf bytes.Buffer
	for {
		select {
		case <-timer.C:
			return nil, fmt.Errorf("CollectSSEStream timed out after %s", timeout)
		case chunk, ok := <-stream.Chan():
			if !ok {
				return buf.Bytes(), nil
			}
			b, err := chunk.Body.Bytes()
			if err != nil {
				return nil, err
			}
			if event, ok := chunk.Metadata["event"]; ok {
				buf.WriteString("event: " + event + "\n")
			}
			if id, ok := chunk.Metadata["id"]; ok {
				buf.WriteString("id: " + id + "\n")
			}
			buf.WriteString("data: ")
			buf.Write(b)
			buf.WriteString("\n\n")
		}
	}
}
