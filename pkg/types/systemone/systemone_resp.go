package systemone

import "encoding/json"

// SystemOneResponse is the response body of POST /v1/systemone.
type SystemOneResponse struct {
	ID        string                     `json:"id,omitempty"`         // returned by some providers (e.g. OpenRouter)
	RequestID string                     `json:"request_id,omitempty"` // returned by some providers (e.g. Bailian)
	Model     string                     `json:"model"`
	Answers   map[string]SystemOneAnswer `json:"answers"` // keyed by question name
	Usage     *SystemOneUsage            `json:"usage,omitempty"`
}

// SystemOneAnswer is one entry of SystemOneResponse.Answers. Which fields are
// set depends on Type.
type SystemOneAnswer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        *string            `json:"choice,omitempty"` // a key of the question's criteria
	Score         *float64           `json:"score,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Legend        json.RawMessage    `json:"legend,omitempty"` // values echo the criteria, which are polymorphic
}

// SystemOneUsage is the token usage of a System One response.
type SystemOneUsage struct {
	InputTokens int `json:"input_tokens"`

	// OutputTokens is a pointer because some providers (e.g. Bailian) omit it,
	// which must stay distinguishable from an explicit zero.
	OutputTokens *int `json:"output_tokens,omitempty"`
}
