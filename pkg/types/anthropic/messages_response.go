package anthropic

import (
	"encoding/json"
)

// ClaudeMessagesResponse represents a complete Anthropic Messages API response
type ClaudeMessagesResponse struct {
	ID           string                 `json:"id"`
	Type         string                 `json:"type"`
	Role         string                 `json:"role"`
	Content      []*MessageContentBlock `json:"content"`
	Model        string                 `json:"model"`
	StopReason   string                 `json:"stop_reason,omitempty"`
	StopSequence *string                `json:"stop_sequence,omitempty"`
	Usage        *Usage                 `json:"usage"`
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

	ContentBlock MessageContent  `json:"content_block,omitempty"`
	DeltaRaw     json.RawMessage `json:"delta,omitempty"`
	Usage        *Usage          `json:"usage,omitempty"`
	Error        *APIError       `json:"error,omitempty"`
}

// UnmarshalJSON implements custom JSON unmarshaling for ClaudeMessagesStreamEvent
func (e *ClaudeMessagesStreamEvent) UnmarshalJSON(data []byte) error {
	type Alias ClaudeMessagesStreamEvent
	aux := struct {
		ContentBlockRaw json.RawMessage `json:"content_block,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(e),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Parse ContentBlock field if present
	if len(aux.ContentBlockRaw) > 0 && string(aux.ContentBlockRaw) != "null" {
		var contentBlock MessageContentBlock
		if err := json.Unmarshal(aux.ContentBlockRaw, &contentBlock); err != nil {
			return err
		}
		e.ContentBlock = &contentBlock
	}

	return nil
}

// GetContentBlockDelta returns the delta as ApiContentBlockDelta if applicable
func (e *ClaudeMessagesStreamEvent) GetContentBlockDelta() (*ApiContentBlockDelta, error) {
	if e.Type != "content_block_delta" || len(e.DeltaRaw) == 0 {
		return nil, nil
	}
	var delta ApiContentBlockDelta
	if err := json.Unmarshal(e.DeltaRaw, &delta); err != nil {
		return nil, err
	}
	return &delta, nil
}

// GetMessageDelta returns the delta as ApiMessageDelta if applicable
func (e *ClaudeMessagesStreamEvent) GetMessageDelta() (*ApiMessageDelta, error) {
	if e.Type != "message_delta" || len(e.DeltaRaw) == 0 {
		return nil, nil
	}
	var delta ApiMessageDelta
	if err := json.Unmarshal(e.DeltaRaw, &delta); err != nil {
		return nil, err
	}
	return &delta, nil
}

// ApiContentBlockDelta represents incremental content updates
type ApiContentBlockDelta struct {
	Type string `json:"type"` // "text_delta", "input_json_delta", "thinking_delta"

	// For text_delta
	Text *string `json:"text,omitempty"`

	// For input_json_delta (tool use)
	PartialJSON *string `json:"partial_json,omitempty"`

	// For thinking_delta
	Thinking *string `json:"thinking,omitempty"`
}

// ApiMessageDelta represents message-level delta updates
type ApiMessageDelta struct {
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
	text := ""
	for _, block := range r.Content {
		if block == nil {
			continue
		}
		if block.Type == "text" && block.Text != nil {
			text += *block.Text
		}
		if block.Type == "thinking" && block.MessageContentThinking != nil {
			text += block.MessageContentThinking.Thinking
		}
	}
	return text
}

// ExtractToolUses extracts all tool use blocks from the response
func (r *ClaudeMessagesResponse) ExtractToolUses() []*MessageContentToolUse {
	var toolUses []*MessageContentToolUse
	for _, block := range r.Content {
		if block == nil {
			continue
		}
		if block.Type == "tool_use" && block.MessageContentToolUse != nil {
			toolUses = append(toolUses, block.MessageContentToolUse)
		}
	}
	return toolUses
}

// IsToolUse checks if the response contains tool use
func (r *ClaudeMessagesResponse) IsToolUse() bool {
	return r.StopReason == "tool_use"
}

// IsError checks if this is an error event
func (e *ClaudeMessagesStreamEvent) IsError() bool {
	return e.Type == "error"
}

// IsMessageStart checks if this is a message start event
func (e *ClaudeMessagesStreamEvent) IsMessageStart() bool {
	return e.Type == "message_start"
}

// IsMessageStop checks if this is a message stop event
func (e *ClaudeMessagesStreamEvent) IsMessageStop() bool {
	return e.Type == "message_stop"
}

// IsContentBlockDelta checks if this is a content block delta event
func (e *ClaudeMessagesStreamEvent) IsContentBlockDelta() bool {
	return e.Type == "content_block_delta"
}

// IsContentBlockStart checks if this is a content block start event
func (e *ClaudeMessagesStreamEvent) IsContentBlockStart() bool {
	return e.Type == "content_block_start"
}

// IsContentBlockStop checks if this is a content block stop event
func (e *ClaudeMessagesStreamEvent) IsContentBlockStop() bool {
	return e.Type == "content_block_stop"
}
