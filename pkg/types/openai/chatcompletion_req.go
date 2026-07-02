// Structs in this file have AI-generated jlexer parsers in:
//   chatcompletion_req_jlexer.go
// If you modify any struct definition below, regenerate the jlexer file
// following the guide at pkg/types/JLEXER_PARSER_GUIDE.md.
// See the header comment of chatcompletion_req_jlexer.go for the source hash.

package openai

import (
	"encoding/json"
	"fmt"
)

type ChatCompletionRequest struct {
	Model               string          `json:"model"`
	Messages            []*Message      `json:"messages" binding:"required"`
	Thinking            *Thinking       `json:"thinking,omitempty"`
	MaxTokens           *int            `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int            `json:"max_completion_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	TopK                *int            `json:"top_k,omitempty"`
	Stop                StopValue       `json:"stop,omitempty"`
	Stream              *bool           `json:"stream,omitempty"`
	Tools               []*Tool         `json:"tools,omitempty"` // 可用函数工具列表
	ToolChoice          ToolChoiceValue `json:"tool_choice,omitempty"` // 指定强制调用的函数（可选）
}

func (r *ChatCompletionRequest) UnmarshalJSON(data []byte) error {
	type Alias ChatCompletionRequest
	aux := struct {
		Stop       stopField       `json:"stop,omitempty"`
		ToolChoice toolChoiceField `json:"tool_choice,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(r),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	r.Stop = aux.Stop.Value
	r.ToolChoice = aux.ToolChoice.Value
	return nil
}

type Thinking struct {
	Type string `json:"type"`
}

type Message struct {
	Role             string             `json:"role" binding:"required"`
	Content          MessageContent     `json:"content,omitempty"`
	ReasoningContent MessageContent     `json:"reasoning_content,omitempty"`
	Name             string             `json:"name,omitempty"`
	ToolCalls        []*MessageToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string             `json:"tool_call_id,omitempty"`
}

type MessageToolCall struct {
	ID       string            `json:"id" binding:"required"`
	Index    int               `json:"index" binding:"required"`
	Type     string            `json:"type" binding:"required"`
	Function *ToolCallFunction `json:"function" binding:"required"`
}

type ToolCallFunction struct {
	Name      string `json:"name" binding:"required"`
	Arguments string `json:"arguments" binding:"required"`
}

func (m *Message) UnmarshalJSON(data []byte) error {
	type Alias Message // define alias type to avoid infinite recursion on UnmarshalJSON method
	aux := struct {
		Content          messageContentField `json:"content"`
		ReasoningContent messageContentField `json:"reasoning_content"`
		*Alias
	}{
		Alias: (*Alias)(m),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	m.Content = aux.Content.Value
	m.ReasoningContent = aux.ReasoningContent.Value
	return nil
}

type MessageContent interface {
	ExtractText() string
}

type messageContentField struct {
	Value MessageContent
}

func (m *messageContentField) UnmarshalJSON(data []byte) error {
	// 尝试解析为字符串
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		m.Value = MessageContentString(str)
		return nil
	}

	// 尝试解析为数组
	var arr []*MessageContentItem
	if err := json.Unmarshal(data, &arr); err == nil {
		m.Value = MessageContentArray(arr)
		return nil
	}

	return nil
}

type MessageContentString string

func (m MessageContentString) ExtractText() string { return string(m) }

type MessageContentArray []*MessageContentItem

func (m MessageContentArray) ExtractText() string {
	text := ""
	for _, item := range m {
		if item == nil {
			continue
		}
		switch item.Type {
		case "text":
			text += item.Text
		case "image_url":
			if item.ImageURL != nil {
				text += fmt.Sprintf("[img:%s]", item.ImageURL.GetImageUrl())
			}
		case "file":
			if item.File != nil {
				text += fmt.Sprintf("[file:%s]", item.File.FileURI)
			}
		}
	}
	return text
}

type MessageContentItem struct {
	Type       string                        `json:"type" binding:"required"`
	Text       string                        `json:"text,omitempty"`
	ImageURL   ImageURLContent               `json:"image_url,omitempty"`
	VideoURL   *MessageContentItemVideoURL   `json:"video_url,omitempty"`
	AudioURL   *MessageContentItemAudioURL   `json:"audio_url,omitempty"`
	InputAudio *MessageContentItemInputAudio `json:"input_audio,omitempty"`
	File       *MessageContentItemFile       `json:"file,omitempty"` // Added for Gemini file support
}

func (m *MessageContentItem) UnmarshalJSON(data []byte) error {
	type Alias MessageContentItem
	aux := struct {
		ImageURL imageURLField `json:"image_url,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(m),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	m.ImageURL = aux.ImageURL.Value
	return nil
}

type ImageURLContent interface {
	GetImageUrl() string
}

type imageURLField struct {
	Value ImageURLContent
}

func (i *imageURLField) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		i.Value = MessageContentItemImageURLString(str)
		return nil
	}

	var obj MessageContentItemImageURL
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	i.Value = &obj
	return nil
}

