package octollm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	if b.reader == nil {
		return nil
	}
	return b.reader.Close()
}

type Request struct {
	Method string
	Format APIFormat
	URL    *url.URL
	Query  url.Values
	Header http.Header
	Body   *UnifiedBody

	features map[string]FeatureExtractor
	exprEnv  *RequestExprEnv

	ctx context.Context
}

type Response struct {
	StatusCode int
	Header     http.Header
	Body       *UnifiedBody
	Stream     *StreamChan

	// metadata stores engine-specific information that child engines can write
	// and parent engines can retrieve. Used for passing data up the engine chain
	// (e.g., retry attempts, selected backend, timing info, custom flags).
	metadata map[any]any
}

// GetMetadataValue retrieves a value from metadata by key with type assertion.
func (r *Response) GetMetadataValue(key any) (any, bool) {
	if r.metadata == nil {
		return nil, false
	}
	val, ok := r.metadata[key]
	return val, ok
}

// SetMetadataValue sets a value in metadata by key. Initializes metadata map if nil.
func (r *Response) SetMetadataValue(key any, value any) {
	if r.metadata == nil {
		r.metadata = make(map[any]any)
	}
	r.metadata[key] = value
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

func NewRequest(r *http.Request, format APIFormat) *Request {
	u := &Request{
		Method: r.Method,
		Format: format,
		URL:    r.URL,
		Query:  r.URL.Query(),
		Header: make(http.Header),
		ctx:    r.Context(),
		Body: &UnifiedBody{
			reader: r.Body,
		},
	}
	return u
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

func (u *Request) GetExprEnv() *RequestExprEnv {
	if u.exprEnv != nil {
		return u.exprEnv
	}

	u.exprEnv = &RequestExprEnv{
		req: u,
	}

	return u.exprEnv
}

func (u *Request) RegisterFeature(name string, extractor FeatureExtractor) {
	if u.features == nil {
		u.features = make(map[string]FeatureExtractor)
	}
	u.features[name] = extractor
}

func (u *Request) GetFeatureExtractor(name string) (FeatureExtractor, bool) {
	if u.features == nil {
		return nil, false
	}
	extractor, ok := u.features[name]
	return extractor, ok
}

type RequestExprEnv struct {
	req    *Request
	rawReq map[string]any
}

type FeatureExtractor interface {
	Features(req *Request) (any, error)
}

type FeatureExtractorFunc func(req *Request) (any, error)

func (f FeatureExtractorFunc) Features(req *Request) (any, error) {
	return f(req)
}

// RawReq returns the raw request body as a map[string]any. It caches the result after the first call.
func (r *RequestExprEnv) RawReq() map[string]any {
	if r.rawReq != nil {
		return r.rawReq
	}

	b, err := r.req.Body.Bytes()
	if err != nil {
		return nil
	}
	var rawReq map[string]any
	if err := json.Unmarshal(b, &rawReq); err != nil {
		return nil
	}

	r.rawReq = rawReq
	return rawReq
}

// CachedResult returns the cached result for a given key. It returns nil if the key does not exist.
func (r *RequestExprEnv) Context(key string) any {
	return r.req.Context().Value(key)
}

// Feature returns the extracted feature value for a given key. It returns nil if the key does not exist or if there is an error during extraction.
func (r *RequestExprEnv) Feature(key string) any {
	if extractor, ok := r.req.GetFeatureExtractor(key); ok {
		val, err := extractor.Features(r.req)
		if err != nil {
			return nil
		}
		return val
	}
	return nil
}
