package vertex

import (
	"fmt"
	"strings"
)

// partString formats a Part safely for logging (no sensitive data).
func partString(p Part) string {
	switch {
	case p.Text != "":
		return fmt.Sprintf("text(len=%d)", len(p.Text))
	case p.InlineData != nil:
		return fmt.Sprintf("inlineData(mimeType=%s,len=%d)", p.InlineData.MimeType, len(p.InlineData.Data))
	case p.FileData != nil:
		return fmt.Sprintf("fileData(mimeType=%s)", p.FileData.MimeType)
	case p.FunctionCall != nil:
		return fmt.Sprintf("functionCall(name=%s)", p.FunctionCall.Name)
	case p.FunctionResponse != nil:
		return fmt.Sprintf("functionResponse(name=%s)", p.FunctionResponse.Name)
	default:
		return "unknown"
	}
}

// contentString formats a Content safely for logging.
func contentString(c Content) string {
	parts := make([]string, 0, len(c.Parts))
	for _, p := range c.Parts {
		parts = append(parts, partString(p))
	}
	return fmt.Sprintf("{role=%q, parts=[%s]}", c.Role, strings.Join(parts, ", "))
}

// String formats GenerateContentRequest safely for logging (no sensitive data).
func (r GenerateContentRequest) String() string {
	w := &strings.Builder{}
	if r.CachedContent != "" {
		fmt.Fprintf(w, "  CachedContent: %q\n", r.CachedContent)
	}
	if r.SystemInstruction != nil {
		fmt.Fprintf(w, "  SystemInstruction: %s\n", contentString(*r.SystemInstruction))
	}
	fmt.Fprintf(w, "  Contents: len(%d)\n", len(r.Contents))
	for _, c := range r.Contents {
		fmt.Fprintf(w, "    %s\n", contentString(c))
	}
	if len(r.Tools) > 0 {
		total := 0
		for _, t := range r.Tools {
			total += len(t.FunctionDeclarations)
		}
		fmt.Fprintf(w, "  Tools: len(%d), FunctionDeclarations: len(%d)\n", len(r.Tools), total)
	}
	if r.ToolConfig != nil && r.ToolConfig.FunctionCallingConfig != nil {
		fmt.Fprintf(w, "  ToolConfig: mode=%s\n", r.ToolConfig.FunctionCallingConfig.Mode)
	}
	if len(r.SafetySettings) > 0 {
		fmt.Fprintf(w, "  SafetySettings: len(%d)\n", len(r.SafetySettings))
	}
	if gc := r.GenerationConfig; gc != nil {
		if gc.Temperature != nil {
			fmt.Fprintf(w, "  Temperature: %.6f\n", *gc.Temperature)
		}
		if gc.TopP != nil {
			fmt.Fprintf(w, "  TopP: %.6f\n", *gc.TopP)
		}
		if gc.TopK != nil {
			fmt.Fprintf(w, "  TopK: %d\n", *gc.TopK)
		}
		if gc.MaxOutputTokens != nil {
			fmt.Fprintf(w, "  MaxOutputTokens: %d\n", *gc.MaxOutputTokens)
		}
		if gc.CandidateCount != nil {
			fmt.Fprintf(w, "  CandidateCount: %d\n", *gc.CandidateCount)
		}
		if gc.ResponseMimeType != "" {
			fmt.Fprintf(w, "  ResponseMimeType: %s\n", gc.ResponseMimeType)
		}
		if gc.ThinkingConfig != nil {
			tc := gc.ThinkingConfig
			if tc.ThinkingBudget != nil {
				fmt.Fprintf(w, "  ThinkingBudget: %d\n", *tc.ThinkingBudget)
			}
			if tc.ThinkingLevel != "" {
				fmt.Fprintf(w, "  ThinkingLevel: %s\n", tc.ThinkingLevel)
			}
		}
	}
	return fmt.Sprintf("(GenerateContentRequest) {\n%s}", w.String())
}

// candidateString formats a Candidate safely for logging.
func candidateString(c Candidate) string {
	w := &strings.Builder{}
	fmt.Fprintf(w, "finishReason=%s", c.FinishReason)
	if c.Content != nil {
		fmt.Fprintf(w, ", content=%s", contentString(*c.Content))
	}
	if len(c.SafetyRatings) > 0 {
		fmt.Fprintf(w, ", safetyRatings=len(%d)", len(c.SafetyRatings))
	}
	return fmt.Sprintf("{%s}", w.String())
}

// String formats GenerateContentResponse safely for logging (no sensitive data).
func (r GenerateContentResponse) String() string {
	w := &strings.Builder{}
	if r.ModelVersion != "" {
		fmt.Fprintf(w, "  ModelVersion: %q\n", r.ModelVersion)
	}
	if r.ResponseID != "" {
		fmt.Fprintf(w, "  ResponseID: %q\n", r.ResponseID)
	}
	fmt.Fprintf(w, "  Candidates: len(%d)\n", len(r.Candidates))
	for _, c := range r.Candidates {
		fmt.Fprintf(w, "    %s\n", candidateString(c))
	}
	if u := r.UsageMetadata; u != nil {
		fmt.Fprintf(w, "  Usage: prompt=%d, candidates=%d, total=%d",
			u.PromptTokenCount, u.CandidatesTokenCount, u.TotalTokenCount)
		if u.ThoughtsTokenCount > 0 {
			fmt.Fprintf(w, ", thoughts=%d", u.ThoughtsTokenCount)
		}
		if u.CachedContentTokenCount > 0 {
			fmt.Fprintf(w, ", cached=%d", u.CachedContentTokenCount)
		}
		w.WriteString("\n")
	}
	return fmt.Sprintf("(GenerateContentResponse) {\n%s}", w.String())
}

// String formats StreamGenerateContentResponse safely for logging (no sensitive data).
func (r StreamGenerateContentResponse) String() string {
	w := &strings.Builder{}
	if r.ModelVersion != "" {
		fmt.Fprintf(w, "  ModelVersion: %q\n", r.ModelVersion)
	}
	if r.ResponseID != "" {
		fmt.Fprintf(w, "  ResponseID: %q\n", r.ResponseID)
	}
	fmt.Fprintf(w, "  Candidates: len(%d)\n", len(r.Candidates))
	for _, c := range r.Candidates {
		fmt.Fprintf(w, "    %s\n", candidateString(c))
	}
	if u := r.UsageMetadata; u != nil {
		fmt.Fprintf(w, "  Usage: prompt=%d, candidates=%d, total=%d",
			u.PromptTokenCount, u.CandidatesTokenCount, u.TotalTokenCount)
		if u.ThoughtsTokenCount > 0 {
			fmt.Fprintf(w, ", thoughts=%d", u.ThoughtsTokenCount)
		}
		w.WriteString("\n")
	}
	return fmt.Sprintf("(StreamGenerateContentResponse) {\n%s}", w.String())
}
