package streammerge

import (
	"errors"
	"sort"
	"strings"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
)

// chatCompletionMerger accumulates OpenAI chat.completion.chunk SSE events into
// a single chat.completion response.
//
// Streamed text fragments are gathered in strings.Builder accumulators and
// materialized once in finalize, so building the result is O(total bytes)
// rather than the O(n^2) of repeated string concatenation.
type chatCompletionMerger struct {
	result    openai.ChatCompletionResponse
	choices   map[int]*chatChoiceAcc
	finalized bool
}

// chatChoiceAcc accumulates the streamed deltas for a single choice index.
type chatChoiceAcc struct {
	index        int
	role         string
	finishReason string
	content      strings.Builder
	sawContent   bool
	reasoning    strings.Builder
	sawReasoning bool
	toolCalls    []*chatToolCallAcc // matched by tool-call index
}

// chatToolCallAcc accumulates the streamed fragments of one tool call.
type chatToolCallAcc struct {
	index   int
	id      string
	typ     string
	hasFunc bool
	name    strings.Builder
	args    strings.Builder
}

// NewChatCompletionMerger returns a Merger for OpenAI chat/completions streams.
func NewChatCompletionMerger() Merger {
	return &chatCompletionMerger{
		result:  openai.ChatCompletionResponse{Object: "chat.completion"},
		choices: make(map[int]*chatChoiceAcc),
	}
}

func (m *chatCompletionMerger) Merge(chunk *octollm.StreamChunk) error {
	parsed, err := chunk.Body.Parsed()
	if err != nil {
		if errors.Is(err, octollm.ErrStreamDone) {
			return nil
		}
		return err
	}
	resp, ok := parsed.(*openai.ChatCompletionStreamChunk)
	if !ok || resp == nil {
		return nil // unexpected type or terminator
	}

	if resp.ID != "" {
		m.result.ID = resp.ID
	}
	if resp.Model != "" {
		m.result.Model = resp.Model
	}
	if resp.Created != 0 {
		m.result.Created = resp.Created
	}
	// Usage only appears on the final chunk (stream_options.include_usage),
	// but merge field-by-field so multiple usage-bearing chunks accumulate
	// instead of the later one clobbering the earlier.
	if resp.Usage != nil {
		m.result.Usage = mergeChatCompletionUsage(m.result.Usage, resp.Usage)
	}

	for _, sc := range resp.Choices {
		if sc == nil {
			continue
		}
		acc, ok := m.choices[sc.Index]
		if !ok {
			acc = &chatChoiceAcc{index: sc.Index}
			m.choices[sc.Index] = acc
		}
		if sc.FinishReason != "" {
			acc.finishReason = sc.FinishReason
		}
		d := sc.Delta
		if d == nil {
			continue
		}
		if d.Role != "" {
			acc.role = d.Role
		}
		// Non-string content (arrays) is not expected in chat deltas; only the
		// streamed string fragments are accumulated.
		if s, ok := d.Content.(openai.MessageContentString); ok {
			acc.content.WriteString(string(s))
			acc.sawContent = true
		}
		if s, ok := d.ReasoningContent.(openai.MessageContentString); ok {
			acc.reasoning.WriteString(string(s))
			acc.sawReasoning = true
		}
		for _, tc := range d.ToolCalls {
			acc.mergeToolCall(tc)
		}
	}
	return nil
}

// mergeChatCompletionUsage folds a usage object from a chunk into the running
// total field-by-field, so token counts spread across multiple usage-bearing
// chunks accumulate rather than a later chunk clobbering the earlier one. A
// non-zero src field overrides dst; zero fields are left untouched.
func mergeChatCompletionUsage(dst, src *openai.Usage) *openai.Usage {
	if src == nil {
		return dst
	}
	if dst == nil {
		cp := *src
		return &cp
	}
	if src.CompletionTokens != 0 {
		dst.CompletionTokens = src.CompletionTokens
	}
	if src.PromptTokens != 0 {
		dst.PromptTokens = src.PromptTokens
	}
	if src.TotalTokens != 0 {
		dst.TotalTokens = src.TotalTokens
	}
	dst.CompletionTokensDetails = mergeCompletionTokensDetails(dst.CompletionTokensDetails, src.CompletionTokensDetails)
	dst.PromptTokensDetails = mergePromptTokensDetails(dst.PromptTokensDetails, src.PromptTokensDetails)
	return dst
}

