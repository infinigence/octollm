// Package systemone defines the TypeSafe System One decision API types
// (POST /v1/systemone). See https://docs.typesafe.ai/api.
//
// The protocol allows almost any JSON in instructions and criteria. The
// question types below are limited to the shapes that real clients use (plain
// strings, and null for an omitted description); other shapes fail to parse.
// Undocumented fields are not declared; requests are forwarded from the
// original bytes, so they are never lost.
package systemone

import "encoding/json"

// SystemOneRequest is the request body of POST /v1/systemone.
type SystemOneRequest struct {
	Model     string             `json:"model"`
	State     json.RawMessage    `json:"state"`     // string / object / array
	Questions SystemOneQuestions `json:"questions"` // keyed by question name
}

// SystemOneQuestions maps question names to questions.
type SystemOneQuestions map[string]SystemOneQuestion

func (q *SystemOneQuestions) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || len(data) == 0 {
		return nil
	}
	var raws map[string]json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		return err
	}
	out := make(SystemOneQuestions, len(raws))
	for name, raw := range raws {
		var f systemOneQuestionField
		if err := json.Unmarshal(raw, &f); err != nil {
			return err
		}
		out[name] = f.Value
	}
	*q = out
	return nil
}

// SystemOneQuestion is one question, discriminated by its `type` field.
type SystemOneQuestion interface {
	isSystemOneQuestion()
}

// SystemOneNoulQuestion is a yes/no question.
type SystemOneNoulQuestion struct {
	Type         string                 `json:"type"` // "noul"
	Instructions string                 `json:"instructions,omitempty"`
	Criteria     *SystemOneNoulCriteria `json:"criteria,omitempty"`
}

func (*SystemOneNoulQuestion) isSystemOneQuestion() {}

// SystemOneNoulCriteria describes what makes a noul answer true or false.
type SystemOneNoulCriteria struct {
	True  *string `json:"true,omitempty"`
	False *string `json:"false,omitempty"`
}

// SystemOneChoiceQuestion picks one label from Criteria.
type SystemOneChoiceQuestion struct {
	Type         string `json:"type"` // "choice"
	Instructions string `json:"instructions,omitempty"`

	// Criteria maps each candidate label to its description; a nil
	// description is sent as null.
	Criteria map[string]*string `json:"criteria"`
}

func (*SystemOneChoiceQuestion) isSystemOneQuestion() {}

// SystemOneScoreQuestion rates the state on the ordered levels in Criteria,
// from lowest to highest. The score is a level index.
type SystemOneScoreQuestion struct {
	Type         string   `json:"type"` // "score"
	Instructions string   `json:"instructions,omitempty"`
	Criteria     []string `json:"criteria"`
}

func (*SystemOneScoreQuestion) isSystemOneQuestion() {}

// SystemOneBoxQuestion locates something in an image and answers with its
// bounding box. It takes no criteria.
type SystemOneBoxQuestion struct {
	Type         string `json:"type"` // "box"
	Instructions string `json:"instructions,omitempty"`
}

func (*SystemOneBoxQuestion) isSystemOneQuestion() {}

// SystemOneGeneralQuestion is the fallback for question types not modeled
// above, so new types do not break parsing.
type SystemOneGeneralQuestion struct {
	Type string `json:"type"`
}

func (*SystemOneGeneralQuestion) isSystemOneQuestion() {}

type systemOneQuestionField struct {
	Value SystemOneQuestion
}

func (f *systemOneQuestionField) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || len(data) == 0 {
		return nil
	}
	var peek struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &peek); err != nil {
		return err
	}
	var v SystemOneQuestion = &SystemOneGeneralQuestion{}
	switch peek.Type {
	case "noul":
		v = &SystemOneNoulQuestion{}
	case "choice":
		v = &SystemOneChoiceQuestion{}
	case "score":
		v = &SystemOneScoreQuestion{}
	case "box":
		v = &SystemOneBoxQuestion{}
	}
	if err := json.Unmarshal(data, v); err != nil {
		return err
	}
	f.Value = v
	return nil
}
