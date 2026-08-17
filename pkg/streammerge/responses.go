package streammerge

import (
	"errors"
	"sort"
	"strings"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
)

// responsesMerger accumulates OpenAI Responses API SSE events into a single
// response object. Events arrive as: response.created, then per output item
// output_item.added / (content_part.added, output_text.delta*, ... ) /
// output_item.done, then a terminal response.completed (or .incomplete/.failed)
// carrying the final status and usage.
//
// Two sources describe each item and the merger prefers the more reliable one:
// output_item.done carries the finished item verbatim, so it wins; the streamed
// deltas are the fallback for gateways that never emit it. Streamed fragments
// are gathered in strings.Builder accumulators and materialized once in
// finalize, so building the result is O(total bytes) rather than the O(n^2) of
// repeated string concatenation.
type responsesMerger struct {
	response openai.ResponsesResponse
	items    map[int]*responsesItemAcc
	// indexByItemID resolves events that identify their item by id instead of
	// output_index.
	indexByItemID map[string]int
	// lastIndex is the fallback for events carrying neither output_index nor a
	// known item_id.
	lastIndex int
	// snapshotOutput is the output array from a terminal lifecycle event, used
	// only when no per-item events were seen at all.
	snapshotOutput []*openai.ResponsesOutputItem
	finalized      bool
}

// responsesItemAcc accumulates the streamed events of one output item.
type responsesItemAcc struct {
	index int

	// final is the complete item from output_item.done. When set it is
	// authoritative and the accumulated fields below are ignored.
	final *openai.ResponsesOutputItem

	id     string
	typ    string
	role   string
	callID string
	name   string

	text       strings.Builder
	sawText    bool
	refusal    strings.Builder
	sawRefusal bool
	args       strings.Builder
	sawArgs    bool
	summary    strings.Builder
	sawSummary bool
}

// NewResponsesMerger returns a Merger for OpenAI Responses API streams.
func NewResponsesMerger() Merger {
	return &responsesMerger{
		response:      openai.ResponsesResponse{Object: "response"},
		items:         make(map[int]*responsesItemAcc),
		indexByItemID: make(map[string]int),
	}
}

func (m *responsesMerger) Merge(chunk *octollm.StreamChunk) error {
	parsed, err := chunk.Body.Parsed()
	if err != nil {
		if errors.Is(err, octollm.ErrStreamDone) {
			return nil
		}
		return err
	}
	event, ok := parsed.(*openai.ResponseStreamChunk)
	if !ok || event == nil {
		return nil // unexpected type or terminator
	}

	switch event.Type {
	case "response.created", "response.in_progress", "response.queued",
		"response.completed", "response.incomplete", "response.failed":
		m.mergeSnapshot(event.Response)

	case "response.output_item.added":
		if event.Item == nil {
			return nil
		}
		acc := m.accFor(event)
		acc.id = firstNonEmpty(event.Item.ID, acc.id)
		acc.typ = firstNonEmpty(event.Item.Type, acc.typ)
		acc.role = firstNonEmpty(event.Item.Role, acc.role)
		acc.callID = firstNonEmpty(event.Item.CallID, acc.callID)
		acc.name = firstNonEmpty(event.Item.Name, acc.name)
		if event.Item.ID != "" {
			m.indexByItemID[event.Item.ID] = acc.index
		}

	case "response.output_item.done":
		if event.Item == nil {
			return nil
		}
		acc := m.accFor(event)
		// The done event carries the finished item, so keep it verbatim rather
		// than rebuilding it from the deltas.
		acc.final = event.Item

	case "response.content_part.added":
		// The part is empty at this point; it only announces which kind of
		// content the following deltas carry.
		acc := m.accFor(event)
		if event.Part != nil && event.Part.Type == "refusal" {
			acc.sawRefusal = true
		}

	case "response.output_text.delta":
		acc := m.accFor(event)
		acc.text.WriteString(event.Delta)
		acc.sawText = true

	case "response.output_text.done":
		acc := m.accFor(event)
		// Gateways that skip the deltas deliver the whole text here.
		if acc.text.Len() == 0 && event.Text != "" {
			acc.text.WriteString(event.Text)
		}
		acc.sawText = true

	case "response.refusal.delta":
		acc := m.accFor(event)
		acc.refusal.WriteString(event.Delta)
		acc.sawRefusal = true

	case "response.refusal.done":
		acc := m.accFor(event)
		if acc.refusal.Len() == 0 && event.Text != "" {
			acc.refusal.WriteString(event.Text)
		}
		acc.sawRefusal = true

	case "response.function_call_arguments.delta":
		acc := m.accFor(event)
		acc.args.WriteString(event.Delta)
		acc.sawArgs = true

	case "response.function_call_arguments.done":
		acc := m.accFor(event)
		if acc.args.Len() == 0 && event.Arguments != "" {
			acc.args.WriteString(event.Arguments)
		}
		acc.sawArgs = true

	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		acc := m.accFor(event)
		acc.summary.WriteString(event.Delta)
		acc.sawSummary = true

	case "response.reasoning_summary_text.done", "response.reasoning_text.done":
		acc := m.accFor(event)
		if acc.summary.Len() == 0 && event.Text != "" {
			acc.summary.WriteString(event.Text)
		}
		acc.sawSummary = true
	}
	return nil
}

