package ruleengine

import (
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
)

// Message5HashV2Extractor produces a composite hash of the first 5 non-empty messages: for each
// message it feeds the first 75 bytes and the last 75 bytes of text (from Content or first tool
// call's Arguments) into a single cumulative FNV-32a hasher. One hex hash is recorded per message,
// taken right after the 75-byte prefix is written; the 75-byte suffix is then written to seed the
// next message's hash (for messages ≤75 bytes the prefix and suffix overlap, i.e. the whole text
// is written twice). Returns the hex hashes joined by "-" (e.g. "a1b2c3d4-e5f6a7b8-...").
type Message5HashV2Extractor struct{}

func (e *Message5HashV2Extractor) Features(req *octollm.Request) (any, error) {
	reqBody, err := req.Body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("parse request body failed: %w", err)
	}

	switch v := reqBody.(type) {
	case *openai.ChatCompletionRequest:
		return strings.Join(computeMessage5HashesV2(v.Messages), "-"), nil
	case *anthropic.ClaudeMessagesRequest:
		return strings.Join(computeAnthropicMessage5HashesV2(v.System, v.Messages), "-"), nil
	default:
		return nil, fmt.Errorf("unsupported request body type %T", reqBody)
	}
}

// Message5HashArrayV2Extractor produces the same hashes as Message5HashV2Extractor but returns
// []string instead of a joined string.
type Message5HashArrayV2Extractor struct{}

func (e *Message5HashArrayV2Extractor) Features(req *octollm.Request) (any, error) {
	reqBody, err := req.Body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("parse request body failed: %w", err)
	}

	switch v := reqBody.(type) {
	case *openai.ChatCompletionRequest:
		return computeMessage5HashesV2(v.Messages), nil
	case *anthropic.ClaudeMessagesRequest:
		return computeAnthropicMessage5HashesV2(v.System, v.Messages), nil
	default:
		return nil, fmt.Errorf("unsupported request body type %T", reqBody)
	}
}

// computeMessage5HashesV2 computes cumulative FNV-32a hashes over the first 5 non-empty messages.
// For each message it writes the first 75 bytes of the message's text into the hasher, records the
// current hash, then writes the last 75 bytes so it seeds the following message's hash. Returns hex
// hash strings in order.
func computeMessage5HashesV2(messages []*openai.Message) []string {
	hasher := fnv.New32a()
	hashes := make([]string, 0, 5)
	for i := 0; i < len(messages) && len(hashes) < 5; i++ {
		msg := messages[i]
		if msg == nil {
			continue
		}
		msgTxt := chatMessageTextForHashV2(msg)
		if msgTxt == "" {
			continue
		}
		msgBytes := []byte(msgTxt)
		prefix := msgBytes
		if len(prefix) > 75 {
			prefix = prefix[:75]
		}
		hasher.Write(prefix)
		hashes = append(hashes, fmt.Sprintf("%08x", hasher.Sum32()))
		suffix := msgBytes
		if len(suffix) > 75 {
			suffix = suffix[len(suffix)-75:]
		}
		hasher.Write(suffix)
	}
	return hashes
}

// computeAnthropicMessage5HashesV2 computes cumulative FNV-32a hashes over the first 5 non-empty
// entries (system prompt first, then messages), applying the same first-75 + last-75 byte strategy
// as computeMessage5HashesV2 (one hash recorded per entry, prefix-then-suffix into the shared hasher).
// This mirrors the converter which prepends the system prompt as the first OpenAI message.
func computeAnthropicMessage5HashesV2(system anthropic.SystemContent, messages []*anthropic.MessageParam) []string {
	hasher := fnv.New32a()
	hashes := make([]string, 0, 5)

	hashOne := func(txt string) {
		if len(hashes) >= 5 || strings.TrimSpace(txt) == "" {
			return
		}
		b := []byte(txt)
		prefix := b
		if len(prefix) > 75 {
			prefix = prefix[:75]
		}
		hasher.Write(prefix)
		hashes = append(hashes, fmt.Sprintf("%08x", hasher.Sum32()))
		suffix := b
		if len(suffix) > 75 {
			suffix = suffix[len(suffix)-75:]
		}
		hasher.Write(suffix)
	}

	// System prompt counts as the first message (matches converter prepend logic) and goes
	// through the same first-75 + last-75 byte hashing as ordinary messages.
	for _, sysTxt := range anthropicSystemText(system) {
		hashOne(sysTxt)
	}

	for i := 0; i < len(messages) && len(hashes) < 5; i++ {
		msg := messages[i]
		if msg == nil {
			continue
		}
		hashOne(anthropicMessageTextForHashV2(msg))
	}
	return hashes
}

var Msg5HashV2_MessageTextPostProcessor func(string) string = nil

// chatMessageTextForHashV2 returns text from a message for hashing: Content.ExtractText(), or if empty
// and the message has ToolCalls, the first tool call's Function.Arguments.
func chatMessageTextForHashV2(msg *openai.Message) string {
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
	if len(msg.ToolCalls) == 0 {
		return ""
	}
	toolcall := msg.ToolCalls[0]
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
