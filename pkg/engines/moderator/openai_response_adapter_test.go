package moderator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
)

func TestOpenAIResponseAdapter_ExtractTextFromResponsesRequest(t *testing.T) {
	adapter := &OpenAIResponseAdapter{}

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{
			name: "input is string",
			raw: `{
				"model":"gpt-5.4",
				"input":"Tell me a three sentence bedtime story about a unicorn."
			}`,
			want:    "Tell me a three sentence bedtime story about a unicorn.",
			wantErr: false,
		},
		{
			name: "input is array with text and image",
			raw: `{
				"model":"gpt-5.4",
				"input":[
					{
						"role":"user",
						"content":[
							{"type":"input_text","text":"what is in this image?"},
							{
								"type":"input_image",
								"image_url":"https://upload.wikimedia.org/wikipedia/commons/thumb/d/dd/Gfp-wisconsin-madison-the-nature-boardwalk.jpg/2560px-Gfp-wisconsin-madison-the-nature-boardwalk.jpg"
							}
						]
					}
				]
			}`,
			want:    "what is in this image?https://upload.wikimedia.org/wikipedia/commons/thumb/d/dd/Gfp-wisconsin-madison-the-nature-boardwalk.jpg/2560px-Gfp-wisconsin-madison-the-nature-boardwalk.jpg",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req openai.ResponsesRequest
			if err := json.Unmarshal([]byte(tt.raw), &req); err != nil {
				t.Fatalf("unmarshal responses request: %v", err)
			}

			body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ResponsesRequest]{})
			body.SetParsed(&req)

			got, err := adapter.ExtractTextFromBody(context.Background(), body)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractTextFromBody() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if string(got) != tt.want {
				t.Errorf("ExtractTextFromBody() = %v, want %v", string(got), tt.want)
			}
		})
	}
}

func TestOpenAIResponseAdapter_ExtractTextFromResponsesResponse(t *testing.T) {
	adapter := &OpenAIResponseAdapter{}

	resp := &openai.ResponsesResponse{
		Id: "resp_123",
		Output: []*openai.ResponsesOutputItem{
			{
				ID:   "msg_1",
				Type: "message",
				Role: "assistant",
				Content: []*openai.ResponsesOutputContentItem{
					{Type: "output_text", Text: "Hi there! "},
					{Type: "output_text", Text: "How can I help?"},
				},
			},
		},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ResponsesResponse]{})
	body.SetParsed(resp)

	got, err := adapter.ExtractTextFromBody(context.Background(), body)
	if err != nil {
		t.Fatalf("ExtractTextFromBody() error = %v", err)
	}

	want := "Hi there! How can I help?"
	if string(got) != want {
		t.Errorf("ExtractTextFromBody() = %v, want %v", string(got), want)
	}
}

func TestOpenAIResponseAdapter_ExtractTextFromResponseStreamChunk(t *testing.T) {
	adapter := &OpenAIResponseAdapter{}

	tests := []struct {
		name  string
		chunk *openai.ResponseStreamChunk
		want  string
	}{
		{
			name: "output text delta",
			chunk: &openai.ResponseStreamChunk{
				Type:  "response.output_text.delta",
				Delta: "Hi",
			},
			want: "Hi",
		},
		{
			name: "output text done",
			chunk: &openai.ResponseStreamChunk{
				Type: "response.output_text.done",
				Text: "Hi there!",
			},
			want: "Hi there!",
		},
		{
			name: "content part done",
			chunk: &openai.ResponseStreamChunk{
				Type: "response.content_part.done",
				Part: &openai.ResponsesOutputContentItem{
					Type: "output_text",
					Text: "Part text",
				},
			},
			want: "Part text",
		},
		{
			name: "output item done",
			chunk: &openai.ResponseStreamChunk{
				Type: "response.output_item.done",
				Item: &openai.ResponsesOutputItem{
					Type: "message",
					Content: []*openai.ResponsesOutputContentItem{
						{Type: "output_text", Text: "Item text"},
					},
				},
			},
			want: "Item text",
		},
		{
			name: "response completed",
			chunk: &openai.ResponseStreamChunk{
				Type: "response.completed",
				Response: &openai.ResponsesResponse{
					Output: []*openai.ResponsesOutputItem{
						{
							Type: "message",
							Content: []*openai.ResponsesOutputContentItem{
								{Type: "output_text", Text: "Final text"},
							},
						},
					},
				},
			},
			want: "Final text",
		},
		{
			name: "non text event",
			chunk: &openai.ResponseStreamChunk{
				Type: "response.created",
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ResponseStreamChunk]{})
			body.SetParsed(tt.chunk)

			got, err := adapter.ExtractTextFromBody(context.Background(), body)
			if err != nil {
				t.Fatalf("ExtractTextFromBody() error = %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("ExtractTextFromBody() = %v, want %v", string(got), tt.want)
			}
		})
	}
}

