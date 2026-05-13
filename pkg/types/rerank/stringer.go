package rerank

import (
	"fmt"
	"strings"
)

// String formats the struct safely for logging (no sensitive data).
func (r RerankRequest) String() string {
	w := &strings.Builder{}
	fmt.Fprintf(w, "  Model: %q\n", r.Model)
	fmt.Fprintf(w, "  Query: len(%d)\n", len(r.Query))
	fmt.Fprintf(w, "  Documents: len(%d)\n", len(r.Documents))
	if r.TopN != nil {
		fmt.Fprintf(w, "  TopN: %d\n", *r.TopN)
	}
	if r.ReturnDocuments != nil {
		fmt.Fprintf(w, "  ReturnDocuments: %t\n", *r.ReturnDocuments)
	}
	if r.ReturnLen != nil {
		fmt.Fprintf(w, "  ReturnLen: %t\n", *r.ReturnLen)
	}
	return fmt.Sprintf("(RerankRequest) {\n%s}", w.String())
}

// String formats RerankResponse safely for logging (no sensitive data).
func (r RerankResponse) String() string {
	w := &strings.Builder{}
	fmt.Fprintf(w, "  Model: %q\n", r.Model)
	fmt.Fprintf(w, "  Results: len(%d)\n", len(r.Results))
	for _, res := range r.Results {
		fmt.Fprintf(w, "    Result{index=%d, relevance_score=%.6f, document_len=%d}\n", res.Index, res.RelevanceScore, len(res.Document.Text))
	}
	fmt.Fprintf(w, "  Usage: prompt=%d, total=%d\n", r.Usage.PromptTokens, r.Usage.TotalTokens)
	return fmt.Sprintf("(RerankResponse) {\n%s}", w.String())
}
