package testhelper

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/infinigence/octollm/pkg/exprenv"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
)

type reqOptions struct {
	ctx        context.Context
	HttpMethod string
	URL        string
	Body       io.Reader

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

	parser := &octollm.JSONParser[openai.ChatCompletionRequest]{}
	req := octollm.NewRequest(r, octollm.APIFormatChatCompletions)
	req.Body.SetParser(parser)

	// inject features into context
	env := exprenv.Get(req)
	if o.features != nil {
		for name, extractor := range o.features {
			env.WithFeatureExtractor(name, extractor)
		}
	}
	ctx := req.Context()
	ctx = context.WithValue(ctx, octollm.ContextKeyExprEnv, env)
	req = req.WithContext(ctx)

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
