package ruleengine

import (
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
)

// Helper function to extract text from a message
func combinedTextForChatCompletionsMessage(msg *openai.Message) string {
	if msg.Content != nil {
		return msg.Content.ExtractText()
	}
	return ""
}

// combinedTextForAnthropicMessage concatenates ExtractText() from all content blocks.
func combinedTextForAnthropicMessage(msg *anthropic.MessageParam) string {
	var sb strings.Builder
	for _, c := range msg.Content {
		sb.WriteString(c.ExtractText())
	}
	return sb.String()
}

// anthropicSystemText extracts plain text from a system field (SystemString or SystemBlocks).
func anthropicSystemText(system anthropic.SystemContent) string {
	if system == nil {
		return ""
	}
	switch s := system.(type) {
	case anthropic.SystemString:
		return string(s)
	case anthropic.SystemBlocks:
		var sb strings.Builder
		for _, b := range s {
			sb.WriteString(b.Text)
		}
		return sb.String()
	}
	return ""
}

// anthropicFirstMessageText returns the text of the "first message" in an Anthropic request,
// treating the system prompt as the first entry (matching converter behaviour).
func anthropicFirstMessageText(v *anthropic.ClaudeMessagesRequest) string {
	if sysTxt := strings.TrimSpace(anthropicSystemText(v.System)); sysTxt != "" {
		return sysTxt
	}
	if len(v.Messages) > 0 {
		return strings.TrimSpace(combinedTextForAnthropicMessage(v.Messages[0]))
	}
	return ""
}

// anthropicMessageTextForHash returns text for hashing: combined content text, or if empty,
// the Input JSON of the first tool_use block.
func anthropicMessageTextForHash(msg *anthropic.MessageParam) string {
	txt := combinedTextForAnthropicMessage(msg)
	if strings.TrimSpace(txt) != "" {
		return txt
	}
	for _, c := range msg.Content {
		if b, ok := c.(*anthropic.MessageContentBlock); ok && b.Type == "tool_use" && b.MessageContentToolUse != nil {
			return string(b.MessageContentToolUse.Input)
		}
	}
	return ""
}

// computeAnthropicMessage5Hashes computes cumulative FNV-32a hashes over the first 5 non-empty
// entries (system prompt first, then messages), taking the first 100 bytes of each text.
// This mirrors the converter which prepends the system prompt as the first OpenAI message.
func computeAnthropicMessage5Hashes(system anthropic.SystemContent, messages []*anthropic.MessageParam) []string {
	hasher := fnv.New32a()
	hashes := make([]string, 0, 5)

	hashOne := func(txt string) {
		if len(hashes) >= 5 || strings.TrimSpace(txt) == "" {
			return
		}
		b := []byte(txt)
		if len(b) > 100 {
			b = b[:100]
		}
		hasher.Write(b)
		hashes = append(hashes, fmt.Sprintf("%08x", hasher.Sum32()))
	}

	// System prompt counts as the first message (matches converter prepend logic).
	hashOne(anthropicSystemText(system))

	for i := 0; i < len(messages) && len(hashes) < 5; i++ {
		msg := messages[i]
		if msg == nil {
			continue
		}
		hashOne(anthropicMessageTextForHash(msg))
	}
	return hashes
}

// PromptTextLenExtractor extracts the total length of all message texts
type PromptTextLenExtractor struct{}

func (e *PromptTextLenExtractor) Features(req *octollm.Request) (any, error) {
	reqBody, err := req.Body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("parse request body failed: %w", err)
	}

	switch v := reqBody.(type) {
	case *openai.ChatCompletionRequest:
		allMsgTextLen := 0
		for _, msg := range v.Messages {
			allMsgTextLen += len([]rune(combinedTextForChatCompletionsMessage(msg)))
		}
		return allMsgTextLen, nil
	case *anthropic.ClaudeMessagesRequest:
		allMsgTextLen := len([]rune(anthropicSystemText(v.System)))
		for _, msg := range v.Messages {
			allMsgTextLen += len([]rune(combinedTextForAnthropicMessage(msg)))
		}
		return allMsgTextLen, nil
	default:
		return nil, fmt.Errorf("unsupported request body type %T", reqBody)
	}
}

// PrefixHashExtractor extracts a hash of the prefix of the first message
type PrefixHashExtractor struct {
	Length int
}

func (e *PrefixHashExtractor) Features(req *octollm.Request) (any, error) {
	reqBody, err := req.Body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("parse request body failed: %w", err)
	}

	switch v := reqBody.(type) {
	case *openai.ChatCompletionRequest:
		if len(v.Messages) == 0 {
			return "", nil
		}
		msg0txt := combinedTextForChatCompletionsMessage(v.Messages[0])
		msg0txt = strings.TrimSpace(msg0txt)
		// first l runes
		prefix := []rune(msg0txt)
		if len(prefix) > e.Length {
			prefix = prefix[:e.Length]
		}

		hasher := fnv.New32a()
		hasher.Write([]byte(v.Model))
		hasher.Write([]byte(string(prefix)))

		return fmt.Sprintf("%08x", hasher.Sum32()), nil
	case *anthropic.ClaudeMessagesRequest:
		msg0txt := anthropicFirstMessageText(v)
		if msg0txt == "" {
			return "", nil
		}
		prefix := []rune(msg0txt)
		if len(prefix) > e.Length {
			prefix = prefix[:e.Length]
		}

		hasher := fnv.New32a()
		hasher.Write([]byte(v.Model))
		hasher.Write([]byte(string(prefix)))

		return fmt.Sprintf("%08x", hasher.Sum32()), nil
	default:
		return nil, fmt.Errorf("unsupported request body type %T", reqBody)
	}
}

