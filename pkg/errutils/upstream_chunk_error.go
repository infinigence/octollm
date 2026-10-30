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
	// ErrorJSON is the raw error object carried by the chunk, for matching.
	ErrorJSON json.RawMessage
	// Header is the header of the upstream response that produced the chunk,
	// so the caller can answer with the same upstream metadata (e.g. rate-limit
	// hints) as it does for a non-200 upstream response. Nil until filled in by
	// the engine that still holds the response.
	Header http.Header
	// Body is the full raw chunk, for passthrough bodies.
	Body json.RawMessage
}

func (e *UpstreamChunkError) Error() string {
	// Truncate for error message, aligned with UpstreamRespError.
	raw := e.ErrorJSON
	if len(raw) > 1024 {
		return fmt.Sprintf("upstream chunk error: %s (truncated)", string(raw[:1024]))
	}
	return "upstream chunk error: " + string(raw)
}
