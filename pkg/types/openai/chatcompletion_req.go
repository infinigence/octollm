package openai

import (
	"encoding/json"
	"fmt"
)

type ChatCompletionRequest struct {
	Model               string     `json:"model"`
	Messages            []*Message `json:"messages" binding:"required"`
	Thinking            *Thinking  `json:"thinking,omitempty"`
	MaxTokens           *int       `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int       `json:"max_completion_tokens,omitempty"`
	Temperature         *float64   `json:"temperature,omitempty"`
	TopP                *float64   `json:"top_p,omitempty"`
	TopK                *int       `json:"top_k,omitempty"`
	Stop                *StopUnion `json:"stop,omitempty"`
	Stream              *bool      `json:"stream,omitempty"`
	Tools               []*Tool    `json:"tools,omitempty"` // 可用函数工具列表

	ToolChoice *ToolChoice `json:"tool_choice,omitempty"` // 指定强制调用的函数（可选）
}

type Thinking struct {
	Type string `json:"type"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"` // 是否在流式响应中包含 usage 信息
}

type ResponseFormat struct {
	Type       string          `json:"type" binding:"required"`
	JsonSchema json.RawMessage `json:"json_schema,omitempty"`
}

type Message struct {
	Role             string         `json:"role" binding:"required"`
	Content          MessageContent `json:"content,omitempty"`
	ReasoningContent MessageContent `json:"reasoning_content,omitempty"`
	Name             string         `json:"name,omitempty"`
	ToolCalls        []*ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
}

// UnmarshalJSON 实现 Message 的自定义 JSON 反序列化
func (m *Message) UnmarshalJSON(data []byte) error {
	// 定义一个临时结构体，Content 和 ReasoningContent 使用 RawMessage
	type Alias struct {
		Role             string          `json:"role"`
		Content          json.RawMessage `json:"content,omitempty"`
		ReasoningContent json.RawMessage `json:"reasoning_content,omitempty"`
		Name             string          `json:"name,omitempty"`
		ToolCalls        []*ToolCall     `json:"tool_calls,omitempty"`
		ToolCallID       string          `json:"tool_call_id,omitempty"`
	}

	var alias Alias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}

	m.Role = alias.Role
	m.Name = alias.Name
	m.ToolCalls = alias.ToolCalls
	m.ToolCallID = alias.ToolCallID

	// 解析 Content 字段
	if len(alias.Content) > 0 {
		content, err := unmarshalMessageContent(alias.Content)
		if err != nil {
			return err
		}
		m.Content = content
	}

	// 解析 ReasoningContent 字段
	if len(alias.ReasoningContent) > 0 {
		reasoningContent, err := unmarshalMessageContent(alias.ReasoningContent)
		if err != nil {
			return err
		}
		m.ReasoningContent = reasoningContent
	}

	return nil
}

// unmarshalMessageContent 根据 JSON 数据类型解析 MessageContent
func unmarshalMessageContent(data json.RawMessage) (MessageContent, error) {
	// 尝试解析为字符串
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		return MessageContentString(str), nil
	}

	// 尝试解析为数组
	var arr []*MessageContentItem
	if err := json.Unmarshal(data, &arr); err == nil {
		return MessageContentArray(arr), nil
	}

	return nil, nil
}

// MarshalJSON 实现 Message 的自定义 JSON 序列化
func (m Message) MarshalJSON() ([]byte, error) {
	type Alias struct {
		Role             string          `json:"role"`
		Content          json.RawMessage `json:"content,omitempty"`
		ReasoningContent json.RawMessage `json:"reasoning_content,omitempty"`
		Name             string          `json:"name,omitempty"`
		ToolCalls        []*ToolCall     `json:"tool_calls,omitempty"`
		ToolCallID       string          `json:"tool_call_id,omitempty"`
	}

	alias := Alias{
		Role:       m.Role,
		Name:       m.Name,
		ToolCalls:  m.ToolCalls,
		ToolCallID: m.ToolCallID,
	}

	// 序列化 Content
	if m.Content != nil {
		contentBytes, err := json.Marshal(m.Content)
		if err != nil {
			return nil, err
		}
		alias.Content = contentBytes
	}

	// 序列化 ReasoningContent
	if m.ReasoningContent != nil {
		reasoningBytes, err := json.Marshal(m.ReasoningContent)
		if err != nil {
			return nil, err
		}
		alias.ReasoningContent = reasoningBytes
	}

	return json.Marshal(alias)
}

