package engines

import (
	"context"
	"fmt"
	"net/http"

	"github.com/tidwall/sjson"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/streammerge"
)

// NonStreamToStreamEngine turns a non-stream request into a streaming one before
// handing it to the next engine, then merges the streamed chunks back into a
// single non-stream response for the client. It only applies to formats that
// have a chunk merger (see streammerge.For); requests that are already streaming,
// or whose format has no merger, pass through untouched.
//
// By default it applies to every format that has a merger. Use WithEnabledFormats
// to restrict it to an allowlist; any other format then passes straight through.
//
// Placement matters: it must wrap (sit outside) the request-rewrite engine so
// the downstream rewrite sees a stream request. It marks the request as streaming
// in a format-specific way (a body `stream` flag for most formats, or the
// streamGenerateContent action + ?alt=sse for Gemini) and sets
// octollm.ContextKeyIsStream in the context, which the rewrite engine keys off of
// so stream-specific rewrites (e.g. stream_options.include_usage) are applied.
type NonStreamToStreamEngine struct {
	Next octollm.Engine

	// enabled is the allowlist of formats to force-stream. An empty set means
	// "every format that has a merger".
	enabled map[octollm.APIFormat]struct{}
}

// nonStreamToStreamConfig holds the settings options may set. Options operate on
// this rather than the engine, so they cannot touch Next or the runtime state.
type nonStreamToStreamConfig struct {
	enabled map[octollm.APIFormat]struct{}
}

// NonStreamToStreamOption configures a NonStreamToStreamEngine.
type NonStreamToStreamOption func(*nonStreamToStreamConfig)

// WithEnabledFormats restricts force-streaming to the given API formats. Without
// it, the default is to force-stream every format that has a merger.
func WithEnabledFormats(formats ...octollm.APIFormat) NonStreamToStreamOption {
	return func(c *nonStreamToStreamConfig) {
		c.enabled = make(map[octollm.APIFormat]struct{}, len(formats))
		for _, f := range formats {
			c.enabled[f] = struct{}{}
		}
	}
}

var _ octollm.Engine = (*NonStreamToStreamEngine)(nil)

// NewNonStreamToStreamEngine wraps next, applying any options over the default
// behavior (force-stream every format that has a chunk merger).
func NewNonStreamToStreamEngine(next octollm.Engine, opts ...NonStreamToStreamOption) *NonStreamToStreamEngine {
	var cfg nonStreamToStreamConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	return &NonStreamToStreamEngine{Next: next, enabled: cfg.enabled}
}

// enabledFor reports whether req's format should be force-streamed. An empty
// allowlist enables every format (the merger check happens separately).
func (e *NonStreamToStreamEngine) enabledFor(format octollm.APIFormat) bool {
	if len(e.enabled) == 0 {
		return true
	}
	_, ok := e.enabled[format]
	return ok
}

func (e *NonStreamToStreamEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	// Outside the allowlist (when one is configured): pass through untouched.
	if !e.enabledFor(req.Format) {
		return e.Next.Process(req)
	}
	// Already streaming, or no merger for this format: pass through untouched.
	// A body that cannot be parsed is treated as non-stream; the downstream
	// engine will surface the real error.
	if isStream, _ := octollm.IsStreamRequest(req); isStream {
		return e.Next.Process(req)
	}
	merger, err := streammerge.For(req.Format)
	if err != nil {
		return e.Next.Process(req)
	}

	streamReq, err := toStreamRequest(req)
	if err != nil {
		return nil, fmt.Errorf("convert request to stream: %w", err)
	}

	resp, err := e.Next.Process(streamReq)
	if err != nil {
		return nil, err
	}

	// Upstream may still answer non-stream (e.g. an error short-circuit returned
	// a plain body). Nothing to merge, pass it through as-is.
	if resp.Stream == nil {
		return resp, nil
	}
	defer resp.Stream.Close()

	for chunk := range resp.Stream.Chan() {
		if err := merger.Merge(chunk); err != nil {
			return nil, fmt.Errorf("merge stream chunk: %w", err)
		}
	}
	body, err := merger.Merged()
	if err != nil {
		return nil, fmt.Errorf("get merged response body: %w", err)
	}

	header := resp.Header.Clone()
	if header == nil {
		header = http.Header{}
	}
	header.Set("Content-Type", "application/json")
	// The merged body is freshly serialized and uncompressed, so any framing
	// headers carried over from the stream response no longer apply.
	header.Del("Content-Length")
	header.Del("Content-Encoding")
	header.Del("Transfer-Encoding")
	return octollm.NewNonStreamResponse(http.StatusOK, header, body), nil
}

// vertexStreamAction is the Gemini generateContent action's streaming variant.
const vertexStreamAction = "streamGenerateContent"

// toStreamRequest returns a copy of req that downstream engines and the upstream
// client will treat as a streaming request, without mutating state shared with
// the original request. How "streaming" is expressed is format-specific, so each
// format is handled explicitly.
func toStreamRequest(req *octollm.Request) (*octollm.Request, error) {
	switch req.Format {
	case octollm.APIFormatChatCompletions,
		octollm.APIFormatCompletions,
		octollm.APIFormatResponses,
		octollm.APIFormatClaudeMessages:
		return bodyStreamRequest(req)
	case octollm.APIFormatGoogleGenerateContent:
		return vertexStreamRequest(req)
	default:
		return nil, fmt.Errorf("force-stream not supported for format %q", req.Format)
	}
}

// bodyStreamRequest flips the top-level `stream` flag in the JSON body and marks
// the context as streaming. Used by formats that signal streaming via the body
// (OpenAI chat/completions, completions, responses; Claude messages).
func bodyStreamRequest(req *octollm.Request) (*octollm.Request, error) {
	b, err := req.Body.Bytes()
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	b, err = sjson.SetBytes(b, "stream", true)
	if err != nil {
		return nil, fmt.Errorf("set stream flag: %w", err)
	}

	ctx := context.WithValue(req.Context(), octollm.ContextKeyIsStream, true)
	streamReq := req.WithContext(ctx)
	// WithContext shallow-copies the Request, so it shares the Body pointer with
	// req. Give it an independent body so flipping `stream` does not leak into the
	// original request still held by outer engines.
	streamReq.Body = octollm.NewBodyFromBytes(b, req.Body.Parser())
	return streamReq, nil
}

// vertexStreamRequest switches a Gemini generateContent request to its streaming
// form. Vertex has no body `stream` flag: the client builds the upstream URL from
// the action (streamGenerateContent) and appends ?alt=sse when ContextKeyIsSSE is
// set, so this only touches the context and leaves the body untouched.
func vertexStreamRequest(req *octollm.Request) (*octollm.Request, error) {
	ctx := req.Context()
	ctx = context.WithValue(ctx, octollm.ContextKeyIsStream, true)
	ctx = context.WithValue(ctx, octollm.ContextKeyAction, vertexStreamAction)
	ctx = context.WithValue(ctx, octollm.ContextKeyIsSSE, true)
	return req.WithContext(ctx), nil
}
