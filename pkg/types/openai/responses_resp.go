package openai

// ResponsesResponse is a minimal POST /v1/responses response object: only usage is typed
// for gateway logic; other JSON keys are ignored on unmarshal.
type ResponsesResponse struct {
	Id     string                 `json:"id"`
	Output []*ResponsesOutputItem `json:"output,omitempty"`
	Usage  *ResponsesUsage        `json:"usage,omitempty"`
}

// ResponsesOutputItem is one item in Responses API `output` array.
type ResponsesOutputItem struct {
	ID      string                        `json:"id,omitempty"`
	Type    string                        `json:"type,omitempty"`
	Role    string                        `json:"role,omitempty"`
	Content []*ResponsesOutputContentItem `json:"content,omitempty"`
}

func (i *ResponsesOutputItem) ExtractText() string {
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

// ResponsesOutputContentItem is one message content part inside Responses output.
type ResponsesOutputContentItem struct {
	Type    string `json:"type,omitempty"`
	Text    string `json:"text,omitempty"`
	Refusal string `json:"refusal,omitempty"`
}

func (i *ResponsesOutputContentItem) ExtractText() string {
	if i == nil {
		return ""
	}

	switch i.Type {
	case "output_text":
		return i.Text
	case "refusal":
		return i.Refusal
	default:
		return ""
	}
}

// ResponsesUsage is token usage on completed responses (fixed OpenAI shape).
type ResponsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`

	InputTokensDetails  *ResponsesInputTokenDetails  `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *ResponsesOutputTokenDetails `json:"output_tokens_details,omitempty"`
}

// ResponsesInputTokenDetails breaks down input tokens (e.g. prompt caching).
type ResponsesInputTokenDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

// ResponsesOutputTokenDetails breaks down output tokens (e.g. reasoning).
type ResponsesOutputTokenDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// ResponseStreamChunk is one SSE JSON object from POST /v1/responses with stream=true.
//
// Reference: https://platform.openai.com/docs/api-reference/responses-streaming
type ResponseStreamChunk struct {
	Type string `json:"type"`

	// response.output_text.delta
	Delta string `json:"delta,omitempty"`
	// response.output_text.done
	Text string `json:"text,omitempty"`

	// response.content_part.added / response.content_part.done
	Part *ResponsesOutputContentItem `json:"part,omitempty"`
	// response.output_item.added / response.output_item.done
	Item *ResponsesOutputItem `json:"item,omitempty"`

	// Lifecycle snapshots (response.created, response.in_progress, response.completed, …).
	Response *ResponsesResponse `json:"response,omitempty"`
}
