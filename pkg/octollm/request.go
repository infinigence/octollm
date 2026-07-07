package octollm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
)

type APIFormat string

const (
	APIFormatUnknown               APIFormat = ""
	APIFormatChatCompletions       APIFormat = "chat/completions"
	APIFormatCompletions           APIFormat = "completions"
	APIFormatClaudeMessages        APIFormat = "messages"
	APIFormatGoogleGenerateContent APIFormat = "generateContent"
	APIFormatEmbeddings            APIFormat = "embeddings"
	APIFormatRerank                APIFormat = "rerank"
	APIFormatResponses             APIFormat = "responses"
)

// Parser parses and serializes body of requests or responses.
type Parser interface {
	Parse(data []byte) (any, error)
	Serialize(data any) ([]byte, error)
	// ContentType() string
}

// UnifiedBody is the body of requests or responses.
// It supports lazy parsing and caching.
type UnifiedBody struct {
	reader io.ReadCloser // original reader
	bytes  []byte        // cached bytes (filled after reading)

	parsed   any    // cached parsed data
	parser   Parser // parser (must be set before use)
	parseErr error  // parsing error
	isDirty  bool   // marks if parsed data is manually modified

	closeFunc func()
}

func NewBodyFromReader(reader io.ReadCloser, parser Parser) *UnifiedBody {
	return &UnifiedBody{
		reader: reader,
		parser: parser,
	}
}

func NewBodyFromBytes(bytes []byte, parser Parser) *UnifiedBody {
	return &UnifiedBody{
		bytes:  bytes,
		parser: parser,
	}
}

func NewBodyFromParsed(parsed any, parser Parser) *UnifiedBody {
	return &UnifiedBody{
		parsed:  parsed,
		parser:  parser,
		isDirty: true,
	}
}

// Parsed lazily parses the body and returns the parsed data.
// It caches the parsed data and error for future calls.
func (b *UnifiedBody) Parsed() (any, error) {
	if b.parsed != nil {
		return b.parsed, b.parseErr
	}

	if b.reader != nil {
		bytes, err := io.ReadAll(b.reader)
		if err != nil {
			return nil, fmt.Errorf("read body error: %w", err)
		}
		b.bytes = bytes
		b.reader.Close()
		b.reader = nil
	}

	b.parsed, b.parseErr = b.parser.Parse(b.bytes)
	return b.parsed, b.parseErr
}

// Bytes returns the serialized bytes of the parsed data.
// If the parsed data is dirty (isDirty=true), it will be serialized again.
func (b *UnifiedBody) Bytes() ([]byte, error) {
	if !b.isDirty && b.bytes != nil {
		return b.bytes, nil
	}

	if b.isDirty {
		if b.parsed == nil {
			return nil, fmt.Errorf("parsed body must not be nil")
		}
		// serialize parsed data
		bytes, err := b.parser.Serialize(b.parsed)
		if err != nil {
			return nil, fmt.Errorf("serialize body error: %w", err)
		}
		b.bytes = bytes
		b.isDirty = false
		return b.bytes, nil
	}

	// read from reader
	if b.reader == nil {
		return nil, fmt.Errorf("reader must not be nil")
	}

	bytes, err := io.ReadAll(b.reader)
	if err != nil {
		return nil, fmt.Errorf("read body error: %w", err)
	}
	b.bytes = bytes
	// after read, reset reader
	b.reader.Close()
	b.reader = nil
	return b.bytes, nil
}

// SetBytes sets the raw bytes and resets the cached state.
// It also clears the reader to ensure the bytes are not read twice.
func (b *UnifiedBody) SetBytes(bytes []byte) {
	b.bytes = bytes
	b.parsed = nil
	b.parseErr = nil
	b.isDirty = false
	if b.reader != nil {
		b.reader.Close()
		b.reader = nil
	}
}

