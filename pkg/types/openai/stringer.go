package openai

import (
	"fmt"
	"strings"
)

// String formats the struct safely for logging (no sensitive data).
func (m Message) String() string {
	w := &strings.Builder{}
	fmt.Fprintf(w, "Role: %q, ", m.Role)
	writeMsgContent := func(fieldName string, mc MessageContent) {
		switch v := mc.(type) {
		case MessageContentString:
			fmt.Fprintf(w, "%s: len(%d), ", fieldName, len(v))
		case MessageContentArray:
			fmt.Fprintf(w, "%s: [", fieldName)
			for _, item := range v {
				if item == nil {
					continue
				}
				switch item.Type {
				case "text":
					fmt.Fprintf(w, "text(len=%d), ", len(item.Text))
				case "image_url":
					if item.ImageURL == nil {
						fmt.Fprintf(w, "image_url(nil), ")
					} else {
						url := item.ImageURL.GetImageUrl()
						if obj, ok := item.ImageURL.(*MessageContentItemImageURL); ok {
							fmt.Fprintf(w, "image_url(len=%d,detail=%s), ", len(url), obj.Detail)
						} else {
							fmt.Fprintf(w, "image_url(len=%d), ", len(url))
						}
					}
				case "video_url":
					if item.VideoURL == nil {
						fmt.Fprintf(w, "video_url(nil), ")
					} else {
						fmt.Fprintf(w, "video_url(len=%d), ", len(item.VideoURL.URL))
					}
				case "audio_url":
					if item.AudioURL == nil {
						fmt.Fprintf(w, "audio_url(nil), ")
					} else {
						fmt.Fprintf(w, "audio_url(len=%d), ", len(item.AudioURL.URL))
					}
				case "input_audio":
					if item.InputAudio == nil {
						fmt.Fprintf(w, "input_audio(nil), ")
					} else {
						fmt.Fprintf(w, "input_audio(len=%d,format=%s), ", len(item.InputAudio.Data), item.InputAudio.Format)
					}
				default:
					fmt.Fprintf(w, "%s, ", item.Type)
				}
			}
			w.WriteString("], ")
		}
	}
	if m.Content != nil {
		writeMsgContent("Content", m.Content)
	}
	if m.ReasoningContent != nil {
		writeMsgContent("ReasoningContent", m.ReasoningContent)
	}
	if m.Name != "" {
		fmt.Fprintf(w, "Name: %q, ", m.Name)
	}
	if len(m.ToolCalls) > 0 {
		fmt.Fprintf(w, "ToolCalls: len(%d), ", len(m.ToolCalls))
	}
	if m.ToolCallID != "" {
		fmt.Fprintf(w, "ToolCallID: %q", m.ToolCallID)
	}
	return fmt.Sprintf("(Message) {%s}", w.String())
}

func (t ToolChoiceString) String() string { return string(t) }

func (t ToolChoiceObject) String() string {
	switch t.Type {
	case "function":
		if t.Function != nil {
			return fmt.Sprintf("function(%s)", t.Function.Name)
		}
	case "allowed_tools":
		return "allowed_tools"
	case "custom":
		if t.Custom != nil {
			return fmt.Sprintf("custom(%s)", t.Custom.Name)
		}
	}
	return t.Type
}

// String formats the struct safely for logging (no sensitive data).
func (r CompletionRequest) String() string {
	w := &strings.Builder{}
	fmt.Fprintf(w, "  Model: %q\n", r.Model)
	if len(r.Prompt) > 0 {
		switch r.Prompt[0] {
		case '"':
			fmt.Fprintf(w, "  Prompt: len(%d)\n", len(r.Prompt))
		case '[':
			fmt.Fprintf(w, "  Prompt: array(%d)\n", len(r.Prompt))
		default:
			fmt.Fprintf(w, "  Prompt: other(%d)\n", len(r.Prompt))
		}
	}
	if r.MaxTokens != nil {
		fmt.Fprintf(w, "  MaxTokens: %d\n", *r.MaxTokens)
	}
	if r.Temperature != nil {
		fmt.Fprintf(w, "  Temperature: %.6f\n", *r.Temperature)
	}
	if r.TopP != nil {
		fmt.Fprintf(w, "  TopP: %.6f\n", *r.TopP)
	}
	if r.FrequencyPenalty != nil {
		fmt.Fprintf(w, "  FrequencyPenalty: %.6f\n", *r.FrequencyPenalty)
	}
	if r.PresencePenalty != nil {
		fmt.Fprintf(w, "  PresencePenalty: %.6f\n", *r.PresencePenalty)
	}
	if r.Stop != nil {
		fmt.Fprintf(w, "  Stop: %v\n", r.Stop)
	}
	if r.Seed != nil {
		fmt.Fprintf(w, "  Seed: %d\n", *r.Seed)
	}
	fmt.Fprintf(w, "  Stream: %t\n", r.Stream)
	if r.LogProbs != nil {
		fmt.Fprintf(w, "  LogProbs: %t\n", *r.LogProbs)
	}
	if r.N != nil {
		fmt.Fprintf(w, "  N: %d\n", *r.N)
	}
	if r.BestOf != nil {
		fmt.Fprintf(w, "  BestOf: %d\n", *r.BestOf)
	}
	if r.Echo != nil {
		fmt.Fprintf(w, "  Echo: %t\n", *r.Echo)
	}
	if len(r.LogitBias) > 0 {
		fmt.Fprintf(w, "  LogitBias: len(%d)\n", len(r.LogitBias))
	}
	return fmt.Sprintf("(CompletionRequest) {\n%s}", w.String())
}

