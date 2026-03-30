package anthropic

import (
	"fmt"
	"strings"
)

// String formats the struct safely for logging (no sensitive data).
func (m MessageParam) String() string {
	w := &strings.Builder{}
	fmt.Fprintf(w, "Role: %q, ", m.Role)
	// Single MessageContentString: show byte length, matching the OpenAI Message stringer pattern.
	if len(m.Content) == 1 {
		if s, ok := m.Content[0].(MessageContentString); ok {
			fmt.Fprintf(w, "Content: len(%d), ", len(s))
			return fmt.Sprintf("(MessageParam) {%s}", w.String())
		}
	}
	// Array of blocks: bracket notation, matching the OpenAI array content pattern.
	fmt.Fprintf(w, "Content: [")
	for _, c := range m.Content {
		if c == nil {
			continue
		}
		b, ok := c.(*MessageContentBlock)
		if !ok {
			continue
		}
		switch b.Type {
		case "text":
			if b.Text != nil {
				fmt.Fprintf(w, "text(len=%d), ", len(*b.Text))
			}
		case "image":
			fmt.Fprintf(w, "image, ")
		case "tool_use":
			if b.MessageContentToolUse != nil {
				fmt.Fprintf(w, "tool_use(name=%s,input_len=%d), ", b.MessageContentToolUse.Name, len(b.MessageContentToolUse.Input))
			}
		case "tool_result":
			if b.MessageContentToolResult != nil {
				id := ""
				if b.MessageContentToolResult.ToolUseID != nil {
					id = *b.MessageContentToolResult.ToolUseID
				}
				fmt.Fprintf(w, "tool_result(id=%s,len=%d), ", id, len(b.MessageContentToolResult.Content))
			}
		case "thinking":
			if b.MessageContentThinking != nil {
				fmt.Fprintf(w, "thinking(len=%d), ", len(b.MessageContentThinking.Thinking))
			}
		default:
			fmt.Fprintf(w, "%s, ", b.Type)
		}
	}
	fmt.Fprintf(w, "], ")
	return fmt.Sprintf("(MessageParam) {%s}", w.String())
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
