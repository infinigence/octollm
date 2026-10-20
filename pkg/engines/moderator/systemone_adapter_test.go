package moderator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/systemone"
)

func TestUniversalAdapter_SystemOneFormat(t *testing.T) {
	adapter := NewUniversalAdapter()

	testCases := []struct {
		Name string
		Body *octollm.UnifiedBody
		Want string
	}{
		{
			Name: "Request",
			Body: octollm.NewBodyFromParsed(&systemone.SystemOneRequest{
				Model:     "jev-latest",
				State:     json.RawMessage(`{"ticket":"I was charged twice."}`),
				Questions: systemone.SystemOneQuestions{"refund": &systemone.SystemOneNoulQuestion{Type: "noul", Instructions: "Is a refund requested?"}},
			}, &octollm.JSONParser[systemone.SystemOneRequest]{}),
			Want: "I was charged twice.Is a refund requested?",
		},
		{
			Name: "Response",
			Body: octollm.NewBodyFromParsed(&systemone.SystemOneResponse{Model: "jev-1.13.0"}, &octollm.JSONParser[systemone.SystemOneResponse]{}),
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
