package moderator

import (
	"context"
	"testing"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/vertex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVertexAdapter_ExtractTextFromRequest(t *testing.T) {
	adapter := &VertexAdapter{}

	tests := []struct {
		name string
		req  *vertex.GenerateContentRequest
		want string
	}{
		{
			name: "user content only",
			req: &vertex.GenerateContentRequest{
				Contents: []vertex.Content{
					{Role: "user", Parts: []vertex.Part{{Text: "Hello, world!"}}},
				},
			},
			want: "Hello, world!",
		},
		{
			name: "system instruction and user content",
			req: &vertex.GenerateContentRequest{
				SystemInstruction: &vertex.Content{
					Parts: []vertex.Part{{Text: "You are a helpful assistant."}},
				},
				Contents: []vertex.Content{
					{Role: "user", Parts: []vertex.Part{{Text: "Hi!"}}},
				},
			},
			want: "You are a helpful assistant.Hi!",
		},
		{
			name: "multi-turn conversation",
			req: &vertex.GenerateContentRequest{
				Contents: []vertex.Content{
					{Role: "user", Parts: []vertex.Part{{Text: "Question"}}},
					{Role: "model", Parts: []vertex.Part{{Text: "Answer"}}},
					{Role: "user", Parts: []vertex.Part{{Text: "Follow-up"}}},
				},
			},
			want: "QuestionAnswerFollow-up",
		},
		{
			name: "function call args extracted",
			req: &vertex.GenerateContentRequest{
				Contents: []vertex.Content{
					{
						Role: "model",
						Parts: []vertex.Part{
							{
								FunctionCall: &vertex.FunctionCall{
									Name: "search",
									Args: map[string]interface{}{"query": "test"},
								},
							},
						},
					},
				},
			},
			want: `{"query":"test"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[vertex.GenerateContentRequest]{})
			body.SetParsed(tt.req)

			got, err := adapter.ExtractTextFromBody(context.Background(), body)
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func TestVertexAdapter_ExtractTextFromResponse_NonStreaming(t *testing.T) {
	adapter := &VertexAdapter{}

	resp := &vertex.GenerateContentResponse{
		Candidates: []vertex.Candidate{
			{
				Content: &vertex.Content{
					Role:  "model",
					Parts: []vertex.Part{{Text: "This is a test response."}},
				},
				FinishReason: "STOP",
			},
		},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[vertex.GenerateContentResponse]{})
	body.SetParsed(resp)

	got, err := adapter.ExtractTextFromBody(context.Background(), body)
	require.NoError(t, err)
	assert.Equal(t, "This is a test response.", string(got))
}

func TestVertexAdapter_ExtractTextFromResponse_Streaming(t *testing.T) {
	adapter := &VertexAdapter{}

	resp := &vertex.StreamGenerateContentResponse{
		Candidates: []vertex.Candidate{
			{
				Content: &vertex.Content{
					Role:  "model",
					Parts: []vertex.Part{{Text: "Hello"}},
				},
			},
		},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[vertex.StreamGenerateContentResponse]{})
	body.SetParsed(resp)

	got, err := adapter.ExtractTextFromBody(context.Background(), body)
	require.NoError(t, err)
	assert.Equal(t, "Hello", string(got))
}

func TestVertexAdapter_ExtractTextFromResponse_NilContent(t *testing.T) {
	adapter := &VertexAdapter{}

	// Candidates with nil Content (e.g. safety-blocked candidates)
	resp := &vertex.GenerateContentResponse{
		Candidates: []vertex.Candidate{
			{Content: nil, FinishReason: "SAFETY"},
		},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[vertex.GenerateContentResponse]{})
	body.SetParsed(resp)

	got, err := adapter.ExtractTextFromBody(context.Background(), body)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestVertexAdapter_GetReplacementBody_NonStreaming(t *testing.T) {
	adapter := &VertexAdapter{
		ReplacementTextForNonStreaming: "Content has been blocked.",
		ReplacementFinishReason:        "SAFETY",
	}

	original := &vertex.GenerateContentResponse{
		ModelVersion: "gemini-2.0-flash",
		Candidates: []vertex.Candidate{
			{
				Content:      &vertex.Content{Role: "model", Parts: []vertex.Part{{Text: "Bad content"}}},
				FinishReason: "STOP",
			},
		},
		UsageMetadata: &vertex.UsageMetadata{TotalTokenCount: 42},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[vertex.GenerateContentResponse]{})
	body.SetParsed(original)

	replacementBody := adapter.GetReplacementBody(context.Background(), body)
	require.NotNil(t, replacementBody)

	parsed, err := replacementBody.Parsed()
	require.NoError(t, err)

	r := parsed.(*vertex.GenerateContentResponse)
	assert.Equal(t, "gemini-2.0-flash", r.ModelVersion)
	require.Len(t, r.Candidates, 1)
	assert.Equal(t, "SAFETY", r.Candidates[0].FinishReason)
	require.NotNil(t, r.Candidates[0].Content)
	require.Len(t, r.Candidates[0].Content.Parts, 1)
	assert.Equal(t, "Content has been blocked.", r.Candidates[0].Content.Parts[0].Text)
	assert.Equal(t, "model", r.Candidates[0].Content.Role)
}

func TestVertexAdapter_GetReplacementBody_Streaming(t *testing.T) {
	adapter := &VertexAdapter{
		ReplacementTextForStreaming: "[Blocked]",
		ReplacementFinishReason:    "SAFETY",
	}

	original := &vertex.StreamGenerateContentResponse{
		ModelVersion: "gemini-2.0-flash",
		Candidates: []vertex.Candidate{
			{
				Content:      &vertex.Content{Role: "model", Parts: []vertex.Part{{Text: "Bad chunk"}}},
				FinishReason: "STOP",
			},
		},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[vertex.StreamGenerateContentResponse]{})
	body.SetParsed(original)

	replacementBody := adapter.GetReplacementBody(context.Background(), body)
	require.NotNil(t, replacementBody)

	parsed, err := replacementBody.Parsed()
	require.NoError(t, err)

	r := parsed.(*vertex.StreamGenerateContentResponse)
	assert.Equal(t, "gemini-2.0-flash", r.ModelVersion)
	require.Len(t, r.Candidates, 1)
	assert.Equal(t, "SAFETY", r.Candidates[0].FinishReason)
	require.NotNil(t, r.Candidates[0].Content)
	assert.Equal(t, "[Blocked]", r.Candidates[0].Content.Parts[0].Text)
}

func TestVertexAdapter_GetReplacementBody_NoReplacementText(t *testing.T) {
	adapter := &VertexAdapter{} // no replacement text set

	original := &vertex.GenerateContentResponse{
		Candidates: []vertex.Candidate{
			{Content: &vertex.Content{Parts: []vertex.Part{{Text: "content"}}}},
		},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[vertex.GenerateContentResponse]{})
	body.SetParsed(original)

	assert.Nil(t, adapter.GetReplacementBody(context.Background(), body))
}

func TestVertexAdapter_GetReplacementBody_DefaultFinishReason(t *testing.T) {
	adapter := &VertexAdapter{
		ReplacementTextForNonStreaming: "blocked",
		// ReplacementFinishReason intentionally left empty
	}

	original := &vertex.GenerateContentResponse{
		Candidates: []vertex.Candidate{
			{Content: &vertex.Content{Parts: []vertex.Part{{Text: "bad"}}}},
		},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[vertex.GenerateContentResponse]{})
	body.SetParsed(original)

	replacementBody := adapter.GetReplacementBody(context.Background(), body)
	require.NotNil(t, replacementBody)

	parsed, err := replacementBody.Parsed()
	require.NoError(t, err)

	r := parsed.(*vertex.GenerateContentResponse)
	assert.Equal(t, "SAFETY", r.Candidates[0].FinishReason)
}

func TestUniversalAdapter_VertexFormat(t *testing.T) {
	adapter := NewUniversalAdapter()

	resp := &vertex.GenerateContentResponse{
		Candidates: []vertex.Candidate{
			{
				Content: &vertex.Content{
					Role:  "model",
					Parts: []vertex.Part{{Text: "This is a Vertex response."}},
				},
			},
		},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[vertex.GenerateContentResponse]{})
	body.SetParsed(resp)

	got, err := adapter.ExtractTextFromBody(context.Background(), body)
	require.NoError(t, err)
	assert.Equal(t, "This is a Vertex response.", string(got))
}