// String formats the struct safely for logging (no sensitive data).
func (r ChatCompletionRequest) String() string {
	w := &strings.Builder{}
	fmt.Fprintf(w, "  Model: %q\n", r.Model)
	fmt.Fprintf(w, "  Messages: len(%d)\n", len(r.Messages))
	for _, m := range r.Messages {
		if m == nil {
			continue
		}
		fmt.Fprintf(w, "    %s\n", m.String())
	}
	if r.MaxTokens != nil {
		fmt.Fprintf(w, "  MaxTokens: %d\n", *r.MaxTokens)
	}
	if r.MaxCompletionTokens != nil {
		fmt.Fprintf(w, "  MaxCompletionTokens: %d\n", *r.MaxCompletionTokens)
	}
	if r.Temperature != nil {
		fmt.Fprintf(w, "  Temperature: %.6f\n", *r.Temperature)
	}
	if r.TopP != nil {
		fmt.Fprintf(w, "  TopP: %.6f\n", *r.TopP)
	}
	if r.TopK != nil {
		fmt.Fprintf(w, "  TopK: %d\n", *r.TopK)
	}
	if r.Stop != nil {
		fmt.Fprintf(w, "  Stop: %s\n", r.Stop)
	}
	if r.Stream != nil {
		fmt.Fprintf(w, "  Stream: %t\n", *r.Stream)
	}
	if len(r.Tools) > 0 {
		fmt.Fprintf(w, "  Tools: len(%d)\n", len(r.Tools))
		for _, t := range r.Tools {
			if t == nil {
				continue
			}
			name := ""
			if t.Function.Name != nil {
				name = *t.Function.Name
			}
			fmt.Fprintf(w, "    Tool{type=%s, name=%s}\n", t.Type, name)
		}
	}
	if r.ToolChoice != nil {
		fmt.Fprintf(w, "  ToolChoice: %s\n", r.ToolChoice)
	}
	if r.Thinking != nil {
		fmt.Fprintf(w, "  Thinking: type=%s\n", r.Thinking.Type)
	}
	return fmt.Sprintf("(ChatCompletionRequest) {\n%s}", w.String())
}

// String formats the struct safely for logging (no sensitive data).
func (r EmbeddingRequest) String() string {
	w := &strings.Builder{}
	fmt.Fprintf(w, "  Model: %q\n", r.Model)
	if r.Input != nil {
		if r.Input.IsArray() {
			fmt.Fprintf(w, "  Input: array(len=%d)\n", r.Input.GetDataLength())
		} else {
			fmt.Fprintf(w, "  Input: string(len=%d)\n", r.Input.GetDataLength())
		}
	}
	if r.NormalizeEmbeddings != nil {
		fmt.Fprintf(w, "  NormalizeEmbeddings: %t\n", *r.NormalizeEmbeddings)
	}
	return fmt.Sprintf("(EmbeddingRequest) {\n%s}", w.String())
}

// usageString formats a Usage safely for logging.
func usageString(u *Usage) string {
	if u == nil {
		return ""
	}
	w := &strings.Builder{}
	fmt.Fprintf(w, "prompt=%d, completion=%d, total=%d", u.PromptTokens, u.CompletionTokens, u.TotalTokens)
	if d := u.CompletionTokensDetails; d != nil {
		if d.ReasoningTokens > 0 {
			fmt.Fprintf(w, ", reasoning=%d", d.ReasoningTokens)
		}
		if d.AudioTokens > 0 {
			fmt.Fprintf(w, ", completion_audio=%d", d.AudioTokens)
		}
	}
	if d := u.PromptTokensDetails; d != nil {
		if d.CachedTokens > 0 {
			fmt.Fprintf(w, ", cached=%d", d.CachedTokens)
		}
		if d.AudioTokens > 0 {
			fmt.Fprintf(w, ", prompt_audio=%d", d.AudioTokens)
		}
	}
	return w.String()
}

