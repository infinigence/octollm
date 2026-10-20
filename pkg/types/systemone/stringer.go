package systemone

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// String formats the struct safely for logging (no sensitive data).
func (r SystemOneRequest) String() string {
	w := &strings.Builder{}
	fmt.Fprintf(w, "  Model: %q\n", r.Model)
	fmt.Fprintf(w, "  State: %s\n", rawString(r.State))
	fmt.Fprintf(w, "  Questions: len(%d)\n", len(r.Questions))
	for _, name := range slices.Sorted(maps.Keys(r.Questions)) {
		fmt.Fprintf(w, "    %s\n", systemOneQuestionString(name, r.Questions[name]))
	}
	return fmt.Sprintf("(SystemOneRequest) {\n%s}", w.String())
}

// systemOneQuestionString formats one question safely for logging: choice
// labels are printed, descriptions and instructions only by length.
func systemOneQuestionString(name string, q SystemOneQuestion) string {
	w := &strings.Builder{}
	fmt.Fprintf(w, "{name=%q, ", name)
	switch v := q.(type) {
	case *SystemOneNoulQuestion:
		fmt.Fprintf(w, "type=noul, instructions=len(%d)", len(v.Instructions))
		if c := v.Criteria; c != nil {
			fmt.Fprintf(w, ", criteria={true=%s, false=%s}", optionalLen(c.True), optionalLen(c.False))
		}
	case *SystemOneChoiceQuestion:
		fmt.Fprintf(w, "type=choice, instructions=len(%d), criteria=[", len(v.Instructions))
		for i, label := range slices.Sorted(maps.Keys(v.Criteria)) {
			if i > 0 {
				w.WriteString(", ")
			}
			fmt.Fprintf(w, "%q", label)
		}
		w.WriteString("]")
	case *SystemOneScoreQuestion:
		fmt.Fprintf(w, "type=score, instructions=len(%d), criteria=len(%d)", len(v.Instructions), len(v.Criteria))
	case *SystemOneBoxQuestion:
		fmt.Fprintf(w, "type=box, instructions=len(%d)", len(v.Instructions))
	case *SystemOneGeneralQuestion:
		fmt.Fprintf(w, "type=%s", v.Type)
	default:
		w.WriteString("type=<nil>")
	}
	w.WriteString("}")
	return w.String()
}

func optionalLen(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return fmt.Sprintf("len(%d)", len(*s))
}

// String formats the struct safely for logging (no sensitive data).
func (r SystemOneResponse) String() string {
	w := &strings.Builder{}
	if r.ID != "" {
		fmt.Fprintf(w, "  ID: %q\n", r.ID)
	}
	if r.RequestID != "" {
		fmt.Fprintf(w, "  RequestID: %q\n", r.RequestID)
	}
	fmt.Fprintf(w, "  Model: %q\n", r.Model)
	fmt.Fprintf(w, "  Answers: len(%d)\n", len(r.Answers))
	for _, name := range slices.Sorted(maps.Keys(r.Answers)) {
		fmt.Fprintf(w, "    %s\n", r.Answers[name].string(name))
	}
	if u := r.Usage; u != nil {
		fmt.Fprintf(w, "  Usage: input=%d", u.InputTokens)
		if u.OutputTokens != nil {
			fmt.Fprintf(w, ", output=%d", *u.OutputTokens)
		}
		w.WriteString("\n")
	}
	return fmt.Sprintf("(SystemOneResponse) {\n%s}", w.String())
}

func (a SystemOneAnswer) string(name string) string {
	w := &strings.Builder{}
	fmt.Fprintf(w, "{name=%q, type=%s", name, a.Type)
	if a.Noul != nil {
		fmt.Fprintf(w, ", noul=%g", *a.Noul)
	}
	if a.Choice != nil {
		fmt.Fprintf(w, ", choice=%q", *a.Choice)
	}
	if a.Score != nil {
		fmt.Fprintf(w, ", score=%g", *a.Score)
	}
	if a.Confidence != nil {
		fmt.Fprintf(w, ", confidence=%g", *a.Confidence)
	}
	if len(a.Probabilities) > 0 {
		w.WriteString(", probabilities={")
		for i, k := range slices.Sorted(maps.Keys(a.Probabilities)) {
			if i > 0 {
				w.WriteString(", ")
			}
			fmt.Fprintf(w, "%q: %g", k, a.Probabilities[k])
		}
		w.WriteString("}")
	}
	if len(a.Legend) > 0 {
		fmt.Fprintf(w, ", legend=%s", rawString(a.Legend))
	}
	w.WriteString("}")
	return w.String()
}

// rawString describes a raw JSON value by its kind and byte length, without
// exposing its content.
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "<nil>"
	}
	switch raw[0] {
	case '"':
		return fmt.Sprintf("string(len=%d)", len(raw))
	case '{':
		return fmt.Sprintf("object(len=%d)", len(raw))
	case '[':
		return fmt.Sprintf("array(len=%d)", len(raw))
	default:
		return fmt.Sprintf("other(len=%d)", len(raw))
	}
}
