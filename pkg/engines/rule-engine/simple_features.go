package ruleengine

import (
	"fmt"
	"hash"
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
	if msg.Content != nil {
		return msg.Content.ExtractText()
	}
	return ""
}

func checkIsAnthropicBillingHead(text string) bool {
	return strings.HasPrefix(text, "x-anthropic-billing-header:")
}

// anthropicSystemText extracts plain text from a system field (SystemString or SystemBlocks).
func anthropicSystemText(system anthropic.SystemContent) []string {
	if system == nil {
		return nil
	}
	switch s := system.(type) {
	case anthropic.SystemString:
		return []string{string(s)}
	case anthropic.SystemBlocks:
		var res []string
		for i, b := range s {
			if i == 0 && checkIsAnthropicBillingHead(b.Text) {
				continue
			}
			res = append(res, b.Text)
		}
		return res
	}
	return nil
}

// anthropicFirstMessageText returns the text of the "first message" in an Anthropic request,
// treating the system prompt as the first entry (matching converter behaviour).
func anthropicFirstMessageText(v *anthropic.ClaudeMessagesRequest) string {
	if sysTxt := strings.TrimSpace(strings.Join(anthropicSystemText(v.System), "")); sysTxt != "" {
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
	if arr, ok := msg.Content.(anthropic.MessageContentBlockArray); ok {
		for _, block := range arr {
			if b, ok := block.(*anthropic.ToolUseBlockParam); ok {
				return string(b.Input)
			}
		}
	}
	return ""
}

// computeAnthropicMessage5Hashes computes cumulative FNV-32a hashes over the first 5 non-empty
// entries (system prompt first, then messages), taking the first 100 bytes of each text.
// This mirrors the converter which prepends the system prompt as the first OpenAI message.
func computeAnthropicMessage5Hashes(system anthropic.SystemContent, messages []*anthropic.MessageParam) []string {
	hasher := newMessage5HasherV1()

	// Note the blank check differs from the chat-completions path, which skips on == "".
	// Changing it would reshuffle existing shard keys, so it is left as is.
	hashOne := func(txt string) {
		if hasher.count() >= 5 || strings.TrimSpace(txt) == "" {
			return
		}
		hasher.add(txt)
	}

	// System prompt counts as the first message (matches converter prepend logic).
	for _, sysTxt := range anthropicSystemText(system) {
		hashOne(sysTxt)
	}

	for i := 0; i < len(messages) && hasher.count() < 5; i++ {
		msg := messages[i]
		if msg == nil {
			continue
		}
		hashOne(anthropicMessageTextForHash(msg))
	}
	return hasher.hashes
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
		allMsgTextLen := 0
		for _, sysTxt := range anthropicSystemText(v.System) {
			allMsgTextLen += len([]rune(sysTxt))
		}
		for _, msg := range v.Messages {
			allMsgTextLen += len([]rune(combinedTextForAnthropicMessage(msg)))
		}
		return allMsgTextLen, nil
	case *openai.ResponsesRequest:
		allMsgTextLen := 0
		for _, msg := range replayResponsesMessages(v) {
			allMsgTextLen += len([]rune(msg.contentText))
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
	case *openai.ResponsesRequest:
		msgs := replayResponsesMessages(v)
		if len(msgs) == 0 {
			return "", nil
		}
		msg0txt := strings.TrimSpace(msgs[0].contentText)
		// first l runes
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
	case *openai.ResponsesRequest:
		msgs := replayResponsesMessages(v)
		if len(msgs) == 0 {
			return "", nil
		}
		msg0txt := strings.TrimSpace(msgs[0].contentText)
		// last l runes
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
	case *openai.ResponsesRequest:
		return strings.Join(computeResponsesMessage5Hashes(v), "-"), nil
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
	case *openai.ResponsesRequest:
		return computeResponsesMessage5Hashes(v), nil
	default:
		return nil, fmt.Errorf("unsupported request body type %T", reqBody)
	}
}

// messageTextForHash returns text from a message for hashing: Content.ExtractText(), or if empty
// and the message has ToolCalls, the first tool call's Function.Arguments.
func messageTextForHash(msg *openai.Message) string {
	return textForHash(combinedTextForChatCompletionsMessage(msg), firstToolCallArgs(msg.ToolCalls))
}

// textForHash picks the text a message contributes to the v1 hash: its content text, or the
// first tool call's arguments when the content text is blank. Shared by the chat-completions
// path and the Responses replay. Unlike messageTextForHashV2 it applies no post-processor.
func textForHash(contentText, firstToolCallArgs string) string {
	if strings.TrimSpace(contentText) != "" {
		return contentText
	}
	return firstToolCallArgs
}

// message5HasherV1 accumulates the cumulative FNV-32a hashes described on
// Message5HashExtractor. Every request format folds its messages in through this type so they
// all hash identically.
type message5HasherV1 struct {
	hasher hash.Hash32
	hashes []string
}

func newMessage5HasherV1() *message5HasherV1 {
	return &message5HasherV1{hasher: fnv.New32a(), hashes: make([]string, 0, 5)}
}

func (h *message5HasherV1) count() int { return len(h.hashes) }

// add folds one message's text in: the first 100 bytes are written and the resulting hash is
// recorded. Empty text records nothing.
func (h *message5HasherV1) add(text string) {
	if text == "" {
		return
	}
	b := []byte(text)
	if len(b) > 100 {
		b = b[:100]
	}
	h.hasher.Write(b)
	h.hashes = append(h.hashes, fmt.Sprintf("%08x", h.hasher.Sum32()))
}

// computeMessage5Hashes computes cumulative FNV-32a hashes over the first 5 non-empty messages,
// taking the first 100 bytes of each message's text. Returns hex hash strings in order.
func computeMessage5Hashes(messages []*openai.Message) []string {
	hasher := newMessage5HasherV1()
	for i := 0; i < len(messages) && hasher.count() < 5; i++ {
		msg := messages[i]
		if msg == nil {
			continue
		}
		hasher.add(messageTextForHash(msg))
	}
	return hasher.hashes
}
