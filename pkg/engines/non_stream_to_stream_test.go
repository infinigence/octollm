package engines

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
)

// streamFromChunks builds a stream response whose chunks are the given SSE data
// payloads, parsed as ChatCompletionStreamChunk just like the real client does.
func streamFromChunks(datas ...string) *octollm.Response {
	ch := make(chan *octollm.StreamChunk, len(datas))
	for _, d := range datas {
		ch <- &octollm.StreamChunk{
			Body: octollm.NewBodyFromBytes([]byte(d), &octollm.JSONParser[openai.ChatCompletionStreamChunk]{}),
		}
	}
	close(ch)
	return octollm.NewStreamResponse(200, http.Header{}, octollm.NewStreamChan(ch, func() {}))
}

func chatReq(ctx context.Context) *octollm.Request {
	r := octollm.NewEmptyRequest(ctx)
	r.Format = octollm.APIFormatChatCompletions
	r.Body = octollm.NewBodyFromBytes([]byte(`{"model":"m"}`), &octollm.JSONParser[openai.ChatCompletionRequest]{})
	return r
}

func TestNonStreamToStream_MergesChunks(t *testing.T) {
	var gotStreamFlag, gotIsStreamCtx bool
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		b, _ := req.Body.Bytes()
		gotStreamFlag = gjson.GetBytes(b, "stream").Bool()
		v, _ := octollm.GetCtxValue[bool](req, octollm.ContextKeyIsStream)
		gotIsStreamCtx = v
		return streamFromChunks(
			`{"id":"cmpl-1","created":100,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}`,
			`{"id":"cmpl-1","model":"m","choices":[{"index":0,"delta":{"content":", world"}}]}`,
			`{"id":"cmpl-1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`{"id":"cmpl-1","model":"m","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
			`[DONE]`,
		), nil
	})

	resp, err := NewNonStreamToStreamEngine(next).Process(chatReq(context.Background()))
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}
	if !gotStreamFlag {
		t.Error("downstream request body should have stream:true")
	}
	if !gotIsStreamCtx {
		t.Error("downstream context should report stream via ContextKeyIsStream")
	}
	if resp.Stream != nil {
		t.Fatal("response should be non-stream")
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	parsed, err := resp.Body.Parsed()
	if err != nil {
		t.Fatalf("parse merged body: %v", err)
	}
	merged := parsed.(*openai.ChatCompletionResponse)
	if merged.ID != "cmpl-1" || merged.Object != "chat.completion" {
		t.Errorf("unexpected fields: %+v", merged)
	}
	if len(merged.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(merged.Choices))
	}
	c := merged.Choices[0]
	if got := c.Message.Content.ExtractText(); got != "Hello, world" {
		t.Errorf("content = %q, want %q", got, "Hello, world")
	}
	if c.FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", c.FinishReason)
	}
	if merged.Usage == nil || merged.Usage.TotalTokens != 5 {
		t.Errorf("usage = %+v, want total 5", merged.Usage)
	}
}

func TestNonStreamToStream_PassthroughStreamRequest(t *testing.T) {
	called := false
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		called = true
		if v, _ := octollm.GetCtxValue[bool](req, octollm.ContextKeyIsStream); !v {
			t.Error("expected stream context to be preserved")
		}
		return streamFromChunks(`[DONE]`), nil
	})

	ctx := context.WithValue(context.Background(), octollm.ContextKeyIsStream, true)
	resp, err := NewNonStreamToStreamEngine(next).Process(chatReq(ctx))
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}
	if !called {
		t.Fatal("next engine not called")
	}
	if resp.Stream == nil {
		t.Error("stream request should pass through as stream response")
	}
}

func TestNonStreamToStream_PassthroughUnsupportedFormat(t *testing.T) {
	var gotFormat octollm.APIFormat
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		gotFormat = req.Format
		b, _ := req.Body.Bytes()
		if gjson.GetBytes(b, "stream").Exists() {
			t.Error("unsupported format must not have stream flipped")
		}
		return octollm.NewNonStreamResponse(200, http.Header{}, octollm.NewBodyFromBytes([]byte(`{}`), &octollm.JSONParser[openai.EmbeddingResponse]{})), nil
	})

	r := octollm.NewEmptyRequest(context.Background())
	r.Format = octollm.APIFormatEmbeddings
	r.Body = octollm.NewBodyFromBytes([]byte(`{"model":"m"}`), &octollm.JSONParser[openai.EmbeddingRequest]{})

	resp, err := NewNonStreamToStreamEngine(next).Process(r)
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}
	if gotFormat != octollm.APIFormatEmbeddings {
		t.Errorf("format = %q, want embeddings", gotFormat)
	}
	if resp.Stream != nil {
		t.Error("embeddings response should stay non-stream")
	}
}

