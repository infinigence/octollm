package anthropic

import "encoding/json"

type MessageSimple struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Content      []ContentBlock `json:"content"`
	Model        string         `json:"model"`
	StopReason   *string        `json:"stop_reason,omitempty"`
	StopSequence *string        `json:"stop_sequence,omitempty"`
	Usage        *MessageUsage  `json:"usage,omitempty"`
}

type ContentBlock interface {
	GetContentBlockType() string
}

type ContentBlockText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (c ContentBlockText) GetContentBlockType() string { return c.Type }

type ContentBlockThinking struct {
	Type     string `json:"type"`
	Thinking string `json:"thinking"`
}

func (c ContentBlockThinking) GetContentBlockType() string { return c.Type }

type ContentBlockToolUse struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

func (c ContentBlockToolUse) GetContentBlockType() string { return c.Type }

type MessageUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// StreamEvent is the event type in the Message stream response.
type MessageStreamEvent struct {
	Type         string            `json:"type"`
	Message      *MessageSimple    `json:"message,omitempty"`       // for message_start
	Index        *int              `json:"index,omitempty"`         // for content_block_*
	ContentBlock ContentBlock      `json:"content_block,omitempty"` // for content_block_start
	Delta        ContentBlockDelta `json:"delta,omitempty"`         // for content_block_delta and message_delta
	Usage        *MessageUsage     `json:"usage,omitempty"`         // for message_delta
}

type ContentBlockDelta interface {
	GetContentBlockDeltaType() string
}

type ContentBlockTextDelta struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (c ContentBlockTextDelta) GetContentBlockDeltaType() string { return c.Type }

type ContentBlockThinkingDelta struct {
	Type     string `json:"type"`
	Thinking string `json:"thinking"`
}

func (c ContentBlockThinkingDelta) GetContentBlockDeltaType() string { return c.Type }

type ContentBlockInputJSONDelta struct {
	Type        string `json:"type"`
	PartialJSON string `json:"partial_json"`
}

func (c ContentBlockInputJSONDelta) GetContentBlockDeltaType() string { return c.Type }

type MessageDelta struct {
	StopReason   *string `json:"stop_reason,omitempty"`
	StopSequence *string `json:"stop_sequence,omitempty"`
}

func (c MessageDelta) GetContentBlockDeltaType() string { return "message_delta" }
