package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httputil"
	"slices"
	"strings"
	"time"

	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/octollm"
)

type StreamingType string

const (
	StreamingTypeSSE  StreamingType = "sse"
	StreamingTypeJSON StreamingType = "json"
)

type clientMetadataKey string

const (
	// clientRecvFirstChunkTime stores the timestamp (time.Time) when the first chunk
	// was received from the upstream HTTP endpoint.
	clientRecvFirstChunkTime clientMetadataKey = "recv_first_chunk_time"

	// clientProcessStreamError stores any error (error) that occurs during the processing of the stream response.
	// could be context cancellation, context deadline, scanner error, unexpected EOF, etc.
	clientProcessStreamError clientMetadataKey = "process_stream_error"
)

var ErrStreamScan = errors.New("scanner error reading stream body")

var sensitiveHeaders = []string{"Authorization", "X-Api-Key", "X-Auth-Token", "Api-Key"}

func redactSensitiveHeaders(h http.Header) {
	for _, key := range sensitiveHeaders {
		if h.Get(key) != "" {
			h.Set(key, "[REDACTED]")
		}
	}
}

func GetClientRecvFirstChunkTime(req *octollm.Request) (time.Time, bool) {
	if req == nil {
		return time.Time{}, false
	}
	value, ok := req.GetMetadataValue(clientRecvFirstChunkTime)
	if !ok {
		return time.Time{}, false
	}
	t, ok := value.(time.Time)
	return t, ok
}

func GetClientProcessStreamError(req *octollm.Request) (error, bool) {
	if req == nil {
		return nil, false
	}
	value, ok := req.GetMetadataValue(clientProcessStreamError)
	if !ok {
		return nil, false
	}
	err, ok := value.(error)
	return err, ok
}

type HTTPEndpoint struct {
	client          *http.Client
	getURL          func(req *octollm.Request) (string, error)
	reqModifier     func(req *octollm.Request, hreq *http.Request) *http.Request
	nonstreamParser func(req *octollm.Request) octollm.Parser
	streamParser    func(req *octollm.Request) (octollm.Parser, StreamingType)
}

// HTTPEndpoint implements octollm.Endpoint
var _ octollm.Engine = (*HTTPEndpoint)(nil)

func NewHTTPEndpoint() *HTTPEndpoint {
	return &HTTPEndpoint{}
}

func (e *HTTPEndpoint) WithClient(client *http.Client) *HTTPEndpoint {
	e.client = client
	return e
}

func (e *HTTPEndpoint) WithURLGetter(getURL func(req *octollm.Request) (string, error)) *HTTPEndpoint {
	e.getURL = getURL
	return e
}

func (e *HTTPEndpoint) WithRequestModifier(reqModifier func(req *octollm.Request, hreq *http.Request) *http.Request) *HTTPEndpoint {
	e.reqModifier = reqModifier
	return e
}

func (e *HTTPEndpoint) WithParser(nonstreamParser func(req *octollm.Request) octollm.Parser, streamParser func(req *octollm.Request) (octollm.Parser, StreamingType)) *HTTPEndpoint {
	e.nonstreamParser = nonstreamParser
	e.streamParser = streamParser
	return e
}

