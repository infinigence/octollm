package engines

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
	"github.com/infinigence/octollm/pkg/types/vertex"
)

// streamEngine returns a next engine answering a 200 stream with the given
// chunks and header, reporting whether Close was called on the inner stream.
func streamEngine(header http.Header, closed *bool, datas ...string) octollm.Engine {
	return octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		ch := make(chan *octollm.StreamChunk, len(datas))
		for _, d := range datas {
			ch <- &octollm.StreamChunk{
				Body: octollm.NewBodyFromBytes([]byte(d), &octollm.JSONParser[openai.ChatCompletionStreamChunk]{}),
			}
		}
		close(ch)
		return octollm.NewStreamResponse(200, header, octollm.NewStreamChan(ch, func() { *closed = true })), nil
	})
}

// TestStreamFirstChunkError_ErrorFirstChunk confirms a stream whose first
// chunk is an in-stream error fails the call with errutils.UpstreamChunkError
// carrying the upstream header and the raw chunk, draining and closing the
// inner stream so its producer is not abandoned.
func TestStreamFirstChunkError_ErrorFirstChunk(t *testing.T) {
	upstreamHeader := http.Header{
		"Retry-After":           []string{"7"},
		"X-Ratelimit-Remaining": []string{"0"},
	}
	errChunk := `{"error":{"message":"RPM limit exceeded","type":"rate_limit_error"}}`
	var closed bool
	next := streamEngine(upstreamHeader, &closed,
		errChunk,
		`{"id":"cmpl-1","choices":[{"index":0,"delta":{"content":"leftover"}}]}`, // must be drained
	)

	_, err := NewStreamFirstChunkErrorEngine(next).Process(chatReq(context.Background()))
	var chunkErr *errutils.UpstreamChunkError
	if !errors.As(err, &chunkErr) {
		t.Fatalf("error = %v, want errutils.UpstreamChunkError", err)
	}
	if got := chunkErr.Header.Get("Retry-After"); got != "7" {
		t.Errorf("Header[Retry-After] = %q, want %q", got, "7")
	}
	if chunkErr.ChunkIndex != 0 {
		t.Errorf("ChunkIndex = %d, want 0 (only the first chunk is inspected)", chunkErr.ChunkIndex)
	}
	if !jsonEq(string(chunkErr.ChunkBody), errChunk) {
		t.Errorf("ChunkBody = %s, want %s", chunkErr.ChunkBody, errChunk)
	}
	if !closed {
		t.Error("inner stream must be closed after the call fails")
	}
	// Clone, not alias: later mutation of the upstream header must not leak in.
	upstreamHeader.Set("Retry-After", "99")
	if got := chunkErr.Header.Get("Retry-After"); got != "7" {
		t.Errorf("Header aliases the upstream header: got %q after mutation", got)
	}
}

// TestStreamFirstChunkError_ErrorFirstChunkReleasesProducer confirms the drain
// releases a producer still blocked on an unbuffered send when the call fails.
func TestStreamFirstChunkError_ErrorFirstChunkReleasesProducer(t *testing.T) {
	producerDone := make(chan struct{})
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		ch := make(chan *octollm.StreamChunk) // unbuffered: producer blocks per send
		go func() {
			defer close(producerDone)
			defer close(ch)
			for _, d := range []string{
				`{"error":{"message":"overloaded","type":"overloaded_error"}}`,
				`{"id":"cmpl-1","choices":[{"index":0,"delta":{"content":"leftover"}}]}`,
			} {
				ch <- &octollm.StreamChunk{
					Body: octollm.NewBodyFromBytes([]byte(d), &octollm.JSONParser[openai.ChatCompletionStreamChunk]{}),
				}
			}
		}()
		return octollm.NewStreamResponse(200, http.Header{}, octollm.NewStreamChan(ch, func() {})), nil
	})

	_, err := NewStreamFirstChunkErrorEngine(next).Process(chatReq(context.Background()))
	var chunkErr *errutils.UpstreamChunkError
	if !errors.As(err, &chunkErr) {
		t.Fatalf("error = %v, want errutils.UpstreamChunkError", err)
	}
	select {
	case <-producerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("producer still blocked: leftover chunks were not drained")
	}
}