type ExtraBody struct {
	Google json.RawMessage `json:"google,omitempty"`
}

type ExtraPart struct {
	Google json.RawMessage `json:"google,omitempty"`
}

type MessageContent interface {
	ExtractText() string
}

type MessageContentString string

func (m MessageContentString) ExtractText() string { return string(m) }
func (m MessageContentString) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(m))
}

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
func (m MessageContentArray) MarshalJSON() ([]byte, error) {
	return json.Marshal([]*MessageContentItem(m))
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

// UnmarshalJSON 实现 MessageContentItem 的自定义 JSON 反序列化，支持 image_url 为 string 或 struct
func (m *MessageContentItem) UnmarshalJSON(data []byte) error {
	type Alias struct {
		Type       string                        `json:"type"`
		Text       string                        `json:"text,omitempty"`
		ImageURL   json.RawMessage               `json:"image_url,omitempty"`
		VideoURL   *MessageContentItemVideoURL   `json:"video_url,omitempty"`
		AudioURL   *MessageContentItemAudioURL   `json:"audio_url,omitempty"`
		InputAudio *MessageContentItemInputAudio `json:"input_audio,omitempty"`
		File       *MessageContentItemFile       `json:"file,omitempty"`
	}

	var alias Alias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}

	m.Type = alias.Type
	m.Text = alias.Text
	m.VideoURL = alias.VideoURL
	m.AudioURL = alias.AudioURL
	m.InputAudio = alias.InputAudio
	m.File = alias.File

	if len(alias.ImageURL) > 0 {
		imageURL, err := unmarshalImageURLContent(alias.ImageURL)
		if err != nil {
			return err
		}
		m.ImageURL = imageURL
	}

	return nil
}

// unmarshalImageURLContent 根据 JSON 数据类型解析 ImageURLContent（string 或 struct）
func unmarshalImageURLContent(data json.RawMessage) (ImageURLContent, error) {
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		return ImageURLString(str), nil
	}

	var obj MessageContentItemImageURL
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}
	return &obj, nil
}

type MessageContentItemImageURL struct {
	URL    string `json:"url" binding:"required"`
	Detail string `json:"detail,omitempty"`
}

func (i *MessageContentItemImageURL) GetImageUrl() string {
	return i.URL
}

// ImageURLContent 接口，用于统一获取 image_url（支持 string 或 struct 两种形式）
type ImageURLContent interface {
	GetImageUrl() string
}

// ImageURLString image_url 为 string 类型
type ImageURLString string

func (i ImageURLString) GetImageUrl() string {
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
	Type     string       `json:"type,omitempty"`     // 类型（固定为"function"）
	Function ToolFunction `json:"function,omitempty"` // 函数元数据
	Custom   *CustomTool  `json:"custom,omitempty"`   // 自定义工具
}

type CustomTool struct {
	Name        string          `json:"name,omitempty"`        // 工具名称
	Description string          `json:"description,omitempty"` // 工具描述
	Format      json.RawMessage `json:"format,omitempty"`      // 输入参数Schema
}

// ToolChoice 工具选择策略
// 支持 4 种形式：
// 1. 字符串形式："auto", "none", "required"
// 2. 对象形式（function）：{"type": "function", "function": {"name": "xxx"}}
// 3. 对象形式（allowed_tools）：{"type": "allowed_tools", "allowed_tools": {...}}
// 4. 对象形式（custom）：{"type": "custom", "custom": {"name": "xxx"}}
type ToolChoice struct {
	// 字符串形式
	String *string `json:"-"`
	// 对象形式（function）
	Function *ToolChoiceFunction `json:"-"`
	// 对象形式（allowed_tools）
	AllowedTools *ToolChoiceAllowedTools `json:"-"`
	// 对象形式（custom）
	Custom *ToolChoiceCustom `json:"-"`
}

// ToolChoiceFunction function 类型的工具选择
type ToolChoiceFunction struct {
	Type     string                   `json:"type"`     // 固定为 "function"
	Function *ToolChoiceFunctionParam `json:"function"` // 函数选择
}

