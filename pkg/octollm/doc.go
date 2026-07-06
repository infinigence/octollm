/*
Package octollm defines the core interfaces and types for building composable LLM gateways.

# Engine

Engine is the central abstraction. Every processing step—protocol conversion, rule matching,
rewriting, load balancing, upstream communication—is implemented as an Engine:

	type Engine interface {
	    Process(req *Request) (*Response, error)
	}

Engines are composed into chains: an outer Engine receives the Request, does its work, and
delegates to the next Engine via a stored reference (conventionally named "next"). The chain
terminates at a client Engine that talks to the actual upstream provider.

EngineFunc adapts a plain function into an Engine:

	var myEngine octollm.EngineFunc = func(req *octollm.Request) (*octollm.Response, error) { … }

# Request

Request carries everything an Engine needs: HTTP method, API format, URL, query parameters,
headers, and a lazy body (UnifiedBody). It also holds a context and a thread-safe metadata map.

Creating a Request from an incoming HTTP call:

	req := octollm.NewRequestWithBodyParser(httpReq, octollm.APIFormatChatCompletions, parser)

Creating a blank Request for internal use:

	req := octollm.NewEmptyRequest(ctx)

Modifying a Request inside Engine.Process:

You may directly mutate the original Request—changes persist for subsequent Engines in the
chain. If you want modifications to be visible only to inner Engines (not to outer callers),
make a shallow copy:

	req2 := req.WithContext(newCtx) // shallow copy with different context
	req3 := req.WithBody(newBody)   // shallow copy with different body

Metadata is for passing engine-specific data through the chain (retry count, selected backend,
timing info, etc.):

	req.SetMetadataValue("retry", 2)
	val, ok := req.GetMetadataValue("retry")

Context values can be retrieved with type-safe helpers:

	model, ok := octollm.GetCtxValue[string](req, someKey)

Do not assume Request.Body will be closed by the framework. Request.Body.Close() is not
guaranteed to be called; only Response bodies and streams have lifecycle obligations (see below).

# Response

Response is either non-streaming (Body is set) or streaming (Stream is set), never both.

Non-streaming:

	resp := octollm.NewNonStreamResponse(200, header, body)

Streaming:

	resp := octollm.NewStreamResponse(200, header, streamChan)

Resource lifecycle:

  - Non-streaming: the caller that consumes the Response must call resp.Body.Close() when done.
  - Streaming: the caller must call resp.Stream.Close() when done reading from the channel.

Important: Close is usually not called at Process return time. After Process returns, other
goroutines may still be reading from resp.Stream or resp.Body.

Use OnClose to register cleanup callbacks that run when Close is called:

	resp.Body.OnClose(func() { releaseResource() })
	resp.Stream.OnClose(func() { cancelContext() })

# UnifiedBody

UnifiedBody is the lazy, thread-safe body abstraction shared by Request, Response, and
StreamChunk. It supports three construction modes:

  - From an io.ReadCloser (streaming source):  NewBodyFromReader(r, parser)
  - From raw bytes:                            NewBodyFromBytes(data, parser)
  - From pre-parsed data:                      NewBodyFromParsed(obj, parser)

Lazy access:

  - Parsed() reads from the underlying reader (if any), caches the bytes, parses once, and
    returns the parsed value. Subsequent calls return the cached result.
  - Bytes() returns the raw byte representation. If the parsed data has been modified
    (isDirty), it re-serializes via the Parser.
  - Reader() returns an io.ReadCloser. If the original reader is still available it is
    returned directly; otherwise a new reader is created from the cached bytes.

Thread safety:

All public methods (Parsed, Bytes, Reader, Close, OnClose) are protected by an internal
mutex, so UnifiedBody is safe for concurrent use from multiple goroutines.

Immutability:

UnifiedBody is logically immutable. If you need to change the body content, create a new
UnifiedBody rather than modifying the existing one. Internally, private state transitions
(reader → bytes → parsed) do occur, but the externally visible content does not change
unless a new UnifiedBody is constructed.

# StreamChan and StreamChunk

StreamChan wraps a read-only channel of StreamChunk values plus a Close function. Each
StreamChunk carries optional SSE metadata (event type, id) and a UnifiedBody for the chunk
payload.

Chunks are consumed by ranging over the channel:

	for chunk := range resp.Stream.Chan() {
	    data, _ := chunk.Body.Bytes()
	    // process data…
	}

Always call resp.Stream.Close() after consumption to release upstream resources.

# SafeGo

When spawning goroutines inside request processing (e.g. fire-and-forget replication, async
stream draining), use SafeGo instead of a bare go statement:

	octollm.SafeGo(req, func() {
	    // this goroutine is recovered on panic; gin.Recovery only covers the handler goroutine
	})

# Parser

Parser is the interface for encoding/decoding body content:

	type Parser interface {
	    Parse(data []byte) (any, error)
	    Serialize(data any) ([]byte, error)
	}

JSONParser[T] provides a generic JSON implementation:

	parser := &octollm.JSONParser[openai.ChatCompletionRequest]{}
*/
package octollm
