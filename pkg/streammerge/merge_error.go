package streammerge

import "fmt"

// BadChunkError indicates that a chunk failed to merge — currently, that a
// data chunk carries a non-null top-level "error" payload ({error:{...}}).
//
// It is deliberately raw: it carries only the chunk bytes. The merger
// knows what a bad chunk is, but it neither sees the upstream response
// (so no header) nor decides what the error means (so no parsed error
// object, no HTTP status). Both belong to the caller; typically the engine
// that still holds the response (e.g. engines.NonStreamToStreamEngine)
// translates it into the caller-owned errutils.UpstreamChunkError.
type BadChunkError struct {
	// ChunkBody is the full raw chunk.
	ChunkBody []byte
}

func (e *BadChunkError) Error() string {
	// Truncate for error message, aligned with errutils.UpstreamRespError.
	if len(e.ChunkBody) > 1024 {
		return fmt.Sprintf("upstream chunk error: %s (truncated)", string(e.ChunkBody[:1024]))
	}
	return "upstream chunk error: " + string(e.ChunkBody)
}
