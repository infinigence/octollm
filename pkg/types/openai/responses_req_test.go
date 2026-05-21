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
		{
			name: "input array with string content",
			raw:  `{"model":"gpt-5.4","input":[{"role":"user","content":"plain text message"}]}`,
			want: "plain text message",
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

func TestResponsesInputContent_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{
			name: "string",
			raw:  `"hello"`,
			want: "hello",
		},
		{
			name: "array",
			raw:  `[{"type":"input_text","text":"hi"},{"type":"input_image","image_url":"https://example.com/x.png"}]`,
			want: "hihttps://example.com/x.png",
		},
		{
			name:    "invalid number",
			raw:     `42`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var content ResponsesInputContent
			var item ResponsesInputItem
			if err := json.Unmarshal([]byte(`{"role":"user","content":`+tt.raw+`}`), &item); err != nil {
				if tt.wantErr {
					return
				}
				t.Fatal(err)
			}
			content = item.Content
			if tt.wantErr {
				t.Fatal("expected error")
			}
			if got := content.ExtractText(); got != tt.want {
				t.Fatalf("ExtractText()=%q want=%q", got, tt.want)
			}
		})
	}
}

func TestResponsesInputContent_MarshalRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "string", raw: `{"role":"user","content":"round trip"}`},
		{name: "array", raw: `{"role":"user","content":[{"type":"input_text","text":"round trip"}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var item ResponsesInputItem
			if err := json.Unmarshal([]byte(tt.raw), &item); err != nil {
				t.Fatal(err)
			}
			out, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			var again ResponsesInputItem
			if err := json.Unmarshal(out, &again); err != nil {
				t.Fatal(err)
			}
			if item.Content.ExtractText() != again.Content.ExtractText() {
				t.Fatalf("before=%q after=%q", item.Content.ExtractText(), again.Content.ExtractText())
			}
		})
	}
}

func TestResponsesInputItem_ContentStringThenArray(t *testing.T) {
	var item ResponsesInputItem

	if err := json.Unmarshal([]byte(`{"role":"user","content":"first"}`), &item); err != nil {
		t.Fatal(err)
	}
	if got := item.Content.ExtractText(); got != "first" {
		t.Fatalf("string content: got %q", got)
	}

	if err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"input_text","text":"second"}]}`), &item); err != nil {
		t.Fatal(err)
	}
	if got := item.Content.ExtractText(); got != "second" {
		t.Fatalf("array content after string: got %q", got)
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
