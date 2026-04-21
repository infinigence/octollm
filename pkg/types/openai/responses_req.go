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
	Role    string                       `json:"role,omitempty"`
	Content []*ResponsesInputContentItem `json:"content,omitempty"`
}

func (i *ResponsesInputItem) ExtractText() string {
	if i == nil {
		return ""
	}

	text := ""
	for _, part := range i.Content {
		if part == nil {
			continue
		}
		text += part.ExtractText()
	}
	return text
}

type ResponsesInputContentItem struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL ImageURLContent `json:"image_url,omitempty"`
}

func (i *ResponsesInputContentItem) UnmarshalJSON(data []byte) error {
	type Alias struct {
		Type     string          `json:"type"`
		Text     string          `json:"text,omitempty"`
		ImageURL json.RawMessage `json:"image_url,omitempty"`
	}

	var alias Alias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}

	i.Type = alias.Type
	i.Text = alias.Text

	if len(alias.ImageURL) > 0 {
		imageURL, err := unmarshalImageURLContent(alias.ImageURL)
		if err != nil {
			return err
		}
		i.ImageURL = imageURL
	}

	return nil
}

func (i ResponsesInputContentItem) MarshalJSON() ([]byte, error) {
	type Alias struct {
		Type     string          `json:"type"`
		Text     string          `json:"text,omitempty"`
		ImageURL json.RawMessage `json:"image_url,omitempty"`
	}

	alias := Alias{
		Type: i.Type,
		Text: i.Text,
	}

	if i.ImageURL != nil {
		imageURLBytes, err := json.Marshal(i.ImageURL)
		if err != nil {
			return nil, err
		}
		alias.ImageURL = imageURLBytes
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
