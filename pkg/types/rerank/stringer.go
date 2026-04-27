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
