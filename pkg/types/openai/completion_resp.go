package openai

import "encoding/json"

// CompletionResponse represents a completion response
// For non-streaming: object = "text_completion", usage always present
// For streaming: object = "text_completion.chunk", usage only in final chunk
type CompletionResponse struct {
	ID                string             `json:"id"`
	Created           int                `json:"created"`
	Model             string             `json:"model"`
	Object            string             `json:"object"` // "text_completion" or "text_completion.chunk"
	Choices           []CompletionChoice `json:"choices"`
	Usage             *Usage             `json:"usage,omitempty"`
	SystemFingerprint *string            `json:"system_fingerprint,omitempty"`

	// Error carries the raw error object of an in-stream error chunk (see
	// ChatCompletionStreamChunk.Error).
	Error json.RawMessage `json:"error,omitempty"`
}

// CompletionStreamChunk is an alias for CompletionResponse
type CompletionStreamChunk = CompletionResponse

// CompletionChoice represents a single completion choice
type CompletionChoice struct {
	Text         string              `json:"text"`
	Index        int                 `json:"index"`
	LogProbs     *CompletionLogProbs `json:"logprobs,omitempty"`
	FinishReason *string             `json:"finish_reason,omitempty"` // "stop", "length", "content_filter" 或 null
}

// CompletionLogProbs represents logprobs information
type CompletionLogProbs struct {
	Tokens        []string             `json:"tokens,omitempty"`
	TokenLogProbs []float64            `json:"token_logprobs,omitempty"`
	TopLogProbs   []map[string]float64 `json:"top_logprobs,omitempty"`
	TextOffset    []int                `json:"text_offset,omitempty"`
}
