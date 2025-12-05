package client

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"

	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/openai/openai-go/v3"
	"github.com/sirupsen/logrus"
)

type OpenAIChatCompletionsEndpoint struct {
	baseAddr string
	endpoint string
	apiKey   string
	client   *http.Client
}

// OpenAIChatCompletionsEndpoint implements octollm.Endpoint
var _ octollm.Engine = (*OpenAIChatCompletionsEndpoint)(nil)

func NewOpenAIChatCompletionsEndpoint(baseAddr, endpoint, apiKey string) *OpenAIChatCompletionsEndpoint {
	if apiKey == "" {
		// read from env
		apiKey = os.Getenv("OCTOLLM_API_KEY")
	}
	return &OpenAIChatCompletionsEndpoint{
		baseAddr: baseAddr,
		endpoint: endpoint,
		apiKey:   apiKey,
	}
}

func (e *OpenAIChatCompletionsEndpoint) WithClient(client *http.Client) *OpenAIChatCompletionsEndpoint {
	e.client = client
	return e
}

func (e *OpenAIChatCompletionsEndpoint) Process(req *octollm.Request) (*octollm.Response, error) {
	if req.Format != octollm.APIFormatChatCompletions && req.Format != octollm.APIFormatUnknown {
		return nil, fmt.Errorf("unsupported format %s", req.Format)
	}

	if e.client == nil {
		e.client = http.DefaultClient
	}

	bodyReader, err := req.Body.Reader()
	if err != nil {
		return nil, fmt.Errorf("get request body reader error: %w", err)
	}
	defer bodyReader.Close()
	httpReq, err := http.NewRequestWithContext(
		req.Context(),
		http.MethodPost,
		e.baseAddr+e.endpoint,
		bodyReader)
	if err != nil {
		return nil, fmt.Errorf("new request error: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	for k, v := range req.Header {
		for _, vv := range v {
			httpReq.Header.Set(k, vv)
		}
	}
	if e.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	// log request
	// body, _ := httputil.DumpRequest(httpReq, true)
	// logrus.WithContext(req.Context()).Debugf("[openai-chatcompletions] request: %s", string(body))

	resp, err := e.client.Do(httpReq)
	if err != nil {
		return nil, &errutils.UpstreamHTTPError{
			Err: fmt.Errorf("do request error: %w", err),
		}
	}

	if resp.StatusCode != http.StatusOK {
		bodyBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, &errutils.UpstreamHTTPError{
				Err:        fmt.Errorf("read response body error: %w", err),
				StatusCode: resp.StatusCode,
			}
		}
		return nil, &errutils.UpstreamRespError{
			StatusCode: resp.StatusCode,
			Header:     resp.Header,
			Body:       bodyBytes,
		}
	}

	ct := resp.Header.Get("Content-Type")
	logrus.WithContext(req.Context()).Debugf("[openai-chatcompletions] got response with status code %d, content-type %s", resp.StatusCode, ct)
	isStream := false
	if mt, _, err := mime.ParseMediaType(ct); err == nil {
		isStream = strings.EqualFold(mt, "text/event-stream")
	} else {
		isStream = strings.HasPrefix(strings.ToLower(ct), "text/event-stream")
	}
	if !isStream {
		// non-stream
		logrus.WithContext(req.Context()).Debugf("[openai-chatcompletions] returning non-stream response")
		body := octollm.NewBodyFromReader(resp.Body, nil)
		body.SetParser(&octollm.JSONParser[openai.ChatCompletion]{})
		llmresp := octollm.NewNonStreamResponse(resp.StatusCode, resp.Header, body)
		return llmresp, nil
	}

	// stream response
	ch := make(chan *octollm.StreamChunk)
	ctx, cancel := context.WithCancel(req.Context())
	// use a scanner to read SSE messages
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if ctx.Err() != nil {
				logrus.WithContext(ctx).Infof("[openai-chatcompletions] context error during stream response: %v", ctx.Err())
				return
			}
			line := scanner.Bytes()
			if strings.HasPrefix(string(line[:6]), "data: ") {
				line = line[6:]
			}
			if len(line) == 0 {
				continue
			}
			body := octollm.NewBodyFromBytes(line, nil)
			body.SetParser(&octollm.JSONParser[openai.ChatCompletionChunk]{})
			select {
			case ch <- &octollm.StreamChunk{Body: body}:
				logrus.WithContext(ctx).Debugf("[openai-chatcompletions] pushed stream chunk: len=%d", len(line))
			case <-ctx.Done():
				logrus.WithContext(ctx).Infof("[openai-chatcompletions] context error during stream response: %v", ctx.Err())
				return
			}
		}
		if err := scanner.Err(); err != nil {
			logrus.WithContext(ctx).Warnf("[openai-chatcompletions] scan response body error: %v", err)
		}
	}()

	logrus.WithContext(req.Context()).Debugf("[openai-chatcompletions] returning stream response")
	streamChan := octollm.NewStreamChan(ch, cancel)
	llmresp := octollm.NewStreamResponse(resp.StatusCode, resp.Header, streamChan)
	return llmresp, nil
}
