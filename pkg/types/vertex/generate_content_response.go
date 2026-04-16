package vertex

// GenerateContentResponse represents the response from Vertex AI generateContent API
type GenerateContentResponse struct {
	Candidates    []Candidate    `json:"candidates,omitempty"`
	UsageMetadata *UsageMetadata `json:"usageMetadata,omitempty"`
	ModelVersion  string         `json:"modelVersion,omitempty"`
	ResponseID    string         `json:"responseId,omitempty"`
}

// Candidate represents a generated candidate
type Candidate struct {
	Content          *Content          `json:"content,omitempty"`
	FinishReason     string            `json:"finishReason,omitempty"`
	SafetyRatings    []SafetyRating    `json:"safetyRatings,omitempty"`
	CitationMetadata *CitationMetadata `json:"citationMetadata,omitempty"`
	AvgLogprobs      float64           `json:"avgLogprobs,omitempty"`
	LogprobsResult   *LogprobsResult   `json:"logprobsResult,omitempty"`
}

// SafetyRating represents a safety rating
type SafetyRating struct {
	Category    string `json:"category"`
	Probability string `json:"probability"`
	Blocked     bool   `json:"blocked,omitempty"`
}

// CitationMetadata represents citation metadata
type CitationMetadata struct {
	Citations []Citation `json:"citations,omitempty"`
}

// Citation represents a single citation
type Citation struct {
	StartIndex      int    `json:"startIndex,omitempty"`
	EndIndex        int    `json:"endIndex,omitempty"`
	URI             string `json:"uri,omitempty"`
	Title           string `json:"title,omitempty"`
	License         string `json:"license,omitempty"`
	PublicationDate *Date  `json:"publicationDate,omitempty"`
}

// Date represents a publication date
type Date struct {
	Year  int `json:"year,omitempty"`
	Month int `json:"month,omitempty"`
	Day   int `json:"day,omitempty"`
}

// LogprobsResult represents log probabilities result
type LogprobsResult struct {
	TopCandidates    []TopCandidates   `json:"topCandidates,omitempty"`
	ChosenCandidates []ChosenCandidate `json:"chosenCandidates,omitempty"`
}

// TopCandidates represents top candidate tokens at a step
type TopCandidates struct {
	Candidates []TokenLogprob `json:"candidates,omitempty"`
}

// ChosenCandidate represents the chosen token at a step
type ChosenCandidate struct {
	Token          string  `json:"token,omitempty"`
	LogProbability float64 `json:"logProbability,omitempty"`
}

// TokenLogprob represents a token with its log probability
type TokenLogprob struct {
	Token          string  `json:"token,omitempty"`
	LogProbability float64 `json:"logProbability,omitempty"`
}

// UsageMetadata represents token usage metadata
type UsageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount    int `json:"candidatesTokenCount,omitempty"`
	TotalTokenCount         int `json:"totalTokenCount,omitempty"`
	CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount,omitempty"`
}

// StreamGenerateContentResponse represents a streaming response chunk
type StreamGenerateContentResponse struct {
	Candidates    []Candidate    `json:"candidates,omitempty"`
	UsageMetadata *UsageMetadata `json:"usageMetadata,omitempty"`
	ModelVersion  string         `json:"modelVersion,omitempty"`
	ResponseID    string         `json:"responseId,omitempty"`
}
