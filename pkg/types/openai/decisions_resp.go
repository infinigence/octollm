package openai

import "encoding/json"

// DecisionsResponse is the response body of the OpenAI Decisions API.
type DecisionsResponse struct {
	ID      string            `json:"id,omitempty"`
	Model   string            `json:"model"`
	Answers []DecisionsAnswer `json:"answers"` // in the same order as the request questions
	Usage   *DecisionsUsage   `json:"usage,omitempty"`
}

// DecisionsUsage is the token usage of a Decisions response. Its schema is
// identical to the Responses API usage (see openai-python
// src/openai/types/decision.py), so it is an alias of ResponsesUsage.
type DecisionsUsage = ResponsesUsage

// DecisionsAnswer is one entry of DecisionsResponse.Answers. Which fields are
// set depends on Type.
type DecisionsAnswer struct {
	Type          string                 `json:"type"` // predicate / choice / score / refusal
	Name          string                 `json:"name,omitempty"`
	Probability   *float64               `json:"probability,omitempty"`
	Choice        json.RawMessage        `json:"choice,omitempty"` // string / boolean
	Score         *float64               `json:"score,omitempty"`
	Confidence    *float64               `json:"confidence,omitempty"`
	Probabilities []DecisionsProbability `json:"probabilities,omitempty"`
}

// DecisionsProbability is the probability of one choice value or score level.
type DecisionsProbability struct {
	Value       json.RawMessage `json:"value"`           // choice value (string / boolean) or score level
	Label       string          `json:"label,omitempty"` // score level label
	Probability float64         `json:"probability"`
}