type MessageContentItemImageURL struct {
	URL    string `json:"url" binding:"required"`
	Detail string `json:"detail,omitempty"`
}

func (i *MessageContentItemImageURL) GetImageUrl() string {
	return i.URL
}

// MessageContentItemImageURLString image_url 为 string 类型
type MessageContentItemImageURLString string

func (i MessageContentItemImageURLString) GetImageUrl() string {
	return string(i)
}

type MessageContentItemVideoURL struct {
	URL string `json:"url" binding:"required"`
}

type MessageContentItemAudioURL struct {
	URL string `json:"url" binding:"required"`
}

type MessageContentItemInputAudio struct {
	Data   string `json:"data" binding:"required"`
	Format string `json:"format" binding:"required"`
}

// MessageContentItemFile Added for Gemini file support
type MessageContentItemFile struct {
	FileURI  string `json:"file_uri" binding:"required"`
	MIMEType string `json:"mime_type,omitempty"`
}

// Tool 可用工具定义
type Tool struct {
	Type     string        `json:"type,omitempty"`     // 类型（固定为"function"）
	Function *ToolFunction `json:"function,omitempty"` // 函数元数据
	Custom   *CustomTool   `json:"custom,omitempty"`   // 自定义工具
}

// ToolFunction 函数元数据（名称、描述、参数Schema）
type ToolFunction struct {
	Name        *string         `json:"name,omitempty"`        // 函数唯一标识
	Description *string         `json:"description,omitempty"` // 功能描述
	Parameters  json.RawMessage `json:"parameters,omitempty"`  // JSON Schema格式参数定义
}

type CustomTool struct {
	Name        string          `json:"name,omitempty"`        // 工具名称
	Description string          `json:"description,omitempty"` // 工具描述
	Format      json.RawMessage `json:"format,omitempty"`      // 输入参数Schema
}

type ToolChoiceValue interface {
	isToolChoiceValue()
}

type ToolChoiceString string

func (ToolChoiceString) isToolChoiceValue() {}

type ToolChoiceObject struct {
	Type         string                  `json:"type"`
	Function     *ToolChoiceFunction     `json:"function,omitempty"`
	AllowedTools *ToolChoiceAllowedTools `json:"allowed_tools,omitempty"`
	Custom       *ToolChoiceCustom       `json:"custom,omitempty"`
}

func (ToolChoiceObject) isToolChoiceValue() {}

// ToolChoiceFunction function 工具选择的参数
type ToolChoiceFunction struct {
	Name string `json:"name"` // 函数名称
}

// ToolChoiceAllowedTools allowed_tools 类型的工具选择
type ToolChoiceAllowedTools struct {
	Mode  string                   `json:"mode"`  // "auto" 或 "required"
	Tools []map[string]interface{} `json:"tools"` // 工具定义列表
}

// ToolChoiceCustom custom 类型的工具选择
type ToolChoiceCustom struct {
	Name string `json:"name"` // 自定义工具名称
}

type toolChoiceField struct {
	Value ToolChoiceValue
}

func (t *toolChoiceField) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		t.Value = ToolChoiceString(str)
		return nil
	}
	var obj ToolChoiceObject
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	t.Value = obj
	return nil
}

type StopValue interface {
	isStopValue()
}

type StopString string

func (StopString) isStopValue() {}

type StopArray []string

func (StopArray) isStopValue() {}

type stopField struct {
	Value StopValue
}

func (s *stopField) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		s.Value = StopString(str)
		return nil
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return err
	}
	s.Value = StopArray(arr)
	return nil
}
