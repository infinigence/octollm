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
		name     string
		code     string
		setupReq func() *octollm.Request
		want     bool
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
			code: `req.Context("user_name") == "my_org"`,
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
			code: `req.Feature("promptTextLen") > 15`,
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
					testhelper.WithFeature("promptTextLen", &PromptTextLenExtractor{}),
				)
			},
			want: true,
		},
		{
			name: "prompt text length < 15",
			code: `req.Feature("promptTextLen") > 15`,
			setupReq: func() *octollm.Request {
				return testhelper.CreateTestRequest(
					testhelper.WithFeature("promptTextLen", &PromptTextLenExtractor{}),
				)
			},
			want: false,
		},
		{
			name: "invalid feature extractor",
			code: `req.Feature("nonExistFeature") == "some_value"`,
			setupReq: func() *octollm.Request {
				return testhelper.CreateTestRequest()
			},
			want: false,
		},
		{
			name: "prefix hash matches",
			code: `req.Feature("prefix20") == "c6eec1e7"`,
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
					testhelper.WithFeature("prefix20", &PrefixHashExtractor{Length: 20}),
				)
			},
			want: true,
		},
		{
			name: "suffix hash matches",
			code: `req.Feature("suffix20") == "efc888e0"`,
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
					testhelper.WithFeature("suffix20", &SuffixHashExtractor{Length: 20}),
				)
			},
			want: true,
		},
		{
			name: "received header matches",
			code: `req.Header("X-Org-Id") == "my-org"`,
			setupReq: func() *octollm.Request {
				return testhelper.CreateTestRequest(
					testhelper.WithRecvHeader("X-Org-Id", "my-org"),
				)
			},
			want: true,
		},
		{
			name: "received header absent",
			code: `req.Header("X-Org-Id") == "my-org"`,
			setupReq: func() *octollm.Request {
				return testhelper.CreateTestRequest()
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.setupReq()
			m := &ExprMatcher{
				Code: tt.code,
			}
			got := m.Match(req)
			if got != tt.want {
				t.Errorf("Match() = %v, want %v", got, tt.want)
			}
		})
	}
}
