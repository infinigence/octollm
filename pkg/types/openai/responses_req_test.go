package openai

import (
	"encoding/json"
	"testing"
)

func TestResponsesRequest_UnmarshalJSON_ModelAndStream(t *testing.T) {
	raw := `{"model":"gpt-4.1","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	if req.Model != "gpt-4.1" || req.Stream == nil || !*req.Stream {
		t.Fatalf("%+v", req)
	}
	if req.Input == nil {
		t.Fatal("input should not be nil")
	}
	if got := req.Input.ExtractText(); got != "hi" {
		t.Fatalf("unexpected extracted input text: %q", got)
	}
}

func TestResponsesRequest_Input_StringAndArray(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "input string",
			raw:  `{"model":"gpt-5.4","input":"Tell me a three sentence bedtime story about a unicorn."}`,
			want: "Tell me a three sentence bedtime story about a unicorn.",
		},
		{
			name: "input array with text and image",
			raw: `{"model":"gpt-5.4","input":[{"role":"user","content":[{"type":"input_text","text":"what is in this image?"},{"type":"input_image","image_url":"https://example.com/a.jpg"}]}]}`,
			want: "what is in this image?https://example.com/a.jpg",
		},
		{
			name: "input array with image object",
			raw: `{"model":"gpt-5.4","input":[{"role":"user","content":[{"type":"input_image","image_url":{"url":"https://example.com/b.jpg","detail":"high"}}]}]}`,
			want: "https://example.com/b.jpg",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req ResponsesRequest
			if err := json.Unmarshal([]byte(tt.raw), &req); err != nil {
				t.Fatal(err)
			}
			if req.Input == nil {
				t.Fatal("input should not be nil")
			}
			if got := req.Input.ExtractText(); got != tt.want {
				t.Fatalf("ExtractText()=%q want=%q", got, tt.want)
			}
		})
	}
}

func TestResponsesResponse_UnmarshalJSON_Usage(t *testing.T) {
	raw := `{
		"id":"r1","object":"response","created_at":1,"status":"completed","model":"m",
		"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,
			"input_tokens_details":{"cached_tokens":0},
			"output_tokens_details":{"reasoning_tokens":0}
		}
	}`
	var resp ResponsesResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 15 {
		t.Fatal(resp.Usage)
	}
	if resp.Usage.InputTokensDetails == nil || resp.Usage.InputTokensDetails.CachedTokens != 0 {
		t.Fatalf("input_tokens_details: %+v", resp.Usage.InputTokensDetails)
	}
	if resp.Usage.OutputTokensDetails == nil || resp.Usage.OutputTokensDetails.ReasoningTokens != 0 {
		t.Fatalf("output_tokens_details: %+v", resp.Usage.OutputTokensDetails)
	}
}

func TestResponseStreamChunk_UnmarshalJSON(t *testing.T) {
	raw := `{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`
	var ch ResponseStreamChunk
	if err := json.Unmarshal([]byte(raw), &ch); err != nil {
		t.Fatal(err)
	}
	if ch.Type != "response.completed" || ch.Response == nil || ch.Response.Usage == nil || ch.Response.Usage.TotalTokens != 3 {
		t.Fatalf("%+v", ch.Response)
	}
}