// mergeSnapshot overlays the lifecycle metadata of a response snapshot onto the
// running result. Only non-empty fields override, so the zeroed usage on
// response.created does not clobber the real counts from response.completed.
func (m *responsesMerger) mergeSnapshot(src *openai.ResponsesResponse) {
	if src == nil {
		return
	}
	if src.Id != "" {
		m.response.Id = src.Id
	}
	if src.Object != "" {
		m.response.Object = src.Object
	}
	if src.Created != 0 {
		m.response.Created = src.Created
	}
	if src.Status != "" {
		m.response.Status = src.Status
	}
	if src.Model != "" {
		m.response.Model = src.Model
	}
	if src.Error != nil {
		m.response.Error = src.Error
	}
	if src.IncompleteDetails != nil {
		m.response.IncompleteDetails = src.IncompleteDetails
	}
	if len(src.Output) > 0 {
		m.snapshotOutput = src.Output
	}
	m.response.Usage = mergeResponsesUsage(m.response.Usage, src.Usage)
}

// accFor resolves the accumulator an event belongs to, creating it on first
// sight. Events identify their item by output_index, by item_id, or by neither
// (in which case the most recently touched item is assumed).
func (m *responsesMerger) accFor(event *openai.ResponseStreamChunk) *responsesItemAcc {
	index, ok := m.resolveIndex(event)
	if !ok {
		index = m.lastIndex
	}
	acc, exists := m.items[index]
	if !exists {
		acc = &responsesItemAcc{index: index}
		m.items[index] = acc
	}
	if event.ItemID != "" {
		m.indexByItemID[event.ItemID] = index
		if acc.id == "" {
			acc.id = event.ItemID
		}
	}
	if event.CallID != "" && acc.callID == "" {
		acc.callID = event.CallID
	}
	if event.Name != "" && acc.name == "" {
		acc.name = event.Name
	}
	m.lastIndex = index
	return acc
}

// resolveIndex reports the output index an event refers to, and whether it could
// be determined at all.
func (m *responsesMerger) resolveIndex(event *openai.ResponseStreamChunk) (int, bool) {
	if event.OutputIdx != nil {
		return *event.OutputIdx, true
	}
	if event.ItemID != "" {
		if index, ok := m.indexByItemID[event.ItemID]; ok {
			return index, true
		}
	}
	if event.Item != nil && event.Item.ID != "" {
		if index, ok := m.indexByItemID[event.Item.ID]; ok {
			return index, true
		}
	}
	return 0, false
}

