package moderator

import (
	"context"
	"testing"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
)

func TestUniversalAdapter_DecisionsFormat(t *testing.T) {
	adapter := NewUniversalAdapter()

	testCases := []struct {
		Name string
		Body *octollm.UnifiedBody
		Want string
	}{
		{
			Name: "Request",
			Body: octollm.NewBodyFromParsed(&openai.DecisionsRequest{
				Model:     "gpt-6-luna",
				Input:     openai.ResponsesInputString("I was charged twice."),
				Questions: []openai.DecisionsQuestion{{Type: "predicate", Name: "refund", Instructions: "Is a refund requested?"}},
			}, &octollm.JSONParser[openai.DecisionsRequest]{}),
			Want: "I was charged twice.Is a refund requested?",
		},
		{
			Name: "Response",
			Body: octollm.NewBodyFromParsed(&openai.DecisionsResponse{Model: "gpt-6-luna"}, &octollm.JSONParser[openai.DecisionsResponse]{}),
			Want: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			text, err := adapter.ExtractTextFromBody(context.Background(), tc.Body)
			if err != nil {
				t.Fatalf("ExtractTextFromBody() error = %v", err)
			}
			if string(text) != tc.Want {
				t.Fatalf("ExtractTextFromBody() = %q, want %q", string(text), tc.Want)
			}
			if got := adapter.GetReplacementBody(context.Background(), tc.Body); got != nil {
				t.Fatalf("GetReplacementBody() = %v, want nil", got)
			}
		})
	}
}
