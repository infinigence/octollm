package usage

import (
	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
	"github.com/infinigence/octollm/pkg/types/rerank"
	"github.com/infinigence/octollm/pkg/types/vertex"
)

// Usage is the token usage one upstream call reported. Each implementation is
// the upstream's own wire usage type redefined in this package, so a consumer
// that needs a format-specific count type-asserts to it (e.g. *Claude for the
// cache TTL split).
//
// Implementations wrap memory shared with the parsed response body and must be
// treated as read-only.
type Usage interface {
	// GetTotalTokens returns the total the upstream reported, or the sum of the
	// reported parts when it reported no total.
	GetTotalTokens() int64
	// Accumulate returns the usage after later, a subsequent report from the
	// same response, is folded in. It modifies neither side, so a usage already
	// handed out stays valid. A later report of another type replaces this one.
	Accumulate(later Usage) Usage
}

// ChatCompletions is the usage of chat/completions and completions.
type ChatCompletions openai.Usage

type Responses openai.ResponsesUsage

type Claude anthropic.Usage

type Vertex vertex.UsageMetadata

type Embedding openai.EmbeddingUsage

type Rerank rerank.RerankUsage

var (
	_ Usage = (*ChatCompletions)(nil)
	_ Usage = (*Responses)(nil)
	_ Usage = (*Claude)(nil)
	_ Usage = (*Vertex)(nil)
	_ Usage = (*Embedding)(nil)
	_ Usage = (*Rerank)(nil)
)

func (u *ChatCompletions) GetTotalTokens() int64 {
	return orSum(u.TotalTokens, u.PromptTokens, u.CompletionTokens)
}

// Accumulate overlays the counts later reports field by field: a stream may
// carry usage on several chunks, and a count one omits keeps its earlier value.
func (u *ChatCompletions) Accumulate(later Usage) Usage {
	l, ok := later.(*ChatCompletions)
	if !ok {
		return later
	}
	m := *u
	m.PromptTokens = overlay(u.PromptTokens, l.PromptTokens)
	m.CompletionTokens = overlay(u.CompletionTokens, l.CompletionTokens)
	m.TotalTokens = overlay(u.TotalTokens, l.TotalTokens)
	m.PromptTokensDetails = overlayStruct(u.PromptTokensDetails, l.PromptTokensDetails,
		func(p, l openai.PromptTokensDetails) openai.PromptTokensDetails {
			p.CachedTokens = overlay(p.CachedTokens, l.CachedTokens)
			p.AudioTokens = overlay(p.AudioTokens, l.AudioTokens)
			p.CacheWriteTokens = overlay(p.CacheWriteTokens, l.CacheWriteTokens)
			return p
		})
	m.CompletionTokensDetails = overlayStruct(u.CompletionTokensDetails, l.CompletionTokensDetails,
		func(p, l openai.CompletionTokensDetails) openai.CompletionTokensDetails {
			p.ReasoningTokens = overlay(p.ReasoningTokens, l.ReasoningTokens)
			p.AudioTokens = overlay(p.AudioTokens, l.AudioTokens)
			return p
		})
	return &m
}

func (u *Responses) GetTotalTokens() int64 {
	return orSum(u.TotalTokens, u.InputTokens, u.OutputTokens)
}