func TestOpenAIResponseAdapter_GetReplacementBody_ResponsesNonStreaming(t *testing.T) {
	adapter := &OpenAIResponseAdapter{
		ReplacementTextForNonStreaming: "[Blocked by moderator]",
	}

	originalResp := &openai.ResponsesResponse{
		Id: "resp_1",
		Output: []*openai.ResponsesOutputItem{
			{
				Type: "message",
				Role: "assistant",
				Content: []*openai.ResponsesOutputContentItem{
					{Type: "output_text", Text: "Original text"},
				},
			},
		},
		Usage: &openai.ResponsesUsage{TotalTokens: 10},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ResponsesResponse]{})
	body.SetParsed(originalResp)

	replacementBody := adapter.GetReplacementBody(context.Background(), body)
	if replacementBody == nil {
		t.Fatal("GetReplacementBody() returned nil")
	}

	parsed, err := replacementBody.Parsed()
	if err != nil {
		t.Fatalf("Failed to parse replacement body: %v", err)
	}

	replacement := parsed.(*openai.ResponsesResponse)
	if replacement.Id != "resp_1" {
		t.Fatalf("Id = %q, want resp_1", replacement.Id)
	}
	if len(replacement.Output) != 1 || len(replacement.Output[0].Content) != 1 {
		t.Fatalf("unexpected replacement output shape: %+v", replacement.Output)
	}
	if replacement.Output[0].Content[0].Type != "output_text" {
		t.Fatalf("content type = %q, want output_text", replacement.Output[0].Content[0].Type)
	}
	if replacement.Output[0].Content[0].Text != "[Blocked by moderator]" {
		t.Fatalf("content text = %q, want [Blocked by moderator]", replacement.Output[0].Content[0].Text)
	}
	if replacement.Usage == nil || replacement.Usage.TotalTokens != 10 {
		t.Fatalf("usage should be preserved, got %+v", replacement.Usage)
	}
}

func TestOpenAIResponseAdapter_GetReplacementBody_ResponsesStreaming(t *testing.T) {
	adapter := &OpenAIResponseAdapter{
		ReplacementTextForStreaming: "[Blocked stream]",
	}

	originalChunk := &openai.ResponseStreamChunk{
		Type: "response.output_text.done",
		Text: "Original stream text",
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ResponseStreamChunk]{})
	body.SetParsed(originalChunk)

	replacementBody := adapter.GetReplacementBody(context.Background(), body)
	if replacementBody == nil {
		t.Fatal("GetReplacementBody() returned nil")
	}

	parsed, err := replacementBody.Parsed()
	if err != nil {
		t.Fatalf("Failed to parse replacement body: %v", err)
	}

	replacement := parsed.(*openai.ResponseStreamChunk)
	if replacement.Type != "response.output_text.done" {
		t.Fatalf("type = %q, want response.output_text.done", replacement.Type)
	}
	if replacement.Text != "[Blocked stream]" {
		t.Fatalf("text = %q, want [Blocked stream]", replacement.Text)
	}
}
