package openai

// ResponsesResponse is a POST /v1/responses (Responses API) response object.
// Only the fields the gateway or the converter engine needs are typed; any
// other JSON keys are ignored on unmarshal.
type ResponsesResponse struct {
	Id      string                 `json:"id,omitempty"`
	Object  string                 `json:"object,omitempty"`
	Created int                    `json:"created_at,omitempty"`
	Status  string                 `json:"status,omitempty"`
	Model   string                 `json:"model,omitempty"`
	Error   *ResponsesError        `json:"error,omitempty"`
	Output  []*ResponsesOutputItem `json:"output,omitempty"`
	Usage   *ResponsesUsage        `json:"usage,omitempty"`

	// IncompleteDetails is present when Status is "incomplete", carrying the
	// reason the response was cut short (e.g. "max_output_tokens").
	IncompleteDetails *ResponsesIncompleteDetails `json:"incomplete_details,omitempty"`
}

// ResponsesError is the error envelope of a failed Responses response.
type ResponsesError struct {
	Type    string `json:"type,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Param   string `json:"param,omitempty"`
}

// ResponsesIncompleteDetails explains why a response is incomplete.
type ResponsesIncompleteDetails struct {
	Reason string `json:"reason,omitempty"`
}

// ResponsesOutputItem is one item in the Responses API `output` array.
type ResponsesOutputItem struct {
	ID      string                        `json:"id,omitempty"`
	Type    string                        `json:"type,omitempty"`
	Role    string                        `json:"role,omitempty"`
	Status  string                        `json:"status,omitempty"`
	Content []*ResponsesOutputContentItem `json:"content,omitempty"`

	// function_call item fields.
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`

	// reasoning item fields.
	Summary          []*ResponsesReasoningSummaryPart `json:"summary,omitempty"`
	EncryptedContent string                           `json:"encrypted_content,omitempty"`
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
//
// The counts are pointers so an upstream that omits one is distinguishable from
// one reporting an explicit zero, matching PromptTokensDetails on the
// chat/completions side.
type ResponsesInputTokenDetails struct {
	CachedTokens *int `json:"cached_tokens,omitempty"`

	// CacheWriteTokens is the prompt-caching write count: tokens that were
	// written into the cache by this request rather than read from it.
	CacheWriteTokens *int `json:"cache_write_tokens,omitempty"`
}

// ResponsesOutputTokenDetails breaks down output tokens (e.g. reasoning).
type ResponsesOutputTokenDetails struct {
	ReasoningTokens *int `json:"reasoning_tokens,omitempty"`
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

	// function_call argument streaming events.
	ItemID     string `json:"item_id,omitempty"`
	OutputIdx  *int   `json:"output_index,omitempty"`
	ContentIdx *int   `json:"content_index,omitempty"`
	CallID     string `json:"call_id,omitempty"`
	Name       string `json:"name,omitempty"`
	// response.function_call_arguments.done
	Arguments string `json:"arguments,omitempty"`
}