// Accumulate overlays the counts later reports field by field, so the all-zero
// usage some events carry does not clobber real counts.
func (u *Responses) Accumulate(later Usage) Usage {
	l, ok := later.(*Responses)
	if !ok {
		return later
	}
	m := *u
	m.InputTokens = overlay(u.InputTokens, l.InputTokens)
	m.OutputTokens = overlay(u.OutputTokens, l.OutputTokens)
	m.TotalTokens = overlay(u.TotalTokens, l.TotalTokens)
	m.InputTokensDetails = overlayStruct(u.InputTokensDetails, l.InputTokensDetails,
		func(p, l openai.ResponsesInputTokenDetails) openai.ResponsesInputTokenDetails {
			p.CachedTokens = overlay(p.CachedTokens, l.CachedTokens)
			p.CacheWriteTokens = overlay(p.CacheWriteTokens, l.CacheWriteTokens)
			return p
		})
	m.OutputTokensDetails = overlayStruct(u.OutputTokensDetails, l.OutputTokensDetails,
		func(p, l openai.ResponsesOutputTokenDetails) openai.ResponsesOutputTokenDetails {
			p.ReasoningTokens = overlay(p.ReasoningTokens, l.ReasoningTokens)
			return p
		})
	return &m
}

// GetTotalTokens sums input, cache writes, cache reads and output: Anthropic
// reports no total, and those four counts are disjoint.
func (u *Claude) GetTotalTokens() int64 {
	var total int64
	for _, p := range []*int64{u.InputTokens, u.CacheCreationInputTokens, u.CacheReadInputTokens, u.OutputTokens} {
		if p != nil {
			total += *p
		}
	}
	return total
}

// Accumulate overlays the counts later reports: a stream reports input and
// cache counts in message_start and the final output count in message_delta.
func (u *Claude) Accumulate(later Usage) Usage {
	l, ok := later.(*Claude)
	if !ok {
		return later
	}
	m := *u
	m.InputTokens = overlay(u.InputTokens, l.InputTokens)
	m.OutputTokens = overlay(u.OutputTokens, l.OutputTokens)
	m.CacheCreationInputTokens = overlay(u.CacheCreationInputTokens, l.CacheCreationInputTokens)
	m.CacheReadInputTokens = overlay(u.CacheReadInputTokens, l.CacheReadInputTokens)
	m.CacheCreation = overlayStruct(u.CacheCreation, l.CacheCreation,
		func(p, l anthropic.CacheCreation) anthropic.CacheCreation {
			p.Ephemeral5mInputTokens = overlay(p.Ephemeral5mInputTokens, l.Ephemeral5mInputTokens)
			p.Ephemeral1hInputTokens = overlay(p.Ephemeral1hInputTokens, l.Ephemeral1hInputTokens)
			return p
		})
	return &m
}

func (u *Vertex) GetTotalTokens() int64 {
	return orSum(u.TotalTokenCount, u.PromptTokenCount, u.CandidatesTokenCount, u.ThoughtsTokenCount)
}

// Accumulate returns later: every streamed chunk reports the usage so far.
func (u *Vertex) Accumulate(later Usage) Usage { return later }

func (u *Embedding) GetTotalTokens() int64 {
	return orSum(u.TotalTokens, u.PromptTokens)
}

// Accumulate returns later: embeddings are never streamed, so a later report is
// a complete one.
func (u *Embedding) Accumulate(later Usage) Usage { return later }

func (u *Rerank) GetTotalTokens() int64 {
	return orSum(u.TotalTokens, u.PromptTokens)
}

// Accumulate returns later: rerank is never streamed, so a later report is a
// complete one.
func (u *Rerank) Accumulate(later Usage) Usage { return later }

// orSum returns total when it is reported, else the sum of parts.
func orSum(total int, parts ...int) int64 {
	if total > 0 {
		return int64(total)
	}
	var sum int64
	for _, p := range parts {
		sum += int64(p)
	}
	return sum
}

// overlay returns later when it is reported (non-zero, or non-nil for a
// pointer), else prev.
func overlay[T comparable](prev, later T) T {
	var zero T
	if later != zero {
		return later
	}
	return prev
}

// overlayStruct merges two optional detail structs with merge, allocating a new
// one only when both are set so neither input is modified.
func overlayStruct[T any](prev, later *T, merge func(prev, later T) T) *T {
	switch {
	case later == nil:
		return prev
	case prev == nil:
		return later
	}
	m := merge(*prev, *later)
	return &m
}
