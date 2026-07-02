// Structs in this file have AI-generated jlexer parsers in:
//   messages_request_jlexer.go
// If you modify any struct definition below, regenerate the jlexer file
// following the guide at pkg/types/JLEXER_PARSER_GUIDE.md.
// See the header comment of messages_request_jlexer.go for the source hash.

package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ClaudeMessagesRequest represents a complete Anthropic Messages API request
type ClaudeMessagesRequest struct {
	MaxTokens   int64           `json:"max_tokens"`
	Messages    []*MessageParam `json:"messages"`
	Model       string          `json:"model"`
	System      SystemContent   `json:"system,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopK        *int64          `json:"top_k,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`

	StopSequences []string          `json:"stop_sequences,omitempty"`
	Stream        *bool             `json:"stream,omitempty"`
	Metadata      *Metadata         `json:"metadata,omitempty"`
	Thinking      *ThinkingConfig   `json:"thinking,omitempty"`
	Tools         []*ToolDefinition `json:"tools,omitempty"`
	ToolChoice    *ToolChoice       `json:"tool_choice,omitempty"`
	// ServiceTier   *string           `json:"service_tier,omitempty"`

	// AnthropicBeta json.RawMessage `json:"anthropic_beta,omitempty"` // Bedrock 格式需要，直接透传
	// StreamOptions    *StreamOptions  `json:"stream_options,omitempty"`
	// AnthropicVersion string `json:"anthropic_version,omitempty"`
}

type StreamOptions struct {
	IncludeUsage         *bool `json:"include_usage,omitempty"`
	ContinuousUsageStats *bool `json:"continuous_usage_stats,omitempty"`
}

// SystemContent is an interface for system content (string or array of blocks)
type SystemContent interface {
	isSystemContent()
}

// SystemString represents simple string system content
type SystemString string

func (SystemString) isSystemContent() {}

// SystemBlocks represents an array of system blocks
type SystemBlocks []SystemBlock

func (SystemBlocks) isSystemContent() {}

// SystemBlock represents a system prompt text block
type SystemBlock struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`

	// Citations for sources (for document/web search results)
	Citations []json.RawMessage `json:"citations,omitempty"`

	// Cache control for prompt caching
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// MessageParam represents an input message
type MessageParam struct {
	Role    string         `json:"role"` // "user" or "assistant"
	Content MessageContent `json:"content"`
	Name    string         `json:"name,omitempty"`
}

// MessageContent is an interface for message content (string or content block)
// can be MessageContentString or MessageContentBlockArray
type MessageContent interface {
	// ExtractText extracts text content if available
	ExtractText() string
}

// MessageContentString represents simple string content
type MessageContentString string

func (m MessageContentString) ExtractText() string { return string(m) }

// MessageContentBlockArray represents an array of content blocks
type MessageContentBlockArray []MessageContentBlockParam

func (m MessageContentBlockArray) ExtractText() string {
	var sb strings.Builder
	for _, block := range m {
		if block == nil {
			continue
		}
		sb.WriteString(block.ExtractText())
	}
	return sb.String()
}

func (m *MessageContentBlockArray) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || len(data) == 0 {
		return nil
	}
	var fields []messageContentBlockParamField
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*m = make(MessageContentBlockArray, len(fields))
	for i, field := range fields {
		(*m)[i] = field.Value
	}
	return nil
}

// MessageContentBlockParam is an interface for content block parameters
// can be TextBlockParam, ImageBlockParam, DocumentBlockParam, ThinkingBlockParam, RedactedThinkingBlockParam
// ToolUseBlockParam, ToolResultBlockParam, GeneralBlockParam (with type field only)
type MessageContentBlockParam interface {
	ExtractText() string
}

// --- Concrete MessageContentBlockParam implementations ---

// TextBlockParam represents a text content block
type TextBlockParam struct {
	Type         string        `json:"type"` // "text"
	Text         string        `json:"text"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
	Citations    []*Citation   `json:"citations,omitempty"`
}

func (b *TextBlockParam) ExtractText() string { return b.Text }

// ImageBlockParam represents an image content block
type ImageBlockParam struct {
	Type         string                `json:"type"` // "image"
	Source       *MessageContentSource `json:"source"`
	CacheControl *CacheControl         `json:"cache_control,omitempty"`
}

func (b *ImageBlockParam) ExtractText() string {
	if b.Source == nil {
		return ""
	}
	return b.Source.ExtractText()
}