// SuffixHashExtractor extracts a hash of the suffix of the first message
type SuffixHashExtractor struct {
	Length int
}

func (e *SuffixHashExtractor) Features(req *octollm.Request) (any, error) {
	reqBody, err := req.Body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("parse request body failed: %w", err)
	}

	switch v := reqBody.(type) {
	case *openai.ChatCompletionRequest:
		if len(v.Messages) == 0 {
			return "", nil
		}
		msg0txt := combinedTextForChatCompletionsMessage(v.Messages[0])
		msg0txt = strings.TrimSpace(msg0txt)
		// last l runes
		suffix := []rune(msg0txt)
		if len(suffix) > e.Length {
			suffix = suffix[len(suffix)-e.Length:]
		}

		hasher := fnv.New32a()
		hasher.Write([]byte(v.Model))
		hasher.Write([]byte(string(suffix)))

		return fmt.Sprintf("%08x", hasher.Sum32()), nil
	case *anthropic.ClaudeMessagesRequest:
		msg0txt := anthropicFirstMessageText(v)
		if msg0txt == "" {
			return "", nil
		}
		suffix := []rune(msg0txt)
		if len(suffix) > e.Length {
			suffix = suffix[len(suffix)-e.Length:]
		}

		hasher := fnv.New32a()
		hasher.Write([]byte(v.Model))
		hasher.Write([]byte(string(suffix)))

		return fmt.Sprintf("%08x", hasher.Sum32()), nil
	default:
		return nil, fmt.Errorf("unsupported request body type %T", reqBody)
	}
}

// Message5HashExtractor produces a composite hash of the first 5 messages: for each message
// takes the first 100 bytes of text (from Content or first tool call's Arguments), feeds
// them into FNV-32a cumulatively, and returns the hex hashes joined by "-" (e.g. "a1b2c3d4-e5f6a7b8-...").
type Message5HashExtractor struct{}

func (e *Message5HashExtractor) Features(req *octollm.Request) (any, error) {
	reqBody, err := req.Body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("parse request body failed: %w", err)
	}

	switch v := reqBody.(type) {
	case *openai.ChatCompletionRequest:
		return strings.Join(computeMessage5Hashes(v.Messages), "-"), nil
	case *anthropic.ClaudeMessagesRequest:
		return strings.Join(computeAnthropicMessage5Hashes(v.System, v.Messages), "-"), nil
	default:
		return nil, fmt.Errorf("unsupported request body type %T", reqBody)
	}
}

// Message5HashArrayExtractor produces the same hashes as Message5HashExtractor but returns []string
// instead of a joined string.
type Message5HashArrayExtractor struct{}

func (e *Message5HashArrayExtractor) Features(req *octollm.Request) (any, error) {
	reqBody, err := req.Body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("parse request body failed: %w", err)
	}

	switch v := reqBody.(type) {
	case *openai.ChatCompletionRequest:
		return computeMessage5Hashes(v.Messages), nil
	case *anthropic.ClaudeMessagesRequest:
		return computeAnthropicMessage5Hashes(v.System, v.Messages), nil
	default:
		return nil, fmt.Errorf("unsupported request body type %T", reqBody)
	}
}

// messageTextForHash returns text from a message for hashing: Content.ExtractText(), or if empty
// and the message has ToolCalls, the first tool call's Function.Arguments.
func messageTextForHash(msg *openai.Message) string {
	msgTxt := combinedTextForChatCompletionsMessage(msg)
	if strings.TrimSpace(msgTxt) != "" {
		return msgTxt
	}
	if len(msg.ToolCalls) == 0 {
		return ""
	}
	toolcall := msg.ToolCalls[0]
	if toolcall != nil && toolcall.Function != nil {
		return toolcall.Function.Arguments
	}
	return ""
}

// computeMessage5Hashes computes cumulative FNV-32a hashes over the first 5 non-empty messages,
// taking the first 100 bytes of each message's text. Returns hex hash strings in order.
func computeMessage5Hashes(messages []*openai.Message) []string {
	hasher := fnv.New32a()
	hashes := make([]string, 0, 5)
	for i := 0; i < len(messages) && len(hashes) < 5; i++ {
		msg := messages[i]
		if msg == nil {
			continue
		}
		msgTxt := messageTextForHash(msg)
		if msgTxt == "" {
			continue
		}
		prefix := []byte(msgTxt)
		if len(prefix) > 100 {
			prefix = prefix[:100]
		}
		hasher.Write(prefix)
		hashes = append(hashes, fmt.Sprintf("%08x", hasher.Sum32()))
	}
	return hashes
}