// String formats ChatCompletionResponse safely for logging (no sensitive data).
func (r ChatCompletionResponse) String() string {
	w := &strings.Builder{}
	fmt.Fprintf(w, "  ID: %q\n", r.ID)
	fmt.Fprintf(w, "  Model: %q\n", r.Model)
	if r.Object != "" {
		fmt.Fprintf(w, "  Object: %q\n", r.Object)
	}
	fmt.Fprintf(w, "  Created: %d\n", r.Created)
	fmt.Fprintf(w, "  Choices: len(%d)\n", len(r.Choices))
	for _, c := range r.Choices {
		if c == nil {
			continue
		}
		fmt.Fprintf(w, "    Choice{index=%d, finish_reason=%s", c.Index, c.FinishReason)
		if c.Message != nil {
			fmt.Fprintf(w, ", message=%s", c.Message.String())
		}
		w.WriteString("}\n")
	}
	if u := usageString(r.Usage); u != "" {
		fmt.Fprintf(w, "  Usage: %s\n", u)
	}
	if r.Blocked != nil {
		fmt.Fprintf(w, "  Blocked: %t\n", *r.Blocked)
	}
	if r.SystemFingerprint != nil {
		fmt.Fprintf(w, "  SystemFingerprint: %q\n", *r.SystemFingerprint)
	}
	if r.ServiceTier != nil {
		fmt.Fprintf(w, "  ServiceTier: %q\n", *r.ServiceTier)
	}
	return fmt.Sprintf("(ChatCompletionResponse) {\n%s}", w.String())
}

// String formats CompletionResponse safely for logging (no sensitive data).
func (r CompletionResponse) String() string {
	w := &strings.Builder{}
	fmt.Fprintf(w, "  ID: %q\n", r.ID)
	fmt.Fprintf(w, "  Model: %q\n", r.Model)
	if r.Object != "" {
		fmt.Fprintf(w, "  Object: %q\n", r.Object)
	}
	fmt.Fprintf(w, "  Created: %d\n", r.Created)
	fmt.Fprintf(w, "  Choices: len(%d)\n", len(r.Choices))
	for _, c := range r.Choices {
		finish := ""
		if c.FinishReason != nil {
			finish = *c.FinishReason
		}
		fmt.Fprintf(w, "    Choice{index=%d, text_len=%d, finish_reason=%s}\n", c.Index, len(c.Text), finish)
	}
	if u := usageString(r.Usage); u != "" {
		fmt.Fprintf(w, "  Usage: %s\n", u)
	}
	if r.SystemFingerprint != nil {
		fmt.Fprintf(w, "  SystemFingerprint: %q\n", *r.SystemFingerprint)
	}
	return fmt.Sprintf("(CompletionResponse) {\n%s}", w.String())
}

// String formats EmbeddingResponse safely for logging (no sensitive data).
func (r EmbeddingResponse) String() string {
	w := &strings.Builder{}
	fmt.Fprintf(w, "  Model: %q\n", r.Model)
	if r.Object != "" {
		fmt.Fprintf(w, "  Object: %q\n", r.Object)
	}
	fmt.Fprintf(w, "  Data: len(%d)\n", len(r.Data))
	for _, d := range r.Data {
		fmt.Fprintf(w, "    Embedding{index=%d, dims=%d}\n", d.Index, len(d.Embedding))
	}
	fmt.Fprintf(w, "  Usage: prompt=%d, total=%d\n", r.Usage.PromptTokens, r.Usage.TotalTokens)
	return fmt.Sprintf("(EmbeddingResponse) {\n%s}", w.String())
}

// responsesOutputItemString formats a ResponsesOutputItem safely for logging.
func responsesOutputItemString(i *ResponsesOutputItem) string {
	if i == nil {
		return "nil"
	}
	w := &strings.Builder{}
	fmt.Fprintf(w, "{id=%s, type=%s", i.ID, i.Type)
	if i.Role != "" {
		fmt.Fprintf(w, ", role=%s", i.Role)
	}
	if len(i.Content) > 0 {
		fmt.Fprintf(w, ", content=[")
		for _, p := range i.Content {
			if p == nil {
				continue
			}
			switch p.Type {
			case "output_text":
				fmt.Fprintf(w, "output_text(len=%d), ", len(p.Text))
			case "refusal":
				fmt.Fprintf(w, "refusal(len=%d), ", len(p.Refusal))
			default:
				fmt.Fprintf(w, "%s, ", p.Type)
			}
		}
		w.WriteString("]")
	}
	w.WriteString("}")
	return w.String()
}

// String formats ResponsesResponse safely for logging (no sensitive data).
func (r ResponsesResponse) String() string {
	w := &strings.Builder{}
	fmt.Fprintf(w, "  ID: %q\n", r.Id)
	fmt.Fprintf(w, "  Output: len(%d)\n", len(r.Output))
	for _, item := range r.Output {
		fmt.Fprintf(w, "    %s\n", responsesOutputItemString(item))
	}
	if u := r.Usage; u != nil {
		fmt.Fprintf(w, "  Usage: input=%d, output=%d, total=%d", u.InputTokens, u.OutputTokens, u.TotalTokens)
		if d := u.InputTokensDetails; d != nil && d.CachedTokens > 0 {
			fmt.Fprintf(w, ", cached=%d", d.CachedTokens)
		}
		if d := u.OutputTokensDetails; d != nil && d.ReasoningTokens > 0 {
			fmt.Fprintf(w, ", reasoning=%d", d.ReasoningTokens)
		}
		w.WriteString("\n")
	}
	return fmt.Sprintf("(ResponsesResponse) {\n%s}", w.String())
}