// DocumentBlockParam represents a document content block
type DocumentBlockParam struct {
	Type         string                `json:"type"` // "document"
	Source       *MessageContentSource `json:"source"`
	CacheControl *CacheControl         `json:"cache_control,omitempty"`
	Citations    *DocumentCitations    `json:"citations,omitempty"` // CitationsConfigParam
	Context      string                `json:"context,omitempty"`
	Title        string                `json:"title,omitempty"`
}

func (b *DocumentBlockParam) ExtractText() string {
	var sb strings.Builder
	if b.Title != "" {
		sb.WriteString(b.Title)
		sb.WriteString(":")
	}
	if b.Context != "" {
		sb.WriteString(b.Context)
		sb.WriteString(":")
	}
	if b.Source != nil {
		sb.WriteString(b.Source.ExtractText())
	}
	return sb.String()
}

// ThinkingBlockParam represents a thinking content block
type ThinkingBlockParam struct {
	Type      string `json:"type"` // "thinking"
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

func (b *ThinkingBlockParam) ExtractText() string { return b.Thinking }

// RedactedThinkingBlockParam represents a redacted thinking content block
type RedactedThinkingBlockParam struct {
	Type string `json:"type"` // "redacted_thinking"
	Data string `json:"data"`
}

func (b *RedactedThinkingBlockParam) ExtractText() string { return "" }

// ToolUseBlockParam represents a tool_use content block
type ToolUseBlockParam struct {
	Type         string          `json:"type"` // "tool_use"
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Input        json.RawMessage `json:"input"`
	CacheControl *CacheControl   `json:"cache_control,omitempty"`
}

func (b *ToolUseBlockParam) ExtractText() string { return string(b.Input) }

// ToolResultBlockParam represents a tool_result content block
type ToolResultBlockParam struct {
	Type         string         `json:"type"` // "tool_result"
	ToolUseID    string         `json:"tool_use_id"`
	Content      MessageContent `json:"content,omitempty"`
	IsError      *bool          `json:"is_error,omitempty"`
	CacheControl *CacheControl  `json:"cache_control,omitempty"`
}

func (b *ToolResultBlockParam) ExtractText() string {
	if b.Content == nil {
		return ""
	}
	return b.Content.ExtractText()
}

func (b *ToolResultBlockParam) UnmarshalJSON(data []byte) error {
	type Alias ToolResultBlockParam
	aux := struct {
		Content messageContentField `json:"content,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(b),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	b.Content = aux.Content.Value
	return nil
}

// GeneralBlockParam is a fallback for unrecognized block types
type GeneralBlockParam struct {
	Type string `json:"type"`
}

func (b *GeneralBlockParam) ExtractText() string { return "" }

// --- Field helpers for polymorphic dispatch ---

// messageContentBlockParamField dispatches a single ContentBlockParam by type
type messageContentBlockParamField struct {
	Value MessageContentBlockParam
}

func (f *messageContentBlockParamField) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || len(data) == 0 {
		return nil
	}
	var peek struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &peek); err != nil {
		return err
	}
	var v MessageContentBlockParam = &GeneralBlockParam{}
	switch peek.Type {
	case "text":
		v = &TextBlockParam{}
	case "image":
		v = &ImageBlockParam{}
	case "document":
		v = &DocumentBlockParam{}
	case "thinking":
		v = &ThinkingBlockParam{}
	case "redacted_thinking":
		v = &RedactedThinkingBlockParam{}
	case "tool_use":
		v = &ToolUseBlockParam{}
	case "tool_result":
		v = &ToolResultBlockParam{}
	}
	if err := json.Unmarshal(data, v); err != nil {
		return err
	}
	f.Value = v
	return nil
}

type MessageContentSource struct {
	Type      string           `json:"type"`
	MediaType string           `json:"media_type,omitempty"`
	Data      json.RawMessage  `json:"data,omitempty"`
	Content   []MessageContent `json:"content,omitempty"`
	Url       string           `json:"url,omitempty"`
}

func (s *MessageContentSource) ExtractText() string {
	switch s.Type {
	case "url":
		return s.Url
	case "base64":
		return fmt.Sprintf("(base64_inline_%s)", s.MediaType)
	case "text":
		return string(s.Data)
	case "content":
		sb := strings.Builder{}
		for _, c := range s.Content {
			if c != nil {
				sb.WriteString(c.ExtractText())
			}
		}
		return sb.String()
	default:
		return ""
	}
}

type DocumentCitations struct {
	Enabled bool `json:"enabled"`
}

type Citation struct {
	Type          string `json:"type"`
	CitedText     string `json:"cited_text"`
	DocumentIndex int    `json:"document_index"`
	DocumentTitle string `json:"document_title,omitempty"`

	// For char_location citations
	StartCharIndex *int `json:"start_char_index,omitempty"`
	EndCharIndex   *int `json:"end_char_index,omitempty"`

	// For page_number citations
	StartPage *int `json:"start_page,omitempty"`
	EndPage   *int `json:"end_page,omitempty"`

	// For block_index citations
	StartBlockIndex *int `json:"start_block_index,omitempty"`
	EndBlockIndex   *int `json:"end_block_index,omitempty"`
}

// CacheControl for prompt caching
type CacheControl struct {
	Type string  `json:"type"`          // "ephemeral"
	TTL  *string `json:"ttl,omitempty"` // "5m" or "1h"
}

// Metadata about the request
type Metadata struct {
	UserID *string `json:"user_id,omitempty"`
}

// ThinkingConfig for extended thinking
type ThinkingConfig struct {
	Type         string `json:"type"`                    // "enabled"
	BudgetTokens *int64 `json:"budget_tokens,omitempty"` // Minimum 1024 tokens
}

// ToolDefinition for defining tools that the model can use
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`

	CacheControl *CacheControl `json:"cache_control,omitempty"`

	MaxCharacters int `json:"max_characters,omitempty"`

	// Type is required for Anthropic defined tools.
	Type string `json:"type,omitempty"`
	// DisplayWidthPx is a required parameter of the Computer Use tool.
	DisplayWidthPx int `json:"display_width_px,omitempty"`
	// DisplayHeightPx is a required parameter of the Computer Use tool.
	DisplayHeightPx int `json:"display_height_px,omitempty"`
	// DisplayNumber is an optional parameter of the Computer Use tool.
	DisplayNumber *int `json:"display_number,omitempty"`

	AllowedDomains []string      `json:"allowed_domains,omitempty"`
	BlockedDomains []string      `json:"blocked_domains,omitempty"`
	MaxUses        int           `json:"max_uses,omitempty"`
	UserLocation   *UserLocation `json:"user_location,omitempty"`
}

type UserLocation struct {
	Type     *string `json:"type,omitempty"`
	Country  *string `json:"country,omitempty"`
	City     *string `json:"city,omitempty"`
	Region   *string `json:"region,omitempty"`
	Timezone *string `json:"timezone,omitempty"`
}

// ToolChoice controls how the model uses tools
type ToolChoice struct {
	// Type: "auto", "any", "tool"
	Type string `json:"type"`

	// For type="tool", specify the tool name
	Name *string `json:"name,omitempty"`

	// Disable parallel tool use
	DisableParallelToolUse *bool `json:"disable_parallel_tool_use,omitempty"`
}

func (r *ClaudeMessagesRequest) UnmarshalJSON(data []byte) error {
	type Alias ClaudeMessagesRequest
	aux := struct {
		System systemField `json:"system,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(r),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	r.System = aux.System.Value
	return nil
}

type systemField struct {
	Value SystemContent
}

func (f *systemField) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		f.Value = SystemString(s)
		return nil
	}
	var blocks []SystemBlock
	if err := json.Unmarshal(data, &blocks); err != nil {
		return err
	}
	f.Value = SystemBlocks(blocks)
	return nil
}

type messageContentField struct {
	Value MessageContent
}

func (f *messageContentField) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || len(data) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		f.Value = MessageContentString(s)
		return nil
	}
	// Try as array of ContentBlockParam
	var fields MessageContentBlockArray
	if err := json.Unmarshal(data, &fields); err == nil {
		f.Value = fields
		return nil
	}
	// Try as single ContentBlockParam object
	var field messageContentBlockParamField
	if err := json.Unmarshal(data, &field); err != nil {
		return err
	}
	f.Value = MessageContentBlockArray{field.Value}
	return nil
}

func (m *MessageParam) UnmarshalJSON(data []byte) error {
	type Alias MessageParam
	aux := struct {
		Content messageContentField `json:"content"`
		*Alias
	}{
		Alias: (*Alias)(m),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	m.Content = aux.Content.Value
	if m.Content == nil {
		return fmt.Errorf("content field cannot be null or empty")
	}
	return nil
}