func TestNonStreamToStream_NonStreamUpstreamPassthrough(t *testing.T) {
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		// Upstream short-circuits with a plain non-stream body (e.g. an error).
		return octollm.NewNonStreamResponse(429, http.Header{}, octollm.NewBodyFromBytes([]byte(`{"error":"rate"}`), &octollm.JSONParser[openai.ChatCompletionResponse]{})), nil
	})

	resp, err := NewNonStreamToStreamEngine(next).Process(chatReq(context.Background()))
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}
	if resp.StatusCode != 429 {
		t.Errorf("status = %d, want 429 (passed through)", resp.StatusCode)
	}
}

// TestNonStreamToStream_AllowlistBypass confirms WithEnabledFormats restricts the
// engine to the listed formats; a chat/completions request is left untouched when
// only Claude messages is enabled, even though chat/completions has a merger.
func TestNonStreamToStream_AllowlistBypass(t *testing.T) {
	var gotStreamFlag bool
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		b, _ := req.Body.Bytes()
		gotStreamFlag = gjson.GetBytes(b, "stream").Bool()
		return octollm.NewNonStreamResponse(200, http.Header{}, octollm.NewBodyFromBytes([]byte(`{}`), &octollm.JSONParser[openai.ChatCompletionResponse]{})), nil
	})

	eng := NewNonStreamToStreamEngine(next, WithEnabledFormats(octollm.APIFormatClaudeMessages))
	resp, err := eng.Process(chatReq(context.Background()))
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}
	if gotStreamFlag {
		t.Error("chat/completions should pass through unflipped when not in the allowlist")
	}
	if resp.Stream != nil {
		t.Error("response should pass through unchanged")
	}
}

// TestNonStreamToStream_MergeErrorCarriesUpstreamHeader confirms an in-stream
// error chunk leaves the engine as an errutils.UpstreamChunkError carrying the
// header of the upstream response that produced the chunk, so the caller can
// answer with the same upstream metadata (rate-limit hints and the like) as
// for a non-200 response.
func TestNonStreamToStream_MergeErrorCarriesUpstreamHeader(t *testing.T) {
	upstreamHeader := http.Header{
		"Retry-After":           []string{"7"},
		"X-Ratelimit-Remaining": []string{"0"},
		"Content-Type":          []string{"text/event-stream"},
	}
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		ch := make(chan *octollm.StreamChunk, 2)
		ch <- &octollm.StreamChunk{Body: octollm.NewBodyFromBytes(
			[]byte(`{"id":"cmpl-1","choices":[{"index":0,"delta":{"content":"partial"}}]}`),
			&octollm.JSONParser[openai.ChatCompletionStreamChunk]{})}
		ch <- &octollm.StreamChunk{Body: octollm.NewBodyFromBytes(
			[]byte(`{"error":{"message":"RPM limit exceeded","type":"rate_limit_error"}}`),
			&octollm.JSONParser[openai.ChatCompletionStreamChunk]{})}
		close(ch)
		return octollm.NewStreamResponse(200, upstreamHeader, octollm.NewStreamChan(ch, func() {})), nil
	})

	_, err := NewNonStreamToStreamEngine(next).Process(chatReq(context.Background()))
	var chunkErr *errutils.UpstreamChunkError
	if !errors.As(err, &chunkErr) {
		t.Fatalf("error = %v, want errutils.UpstreamChunkError", err)
	}
	if got := chunkErr.Header.Get("Retry-After"); got != "7" {
		t.Errorf("Header[Retry-After] = %q, want %q", got, "7")
	}
	// The error chunk follows one content chunk: it failed mid-generation.
	if chunkErr.ChunkIndex != 1 {
		t.Errorf("ChunkIndex = %d, want 1", chunkErr.ChunkIndex)
	}
	if got := chunkErr.Header.Get("X-Ratelimit-Remaining"); got != "0" {
		t.Errorf("Header[X-Ratelimit-Remaining] = %q, want %q", got, "0")
	}
	// 完整 chunk 原文保留在 ChunkBody（透传 body 用）。
	if want := `{"error":{"message":"RPM limit exceeded","type":"rate_limit_error"}}`; !jsonEq(string(chunkErr.ChunkBody), want) {
		t.Errorf("ChunkBody = %s, want %s", chunkErr.ChunkBody, want)
	}
	// Clone, not alias: later mutation of the upstream header must not leak in.
	upstreamHeader.Set("Retry-After", "99")
	if got := chunkErr.Header.Get("Retry-After"); got != "7" {
		t.Errorf("Header aliases the upstream header: got %q after mutation", got)
	}
}

