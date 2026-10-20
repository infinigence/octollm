// Package systemone defines the TypeSafe System One decision API types
// (POST /v1/systemone). See https://docs.typesafe.ai/api.
//
// The protocol allows almost any JSON in instructions and criteria. The
// question types below are limited to the shapes that real clients use (plain
// strings, and null for an omitted description); other shapes fail to parse.
// Undocumented fields are not declared; requests are forwarded from the
// original bytes, so they are never lost.
package systemone

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

// SystemOneRequest is the request body of POST /v1/systemone.
type SystemOneRequest struct {
	Model     string             `json:"model"`
	State     json.RawMessage    `json:"state"`     // string / object / array
	Questions SystemOneQuestions `json:"questions"` // keyed by question name
}

// ExtractText returns the text of the state and of every question, in
// question-name order. String values inside an object or array state are
// collected; keys, numbers and booleans are not.
func (r *SystemOneRequest) ExtractText() string {
	text := rawJSONStrings(r.State)
	for _, name := range slices.Sorted(maps.Keys(r.Questions)) {
		if q := r.Questions[name]; q != nil {
			text += q.ExtractText()
		}
	}
	return text
}

// rawJSONStrings concatenates the string values of a raw JSON value, in
// document order for arrays and key order for objects. Object keys are skipped.
func rawJSONStrings(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return ""
		}
		return s
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return ""
		}
		var sb strings.Builder
		for _, k := range slices.Sorted(maps.Keys(obj)) {
			sb.WriteString(rawJSONStrings(obj[k]))
		}
		return sb.String()
	case '[':
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			return ""
		}
		var sb strings.Builder
		for _, v := range arr {
			sb.WriteString(rawJSONStrings(v))
		}
		return sb.String()
	default:
		return ""
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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
	// ExtractText returns the instructions and criteria text of the question.
	ExtractText() string
}

// SystemOneNoulQuestion is a yes/no question.
type SystemOneNoulQuestion struct {
	Type         string                 `json:"type"` // "noul"
	Instructions string                 `json:"instructions,omitempty"`
	Criteria     *SystemOneNoulCriteria `json:"criteria,omitempty"`
}

func (q *SystemOneNoulQuestion) ExtractText() string {
	text := q.Instructions
	if c := q.Criteria; c != nil {
		text += deref(c.True) + deref(c.False)
	}
	return text
}

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

func (q *SystemOneChoiceQuestion) ExtractText() string {
	text := q.Instructions
	for _, label := range slices.Sorted(maps.Keys(q.Criteria)) {
		text += label + deref(q.Criteria[label])
	}
	return text
}

// SystemOneScoreQuestion rates the state on the ordered levels in Criteria,
// from lowest to highest. The score is a level index.
type SystemOneScoreQuestion struct {
	Type         string   `json:"type"` // "score"
	Instructions string   `json:"instructions,omitempty"`
	Criteria     []string `json:"criteria"`
}

func (q *SystemOneScoreQuestion) ExtractText() string {
	return q.Instructions + strings.Join(q.Criteria, "")
}

// SystemOneBoxQuestion locates something in an image and answers with its
// bounding box. It takes no criteria.
type SystemOneBoxQuestion struct {
	Type         string `json:"type"` // "box"
	Instructions string `json:"instructions,omitempty"`
}

func (q *SystemOneBoxQuestion) ExtractText() string { return q.Instructions }

// SystemOneGeneralQuestion is the fallback for question types not modeled
// above, so new types do not break parsing.
type SystemOneGeneralQuestion struct {
	Type string `json:"type"`
}

func (*SystemOneGeneralQuestion) ExtractText() string { return "" }

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
