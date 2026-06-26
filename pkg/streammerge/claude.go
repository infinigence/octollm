package streammerge

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/anthropic"
)

// claudeMessagesMerger accumulates Anthropic Messages SSE events into a single
// messages response. Events arrive as: message_start, then per content block
// content_block_start / content_block_delta* / content_block_stop, then
// message_delta and message_stop.
//
// Streamed text/thinking fragments are gathered in strings.Builder accumulators
// and tool-use JSON in a byte slice, so the result is built in O(total bytes)
// instead of the O(n^2) of repeated string concatenation.
type claudeMessagesMerger struct {
	response  anthropic.ClaudeMessagesResponse
	blocks    []*claudeBlockAcc // indexed by content block index
	finalized bool
}

// claudeBlockAcc accumulates the streamed deltas of one content block.
type claudeBlockAcc struct {
	kind      string // "text", "thinking", "tool_use", or "" for passthrough
	id        string // tool_use
	name      string // tool_use
	text      strings.Builder
	signature strings.Builder
	input     []byte // tool_use partial JSON

	// passthrough holds blocks that carry no deltas (redacted_thinking, unknown
	// types) so they survive into the merged response unchanged.
	passthrough anthropic.MessageContentBlockParam
}

// NewClaudeMessagesMerger returns a Merger for Anthropic Messages streams.
func NewClaudeMessagesMerger() Merger {
	return &claudeMessagesMerger{}
}

func (m *claudeMessagesMerger) Merge(chunk *octollm.StreamChunk) error {
	parsed, err := chunk.Body.Parsed()
	if err != nil {
		if errors.Is(err, octollm.ErrStreamDone) {
			return nil
		}
		return err
	}
	event, ok := parsed.(*anthropic.ClaudeMessagesStreamEvent)
	if !ok || event == nil {
		return nil
	}

	switch event.Type {
	case "message_start":
		if msg := event.Message; msg != nil {
			if msg.ID != "" {
				m.response.ID = msg.ID
			}
			if msg.Type != "" {
				m.response.Type = msg.Type
			}
			if msg.Role != "" {
				m.response.Role = msg.Role
			}
			if msg.Model != "" {
				m.response.Model = msg.Model
			}
			m.response.Usage = mergeClaudeUsage(m.response.Usage, msg.Usage)
		}

	case "content_block_start":
		idx := blockIndex(event)
		for len(m.blocks) <= idx {
			m.blocks = append(m.blocks, nil)
		}
		m.blocks[idx] = newBlockAcc(event.ContentBlock)

	case "content_block_delta":
		idx := blockIndex(event)
		if idx < 0 || idx >= len(m.blocks) || m.blocks[idx] == nil {
			return fmt.Errorf("streammerge: content_block_delta index %d out of range (len=%d)", idx, len(m.blocks))
		}
		if event.Delta != nil {
			m.blocks[idx].applyDelta(&event.Delta.ContentBlockDelta)
		}

	case "message_delta":
		if event.Delta != nil {
			if event.Delta.StopReason != nil {
				m.response.StopReason = *event.Delta.StopReason
			}
			if event.Delta.StopSequence != nil {
				m.response.StopSequence = event.Delta.StopSequence
			}
		}
		// message_delta usage carries the final output token count; merge it so
		// the input/cache counts from message_start are preserved.
		m.response.Usage = mergeClaudeUsage(m.response.Usage, event.Usage)
	}
	return nil
}

func blockIndex(event *anthropic.ClaudeMessagesStreamEvent) int {
	if event.Index != nil {
		return *event.Index
	}
	return 0
}

// newBlockAcc creates a fresh, empty accumulator for the block kind announced by
// content_block_start. The start event's partial fields (e.g. an empty tool_use
// input) are not reused to avoid double counting.
func newBlockAcc(started anthropic.MessageContentBlockParam) *claudeBlockAcc {
	switch b := started.(type) {
	case *anthropic.TextBlockParam:
		return &claudeBlockAcc{kind: "text"}
	case *anthropic.ThinkingBlockParam:
		return &claudeBlockAcc{kind: "thinking"}
	case *anthropic.ToolUseBlockParam:
		return &claudeBlockAcc{kind: "tool_use", id: b.ID, name: b.Name}
	case nil:
		return nil
	default:
		// redacted_thinking and unknown blocks have no deltas; keep as-is.
		return &claudeBlockAcc{passthrough: started}
	}
}

func (a *claudeBlockAcc) applyDelta(delta *anthropic.ContentBlockDelta) {
	switch delta.Type {
	case "text_delta":
		if a.kind == "text" && delta.Text != nil {
			a.text.WriteString(*delta.Text)
		}
	case "thinking_delta":
		if a.kind == "thinking" && delta.Thinking != nil {
			a.text.WriteString(*delta.Thinking)
		}
	case "signature_delta":
		if a.kind == "thinking" && delta.Signature != nil {
			a.signature.WriteString(*delta.Signature)
		}
	case "input_json_delta":
		if a.kind == "tool_use" && delta.PartialJSON != nil {
			a.input = append(a.input, []byte(*delta.PartialJSON)...)
		}
	}
}

// build materializes the accumulated deltas into a final content block.
func (a *claudeBlockAcc) build() anthropic.MessageContentBlockParam {
	switch a.kind {
	case "text":
		return &anthropic.TextBlockParam{Type: "text", Text: a.text.String()}
	case "thinking":
		return &anthropic.ThinkingBlockParam{
			Type:      "thinking",
			Thinking:  a.text.String(),
			Signature: a.signature.String(),
		}
	case "tool_use":
		input := json.RawMessage(a.input)
		if len(input) == 0 {
			// An empty tool_use input would marshal as invalid JSON; default to {}.
			input = json.RawMessage("{}")
		}
		return &anthropic.ToolUseBlockParam{Type: "tool_use", ID: a.id, Name: a.name, Input: input}
	default:
		return a.passthrough
	}
}

// mergeClaudeUsage overlays the non-nil token counts from src onto dst, so the
// input/cache tokens (message_start) and output tokens (message_delta) combine
// into one usage object instead of the later event clobbering the earlier one.
func mergeClaudeUsage(dst, src *anthropic.Usage) *anthropic.Usage {
	if src == nil {
		return dst
	}
	if dst == nil {
		cp := *src
		return &cp
	}
	if src.InputTokens != nil {
		dst.InputTokens = src.InputTokens
	}
	if src.OutputTokens != nil {
		dst.OutputTokens = src.OutputTokens
	}
	if src.CacheCreationInputTokens != nil {
		dst.CacheCreationInputTokens = src.CacheCreationInputTokens
	}
	if src.CacheReadInputTokens != nil {
		dst.CacheReadInputTokens = src.CacheReadInputTokens
	}
	return dst
}

func (m *claudeMessagesMerger) finalize() {
	if m.finalized {
		return
	}
	m.finalized = true
	content := make(anthropic.MessageContentBlockArray, 0, len(m.blocks))
	for _, a := range m.blocks {
		if a == nil {
			continue
		}
		if b := a.build(); b != nil {
			content = append(content, b)
		}
	}
	m.response.Content = content
}

func (m *claudeMessagesMerger) Merged() (*octollm.UnifiedBody, error) {
	m.finalize()
	return octollm.NewBodyFromParsed(&m.response, &octollm.JSONParser[anthropic.ClaudeMessagesResponse]{}), nil
}
