package openai

import "encoding/json"

// ResponsesRequest is the request body of POST /v1/responses (Responses API).
//
// Only the fields the gateway or the converter engine needs are typed; any other
// JSON keys are ignored on unmarshal. Callers that forward unmodified bodies
// should rely on octollm.UnifiedBody raw bytes rather than this struct.
type ResponsesRequest struct {
	Model      string                   `json:"model,omitempty"`
	Stream     *bool                    `json:"stream,omitempty"`
	Input      ResponsesInputValue      `json:"input,omitempty"`
	Tools      []*ResponseTool          `json:"tools,omitempty"`
	ToolChoice ResponsesToolChoiceValue `json:"tool_choice,omitempty"`

	Instructions      string             `json:"instructions,omitempty"`
	MaxOutputTokens   *int               `json:"max_output_tokens,omitempty"`
	Temperature       *float64           `json:"temperature,omitempty"`
	TopP              *float64           `json:"top_p,omitempty"`
	Reasoning         *ResponseReasoning `json:"reasoning,omitempty"`
	ParallelToolCalls *bool              `json:"parallel_tool_calls,omitempty"`
}

func (r *ResponsesRequest) UnmarshalJSON(data []byte) error {
	type Alias ResponsesRequest
	aux := struct {
		Input      responsesInputField      `json:"input,omitempty"`
		ToolChoice responsesToolChoiceField `json:"tool_choice,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(r),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	r.Input = aux.Input.Value
	r.ToolChoice = aux.ToolChoice.Value
	return nil
}

// ResponseReasoning controls the reasoning effort for reasoning models.
type ResponseReasoning struct {
	Effort string `json:"effort,omitempty"`
}

// ResponseTool is a function tool definition in the Responses API. Unlike the
// chat-completions `Tool`, the name/description/parameters sit at the top level
// rather than nested under a `function` object.
type ResponseTool struct {
	Type        string          `json:"type"` // "function"
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ResponsesToolChoiceValue is the Responses `tool_choice` field: a string
// ("none", "auto", "required") or a forced function selector object.
type ResponsesToolChoiceValue interface {
	isResponsesToolChoice()
}

type ResponsesToolChoiceString string

func (ResponsesToolChoiceString) isResponsesToolChoice() {}

type ResponsesToolChoiceObject struct {
	Type string `json:"type"` // "function"
	Name string `json:"name,omitempty"`
}

func (ResponsesToolChoiceObject) isResponsesToolChoice() {}

type responsesToolChoiceField struct {
	Value ResponsesToolChoiceValue
}

func (t *responsesToolChoiceField) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		t.Value = ResponsesToolChoiceString(s)
		return nil
	}
	var obj ResponsesToolChoiceObject
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	t.Value = obj
	return nil
}

// ResponsesInputValue is the Responses `input` field: a plain string prompt or
// an array of input items.
type ResponsesInputValue interface {
	ExtractText() string
}

type ResponsesInputString string

func (s ResponsesInputString) ExtractText() string { return string(s) }

type ResponsesInputItemArray []ResponsesInputItem

func (a ResponsesInputItemArray) ExtractText() string {
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
	var items ResponsesInputItemArray
	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}
	f.Value = items
	return nil
}

// ResponsesInputItem is one item in the Responses `input` array. Items are
// discriminated by their `type` field (function_call, function_call_output,
// reasoning); message items carry only a `role` field.
type ResponsesInputItem interface {
	ExtractText() string
}

func (a *ResponsesInputItemArray) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || len(data) == 0 {
		return nil
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		return err
	}
	out := make(ResponsesInputItemArray, len(raws))
	for i, raw := range raws {
		item, err := unmarshalResponsesInputItem(raw)
		if err != nil {
			return err
		}
		out[i] = item
	}
	*a = out
	return nil
}

func unmarshalResponsesInputItem(data []byte) (ResponsesInputItem, error) {
	var peek struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &peek); err != nil {
		return nil, err
	}
	switch peek.Type {
	case "function_call":
		var v ResponsesInputFunctionCall
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		return &v, nil
	case "function_call_output":
		var v ResponsesInputFunctionCallOutput
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		return &v, nil
	case "reasoning":
		var v ResponsesInputReasoning
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		return &v, nil
	case "message", "":
		var v ResponsesInputMessage
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		return &v, nil
	default:
		// Unknown future item types: preserve them verbatim so re-marshaling
		// does not silently drop data.
		var v ResponsesInputRawItem
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		return &v, nil
	}
}

// ResponsesInputRawItem preserves an unrecognized input item verbatim.
type ResponsesInputRawItem struct {
	Raw json.RawMessage
}

func (i *ResponsesInputRawItem) ExtractText() string { return "" }

func (i *ResponsesInputRawItem) UnmarshalJSON(data []byte) error {
	i.Raw = append(i.Raw[:0], data...)
	return nil
}

func (i ResponsesInputRawItem) MarshalJSON() ([]byte, error) {
	return i.Raw, nil
}

// ResponsesInputMessage is a message input item (role + content).
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

// ResponsesInputFunctionCall is a function_call input item.
type ResponsesInputFunctionCall struct {
	Type      string `json:"type"` // "function_call"
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

func (i *ResponsesInputFunctionCall) ExtractText() string {
	if i == nil {
		return ""
	}
	return i.Arguments
}

// ResponsesInputFunctionCallOutput is a function_call_output input item.
type ResponsesInputFunctionCallOutput struct {
	Type   string                           `json:"type"` // "function_call_output"
	CallID string                           `json:"call_id,omitempty"`
	Output ResponsesFunctionCallOutputValue `json:"output,omitempty"`
}

func (i *ResponsesInputFunctionCallOutput) UnmarshalJSON(data []byte) error {
	type Alias ResponsesInputFunctionCallOutput
	aux := struct {
		Output responsesFunctionCallOutputField `json:"output,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(i),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	i.Output = aux.Output.Value
	return nil
}

func (i *ResponsesInputFunctionCallOutput) ExtractText() string {
	if i == nil || i.Output == nil {
		return ""
	}
	return i.Output.ExtractText()
}

// ResponsesFunctionCallOutputValue is the `output` of a function_call_output
// item: a string or an array of input content parts.
type ResponsesFunctionCallOutputValue interface {
	ExtractText() string
}

type ResponsesFunctionCallOutputString string

func (s ResponsesFunctionCallOutputString) ExtractText() string { return string(s) }

type ResponsesFunctionCallOutputArray []*ResponsesInputMessageContentPart

func (a ResponsesFunctionCallOutputArray) ExtractText() string {
	text := ""
	for _, part := range a {
		if part == nil {
			continue
		}
		text += part.ExtractText()
	}
	return text
}

type responsesFunctionCallOutputField struct {
	Value ResponsesFunctionCallOutputValue
}

func (f *responsesFunctionCallOutputField) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || len(data) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		f.Value = ResponsesFunctionCallOutputString(s)
		return nil
	}
	var arr ResponsesFunctionCallOutputArray
	if err := json.Unmarshal(data, &arr); err != nil {
		return err
	}
	f.Value = arr
	return nil
}

// ResponsesInputReasoning is a reasoning input item (a prior reasoning turn).
type ResponsesInputReasoning struct {
	Type             string                           `json:"type"` // "reasoning"
	ID               string                           `json:"id,omitempty"`
	Summary          []*ResponsesReasoningSummaryPart `json:"summary,omitempty"`
	EncryptedContent string                           `json:"encrypted_content,omitempty"`
}

func (i *ResponsesInputReasoning) ExtractText() string {
	if i == nil {
		return ""
	}
	text := ""
	for _, part := range i.Summary {
		if part == nil {
			continue
		}
		text += part.Text
	}
	return text
}

// ResponsesReasoningSummaryPart is one part of a reasoning summary.
type ResponsesReasoningSummaryPart struct {
	Type string `json:"type"` // "summary_text"
	Text string `json:"text,omitempty"`
}

// ResponsesInputMessageContent supports Responses input item `content`
// polymorphism: a string or an array of content parts (input_text, input_image,
// input_file, ...).
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
	FileURL  string          `json:"file_url,omitempty"`
	FileID   string          `json:"file_id,omitempty"`
	Filename string          `json:"filename,omitempty"`
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
