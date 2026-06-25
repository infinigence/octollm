package openai

import "encoding/json"

// ResponsesRequest is a minimal view of POST /v1/responses for routing and moderation.
// Other JSON keys are ignored on unmarshal; callers forwarding unmodified bodies should
// rely on octollm.UnifiedBody raw bytes.
type ResponsesRequest struct {
	Model  string              `json:"model,omitempty"`
	Stream *bool               `json:"stream,omitempty"`
	Input  ResponsesInputValue `json:"input,omitempty"`
}

func (r *ResponsesRequest) UnmarshalJSON(data []byte) error {
	type Alias ResponsesRequest
	aux := struct {
		Input responsesInputField `json:"input,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(r),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	r.Input = aux.Input.Value
	return nil
}

type ResponsesInputValue interface {
	ExtractText() string
}

type ResponsesInputString string

func (s ResponsesInputString) ExtractText() string { return string(s) }

type ResponsesInputMessageArray []*ResponsesInputMessage

func (a ResponsesInputMessageArray) ExtractText() string {
	text := ""
	for _, item := range a {
		if item == nil {
			continue
		}
		text += item.ExtractText()
	}
	return text
}

type responsesInputField struct {
	Value ResponsesInputValue
}

func (f *responsesInputField) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || len(data) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		f.Value = ResponsesInputString(s)
		return nil
	}
	var items ResponsesInputMessageArray
	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}
	f.Value = items
	return nil
}

type ResponsesInputMessage struct {
	Role    string                       `json:"role,omitempty"`
	Content ResponsesInputMessageContent `json:"content,omitempty"`
}

func (i *ResponsesInputMessage) UnmarshalJSON(data []byte) error {
	type Alias ResponsesInputMessage
	aux := struct {
		Content responsesInputMessageContentField `json:"content,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(i),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	i.Content = aux.Content.Value
	return nil
}

func (i *ResponsesInputMessage) ExtractText() string {
	if i == nil || i.Content == nil {
		return ""
	}
	return i.Content.ExtractText()
}

// ResponsesInputMessageContent supports OpenAI Responses input item `content` polymorphism:
// - string
// - array of content parts (input_text, input_image, ...)
type ResponsesInputMessageContent interface {
	ExtractText() string
}

type ResponsesInputMessageContentString string

func (c ResponsesInputMessageContentString) ExtractText() string { return string(c) }

type ResponsesInputMessageContentArray []*ResponsesInputMessageContentPart

func (c ResponsesInputMessageContentArray) ExtractText() string {
	text := ""
	for _, part := range c {
		if part == nil {
			continue
		}
		text += part.ExtractText()
	}
	return text
}

type responsesInputMessageContentField struct {
	Value ResponsesInputMessageContent
}

func (f *responsesInputMessageContentField) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		f.Value = ResponsesInputMessageContentString(s)
		return nil
	}
	var items ResponsesInputMessageContentArray
	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}
	f.Value = items
	return nil
}

type ResponsesInputMessageContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL ImageURLContent `json:"image_url,omitempty"`
}

func (i *ResponsesInputMessageContentPart) UnmarshalJSON(data []byte) error {
	type Alias ResponsesInputMessageContentPart
	aux := struct {
		ImageURL imageURLField `json:"image_url,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(i),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	i.ImageURL = aux.ImageURL.Value
	return nil
}

func (i *ResponsesInputMessageContentPart) ExtractText() string {
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
