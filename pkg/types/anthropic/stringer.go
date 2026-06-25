package anthropic

import (
	"fmt"
	"strings"
)

// String formats the struct safely for logging (no sensitive data).
func (m MessageParam) String() string {
	w := &strings.Builder{}
	fmt.Fprintf(w, "Role: %q, ", m.Role)
	switch c := m.Content.(type) {
	case MessageContentString:
		fmt.Fprintf(w, "Content: len(%d), ", len(c))
	case MessageContentBlockArray:
		fmt.Fprintf(w, "Content: [")
		for _, block := range c {
			if block == nil {
				continue
			}
			switch b := block.(type) {
			case *TextBlockParam:
				fmt.Fprintf(w, "text(len=%d), ", len(b.Text))
			case *ImageBlockParam:
				fmt.Fprintf(w, "image, ")
			case *ToolUseBlockParam:
				fmt.Fprintf(w, "tool_use(name=%s,input_len=%d), ", b.Name, len(b.Input))
			case *ToolResultBlockParam:
				fmt.Fprintf(w, "tool_result(id=%s,len=%d), ", b.ToolUseID, contentLen(b.Content))
			case *ThinkingBlockParam:
				fmt.Fprintf(w, "thinking(len=%d), ", len(b.Thinking))
			case *GeneralBlockParam:
				fmt.Fprintf(w, "%s, ", b.Type)
			default:
				fmt.Fprintf(w, "%T, ", b)
			}
		}
		fmt.Fprintf(w, "], ")
	default:
		fmt.Fprintf(w, "Content: %T, ", c)
	}
	return fmt.Sprintf("(MessageParam) {%s}", w.String())
}

// contentLen returns the number of content elements in a MessageContent.
func contentLen(c MessageContent) int {
	if c == nil {
		return 0
	}
	if arr, ok := c.(MessageContentBlockArray); ok {
		return len(arr)
	}
	return 0
}

// String formats the struct safely for logging (no sensitive data).
func (r ClaudeMessagesRequest) String() string {
	w := &strings.Builder{}
	fmt.Fprintf(w, "  Model: %q\n", r.Model)
	fmt.Fprintf(w, "  MaxTokens: %d\n", r.MaxTokens)
	if r.System != nil {
		switch s := r.System.(type) {
		case SystemString:
			fmt.Fprintf(w, "  System: len(%d)\n", len(s))
		case SystemBlocks:
			fmt.Fprintf(w, "  System: blocks(%d)\n", len(s))
		}
	}
	fmt.Fprintf(w, "  Messages: len(%d)\n", len(r.Messages))
	for _, m := range r.Messages {
		if m == nil {
			continue
		}
		fmt.Fprintf(w, "    %s\n", m.String())
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
	if len(r.StopSequences) > 0 {
		fmt.Fprintf(w, "  StopSequences: %v\n", r.StopSequences)
	}
	if r.Stream != nil {
		fmt.Fprintf(w, "  Stream: %t\n", *r.Stream)
	}
	if r.Thinking != nil {
		fmt.Fprintf(w, "  Thinking: type=%s", r.Thinking.Type)
		if r.Thinking.BudgetTokens != nil {
			fmt.Fprintf(w, ", budget=%d", *r.Thinking.BudgetTokens)
		}
		w.WriteString("\n")
	}
	if len(r.Tools) > 0 {
		fmt.Fprintf(w, "  Tools: len(%d)\n", len(r.Tools))
		for _, t := range r.Tools {
			if t == nil {
				continue
			}
			fmt.Fprintf(w, "    Tool{name=%s}\n", t.Name)
		}
	}
	if r.ToolChoice != nil {
		fmt.Fprintf(w, "  ToolChoice: type=%s", r.ToolChoice.Type)
		if r.ToolChoice.Name != nil {
			fmt.Fprintf(w, ", name=%s", *r.ToolChoice.Name)
		}
		w.WriteString("\n")
	}
	return fmt.Sprintf("(ClaudeMessagesRequest) {\n%s}", w.String())
}

// contentBlockString formats a MessageContentBlockParam safely for logging.
func contentBlockString(b MessageContentBlockParam) string {
	if b == nil {
		return "nil"
	}
	switch v := b.(type) {
	case *TextBlockParam:
		return fmt.Sprintf("text(len=%d)", len(v.Text))
	case *ImageBlockParam:
		return "image"
	case *ToolUseBlockParam:
		return fmt.Sprintf("tool_use(name=%s,input_len=%d)", v.Name, len(v.Input))
	case *ToolResultBlockParam:
		return fmt.Sprintf("tool_result(id=%s,len=%d)", v.ToolUseID, contentLen(v.Content))
	case *ThinkingBlockParam:
		return fmt.Sprintf("thinking(len=%d)", len(v.Thinking))
	case *GeneralBlockParam:
		return v.Type
	default:
		return fmt.Sprintf("%T", v)
	}
}

// String formats ClaudeMessagesResponse safely for logging (no sensitive data).
func (r ClaudeMessagesResponse) String() string {
	w := &strings.Builder{}
	fmt.Fprintf(w, "  ID: %q\n", r.ID)
	if r.Type != "" {
		fmt.Fprintf(w, "  Type: %q\n", r.Type)
	}
	if r.Role != "" {
		fmt.Fprintf(w, "  Role: %q\n", r.Role)
	}
	fmt.Fprintf(w, "  Model: %q\n", r.Model)
	fmt.Fprintf(w, "  Content: len(%d)\n", len(r.Content))
	for _, b := range r.Content {
		fmt.Fprintf(w, "    %s\n", contentBlockString(b))
	}
	if r.StopReason != "" {
		fmt.Fprintf(w, "  StopReason: %s\n", r.StopReason)
	}
	if r.StopSequence != nil {
		fmt.Fprintf(w, "  StopSequence: %q\n", *r.StopSequence)
	}
	if u := r.Usage; u != nil {
		var inputTokens, outputTokens int64
		if u.InputTokens != nil {
			inputTokens = *u.InputTokens
		}
		if u.OutputTokens != nil {
			outputTokens = *u.OutputTokens
		}
		fmt.Fprintf(w, "  Usage: input=%d, output=%d", inputTokens, outputTokens)
		if u.CacheCreationInputTokens != nil {
			fmt.Fprintf(w, ", cache_creation=%d", *u.CacheCreationInputTokens)
		}
		if u.CacheReadInputTokens != nil {
			fmt.Fprintf(w, ", cache_read=%d", *u.CacheReadInputTokens)
		}
		w.WriteString("\n")
	}
	return fmt.Sprintf("(ClaudeMessagesResponse) {\n%s}", w.String())
}