func (e *HTTPEndpoint) Process(req *octollm.Request) (*octollm.Response, error) {
	if e.getURL == nil {
		return nil, fmt.Errorf("getURL is not set")
	}

	url, err := e.getURL(req)
	if err != nil {
		return nil, fmt.Errorf("getURL error: %w", err)
	}

	if e.client == nil {
		e.client = http.DefaultClient
	}

	parsed, err := req.Body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("parse request body error: %w", err)
	}

	bodyReader, err := req.Body.Reader()
	if err != nil {
		return nil, fmt.Errorf("get request body reader error: %w", err)
	}
	defer bodyReader.Close()
	httpReq, err := http.NewRequestWithContext(
		req.Context(),
		http.MethodPost,
		url,
		bodyReader)
	if err != nil {
		return nil, fmt.Errorf("new request error: %w", err)
	}

	httpReq.Header = req.Header
	if e.reqModifier != nil {
		httpReq = e.reqModifier(req, httpReq)
	}
	if httpReq.Header.Get("Content-Type") == "" {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	dumpReq := httpReq.Clone(httpReq.Context())
	redactSensitiveHeaders(dumpReq.Header)
	if reqDump, dumpErr := httputil.DumpRequestOut(dumpReq, false); dumpErr == nil {
		slog.DebugContext(req.Context(), fmt.Sprintf("[http-endpoint] outgoing request:\n%s%v", string(reqDump), parsed))
	}

	resp, err := e.client.Do(httpReq)
	if err != nil {
		return nil, &errutils.UpstreamHTTPError{
			Err: fmt.Errorf("do request error: %w", err),
		}
	}
	if respDump, dumpErr := httputil.DumpResponse(resp, false); dumpErr == nil {
		slog.DebugContext(req.Context(), fmt.Sprintf("[http-endpoint] incoming response:\n%s", string(respDump)))
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
	slog.DebugContext(req.Context(), fmt.Sprintf("[http-endpoint] got response with status code %d, content-type %s", resp.StatusCode, ct))

	// Determine if response is streaming
	isStream := false
	if action, ok := octollm.GetCtxValue[string](req, octollm.ContextKeyAction); ok {
		isStream = octollm.IsStreamAction(action)
	} else {
		// Fallback: use Content-Type header to determine stream mode
		if mt, _, err := mime.ParseMediaType(ct); err == nil {
			isStream = strings.EqualFold(mt, "text/event-stream")
		} else {
			isStream = strings.HasPrefix(strings.ToLower(ct), "text/event-stream")
		}
	}
	if !isStream {
		// non-stream
		slog.DebugContext(req.Context(), "[http-endpoint] returning non-stream response")
		body := octollm.NewBodyFromReader(resp.Body, nil)
		body.SetParser(e.nonstreamParser(req))
		llmresp := octollm.NewNonStreamResponse(resp.StatusCode, resp.Header, body)
		return llmresp, nil
	}

	// stream response
	ch := make(chan *octollm.StreamChunk)
	ctx, cancel := context.WithCancel(req.Context())
	streamChan := octollm.NewStreamChan(ch, cancel)
	llmresp := octollm.NewStreamResponse(resp.StatusCode, resp.Header, streamChan)

	// use a scanner to read SSE messages
	streamParser, streamingType := e.streamParser(req)
	switch streamingType {
	case StreamingTypeSSE:
		go e.processSSEStream(ctx, req, resp, ch, streamParser)
	case StreamingTypeJSON:
		go e.processJSONStream(ctx, req, resp, ch, streamParser)
	default:
		cancel() // just for the linter
		return nil, fmt.Errorf("unsupported streaming type %s", streamingType)
	}

	slog.DebugContext(req.Context(), "[http-endpoint] returning stream response")
	return llmresp, nil
}

func (e *HTTPEndpoint) processSSEStream(ctx context.Context, req *octollm.Request, resp *http.Response, ch chan *octollm.StreamChunk, streamParser octollm.Parser) {
	defer close(ch)
	defer resp.Body.Close()

	metaBuffer := make(map[string]string)
	bodyBuffer := make([]byte, 0, 512)

	var recvFirstChunkTime time.Time

	var totalChunkSent int
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			slog.InfoContext(ctx, fmt.Sprintf("[http-endpoint] context error during stream response: %v", err))
			return
		}
		line := scanner.Bytes()

		// process the line according to https://html.spec.whatwg.org/multipage/server-sent-events.html#event-stream-interpretation
		if len(line) == 0 {
			// dispatch the event and continue; per spec, skip if data buffer is empty
			if len(bodyBuffer) == 0 {
				metaBuffer = make(map[string]string)
				continue
			}
			body := octollm.NewBodyFromBytes(bodyBuffer, streamParser)
			// bodyLen := len(bodyBuffer)
			bodyBuffer = make([]byte, 0, 512)
			chunk := &octollm.StreamChunk{Body: body}
			if len(metaBuffer) > 0 {
				chunk.Metadata = metaBuffer
				metaBuffer = make(map[string]string)
			}
			select {
			case ch <- chunk:
				totalChunkSent++
				// slog.DebugContext(ctx, fmt.Sprintf("[http-endpoint] pushed stream chunk: len=%d", bodyLen))
			case <-ctx.Done():
				slog.InfoContext(ctx, fmt.Sprintf("[http-endpoint] context error during stream response: %v", ctx.Err()))
				return
			}
			continue
		}

		colonIdx := slices.Index(line, ':')
		if colonIdx == 0 {
			continue
		}
		var key string
		if colonIdx == -1 {
			key = string(line)
		} else {
			key = string(line[:colonIdx])
		}

		switch key {
		case "data":
			if recvFirstChunkTime.IsZero() {
				recvFirstChunkTime = time.Now()
				req.SetMetadataValue(clientRecvFirstChunkTime, recvFirstChunkTime)
			}
			// find the first non-space byte
			start := slices.IndexFunc(line[colonIdx+1:], func(b byte) bool {
				return b != ' '
			})
			if start != -1 {
				bodyBuffer = append(bodyBuffer, line[colonIdx+1+start:]...)
			}
		case "event", "id":
			value := strings.TrimLeft(string(line[colonIdx+1:]), " ")
			metaBuffer[key] = value
		default:
			// ignore other fields
			slog.DebugContext(ctx, fmt.Sprintf("[http-endpoint] ignore event line because of unknown key %s", key))
		}
	}
	if err := scanner.Err(); err != nil {
		req.SetMetadataValue(clientProcessStreamError, fmt.Errorf("%w: %w", ErrStreamScan, err))
		slog.WarnContext(ctx, fmt.Sprintf("[http-endpoint] scan response body error: %v, total chunks sent: %d", err, totalChunkSent))
	} else {
		slog.InfoContext(ctx, fmt.Sprintf("[http-endpoint] stream ended: normal completion, total chunks sent: %d", totalChunkSent))
	}
}

