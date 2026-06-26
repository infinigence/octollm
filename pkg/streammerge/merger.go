// Package streammerge accumulates the streamed SSE chunks of an LLM response
// back into a single non-stream response body.
//
// It is the building block behind force-streaming (see
// engines.NonStreamToStreamEngine): the gateway can always talk to the upstream
// in streaming mode and, when the client asked for a non-stream response, fold
// the chunks back into one JSON object. The same mergers are useful anywhere a
// full response must be reconstructed from a stream (logging, moderation, token
// accounting).
//
// Each Merger is single-use and NOT safe for concurrent use: feed every chunk
// of one stream through Merge in order, then call Merged exactly once.
package streammerge

import (
	"errors"

	"github.com/infinigence/octollm/pkg/octollm"
)

// ErrUnsupportedFormat is returned by For when no merger is implemented for the
// requested API format. Callers should treat it as "do not merge" rather than a
// hard failure (e.g. pass the stream through untouched).
var ErrUnsupportedFormat = errors.New("streammerge: no chunk merger for api format")

// Merger folds the chunks of one streamed response into a single response body.
type Merger interface {
	// Merge accumulates a single stream chunk. The [octollm.ErrStreamDone]
	// sentinel (the "[DONE]" terminator) is consumed silently.
	Merge(*octollm.StreamChunk) error
	// Merged returns the assembled non-stream response body. It may be called
	// once after all chunks have been merged; further Merge calls are undefined.
	Merged() (*octollm.UnifiedBody, error)
}

// For returns a fresh Merger for the given API format, or ErrUnsupportedFormat
// if none is implemented. Adding a merger here automatically enables the
// formats that depend on For (e.g. the force-stream engine).
func For(format octollm.APIFormat) (Merger, error) {
	switch format {
	case octollm.APIFormatChatCompletions:
		return NewChatCompletionMerger(), nil
	case octollm.APIFormatClaudeMessages:
		return NewClaudeMessagesMerger(), nil
	default:
		// Responses, Vertex generateContent, completions, etc. are not wired up
		// yet. They fall through to ErrUnsupportedFormat on purpose.
		return nil, ErrUnsupportedFormat
	}
}