// TestStreamFirstChunkError_PassThrough confirms an ordinary stream passes
// through intact: every chunk (including the replayed first one) reaches the
// consumer, and closing the outer stream closes the inner one.
func TestStreamFirstChunkError_PassThrough(t *testing.T) {
	chunks := []string{
		`{"id":"cmpl-1","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}`,
		`{"id":"cmpl-1","choices":[{"index":0,"delta":{"content":", world"},"finish_reason":"stop"}]}`,
		`[DONE]`,
	}
	var closed bool
	next := streamEngine(http.Header{"Content-Type": []string{"text/event-stream"}}, &closed, chunks...)

	resp, err := NewStreamFirstChunkErrorEngine(next).Process(chatReq(context.Background()))
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}
	if resp.Stream == nil {
		t.Fatal("response must stay streaming")
	}
	var got []string
	for chunk := range resp.Stream.Chan() {
		b, err := chunk.Body.Bytes()
		if err != nil {
			t.Fatalf("chunk bytes: %v", err)
		}
		got = append(got, string(b))
	}
	resp.Stream.Close()
	if len(got) != len(chunks) {
		t.Fatalf("got %d chunks, want %d: %q", len(got), len(chunks), got)
	}
	for i := range chunks {
		if got[i] != chunks[i] {
			t.Errorf("chunk[%d] = %s, want %s", i, got[i], chunks[i])
		}
	}
	if !closed {
		t.Error("closing the outer stream must close the inner one")
	}
}

// TestStreamFirstChunkError_ErrorNullIsNotError confirms a literal null error
// field does not fail the call.
func TestStreamFirstChunkError_ErrorNullIsNotError(t *testing.T) {
	var closed bool
	next := streamEngine(http.Header{}, &closed,
		`{"id":"cmpl-1","choices":[{"index":0,"delta":{"content":"ok"}}],"error":null}`,
	)
	resp, err := NewStreamFirstChunkErrorEngine(next).Process(chatReq(context.Background()))
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}
	n := 0
	for range resp.Stream.Chan() {
		n++
	}
	resp.Stream.Close()
	if n != 1 {
		t.Errorf("got %d chunks, want 1", n)
	}
}

// TestStreamFirstChunkError_NonStreamPassThrough confirms non-stream responses
// are untouched.
func TestStreamFirstChunkError_NonStreamPassThrough(t *testing.T) {
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		return octollm.NewNonStreamResponse(http.StatusOK, http.Header{}, octollm.NewBodyFromBytes([]byte(`{}`), nil)), nil
	})
	resp, err := NewStreamFirstChunkErrorEngine(next).Process(chatReq(context.Background()))
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}
	if resp.Stream != nil {
		t.Error("non-stream response must stay non-stream")
	}
}

// TestStreamFirstChunkError_EmptyStream confirms a stream that closes without
// any chunk passes through as-is.
func TestStreamFirstChunkError_EmptyStream(t *testing.T) {
	var closed bool
	next := streamEngine(http.Header{}, &closed)
	resp, err := NewStreamFirstChunkErrorEngine(next).Process(chatReq(context.Background()))
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}
	if _, ok := <-resp.Stream.Chan(); ok {
		t.Error("stream must be closed/empty")
	}
}

