package engines

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
	"github.com/infinigence/octollm/pkg/types/vertex"
)

// StreamFirstChunkErrorEngine fails a streaming call whose first chunk is an
// in-stream error chunk. Some upstreams answer HTTP 200 (SSE) and only then
// emit {"error":{...}} as a data event; without this engine the gateway has
// already committed to a 200 streaming response by the time the error shows
// up, so the client sees a "successful" stream carrying nothing but an error
// payload and a retrying load balancer never gets a chance.
//
// The first chunk is read synchronously inside Process, before the response is
// handed upward, so an error chunk surfaces as a plain Go error — the
// caller-owned errutils.UpstreamChunkError carrying the upstream header and
// the raw chunk — exactly like a non-200 answer, and the usual retry/error
// mapping machinery applies unchanged. On that path the inner stream is
// drained (releasing producers blocked mid-send so intermediate engines can
// run their end-of-stream bookkeeping) and closed before the error returns.
//
// Only the first chunk is inspected: once a content chunk has been forwarded
// the 200 is committed and failing the call is no longer possible, so a
// mid-stream error chunk passes through to the client as-is.
//
// Detection is typed, per format, on the chunk's parsed form (see
// IsInStreamErrorChunk for the exact rules and their sources). Chunks of other
// formats — and unparseable ones, including the [DONE] terminator — pass
// through untouched.
type StreamFirstChunkErrorEngine struct {
	Next octollm.Engine
}

var _ octollm.Engine = (*StreamFirstChunkErrorEngine)(nil)

// NewStreamFirstChunkErrorEngine wraps next with first-chunk error detection
// on streaming responses.
func NewStreamFirstChunkErrorEngine(next octollm.Engine) *StreamFirstChunkErrorEngine {
	return &StreamFirstChunkErrorEngine{Next: next}
}

func (e *StreamFirstChunkErrorEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	resp, err := e.Next.Process(req)
	if err != nil || resp == nil || resp.Stream == nil {
		return resp, err
	}
	src := resp.Stream
	ctx := req.Context()

	var first *octollm.StreamChunk
	select {
	case chunk, ok := <-src.Chan():
		if !ok {
			// Producer closed without a single chunk: nothing to inspect, and
			// the channel is already closed, so pass the response through.
			return resp, nil
		}
		first = chunk
	case <-ctx.Done():
		src.Close()
		return nil, ctx.Err()
	}

	if IsInStreamErrorChunk(first) {
		// The call fails here, so nobody downstream will ever consume this
		// stream: drain the leftover chunks so producers blocked mid-send can
		// finish gracefully, then close to release the upstream transport.
		// See [octollm.StreamChan.DrainRemaining].
		drainCtx, cancelDrain := context.WithTimeout(context.WithoutCancel(ctx), octollm.DefaultDrainTimeout)
		src.DrainRemaining(drainCtx)
		cancelDrain()
		src.Close()

		// Parsed() succeeded inside the check, so Bytes() hits the cached
		// bytes; clone because they alias the body's internal buffer.
		chunkRaw, _ := first.Body.Bytes()
		return nil, &errutils.UpstreamChunkError{
			Header:    resp.Header.Clone(),
			ChunkBody: slices.Clone(chunkRaw),
			// Only the first chunk is ever inspected. Set explicitly rather than
			// relying on the zero value, so the intent survives refactors.
			ChunkIndex: 0,
		}
	}

	// First chunk is ordinary content: replay it, then forward the rest.
	out := make(chan *octollm.StreamChunk)
	// When the consumer stops reading it closes the outer stream; propagate
	// that to the inner one so its resources are released.
	resp.Stream = octollm.NewStreamChan(out, src.Close)
	octollm.SafeGo(req, func() {
		defer close(out)
		send := func(chunk *octollm.StreamChunk) bool {
			select {
			case out <- chunk:
				return true
			case <-ctx.Done():
				return false
			}
		}
		if !send(first) {
			return
		}
		for chunk := range src.Chan() {
			if !send(chunk) {
				return
			}
		}
	})
	return resp, nil
}

// IsInStreamErrorChunk reports whether chunk is an in-stream error chunk. It
// is the single source of the detection rules: StreamFirstChunkErrorEngine
// uses it to fail a call on its first chunk, and callers that only observe
// streams (logging, metrics) should use it too rather than re-implementing
// the rules, so the two never drift apart. The chunk must carry the parsed
// form produced by the HTTP client's per-format stream parser.
//
// The rules are per format, each tied to its source:
//
//   - chat/completions, completions: a non-null top-level "error". Not part of
//     the streaming schema (CreateChatCompletionStreamResponse has no error
//     property); it is the de facto convention of OpenAI, vLLM, sglang and
//     friends, which reuse the non-stream ErrorResponse envelope
//     ({"error": Error}) as a data event.
//   - responses: type "error" (ResponseErrorEvent: code/message/param flat at
//     the root, no "error" key) or type "response.failed" (ResponseFailedEvent:
//     the failure is on response.error), both from the OpenAI OpenAPI spec's
//     ResponseStreamEvent; plus the same top-level "error" convention as chat
//     for compatible upstreams. "response.incomplete" is a normal (truncated)
//     end, not an error.
//   - claude messages: type "error" with an "error" object
//     ({"type":"error","error":{"type":"overloaded_error",...}}), per the
//     Anthropic streaming docs; anthropic-sdk-go's ssestream likewise turns
//     the error event into a stream error. An "error" object without the
//     type is accepted too, for compatible upstreams.
//   - google generateContent (Vertex / Gemini): a non-null top-level "error",
//     the standard Google API error envelope
//     ({"error":{"code":429,"message":...,"status":"RESOURCE_EXHAUSTED"}}),
//     which streamGenerateContent emits as a stream element (SSE data event
//     or JSON array element) once the HTTP 200 is committed.
//
// Unparseable chunks (including the [DONE] terminator) and other formats are
// not error chunks; downstream surfaces any real problem.
func IsInStreamErrorChunk(chunk *octollm.StreamChunk) bool {
	if chunk == nil || chunk.Body == nil {
		return false
	}
	parsed, err := chunk.Body.Parsed()
	if err != nil {
		return false
	}
	switch c := parsed.(type) {
	case *openai.ChatCompletionStreamChunk:
		return nonNullRawJSON(c.Error)
	case *openai.CompletionResponse: // CompletionStreamChunk is an alias
		return nonNullRawJSON(c.Error)
	case *openai.ResponseStreamChunk:
		return c.Type == "error" || c.Type == "response.failed" || nonNullRawJSON(c.Error)
	case *anthropic.ClaudeMessagesStreamEvent:
		return c.Type == "error" || c.Error != nil
	case *vertex.StreamGenerateContentResponse:
		return nonNullRawJSON(c.Error)
	default:
		return false
	}
}

// nonNullRawJSON reports whether raw holds a JSON value other than the null
// literal. RawMessage tokens are exact slices without surrounding whitespace
// (both encoding/json and the jlexer Raw path guarantee this), so a plain
// comparison suffices.
func nonNullRawJSON(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}
