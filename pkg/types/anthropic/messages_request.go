package anthropic

import (
	"encoding/json"
	"fmt"
)

// MessagesRequest represents a complete Anthropic Messages API request
type ClaudeMessagesRequest struct {
	MaxTokens   int64           `json:"max_tokens"`
	Messages    []*MessageParam `json:"messages"`
	Model       string          `json:"model"`
	System      SystemContent   `json:"-"`
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

func (s SystemString) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(s))
}

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
	// Role: "user" or "assistant"
	Role    string           `json:"role"`
	Content []MessageContent `json:"content"`
	Name    string           `json:"name,omitempty"`
}

// MessageContent is an interface for message content (string or content block)
type MessageContent interface {
	// GetType returns the content type (e.g., "text", "image")
	GetType() string
	// ExtractText extracts text content if available
	ExtractText() string
}

// MessageContentString represents simple string content
type MessageContentString string

func (m MessageContentString) GetType() string     { return "text" }
func (m MessageContentString) ExtractText() string { return string(m) }

func (m MessageContentString) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(m))
}

// MessageContentBlock represents a content block object
type MessageContentBlock struct {
	Type string `json:"type"`

	Text *string `json:"text,omitempty"`

	Source *MessageContentSource `json:"source,omitempty"`

	*MessageContentToolResult

	*MessageContentToolUse

	PartialJson *string `json:"partial_json,omitempty"`

	CacheControl *MessageCacheControl `json:"cache_control,omitempty"`

	Citations []Citation `json:"citations_list,omitempty"`

	Citation *Citation `json:"citation_delta,omitempty"`

	// For document content
	Title             string             `json:"title,omitempty"`
	Context           string             `json:"context,omitempty"`
	DocumentCitations *DocumentCitations `json:"citations,omitempty"` // Used in requests

	// Thinking-related fields
	*MessageContentThinking

	*MessageContentRedactedThinking
}

func (m *MessageContentBlock) GetType() string {
	if m == nil {
		return ""
	}
	return m.Type
}
func (m *MessageContentBlock) ExtractText() string {
	if m == nil || m.Text == nil {
		return ""
	}
	return *m.Text
}

type MessageContentToolUse struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type MessageContentToolResult struct {
	ToolUseID *string          `json:"tool_use_id,omitempty"`
	Content   []MessageContent `json:"content,omitempty"`
	IsError   *bool            `json:"is_error,omitempty"`
}

type MessageContentSource struct {
	Type      string           `json:"type"`
	MediaType string           `json:"media_type,omitempty"`
	Data      any              `json:"data,omitempty"`
	Content   []MessageContent `json:"content,omitempty"`
	Url       string           `json:"url,omitempty"`
}

type DocumentCitations struct {
	Enabled bool `json:"enabled"`
}