func (e *HTTPEndpoint) processJSONStream(ctx context.Context, req *octollm.Request, resp *http.Response, ch chan *octollm.StreamChunk, streamParser octollm.Parser) {
	defer close(ch)
	defer resp.Body.Close()

	dec := json.NewDecoder(resp.Body)

	// Read opening bracket '['
	t, err := dec.Token()
	if err != nil {
		slog.WarnContext(ctx, fmt.Sprintf("[http-endpoint] failed to read opening bracket: %v", err))
		return
	}
	// Verify it's an array opening bracket
	if delim, ok := t.(json.Delim); !ok || delim != '[' {
		slog.WarnContext(ctx, fmt.Sprintf("[http-endpoint] expected array opening bracket, got %T: %v", t, t))
		return
	}

	var recvFirstChunkTime time.Time
	var totalChunkSent int

	// Read array elements
	for dec.More() {
		if ctx.Err() != nil {
			slog.InfoContext(ctx, fmt.Sprintf("[http-endpoint] context error during JSON stream response: %v", ctx.Err()))
			return
		}
		// Decode one JSON object into RawMessage to preserve the raw bytes
		var rawMsg json.RawMessage
		if err := dec.Decode(&rawMsg); err != nil {
			slog.WarnContext(ctx, fmt.Sprintf("[http-endpoint] failed to decode JSON object: %v", err))
			return
		}

		if recvFirstChunkTime.IsZero() {
			recvFirstChunkTime = time.Now()
			req.SetMetadataValue(clientRecvFirstChunkTime, recvFirstChunkTime)
		}

		// Create chunk from raw JSON bytes
		body := octollm.NewBodyFromBytes(rawMsg, streamParser)
		chunk := &octollm.StreamChunk{Body: body}

		select {
		case ch <- chunk:
			totalChunkSent++
			// slog.DebugContext(ctx, fmt.Sprintf("[http-endpoint] pushed JSON stream chunk: len=%d", len(rawMsg)))
		case <-ctx.Done():
			slog.InfoContext(ctx, fmt.Sprintf("[http-endpoint] context error during JSON stream response: %v, total chunks sent: %d", ctx.Err(), totalChunkSent))
			return
		}
	}

	// Read closing bracket ']'
	_, err = dec.Token()
	if err != nil {
		req.SetMetadataValue(clientProcessStreamError, err)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			slog.InfoContext(ctx, fmt.Sprintf("[http-endpoint] stream ended: context cancelled: %v, total chunks sent: %d", err, totalChunkSent))
		} else {
			slog.WarnContext(ctx, fmt.Sprintf("[http-endpoint] stream ended: failed to read closing bracket (upstream engine may have crashed): %v, total chunks sent: %d", err, totalChunkSent))
		}
		return
	} else {
		slog.InfoContext(ctx, fmt.Sprintf("[http-endpoint] stream ended: normal completion, total chunks sent: %d", totalChunkSent))
	}
}
