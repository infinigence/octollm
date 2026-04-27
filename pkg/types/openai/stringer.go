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

func toolChoiceString(tc *ToolChoice) string {
	if tc == nil {
		return ""
	}
	if tc.String != nil {
		return *tc.String
	}
	if tc.Function != nil && tc.Function.Function != nil {
		return fmt.Sprintf("function(%s)", tc.Function.Function.Name)
	}
	if tc.AllowedTools != nil {
		return "allowed_tools"
	}
	if tc.Custom != nil && tc.Custom.Custom != nil {
		return fmt.Sprintf("custom(%s)", tc.Custom.Custom.Name)
	}
	return ""
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
		fmt.Fprintf(w, "  Stop: %v\n", r.Stop)
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
		fmt.Fprintf(w, "  ToolChoice: %s\n", toolChoiceString(r.ToolChoice))
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

func (s StopUnion) String() string {
	if s.Str != nil {
		return *s.Str
	}
	return fmt.Sprintf("%v", s.Array)
}