type MessageContentThinking struct {
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

type MessageContentRedactedThinking struct {
	Data string `json:"data,omitempty"`
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

// ApiContentBlock represents a content block in a message
type ApiContentBlock struct {
	// Type: "text", "image", "tool_use", "tool_result", "document"
	Type string `json:"type"`

	// For text blocks
	Text *string `json:"text,omitempty"`

	// For image blocks
	Source *ImageSource `json:"source,omitempty"`

	// For tool_use blocks
	ID    *string         `json:"id,omitempty"`
	Name  *string         `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// For tool_result blocks
	ToolUseID *string         `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"` // string or array
	IsError   *bool           `json:"is_error,omitempty"`

	// Cache control
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// ImageSource represents an image source (base64 or URL)
type ImageSource struct {
	Type      string  `json:"type"`       // "base64" or "url"
	MediaType *string `json:"media_type"` // "image/jpeg", "image/png", etc.
	Data      *string `json:"data,omitempty"`
	URL       *string `json:"url,omitempty"`
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

// Tool definition
type ToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema,omitempty"`

	CacheControl *MessageCacheControl `json:"cache_control,omitempty"`

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

type MessageCacheControl struct {
	Type string  `json:"type"`
	TTL  *string `json:"ttl,omitempty"`
}

func (m ClaudeMessagesRequest) MarshalJSON() ([]byte, error) {
	type Alias ClaudeMessagesRequest
	aux := struct {
		System interface{} `json:"system,omitempty"`
		Alias
	}{
		Alias: (Alias)(m),
	}

	if m.System != nil {
		aux.System = m.System
	}

	return json.Marshal(aux)
}

func (m *ClaudeMessagesRequest) UnmarshalJSON(data []byte) error {
	type Alias ClaudeMessagesRequest
	aux := &struct {
		System json.RawMessage `json:"system,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(m),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Parse System field
	if len(aux.System) > 0 {
		// Try to unmarshal as string first
		var systemStr string
		if err := json.Unmarshal(aux.System, &systemStr); err == nil {
			m.System = SystemString(systemStr)
			return nil
		}

		// Try to unmarshal as array of system blocks
		var systemBlocks []SystemBlock
		if err := json.Unmarshal(aux.System, &systemBlocks); err == nil {
			m.System = SystemBlocks(systemBlocks)
			return nil
		}

		return fmt.Errorf("system must be a string or an array of system blocks")
	}

	return nil
}

func (m *MessageContentBlock) UnmarshalJSON(data []byte) error {
	type Alias MessageContentBlock
	aux := &struct {
		Citations []Citation      `json:"citations"`
		Content   json.RawMessage `json:"content,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(m),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Copy Citations from aux to m
	m.Citations = aux.Citations

	// Handle nested content for tool_result type
	if m.Type == "tool_result" && len(aux.Content) > 0 {
		// Initialize MessageContentToolResult if needed
		if m.MessageContentToolResult == nil {
			m.MessageContentToolResult = &MessageContentToolResult{}
		}

		// Parse the nested content field
		// It can be a string or an array of content blocks
		var contentStr string
		if err := json.Unmarshal(aux.Content, &contentStr); err == nil {
			// Content is a string
			m.MessageContentToolResult.Content = []MessageContent{MessageContentString(contentStr)}
			return nil
		}

		// Try to parse as array of content blocks
		var contentArray []*MessageContentBlock
		if err := json.Unmarshal(aux.Content, &contentArray); err == nil {
			m.MessageContentToolResult.Content = make([]MessageContent, len(contentArray))
			for i, block := range contentArray {
				m.MessageContentToolResult.Content[i] = block
			}
			return nil
		}

		// Try to parse as single content block
		var contentSingle MessageContentBlock
		if err := json.Unmarshal(aux.Content, &contentSingle); err == nil {
			m.MessageContentToolResult.Content = []MessageContent{&contentSingle}
			return nil
		}

		return fmt.Errorf("tool_result content must be a string, object, or array of objects")
	}

	return nil
}

func (m *MessageParam) UnmarshalJSON(data []byte) error {
	// First try to unmarshal as normal structure with content array
	type Alias MessageParam
	aux := &struct {
		Content json.RawMessage `json:"content"`
		*Alias
	}{
		Alias: (*Alias)(m),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Handle null or empty content
	if len(aux.Content) == 0 || string(aux.Content) == "null" {
		return fmt.Errorf("content field cannot be null or empty")
	}

	// Check if content is a string
	var contentStr string
	if err := json.Unmarshal(aux.Content, &contentStr); err == nil {
		// Content is a string, convert to MessageContentString
		m.Content = []MessageContent{MessageContentString(contentStr)}
		return nil
	}

	// Try to unmarshal as array of content blocks
	var contentArray []*MessageContentBlock
	if err := json.Unmarshal(aux.Content, &contentArray); err == nil {
		m.Content = make([]MessageContent, len(contentArray))
		for i, block := range contentArray {
			m.Content[i] = block
		}
		return nil
	}

	// Try to unmarshal as single content block
	var contentSingle MessageContentBlock
	if err := json.Unmarshal(aux.Content, &contentSingle); err == nil {
		m.Content = []MessageContent{&contentSingle}
		return nil
	}

	// Provide more detailed error information
	return fmt.Errorf("content must be a string, object, or array of objects, got: %s", string(aux.Content))
}

func (m *MessageParam) MarshalJSON() ([]byte, error) {
	type Alias MessageParam
	aux := struct {
		Role    string      `json:"role"`
		Content interface{} `json:"content"`
		Name    string      `json:"name,omitempty"`
	}{
		Role: m.Role,
		Name: m.Name,
	}

	// Convert MessageContent interface slice to appropriate JSON format
	if len(m.Content) == 1 {
		// Single content item - check if it's a string
		if str, ok := m.Content[0].(MessageContentString); ok {
			aux.Content = string(str)
		} else {
			aux.Content = m.Content
		}
	} else {
		aux.Content = m.Content
	}

	return json.Marshal(aux)
}