// mergeResponsesUsage folds a usage object from a snapshot into the running
// total field-by-field, so a non-zero src field overrides dst and zero fields
// (e.g. the all-zero usage on response.created) are left untouched.
func mergeResponsesUsage(dst, src *openai.ResponsesUsage) *openai.ResponsesUsage {
	if src == nil {
		return dst
	}
	if dst == nil {
		// Start from a fresh object rather than copying src wholesale: a shallow
		// copy would share the nested details structs with the event, so merging
		// a later event would mutate the earlier event's parsed body.
		dst = &openai.ResponsesUsage{}
	}
	if src.InputTokens != 0 {
		dst.InputTokens = src.InputTokens
	}
	if src.OutputTokens != 0 {
		dst.OutputTokens = src.OutputTokens
	}
	if src.TotalTokens != 0 {
		dst.TotalTokens = src.TotalTokens
	}
	dst.InputTokensDetails = mergeResponsesInputTokenDetails(dst.InputTokensDetails, src.InputTokensDetails)
	dst.OutputTokensDetails = mergeResponsesOutputTokenDetails(dst.OutputTokensDetails, src.OutputTokensDetails)
	return dst
}

func mergeResponsesInputTokenDetails(dst, src *openai.ResponsesInputTokenDetails) *openai.ResponsesInputTokenDetails {
	if src == nil {
		return dst
	}
	if dst == nil {
		cp := *src
		return &cp
	}
	if src.CachedTokens != nil {
		dst.CachedTokens = src.CachedTokens
	}
	if src.CacheWriteTokens != nil {
		dst.CacheWriteTokens = src.CacheWriteTokens
	}
	return dst
}

func mergeResponsesOutputTokenDetails(dst, src *openai.ResponsesOutputTokenDetails) *openai.ResponsesOutputTokenDetails {
	if src == nil {
		return dst
	}
	if dst == nil {
		cp := *src
		return &cp
	}
	if src.ReasoningTokens != nil {
		dst.ReasoningTokens = src.ReasoningTokens
	}
	return dst
}

// build materializes the accumulated events into a final output item.
func (a *responsesItemAcc) build() *openai.ResponsesOutputItem {
	if a.final != nil {
		return a.final
	}

	item := &openai.ResponsesOutputItem{
		ID:     a.id,
		Type:   a.typ,
		Role:   a.role,
		CallID: a.callID,
		Name:   a.name,
	}
	switch {
	case a.sawArgs || a.typ == "function_call":
		item.Arguments = a.args.String()
	case a.sawSummary || a.typ == "reasoning":
		// A reasoning item whose summary never streamed stays summary-less
		// rather than growing an empty summary part.
		if a.sawSummary {
			item.Summary = []*openai.ResponsesReasoningSummaryPart{
				{Type: "summary_text", Text: a.summary.String()},
			}
		}
	default:
		if a.sawText {
			item.Content = append(item.Content, &openai.ResponsesOutputContentItem{
				Type: "output_text",
				Text: a.text.String(),
			})
		}
		if a.sawRefusal {
			item.Content = append(item.Content, &openai.ResponsesOutputContentItem{
				Type:    "refusal",
				Refusal: a.refusal.String(),
			})
		}
	}
	return item
}

func (m *responsesMerger) finalize() {
	if m.finalized {
		return
	}
	m.finalized = true

	if len(m.items) == 0 {
		// No per-item events at all: fall back to the terminal snapshot's output.
		m.response.Output = m.snapshotOutput
		return
	}

	indices := make([]int, 0, len(m.items))
	for index := range m.items {
		indices = append(indices, index)
	}
	sort.Ints(indices)

	output := make([]*openai.ResponsesOutputItem, 0, len(indices))
	for _, index := range indices {
		if item := m.items[index].build(); item != nil {
			output = append(output, item)
		}
	}
	m.response.Output = output
}

func (m *responsesMerger) Merged() (*octollm.UnifiedBody, error) {
	m.finalize()
	return octollm.NewBodyFromParsed(&m.response, &octollm.JSONParser[openai.ResponsesResponse]{}), nil
}

// firstNonEmpty returns v when it is non-empty, otherwise fallback.
func firstNonEmpty(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
