package ruleengine

import (
	"fmt"
	"hash"
	"hash/fnv"
	"strconv"
	"strings"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
)

// MessageNHashV2Extractor produces a composite hash of the first N non-empty messages: for each
// message it feeds the first 75 bytes and the last 75 bytes of text (from Content or first tool
// call's Arguments) into a single cumulative FNV-32a hasher. One hex hash is recorded per message,
// taken right after the 75-byte prefix is written; the 75-byte suffix is then written to seed the
// next message's hash (for messages ≤75 bytes the prefix and suffix overlap, i.e. the whole text
// is written twice). Returns "{N}/{h1}/{h2}/..." with no empty trailing slots
// (e.g. "5/a1b2c3d4/e5f6a7b8").
type MessageNHashV2Extractor struct {
	N int
}

func (e *MessageNHashV2Extractor) Features(req *octollm.Request) (any, error) {
	reqBody, err := req.Body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("parse request body failed: %w", err)
	}

	switch v := reqBody.(type) {
	case *openai.ChatCompletionRequest:
		return formatMessageNHashString(e.N, computeMessageNHashesV2(v.Messages, e.N)), nil
	case *anthropic.ClaudeMessagesRequest:
		return formatMessageNHashString(e.N, computeAnthropicMessageNHashesV2(v.System, v.Messages, e.N)), nil
	case *openai.ResponsesRequest:
		return formatMessageNHashString(e.N, computeResponsesMessageNHashesV2(v, e.N)), nil
	default:
		return nil, fmt.Errorf("unsupported request body type %T", reqBody)
	}
}

// MessageNHashArrayV2Extractor produces the same hashes as MessageNHashV2Extractor but returns
// []string instead of a joined string. Short lists are padded with trailing "" to length N so
// leaf strong-hit can treat N as the complete-prefix depth. Join extractors are not padded.
type MessageNHashArrayV2Extractor struct {
	N int
}

func (e *MessageNHashArrayV2Extractor) Features(req *octollm.Request) (any, error) {
	reqBody, err := req.Body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("parse request body failed: %w", err)
	}

	switch v := reqBody.(type) {
	case *openai.ChatCompletionRequest:
		return padHashListToN(computeMessageNHashesV2(v.Messages, e.N), e.N), nil
	case *anthropic.ClaudeMessagesRequest:
		return padHashListToN(computeAnthropicMessageNHashesV2(v.System, v.Messages, e.N), e.N), nil
	case *openai.ResponsesRequest:
		return padHashListToN(computeResponsesMessageNHashesV2(v, e.N), e.N), nil
	default:
		return nil, fmt.Errorf("unsupported request body type %T", reqBody)
	}
}

// padHashListToN appends trailing empty strings so a short hash list has length n.
// Empty input is left empty (no hashes means no shard keys). n<=0 or len>=n is a no-op.
func padHashListToN(hashes []string, n int) []string {
	if n <= 0 || len(hashes) == 0 || len(hashes) >= n {
		return hashes
	}
	out := make([]string, n)
	copy(out, hashes)
	return out
}

// formatMessageNHashString builds "{n}/{h1}/{h2}/..." from real hashes only.
// Empty hashes or n<=0 return "". Trailing empty slots are not added.
func formatMessageNHashString(n int, hashes []string) string {
	if n <= 0 || len(hashes) == 0 {
		return ""
	}
	return strconv.Itoa(n) + "/" + strings.Join(hashes, "/")
}

// messageNHasherV2 accumulates the cumulative FNV-32a hashes described on
// MessageNHashV2Extractor. Every request format folds its messages in through this type so
// they all hash identically.
type messageNHasherV2 struct {
	hasher hash.Hash32
	hashes []string
}

func newMessageNHasherV2(n int) *messageNHasherV2 {
	return &messageNHasherV2{hasher: fnv.New32a(), hashes: make([]string, 0, n)}
}

func (h *messageNHasherV2) count() int { return len(h.hashes) }

// add folds one message's text in: the first 75 bytes are written and the resulting hash is
// recorded, then the last 75 bytes are written so they seed the next message's hash. For text
// of 75 bytes or less the prefix and suffix overlap, i.e. the whole text is written twice.
// Empty text records nothing.
func (h *messageNHasherV2) add(text string) {
	if text == "" {
		return
	}
	b := []byte(text)
	prefix := b
	if len(prefix) > 75 {
		prefix = prefix[:75]
	}
	h.hasher.Write(prefix)
	h.hashes = append(h.hashes, fmt.Sprintf("%08x", h.hasher.Sum32()))
	suffix := b
	if len(suffix) > 75 {
		suffix = suffix[len(suffix)-75:]
	}
	h.hasher.Write(suffix)
}

