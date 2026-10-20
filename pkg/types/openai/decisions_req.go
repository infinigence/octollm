package openai

import "encoding/json"

// DecisionsRequest is the request body of the OpenAI Decisions API
// (POST /v1/decisions).
//
// The API is still in preview, so only fields that are documented with a
// single, stable JSON type are typed concretely. Polymorphic fields are kept
// as json.RawMessage.
// Undocumented fields are not declared; requests are forwarded from the
// original bytes, so they are never lost.
type DecisionsRequest struct {
	Model string `json:"model"`

	// Input has the same shape as the Responses API input: a string, or an
	// array of messages whose content parts are input_text / input_image.
	Input ResponsesInputValue `json:"input"`

	Questions []DecisionsQuestion `json:"questions"`
}

func (r *DecisionsRequest) UnmarshalJSON(data []byte) error {
	type Alias DecisionsRequest
	aux := struct {
		Input responsesInputField `json:"input"`
		*Alias
	}{
		Alias: (*Alias)(r),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	r.Input = aux.Input.Value
	return nil
}

// DecisionsQuestion is one entry of DecisionsRequest.Questions.
type DecisionsQuestion struct {
	Type         string            `json:"type"` // predicate / choice / score
	Name         string            `json:"name,omitempty"`
	Instructions string            `json:"instructions"`
	Choices      []DecisionsChoice `json:"choices,omitempty"`
	Levels       []DecisionsLevel  `json:"levels,omitempty"`
}

// DecisionsChoice is one option of a choice question.
type DecisionsChoice struct {
	Value       json.RawMessage `json:"value"` // string / boolean
	Description string          `json:"description,omitempty"`
}

// DecisionsLevel is one level of a score question.
type DecisionsLevel struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}