// TestStreamFirstChunkError_Formats checks the per-format detection rules of
// IsInStreamErrorChunk end to end: each first chunk is parsed with the same
// parser the HTTP client uses for that format.
func TestStreamFirstChunkError_Formats(t *testing.T) {
	chatParser := &octollm.JSONParser[openai.ChatCompletionStreamChunk]{}
	completionParser := &octollm.JSONParser[openai.CompletionStreamChunk]{}
	responsesParser := &octollm.JSONParser[openai.ResponseStreamChunk]{}
	claudeParser := &octollm.JSONParser[anthropic.ClaudeMessagesStreamEvent]{}
	vertexParser := &octollm.JSONParser[vertex.StreamGenerateContentResponse]{}

	tests := []struct {
		name    string
		parser  octollm.Parser
		first   string
		wantErr bool
	}{
		// chat/completions: top-level "error" convention.
		{"chat error", chatParser, `{"error":{"message":"RPM limit exceeded","type":"rate_limit_error"}}`, true},
		{"chat error null", chatParser, `{"id":"c","choices":[],"error":null}`, false},
		{"chat content", chatParser, `{"id":"c","choices":[{"index":0,"delta":{"content":"hi"}}]}`, false},

		// completions: same convention.
		{"completions error", completionParser, `{"error":{"message":"overloaded","type":"server_error"}}`, true},
		{"completions content", completionParser, `{"id":"c","object":"text_completion.chunk","choices":[{"index":0,"text":"hi"}]}`, false},

		// responses: spec ResponseErrorEvent / ResponseFailedEvent, plus the
		// top-level "error" convention.
		{"responses error event", responsesParser, `{"type":"error","code":"ERR_SOMETHING","message":"Something went wrong","param":null,"sequence_number":1}`, true},
		{"responses failed event", responsesParser, `{"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"server_error","message":"The model failed to generate a response."}}}`, true},
		{"responses top-level error", responsesParser, `{"error":{"message":"RPM limit exceeded","type":"rate_limit_error"}}`, true},
		{"responses created", responsesParser, `{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`, false},
		{"responses incomplete is not an error", responsesParser, `{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`, false},

		// claude messages: {"type":"error","error":{...}}.
		{"claude error event", claudeParser, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, true},
		{"claude message_start", claudeParser, `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[]}}`, false},
		{"claude ping", claudeParser, `{"type":"ping"}`, false},
		{"claude error object without type", claudeParser, `{"error":{"type":"overloaded_error","message":"Overloaded"}}`, true},

		// google generateContent: standard Google API error envelope.
		{"vertex error", vertexParser, `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`, true},
		{"vertex error null", vertexParser, `{"candidates":[],"error":null}`, false},
		{"vertex content", vertexParser, `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]}}],"modelVersion":"gemini-2.5-pro"}`, false},

		// Unknown formats are never inspected.
		{"raw json error passes", &octollm.JSONParser[json.RawMessage]{}, `{"error":{"message":"x"}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Unparseable chunks are never error chunks, so a fixture that fails
			// to parse would make a pass-through case pass vacuously.
			if _, err := octollm.NewBodyFromBytes([]byte(tt.first), tt.parser).Parsed(); err != nil {
				t.Fatalf("fixture does not parse: %v", err)
			}
			var closed bool
			next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
				ch := make(chan *octollm.StreamChunk, 1)
				ch <- &octollm.StreamChunk{Body: octollm.NewBodyFromBytes([]byte(tt.first), tt.parser)}
				close(ch)
				return octollm.NewStreamResponse(200, http.Header{}, octollm.NewStreamChan(ch, func() { closed = true })), nil
			})

			resp, err := NewStreamFirstChunkErrorEngine(next).Process(chatReq(context.Background()))
			var chunkErr *errutils.UpstreamChunkError
			if tt.wantErr {
				if !errors.As(err, &chunkErr) {
					t.Fatalf("error = %v, want errutils.UpstreamChunkError", err)
				}
				if !jsonEq(string(chunkErr.ChunkBody), tt.first) {
					t.Errorf("ChunkBody = %s, want %s", chunkErr.ChunkBody, tt.first)
				}
				if !closed {
					t.Error("inner stream must be closed after the call fails")
				}
				return
			}
			if err != nil {
				t.Fatalf("Process error: %v", err)
			}
			n := 0
			for range resp.Stream.Chan() {
				n++
			}
			resp.Stream.Close()
			if n != 1 {
				t.Errorf("got %d chunks, want the first chunk replayed", n)
			}
		})
	}
}

// IsInStreamErrorChunk is exported for observers (e.g. gateway logging); it
// must be safe on chunks without a body and on unparseable payloads.
func TestIsInStreamErrorChunk_Degenerate(t *testing.T) {
	chatParser := &octollm.JSONParser[openai.ChatCompletionStreamChunk]{}
	if IsInStreamErrorChunk(nil) {
		t.Error("nil chunk must not be an error chunk")
	}
	if IsInStreamErrorChunk(&octollm.StreamChunk{}) {
		t.Error("chunk without body must not be an error chunk")
	}
	if IsInStreamErrorChunk(&octollm.StreamChunk{Body: octollm.NewBodyFromBytes([]byte(`[DONE]`), chatParser)}) {
		t.Error("[DONE] terminator must not be an error chunk")
	}
	if IsInStreamErrorChunk(&octollm.StreamChunk{Body: octollm.NewBodyFromBytes([]byte(`{not json`), chatParser)}) {
		t.Error("unparseable chunk must not be an error chunk")
	}
}