// computeMessageNHashesV2 computes cumulative FNV-32a hashes over the first n non-empty messages.
// For each message it writes the first 75 bytes of the message's text into the hasher, records the
// current hash, then writes the last 75 bytes so it seeds the following message's hash. Returns hex
// hash strings in order.
func computeMessageNHashesV2(messages []*openai.Message, n int) []string {
	if n <= 0 {
		return nil
	}
	hasher := newMessageNHasherV2(n)
	for i := 0; i < len(messages) && hasher.count() < n; i++ {
		msg := messages[i]
		if msg == nil {
			continue
		}
		hasher.add(chatMessageTextForHashV2(msg))
	}
	return hasher.hashes
}

// computeAnthropicMessageNHashesV2 computes cumulative FNV-32a hashes over the first n non-empty
// entries (system prompt first, then messages), applying the same first-75 + last-75 byte strategy
// as computeMessageNHashesV2 (one hash recorded per entry, prefix-then-suffix into the shared hasher).
// This mirrors the converter which prepends the system prompt as the first OpenAI message.
func computeAnthropicMessageNHashesV2(system anthropic.SystemContent, messages []*anthropic.MessageParam, n int) []string {
	if n <= 0 {
		return nil
	}
	hasher := newMessageNHasherV2(n)

	// Note the blank check differs from the chat-completions path, which skips on == "".
	// Changing it would reshuffle existing shard keys, so it is left as is.
	hashOne := func(txt string) {
		if hasher.count() >= n || strings.TrimSpace(txt) == "" {
			return
		}
		hasher.add(txt)
	}

	// System prompt counts as the first message (matches converter prepend logic) and goes
	// through the same first-75 + last-75 byte hashing as ordinary messages.
	for _, sysTxt := range anthropicSystemText(system) {
		hashOne(sysTxt)
	}

	for i := 0; i < len(messages) && hasher.count() < n; i++ {
		msg := messages[i]
		if msg == nil {
			continue
		}
		hashOne(anthropicMessageTextForHashV2(msg))
	}
	return hasher.hashes
}

var Msg5HashV2_MessageTextPostProcessor func(string) string = nil

// messageTextForHashV2 picks the text a message contributes to the hash: its content text
// (after the optional post-processor), or the first tool call's arguments when the content
// text is blank. Shared by the chat-completions path and the Responses replay so both apply
// the post-processor at the same point.
func messageTextForHashV2(contentText, firstToolCallArgs string) string {
	if Msg5HashV2_MessageTextPostProcessor != nil {
		contentText = Msg5HashV2_MessageTextPostProcessor(contentText)
	}
	if strings.TrimSpace(contentText) != "" {
		return contentText
	}
	return firstToolCallArgs
}

// chatMessageTextForHashV2 returns text from a message for hashing: Content.ExtractText(), or if empty
// and the message has ToolCalls, the first tool call's Function.Arguments.
func chatMessageTextForHashV2(msg *openai.Message) string {
	msgTxt := ""
	if msg.Content != nil {
		msgTxt = msg.Content.ExtractText()
	}
	return messageTextForHashV2(msgTxt, firstToolCallArgs(msg.ToolCalls))
}

// firstToolCallArgs returns the first tool call's raw arguments JSON, or "" when there is none.
func firstToolCallArgs(toolCalls []*openai.MessageToolCall) string {
	if len(toolCalls) == 0 {
		return ""
	}
	toolcall := toolCalls[0]
	if toolcall != nil && toolcall.Function != nil {
		return toolcall.Function.Arguments
	}
	return ""
}

// anthropicMessageTextForHashV2 returns text for hashing: combined content text, or if empty,
// the Input JSON of the first tool_use block.
func anthropicMessageTextForHashV2(msg *anthropic.MessageParam) string {
	msgTxt := ""
	if msg.Content != nil {
		msgTxt = msg.Content.ExtractText()
	}
	if Msg5HashV2_MessageTextPostProcessor != nil {
		msgTxt = Msg5HashV2_MessageTextPostProcessor(msgTxt)
	}
	if strings.TrimSpace(msgTxt) != "" {
		return msgTxt
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
