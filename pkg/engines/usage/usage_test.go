package usage

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
)

func ptr[T any](v T) *T { return &v }

func TestGetTotalTokens(t *testing.T) {
	tests := []struct {
		name  string
		usage Usage
		want  int64
	}{
		{
			name:  "chat completions reported total wins",
			usage: &ChatCompletions{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 130},
			want:  130,
		},
		{
			name:  "chat completions without total sums prompt and completion",
			usage: &ChatCompletions{PromptTokens: 100, CompletionTokens: 20},
			want:  120,
		},
		{
			name:  "responses without total sums input and output",
			usage: &Responses{InputTokens: 50, OutputTokens: 5},
			want:  55,
		},
		{
			name: "claude sums its disjoint counts",
			usage: &Claude{InputTokens: ptr[int64](5), OutputTokens: ptr[int64](7),
				CacheCreationInputTokens: ptr[int64](40), CacheReadInputTokens: ptr[int64](50)},
			want: 102,
		},
		{
			name:  "claude with only some counts reported",
			usage: &Claude{OutputTokens: ptr[int64](9)},
			want:  9,
		},
		{
			name:  "vertex reported total wins",
			usage: &Vertex{PromptTokenCount: 12, CandidatesTokenCount: 3, TotalTokenCount: 25},
			want:  25,
		},
		{
			name:  "vertex without total counts thoughts",
			usage: &Vertex{PromptTokenCount: 12, CandidatesTokenCount: 3, ThoughtsTokenCount: 4, CachedContentTokenCount: 2},
			want:  19,
		},
		{
			name:  "embedding without total",
			usage: &Embedding{PromptTokens: 8},
			want:  8,
		},
		{
			name:  "rerank reporting only total",
			usage: &Rerank{TotalTokens: 9},
			want:  9,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.usage.GetTotalTokens())
		})
	}
}

func TestAccumulate(t *testing.T) {
	tests := []struct {
		name  string
		prev  Usage
		later Usage
		want  Usage
	}{
		{
			name: "chat completions overlays reported fields and details",
			prev: &ChatCompletions{PromptTokens: 10, CompletionTokens: 1,
				PromptTokensDetails: &openai.PromptTokensDetails{CachedTokens: ptr(4)}},
			later: &ChatCompletions{CompletionTokens: 5,
				PromptTokensDetails:     &openai.PromptTokensDetails{CacheWriteTokens: ptr(2)},
				CompletionTokensDetails: &openai.CompletionTokensDetails{ReasoningTokens: ptr(3)}},
			want: &ChatCompletions{PromptTokens: 10, CompletionTokens: 5,
				PromptTokensDetails:     &openai.PromptTokensDetails{CachedTokens: ptr(4), CacheWriteTokens: ptr(2)},
				CompletionTokensDetails: &openai.CompletionTokensDetails{ReasoningTokens: ptr(3)}},
		},
		{
			name: "responses all-zero report keeps earlier counts",
			prev: &Responses{InputTokens: 20, OutputTokens: 3, TotalTokens: 23,
				InputTokensDetails: &openai.ResponsesInputTokenDetails{CachedTokens: ptr(6)}},
			later: &Responses{},
			want: &Responses{InputTokens: 20, OutputTokens: 3, TotalTokens: 23,
				InputTokensDetails: &openai.ResponsesInputTokenDetails{CachedTokens: ptr(6)}},
		},
		{
			name: "claude message_delta keeps message_start input and cache counts",
			prev: &Claude{InputTokens: ptr[int64](5), OutputTokens: ptr[int64](1),
				CacheReadInputTokens: ptr[int64](50), CacheCreationInputTokens: ptr[int64](40),
				CacheCreation: &anthropic.CacheCreation{Ephemeral5mInputTokens: ptr[int64](40)}},
			later: &Claude{OutputTokens: ptr[int64](9),
				CacheCreation: &anthropic.CacheCreation{Ephemeral1hInputTokens: ptr[int64](0)}},
			want: &Claude{InputTokens: ptr[int64](5), OutputTokens: ptr[int64](9),
				CacheReadInputTokens: ptr[int64](50), CacheCreationInputTokens: ptr[int64](40),
				CacheCreation: &anthropic.CacheCreation{Ephemeral5mInputTokens: ptr[int64](40), Ephemeral1hInputTokens: ptr[int64](0)}},
		},
		{
			name:  "vertex takes the later cumulative report",
			prev:  &Vertex{PromptTokenCount: 12, CandidatesTokenCount: 1},
			later: &Vertex{PromptTokenCount: 12, CandidatesTokenCount: 3},
			want:  &Vertex{PromptTokenCount: 12, CandidatesTokenCount: 3},
		},
		{
			name:  "embedding takes the later report",
			prev:  &Embedding{PromptTokens: 8},
			later: &Embedding{PromptTokens: 9},
			want:  &Embedding{PromptTokens: 9},
		},
		{
			name:  "rerank takes the later report",
			prev:  &Rerank{TotalTokens: 8},
			later: &Rerank{TotalTokens: 9},
			want:  &Rerank{TotalTokens: 9},
		},
		{
			name:  "a report of another type replaces",
			prev:  &ChatCompletions{PromptTokens: 10},
			later: &Claude{OutputTokens: ptr[int64](9)},
			want:  &Claude{OutputTokens: ptr[int64](9)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.prev.Accumulate(tt.later))
		})
	}
}

// A usage already handed out through the request metadata must not change
// when a later report is folded into it.
func TestAccumulate_DoesNotModifyInputs(t *testing.T) {
	tests := []struct {
		name        string
		prev, later func() Usage
	}{
		{
			name: "chat completions",
			prev: func() Usage {
				return &ChatCompletions{PromptTokens: 10,
					PromptTokensDetails:     &openai.PromptTokensDetails{CachedTokens: ptr(4)},
					CompletionTokensDetails: &openai.CompletionTokensDetails{AudioTokens: ptr(1)}}
			},
			later: func() Usage {
				return &ChatCompletions{CompletionTokens: 5,
					PromptTokensDetails:     &openai.PromptTokensDetails{CacheWriteTokens: ptr(2)},
					CompletionTokensDetails: &openai.CompletionTokensDetails{ReasoningTokens: ptr(3)}}
			},
		},
		{
			name: "responses",
			prev: func() Usage {
				return &Responses{InputTokens: 20,
					InputTokensDetails:  &openai.ResponsesInputTokenDetails{CachedTokens: ptr(6)},
					OutputTokensDetails: &openai.ResponsesOutputTokenDetails{ReasoningTokens: ptr(1)}}
			},
			later: func() Usage {
				return &Responses{OutputTokens: 3,
					InputTokensDetails:  &openai.ResponsesInputTokenDetails{CacheWriteTokens: ptr(2)},
					OutputTokensDetails: &openai.ResponsesOutputTokenDetails{ReasoningTokens: ptr(2)}}
			},
		},
		{
			name: "claude",
			prev: func() Usage {
				return &Claude{InputTokens: ptr[int64](5),
					CacheCreation: &anthropic.CacheCreation{Ephemeral5mInputTokens: ptr[int64](40)}}
			},
			later: func() Usage {
				return &Claude{OutputTokens: ptr[int64](9),
					CacheCreation: &anthropic.CacheCreation{Ephemeral1hInputTokens: ptr[int64](3)}}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prev, later := tt.prev(), tt.later()
			prev.Accumulate(later)
			assert.Equal(t, tt.prev(), prev)
			assert.Equal(t, tt.later(), later)
		})
	}
}
