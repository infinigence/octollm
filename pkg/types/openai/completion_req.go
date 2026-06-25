package openai

import "encoding/json"

// CompletionRequest represents the request structure for OpenAI /v1/completions API (legacy)
type CompletionRequest struct {
	Model            string             `json:"model"`
	Prompt           json.RawMessage    `json:"prompt" binding:"required"` // Can be string, array of strings, or array of tokens
	BestOf           *int               `json:"best_of,omitempty"`
	Echo             *bool              `json:"echo,omitempty"`
	FrequencyPenalty *float64           `json:"frequency_penalty,omitempty"`
	LogitBias        map[string]float64 `json:"logit_bias,omitempty"`
	LogProbs         *bool              `json:"logprobs,omitempty"`
	MaxTokens        *int               `json:"max_tokens,omitempty"`
	N                *int               `json:"n,omitempty"`
	PresencePenalty  *float64           `json:"presence_penalty,omitempty"`
	Seed             *int               `json:"seed,omitempty"`
	Stop             []string           `json:"stop,omitempty"`
	Stream           bool               `json:"stream,omitempty"`
	StreamOptions    *StreamOptions     `json:"stream_options,omitempty"`
	Temperature      *float64           `json:"temperature,omitempty"`
	TopP             *float64           `json:"top_p,omitempty"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"` // 是否在流式响应中包含 usage 信息
}