// mergeCompletionTokensDetails folds completion-token detail fields into the
// running total, so a non-zero src field overrides dst and zero fields are left
// untouched (rather than the whole pointer being replaced).
func mergeCompletionTokensDetails(dst, src *openai.CompletionTokensDetails) *openai.CompletionTokensDetails {
	if src == nil {
		return dst
	}
	if dst == nil {
		cp := *src
		return &cp
	}
	if src.ReasoningTokens != 0 {
		dst.ReasoningTokens = src.ReasoningTokens
	}
	if src.AudioTokens != 0 {
		dst.AudioTokens = src.AudioTokens
	}
	return dst
}

// mergePromptTokensDetails folds prompt-token detail fields into the running
// total, so a non-zero src field overrides dst and zero fields are left
// untouched (rather than the whole pointer being replaced).
func mergePromptTokensDetails(dst, src *openai.PromptTokensDetails) *openai.PromptTokensDetails {
	if src == nil {
		return dst
	}
	if dst == nil {
		cp := *src
		return &cp
	}
	if src.CachedTokens != 0 {
		dst.CachedTokens = src.CachedTokens
	}
	if src.AudioTokens != 0 {
		dst.AudioTokens = src.AudioTokens
	}
	return dst
}

// mergeToolCall folds a streamed tool-call delta into the choice, accumulating
// function name/arguments fragments for the matching call index.
func (a *chatChoiceAcc) mergeToolCall(delta *openai.MessageToolCall) {
	if delta == nil {
		return
	}
	var tc *chatToolCallAcc
	for _, existing := range a.toolCalls {
		if existing.index == delta.Index {
			tc = existing
			break
		}
	}
	if tc == nil {
		tc = &chatToolCallAcc{index: delta.Index}
		a.toolCalls = append(a.toolCalls, tc)
	}
	if delta.ID != "" {
		tc.id = delta.ID
	}
	if delta.Type != "" {
		tc.typ = delta.Type
	}
	if delta.Function != nil {
		tc.hasFunc = true
		tc.name.WriteString(delta.Function.Name)
		tc.args.WriteString(delta.Function.Arguments)
	}
}

// build materializes the accumulated deltas into a final choice.
func (a *chatChoiceAcc) build() *openai.ChatCompletionChoice {
	msg := &openai.Message{Role: a.role}
	if a.sawContent {
		msg.Content = openai.MessageContentString(a.content.String())
	}
	if a.sawReasoning {
		msg.ReasoningContent = openai.MessageContentString(a.reasoning.String())
	}
	if len(a.toolCalls) > 0 {
		msg.ToolCalls = make([]*openai.MessageToolCall, 0, len(a.toolCalls))
		for _, tc := range a.toolCalls {
			mtc := &openai.MessageToolCall{Index: tc.index, ID: tc.id, Type: tc.typ}
			if tc.hasFunc {
				mtc.Function = &openai.ToolCallFunction{
					Name:      tc.name.String(),
					Arguments: tc.args.String(),
				}
			}
			msg.ToolCalls = append(msg.ToolCalls, mtc)
		}
	}
	return &openai.ChatCompletionChoice{
		Index:        a.index,
		FinishReason: a.finishReason,
		Message:      msg,
	}
}

func (m *chatCompletionMerger) finalize() {
	if m.finalized {
		return
	}
	m.finalized = true
	choices := make([]*openai.ChatCompletionChoice, 0, len(m.choices))
	for _, acc := range m.choices {
		choices = append(choices, acc.build())
	}
	sort.Slice(choices, func(i, j int) bool {
		return choices[i].Index < choices[j].Index
	})
	m.result.Choices = choices
}

func (m *chatCompletionMerger) Merged() (*octollm.UnifiedBody, error) {
	m.finalize()
	return octollm.NewBodyFromParsed(&m.result, &octollm.JSONParser[openai.ChatCompletionResponse]{}), nil
}
