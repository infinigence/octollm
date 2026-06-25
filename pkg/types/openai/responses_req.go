package openai

import "encoding/json"

// ResponsesRequest is a minimal view of POST /v1/responses for routing and moderation.
// Other JSON keys are ignored on unmarshal; callers forwarding unmodified bodies should
// rely on octollm.UnifiedBody raw bytes.
type ResponsesRequest struct {
	Model  string          `json:"model,omitempty"`
	Stream *bool           `json:"stream,omitempty"`
	Input  *ResponsesInput `json:"input,omitempty"`
}

// ResponsesInput supports OpenAI Responses `input` polymorphism:
// - string
// - array of message-like input items
type ResponsesInput struct {
	String *string
	Items  []*ResponsesInputItem
}

func (r *ResponsesInput) UnmarshalJSON(data []byte) error {
	*r = ResponsesInput{}

	if string(data) == "null" || len(data) == 0 {
		return nil
	}

	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		r.String = &s
		return nil
	}

	var items []*ResponsesInputItem
	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}
	r.Items = items
	return nil
}

func (r ResponsesInput) MarshalJSON() ([]byte, error) {
	if r.String != nil {
		return json.Marshal(*r.String)
	}
	return json.Marshal(r.Items)
}

func (r ResponsesInput) ExtractText() string {
	if r.String != nil {
		return *r.String
	}

	text := ""
	for _, item := range r.Items {
		if item == nil {
			continue
		}
		text += item.ExtractText()
	}
	return text
}

type ResponsesInputItem struct {
	Role    string                `json:"role,omitempty"`
	Content ResponsesInputContent `json:"content,omitempty"`
}

func (i *ResponsesInputItem) UnmarshalJSON(data []byte) error {
	type Alias struct {
		Role    string          `json:"role,omitempty"`
		Content json.RawMessage `json:"content,omitempty"`
	}

	var alias Alias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}

	i.Role = alias.Role
	if len(alias.Content) > 0 {
		content, err := unmarshalResponsesInputContent(alias.Content)
		if err != nil {
			return err
		}
		i.Content = content
	}
	return nil
}

func (i *ResponsesInputItem) ExtractText() string {
	if i == nil || i.Content == nil {
		return ""
	}
	return i.Content.ExtractText()
}

// ResponsesInputContent supports OpenAI Responses input item `content` polymorphism:
// - string
// - array of content parts (input_text, input_image, ...)
type ResponsesInputContent interface {
	ExtractText() string
}

type ResponsesInputContentString string

func (c ResponsesInputContentString) ExtractText() string { return string(c) }

type ResponsesInputContentArray []*ResponsesInputContentItem

func (c ResponsesInputContentArray) ExtractText() string {
	text := ""
	for _, part := range c {
		if part == nil {
			continue
		}
		text += part.ExtractText()
	}
	return text
}

func unmarshalResponsesInputContent(data json.RawMessage) (ResponsesInputContent, error) {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		return ResponsesInputContentString(s), nil
	}

	var items []*ResponsesInputContentItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	return ResponsesInputContentArray(items), nil
}

type ResponsesInputContentItem struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL ImageURLContent `json:"image_url,omitempty"`
}

func (i *ResponsesInputContentItem) UnmarshalJSON(data []byte) error {
	type Alias struct {
		Type     string        `json:"type"`
		Text     string        `json:"text,omitempty"`
		ImageURL imageURLField `json:"image_url,omitempty"`
	}

	var alias Alias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}

	i.Type = alias.Type
	i.Text = alias.Text
	i.ImageURL = alias.ImageURL.Value

	return nil
}

func (i ResponsesInputContentItem) MarshalJSON() ([]byte, error) {
	type Alias struct {
		Type     string          `json:"type"`
		Text     string          `json:"text,omitempty"`
		ImageURL ImageURLContent `json:"image_url,omitempty"`
	}

	alias := Alias{
		Type:     i.Type,
		Text:     i.Text,
		ImageURL: i.ImageURL,
	}

	return json.Marshal(alias)
}

func (i *ResponsesInputContentItem) ExtractText() string {
	if i == nil {
		return ""
	}

	switch i.Type {
	case "input_text":
		return i.Text
	case "input_image":
		if i.ImageURL != nil {
			return i.ImageURL.GetImageUrl()
		}
	}
	return ""
}