func (b *UnifiedBody) Reader() (io.ReadCloser, error) {
	if b.reader != nil {
		return b.reader, nil
	}

	data, err := b.Bytes()
	if err != nil {
		return nil, fmt.Errorf("get bytes error: %w", err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// Parser returns the parser associated with this body.
func (b *UnifiedBody) Parser() Parser {
	return b.parser
}

// SetParser set the parser and reset the cached state
func (b *UnifiedBody) SetParser(p Parser) {
	b.parser = p
	b.parsed = nil
	b.parseErr = nil
	b.isDirty = false
}

// SetParsed set the parsed data and mark it as dirty
// Scene: protocol conversion, request rewriting
func (b *UnifiedBody) SetParsed(v any) {
	b.parsed = v
	b.isDirty = true // mark the content as dirty, will be serialized again in Bytes()
	if b.reader != nil {
		b.reader.Close()
		b.reader = nil
	}
}

func (b *UnifiedBody) Close() error {
	if b.closeFunc != nil {
		b.closeFunc()
	}
	if b.reader == nil {
		return nil
	}
	return b.reader.Close()
}

func (b *UnifiedBody) OnClose(closeFunc func()) {
	if b.closeFunc == nil {
		b.closeFunc = closeFunc
	} else {
		prevClose := b.closeFunc
		b.closeFunc = func() {
			prevClose()
			closeFunc()
		}
	}
}

type Request struct {
	Method string
	Format APIFormat
	URL    *url.URL
	Query  url.Values
	Header http.Header
	Body   *UnifiedBody

	// metadata stores engine-specific information that engines can write and read.
	// Used for passing data through the engine chain
	// (e.g., retry attempts, selected backend, timing info, custom flags).
	metadata *sync.Map

	ctx context.Context
}

// GetMetadataValue retrieves a value from metadata by key with type assertion.
func (u *Request) GetMetadataValue(key any) (any, bool) {
	if u.metadata == nil {
		return nil, false
	}
	return u.metadata.Load(key)
}

// SetMetadataValue sets a value in metadata by key.
func (u *Request) SetMetadataValue(key any, value any) {
	if u.metadata == nil {
		u.metadata = &sync.Map{}
	}
	u.metadata.Store(key, value)
}

type Response struct {
	StatusCode int
	Header     http.Header
	Body       *UnifiedBody
	Stream     *StreamChan
}

type StreamChan struct {
	ch        <-chan *StreamChunk
	closeFunc func()
}

type StreamChunk struct {
	Metadata map[string]string // optionally contains id or event fields from SSE
	Body     *UnifiedBody
}

func NewStreamChan(ch <-chan *StreamChunk, closeFunc func()) *StreamChan {
	return &StreamChan{
		ch:        ch,
		closeFunc: closeFunc,
	}
}

func (sc *StreamChan) Chan() <-chan *StreamChunk {
	return sc.ch
}

func (sc *StreamChan) Close() {
	if sc.closeFunc != nil {
		sc.closeFunc()
	}
}

func (sc *StreamChan) OnClose(closeFunc func()) {
	if sc.closeFunc == nil {
		sc.closeFunc = closeFunc
	} else {
		prevClose := sc.closeFunc
		sc.closeFunc = func() {
			prevClose()
			closeFunc()
		}
	}
}

func NewRequest(r *http.Request, format APIFormat) *Request {
	u := &Request{
		Method:   r.Method,
		Format:   format,
		URL:      r.URL,
		Query:    r.URL.Query(),
		Header:   make(http.Header),
		ctx:      r.Context(),
		metadata: &sync.Map{},
		Body: &UnifiedBody{
			reader: r.Body,
		},
	}
	return u
}

// NewEmptyRequest returns a request with ctx and a new empty metadata map.
// All other fields are zero: Method and Format are "", URL/Query/Header/Body are nil.
// Callers that fork an existing request (e.g. traffic replication) should set Method, Format,
// and deep-copy URL, Query, Header, and Body themselves.
func NewEmptyRequest(ctx context.Context) *Request {
	if ctx == nil {
		panic("nil context")
	}
	return &Request{
		ctx:      ctx,
		metadata: &sync.Map{},
	}
}

func (u *Request) Context() context.Context {
	return u.ctx
}

// WithContext returns a shallow copy of u with its context changed to ctx.
// The provided ctx must be non-nil.
// This method follows the same pattern as http.Request.WithContext.
func (u *Request) WithContext(ctx context.Context) *Request {
	if ctx == nil {
		panic("nil context")
	}
	u2 := new(Request)
	*u2 = *u
	u2.ctx = ctx
	return u2
}

// IsStream reports whether the request asks for a streaming response, using
// whichever signal the protocol carries. It returns an error only when a
// body-based protocol's body cannot be parsed.
//
// Detection order:
//  1. ContextKeyIsStream — the cached (or deliberately overridden) flag,
//     authoritative when present.
//  2. ContextKeyAction, for URL-action protocols (e.g. Vertex
//     streamGenerateContent) where streaming is not a body field.
//  3. the typed request body's stream flag, for body-based protocols
//     (OpenAI chat/completions & completions, Responses, Claude messages).
//
// On a successful classification the result is cached by replacing u's context
// with one carrying ContextKeyIsStream, so later calls (including on engines
// downstream of the same *Request) skip re-parsing. Two consequences:
//
//   - A successful call changes what u.Context() returns. Do not assume the
//     context is stable across a call, and do not install a context derived
//     from a pre-call u.Context() snapshot afterwards — that silently drops
//     the cached flag (harmless, it is recomputed, but wasteful).
//   - The ctx swap is not synchronized; like Body, a Request must not be
//     accessed concurrently.
//
// Because of this caching, engines never need to set ContextKeyIsStream
// themselves. The one exception is an engine that changes the streamness of a
// request (e.g. NonStreamToStreamEngine): it must set the key on the derived
// request's context to override the possibly-cached stale value.
func (u *Request) IsStream() (bool, error) {
	if u == nil {
		return false, nil
	}
	if v, ok := GetCtxValue[bool](u, ContextKeyIsStream); ok {
		return v, nil
	}
	isStream, err := u.detectIsStream()
	if err != nil {
		return false, err
	}
	if u.ctx != nil {
		u.ctx = context.WithValue(u.ctx, ContextKeyIsStream, isStream)
	}
	return isStream, nil
}

// detectIsStream computes the stream flag from the action or the parsed body,
// ignoring any cached value. Formats without a stream signal (embeddings,
// rerank, unknown bodies) are non-stream.
func (u *Request) detectIsStream() (bool, error) {
	if action, ok := GetCtxValue[string](u, ContextKeyAction); ok {
		return IsStreamAction(action), nil
	}
	if u.Body == nil {
		return false, nil
	}

	parsed, err := u.Body.Parsed()
	if err != nil {
		return false, err
	}
	switch body := parsed.(type) {
	case *openai.ChatCompletionRequest:
		return body.Stream != nil && *body.Stream, nil
	case *openai.CompletionRequest:
		return body.Stream, nil
	case *openai.ResponsesRequest:
		return body.Stream != nil && *body.Stream, nil
	case *anthropic.ClaudeMessagesRequest:
		return body.Stream != nil && *body.Stream, nil
	}
	return false, nil
}

func GetCtxValue[T any](req *Request, key any) (T, bool) {
	var zero T
	if req == nil || req.ctx == nil {
		return zero, false
	}
	raw := req.ctx.Value(key)
	if raw == nil {
		return zero, false
	}
	v, ok := raw.(T)
	if !ok {
		return zero, false
	}
	return v, true
}

func NewNonStreamResponse(statusCode int, header http.Header, body *UnifiedBody) *Response {
	return &Response{
		StatusCode: statusCode,
		Header:     header,
		Body:       body,
	}
}

func NewStreamResponse(statusCode int, header http.Header, stream *StreamChan) *Response {
	return &Response{
		StatusCode: statusCode,
		Header:     header,
		Stream:     stream,
	}
}