// ToolChoiceFunctionParam function 工具选择的参数
type ToolChoiceFunctionParam struct {
	Name string `json:"name"` // 函数名称
}

// ToolChoiceAllowedTools allowed_tools 类型的工具选择
type ToolChoiceAllowedTools struct {
	Type         string                       `json:"type"`          // 固定为 "allowed_tools"
	AllowedTools *ToolChoiceAllowedToolsParam `json:"allowed_tools"` // 允许的工具配置
}

// ToolChoiceAllowedToolsParam allowed_tools 的配置参数
type ToolChoiceAllowedToolsParam struct {
	Mode  string                   `json:"mode"`  // "auto" 或 "required"
	Tools []map[string]interface{} `json:"tools"` // 工具定义列表
}

// ToolChoiceCustom custom 类型的工具选择
type ToolChoiceCustom struct {
	Type   string                 `json:"type"`   // 固定为 "custom"
	Custom *ToolChoiceCustomParam `json:"custom"` // 自定义工具选择
}

// ToolChoiceCustomParam custom 工具选择的参数
type ToolChoiceCustomParam struct {
	Name string `json:"name"` // 自定义工具名称
}

// MarshalJSON 实现 ToolChoice 的 JSON 序列化
func (tc ToolChoice) MarshalJSON() ([]byte, error) {
	if tc.String != nil {
		return json.Marshal(*tc.String)
	}
	if tc.Function != nil {
		return json.Marshal(tc.Function)
	}
	if tc.AllowedTools != nil {
		return json.Marshal(tc.AllowedTools)
	}
	if tc.Custom != nil {
		return json.Marshal(tc.Custom)
	}
	return []byte("null"), nil
}

// UnmarshalJSON 实现 ToolChoice 的 JSON 反序列化
func (tc *ToolChoice) UnmarshalJSON(data []byte) error {
	// 尝试解析为字符串
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		tc.String = &s
		return nil
	}

	// 尝试解析为对象，先解析 type 字段来判断类型
	var typeObj struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &typeObj); err != nil {
		return err
	}

	switch typeObj.Type {
	case "function":
		var obj ToolChoiceFunction
		if err := json.Unmarshal(data, &obj); err != nil {
			return err
		}
		tc.Function = &obj
		return nil
	case "allowed_tools":
		var obj ToolChoiceAllowedTools
		if err := json.Unmarshal(data, &obj); err != nil {
			return err
		}
		tc.AllowedTools = &obj
		return nil
	case "custom":
		var obj ToolChoiceCustom
		if err := json.Unmarshal(data, &obj); err != nil {
			return err
		}
		tc.Custom = &obj
		return nil
	default:
		// 如果 type 字段不存在或无法识别，尝试按旧的 Object 格式解析（向后兼容）
		var obj ToolChoiceFunction
		if err := json.Unmarshal(data, &obj); err == nil && obj.Type == "function" {
			tc.Function = &obj
			return nil
		}
		return fmt.Errorf("unknown tool_choice type: %s", typeObj.Type)
	}
}

// ToolFunction 函数元数据（名称、描述、参数Schema）
type ToolFunction struct {
	Name        *string         `json:"name,omitempty"`        // 函数唯一标识
	Description *string         `json:"description,omitempty"` // 功能描述
	Parameters  json.RawMessage `json:"parameters,omitempty"`  // JSON Schema格式参数定义
}

type ToolCall struct {
	ID       string            `json:"id" binding:"required"`
	Index    int               `json:"index" binding:"required"`
	Type     string            `json:"type" binding:"required"`
	Function *ToolCallFunction `json:"function" binding:"required"`
}

type ToolCallFunction struct {
	Name      string `json:"name" binding:"required"`
	Arguments string `json:"arguments" binding:"required"`
}

// StopUnion holds a JSON value that may be either a single string or an array of strings.
type StopUnion struct {
	Str   *string
	Array []string
}

func (s StopUnion) MarshalJSON() ([]byte, error) {
	if s.Str != nil {
		return json.Marshal(*s.Str)
	}
	return json.Marshal(s.Array)
}

func (s *StopUnion) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		s.Str = &str
		return nil
	}
	return json.Unmarshal(data, &s.Array)
}
