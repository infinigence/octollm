// Structs in this file have AI-generated jlexer parsers in:
//   messages_response_jlexer.go
// If you modify any struct definition below, regenerate the jlexer file
// following the guide at pkg/types/JLEXER_PARSER_GUIDE.md.
// See the header comment of messages_response_jlexer.go for the source hash.

package anthropic

import (
	"encoding/json"
)

// ClaudeMessagesResponse represents a complete Anthropic Messages API response
type ClaudeMessagesResponse struct {
	ID           string                   `json:"id"`
	Type         string                   `json:"type"`
	Role         string                   `json:"role"`
	Content      MessageContentBlockArray `json:"content"`
	Model        string                   `json:"model"`
	StopReason   string                   `json:"stop_reason,omitempty"`
	StopSequence *string                  `json:"stop_sequence,omitempty"`
	Usage        *Usage                   `json:"usage"`
}

// Usage represents token usage information
type Usage struct {
	// Total input tokens
	InputTokens *int64 `json:"input_tokens,omitempty"`

	// Total output tokens
	OutputTokens *int64 `json:"output_tokens,omitempty"`

	// Tokens from cache creation (prompt caching)
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens,omitempty"`

	// Tokens from cache read (prompt caching)
	CacheReadInputTokens *int64 `json:"cache_read_input_tokens,omitempty"`
}

// ClaudeMessagesStreamEvent represents a streaming event
// Aligned with MessageStreamEventUnion from SDK
type ClaudeMessagesStreamEvent struct {
	// Event type: "message_start", "content_block_start", "content_block_delta",
	// "content_block_stop", "message_delta", "message_stop", "ping", "error"
	Type string `json:"type"`

	// For message_start event
	Message *ClaudeMessagesResponse `json:"message,omitempty"`

	// For content_block_start event
	Index *int `json:"index,omitempty"`

	ContentBlock MessageContentBlockParam `json:"content_block,omitempty"`

	// Delta is included in both content_block_delta and message_delta events.
	// ContentBlockDelta fields are populated for content_block_delta;
	// MessageDelta fields are populated for message_delta.
	Delta *DeltaUnion `json:"delta,omitempty"`
	Usage *Usage      `json:"usage,omitempty"`
	Error *APIError   `json:"error,omitempty"`
}

// UnmarshalJSON implements custom JSON unmarshaling for ClaudeMessagesStreamEvent
func (e *ClaudeMessagesStreamEvent) UnmarshalJSON(data []byte) error {
	type Alias ClaudeMessagesStreamEvent
	aux := struct {
		ContentBlock messageContentBlockParamField `json:"content_block,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(e),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	e.ContentBlock = aux.ContentBlock.Value
	return nil
}

type DeltaUnion struct {
	ContentBlockDelta
	MessageDelta
}

// ContentBlockDelta represents incremental content updates
type ContentBlockDelta struct {
	Type string `json:"type,omitempty"` // "text_delta", "input_json_delta", "thinking_delta", "signature_delta"
	// "citation_delta" not supported yet. If needed, define an Type 2 polymorphic union for citation field.

	// For text_delta
	Text *string `json:"text,omitempty"`

	// For input_json_delta (tool use)
	PartialJSON *string `json:"partial_json,omitempty"`

	// For thinking_delta
	Thinking *string `json:"thinking,omitempty"`

	// For signature_delta
	Signature *string `json:"signature,omitempty"`
}

// MessageDelta represents message-level delta updates
type MessageDelta struct {
	StopReason   *string `json:"stop_reason,omitempty"`
	StopSequence *string `json:"stop_sequence,omitempty"`
}

// APIError represents an error response
type APIError struct {
	Type    string `json:"type"` // e.g., "invalid_request_error"
	Message string `json:"message"`
}

// ExtractText extracts all text content from the response
func (r *ClaudeMessagesResponse) ExtractText() string {
	return r.Content.ExtractText()
}

// ExtractToolUses extracts all tool use blocks from the response
func (r *ClaudeMessagesResponse) ExtractToolUses() []*ToolUseBlockParam {
	var toolUses []*ToolUseBlockParam
	for _, block := range r.Content {
		if b, ok := block.(*ToolUseBlockParam); ok {
			toolUses = append(toolUses, b)
		}
	}
	return toolUses
}
