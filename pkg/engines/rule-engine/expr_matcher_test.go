package ruleengine

import (
	"context"
	"testing"

	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
)

func TestExprMatcher_Match(t *testing.T) {
	tests := []struct {
		name              string
		code              string
		setupReq          func() *octollm.Request
		featureExtractors map[string]FeatureExtractor
		want              bool
	}{
		{
			name: "always true",
			code: "true",
			setupReq: func() *octollm.Request {
				return testhelper.CreateTestRequest()
			},
			want: true,
		},
		{
			name: "always false",
			code: "false",
			setupReq: func() *octollm.Request {
				return testhelper.CreateTestRequest()
			},
			want: false,
		},
		{
			name: "ctx value",
			code: "Req.Context().Value(\"user_name\") == 'my_org'",
			setupReq: func() *octollm.Request {
				ctx := context.Background()
				ctx = context.WithValue(ctx, "user_name", "my_org")
				return testhelper.CreateTestRequest(
					testhelper.WithContext(ctx),
				)
			},
			want: true,
		},
		{
			name: "prompt text length > 15",
			code: `ExtractFeature("simpleFeature")["promptTextLen"] > 15`,
			setupReq: func() *octollm.Request {
				return testhelper.CreateTestRequest(
					testhelper.WithBody(
						openai.ChatCompletionRequest{
							Model: "glm-4.7",
							Messages: []*openai.Message{
								{
									Role:    "user",
									Content: openai.MessageContentString("who are you? I am testing the rule engine."),
								},
							},
						},
					),
				)
			},
			featureExtractors: map[string]FeatureExtractor{
				"simpleFeature": &SimpleFeatureExtractor{},
			},
			want: true,
		},
		{
			name: "prompt text length < 15",
			code: `ExtractFeature("simpleFeature")["promptTextLen"] > 15`,
			setupReq: func() *octollm.Request {
				return testhelper.CreateTestRequest()
			},
			featureExtractors: map[string]FeatureExtractor{
				"simpleFeature": &SimpleFeatureExtractor{},
			},
			want: false,
		},
		{
			name: "invalid feature extractor",
			code: `ExtractFeature("nonExistFeature") == "some_value"`,
			setupReq: func() *octollm.Request {
				return testhelper.CreateTestRequest()
			},
			featureExtractors: map[string]FeatureExtractor{
				"simpleFeature": &SimpleFeatureExtractor{},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.setupReq()
			m := &ExprMatcher{
				Code:              tt.code,
				FeatureExtractors: tt.featureExtractors,
			}
			got := m.Match(req)
			if got != tt.want {
				t.Errorf("Match() = %v, want %v", got, tt.want)
			}
		})
	}
}
