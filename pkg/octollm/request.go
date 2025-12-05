package octollm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

type APIFormat string

const (
	APIFormatUnknown               APIFormat = ""
	APIFormatChatCompletions       APIFormat = "chat/completions"
	APIFormatLegacyCompletions     APIFormat = "completions"
	APIFormatClaudeMessages        APIFormat = "messages"
	APIFormatVertexGenerateContent APIFormat = "vertex"
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

	b1, err := b.Bytes()
	if err != nil {
		return nil, fmt.Errorf("get bytes error: %w", err)
	}
	return io.NopCloser(bytes.NewReader(b1)), nil
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

	ctx context.Context
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
	Body *UnifiedBody
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
		Header: r.Header,
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