// jsonEq reports whether two JSON documents are semantically equal.
func jsonEq(a, b string) bool {
	return gjson.Get(a, "@this").Raw == gjson.Get(b, "@this").Raw
}

// chatChunk wraps a raw chat.completion.chunk payload as a stream chunk.
func chatChunk(raw string) *octollm.StreamChunk {
	return &octollm.StreamChunk{Body: octollm.NewBodyFromBytes([]byte(raw), &octollm.JSONParser[openai.ChatCompletionStreamChunk]{})}
}

// TestNonStreamToStream_ErrorChunkDrainsProducer confirms an in-stream error
// chunk drains the leftover chunks, releasing a producer blocked on an
// unbuffered send instead of abandoning it mid-send.
func TestNonStreamToStream_ErrorChunkDrainsProducer(t *testing.T) {
	var drained atomic.Bool
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		ch := make(chan *octollm.StreamChunk) // unbuffered: producer blocks per send
		go func() {
			defer close(ch)
			ch <- chatChunk(`{"error":{"message":"overloaded","type":"overloaded_error"}}`)
			ch <- chatChunk(`[DONE]`) // only received by the drain
			drained.Store(true)
		}()
		return octollm.NewStreamResponse(200, http.Header{}, octollm.NewStreamChan(ch, func() {})), nil
	})

	_, err := NewNonStreamToStreamEngine(next).Process(chatReq(context.Background()))
	var chunkErr *errutils.UpstreamChunkError
	if !errors.As(err, &chunkErr) {
		t.Fatalf("error = %v, want errutils.UpstreamChunkError", err)
	}
	if chunkErr.ChunkIndex != 0 {
		t.Errorf("ChunkIndex = %d, want 0 (error on the first chunk)", chunkErr.ChunkIndex)
	}
	if !drained.Load() {
		t.Error("leftover chunks were not drained before returning")
	}
}

// TestNonStreamToStream_LocalMergeErrorSkipsDrain confirms a merge error that
// is not an in-stream error chunk (here: an unparseable chunk) returns
// immediately and closes the stream, instead of draining an upstream that is
// still generating until the drain timeout.
func TestNonStreamToStream_LocalMergeErrorSkipsDrain(t *testing.T) {
	stop := make(chan struct{})
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		ch := make(chan *octollm.StreamChunk)
		go func() {
			defer close(ch)
			ch <- chatChunk(`{not json`)
			// Upstream still generating: keeps sending until the stream is closed.
			for {
				select {
				case ch <- chatChunk(`{"id":"c","choices":[{"index":0,"delta":{"content":"x"}}]}`):
				case <-stop:
					return
				}
			}
		}()
		return octollm.NewStreamResponse(200, http.Header{}, octollm.NewStreamChan(ch, func() { close(stop) })), nil
	})

	start := time.Now()
	_, err := NewNonStreamToStreamEngine(next).Process(chatReq(context.Background()))
	if err == nil {
		t.Fatal("expected merge error")
	}
	var chunkErr *errutils.UpstreamChunkError
	if errors.As(err, &chunkErr) {
		t.Fatalf("error = %v, must not be an UpstreamChunkError", err)
	}
	if elapsed := time.Since(start); elapsed >= octollm.DefaultDrainTimeout/2 {
		t.Errorf("Process took %v: a local merge error must not wait on a drain", elapsed)
	}
	select {
	case <-stop:
	default:
		t.Error("stream must be closed on a local merge error")
	}
}
