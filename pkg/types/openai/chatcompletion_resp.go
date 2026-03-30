package openai

// ChatCompletionResponse represents a non-streaming chat completion response
type ChatCompletionResponse struct {
	ID                string                  `json:"id"`
	Created           int                     `json:"created"`
	Object            string                  `json:"object,omitempty"`
	Model             string                  `json:"model"`
	Choices           []*ChatCompletionChoice `json:"choices"`
	Usage             *Usage                  `json:"usage"`
	Blocked           *bool                   `json:"blocked,omitempty"`
	SystemFingerprint *string                 `json:"system_fingerprint,omitempty"` // OpenAI 系统指纹
	ServiceTier       *string                 `json:"service_tier,omitempty"`       // 服务层级信息
}

// ChatCompletionStreamChunk represents a streaming chat completion response chunk
type ChatCompletionStreamChunk struct {
	ID                string                        `json:"id"`
	Created           int                           `json:"created"`
	Object            string                        `json:"object,omitempty"`
	Model             string                        `json:"model"`
	Choices           []*ChatCompletionStreamChoice `json:"choices"`
	Usage             *Usage                        `json:"usage,omitempty"` // Only present in the final chunk
	SystemFingerprint *string                       `json:"system_fingerprint,omitempty"`
	ServiceTier       *string                       `json:"service_tier,omitempty"`
}

// ChatCompletionChoice represents a non-streaming choice
type ChatCompletionChoice struct {
	FinishReason string   `json:"finish_reason"`
	Index        int      `json:"index"`
	Message      *Message `json:"message"`
}

// ChatCompletionStreamChoice represents a streaming choice with delta
type ChatCompletionStreamChoice struct {
	FinishReason string   `json:"finish_reason,omitempty"` // Only present when stream ends
	Index        int      `json:"index"`
	Delta        *Message `json:"delta"`
}

type Logprobs struct {
	Content []TokenLogprob `json:"content,omitempty"` // Token 级别的概率信息
}

type TokenLogprob struct {
	Token       string       `json:"token"`                  // Token 文本
	Logprob     float64      `json:"logprob"`                // 对数概率
	Bytes       []int        `json:"bytes,omitempty"`        // UTF-8 字节序列
	TopLogprobs []TopLogprob `json:"top_logprobs,omitempty"` // 前 N 个候选 token
}

type TopLogprob struct {
	Token   string  `json:"token"`           // 候选 Token 文本
	Logprob float64 `json:"logprob"`         // 对数概率
	Bytes   []int   `json:"bytes,omitempty"` // UTF-8 字节序列
}

type Usage struct {
	CompletionTokens        int                      `json:"completion_tokens"`
	PromptTokens            int                      `json:"prompt_tokens"`
	TotalTokens             int                      `json:"total_tokens"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"` // 详细的 completion tokens 信息
	PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details,omitempty"`     // 详细的 prompt tokens 信息
}

// CompletionTokensDetails 完成 token 的详细信息
type CompletionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"` // 推理过程使用的 tokens（如 o1 模型）
	AudioTokens     int `json:"audio_tokens,omitempty"`     // 音频生成使用的 tokens
}

// PromptTokensDetails 提示 token 的详细信息
type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"` // 缓存命中的 tokens
	AudioTokens  int `json:"audio_tokens,omitempty"`  // 音频输入使用的 tokens
}
