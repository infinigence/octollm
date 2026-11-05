package errutils

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// UpstreamChunkError indicates an error the upstream expressed as an
// in-stream chunk: the HTTP response was 200 (SSE), and one data event
// carried an error payload ({"error":{...}}) instead of content. It is the
// streaming counterpart of UpstreamRespError.
//
// It carries only what the upstream said; mapping it to an equivalent HTTP
// status (and sanitizing it) belongs to the caller.
type UpstreamChunkError struct {
	// Header is the header of the upstream response that produced the chunk,
	// attached by the engine that still holds the response.
	Header http.Header
	// ChunkBody is the full raw chunk, for passthrough bodies.
	ChunkBody json.RawMessage
	// ChunkIndex is the 0-based position of the error chunk in the stream, as
	// observed by the engine that detected it: 0 means the upstream failed
	// before producing any content, > 0 means it failed mid-generation.
	//
	// It counts the chunks that engine received, which is not necessarily the
	// upstream's raw SSE event number: the client skips SSE comments and empty
	// events, and engines in between (converters, moderators) may split,
	// buffer or merge chunks.
	ChunkIndex int
}

func (e *UpstreamChunkError) Error() string {
	// Truncate for error message, aligned with UpstreamRespError.
	raw := e.ChunkBody
	if len(raw) > 1024 {
		return fmt.Sprintf("upstream chunk error at chunk %d: %s (truncated)", e.ChunkIndex, string(raw[:1024]))
	}
	return fmt.Sprintf("upstream chunk error at chunk %d: %s", e.ChunkIndex, string(raw))
}
