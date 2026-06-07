package image_url_fetch

import (
	"encoding/json"
	"testing"

	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
)

func TestCollectFromOpenAIMessageContent(t *testing.T) {
	t.Run("string content", func(t *testing.T) {
		var c openai.MessageContent = openai.MessageContentString("hello")
		if got := collectFromOpenAIMessageContent(0, "content", c); len(got) != 0 {
			t.Fatalf("got %v, want empty", got)
		}
	})

	t.Run("array text only", func(t *testing.T) {
		var c openai.MessageContent = openai.MessageContentArray([]*openai.MessageContentItem{
			{Type: "text", Text: "hi"},
		})
		if got := collectFromOpenAIMessageContent(0, "content", c); len(got) != 0 {
			t.Fatalf("got %v, want empty", got)
		}
	})

	t.Run("array with image_url string and struct", func(t *testing.T) {
		var c openai.MessageContent = openai.MessageContentArray([]*openai.MessageContentItem{
			{Type: "text", Text: "x"},
			{Type: "image_url", ImageURL: openai.ImageURLString("https://a.example/1.png")},
			{Type: "image_url", ImageURL: &openai.MessageContentItemImageURL{URL: "https://b.example/2.png", Detail: "high"}},
		})
		got := collectFromOpenAIMessageContent(3, "content", c)
		want := []openaiImageReplaceJob{
			{MsgIndex: 3, Field: "content", PartIndex: 1, URL: "https://a.example/1.png", IsObjectForm: false},
			{MsgIndex: 3, Field: "content", PartIndex: 2, URL: "https://b.example/2.png", IsObjectForm: true},
		}
		if len(got) != len(want) {
			t.Fatalf("len = %d, want %d", len(got), len(want))
		}
		for i := range want {
			o, ok := got[i].(openaiImageReplaceJob)
			if !ok || o != want[i] {
				t.Errorf("[%d] got %#v, want %#v", i, got[i], want[i])
			}
		}
	})

	t.Run("from JSON unmarshal string image_url", func(t *testing.T) {
		jsonData := `{"role":"user","content":[
			{"type":"text","text":"see"},
			{"type":"image_url","image_url":"https://u.example/x.png"}
		]}`
		var msg openai.Message
		if err := json.Unmarshal([]byte(jsonData), &msg); err != nil {
			t.Fatal(err)
		}
		got := collectFromOpenAIMessageContent(1, "content", msg.Content)
		if len(got) != 1 {
			t.Fatalf("got %v", got)
		}
		o, ok := got[0].(openaiImageReplaceJob)
		if !ok {
			t.Fatalf("got %T", got[0])
		}
		if o.URL != "https://u.example/x.png" || o.PartIndex != 1 || o.IsObjectForm || o.MsgIndex != 1 || o.Field != "content" {
			t.Fatalf("got %#v", o)
		}
	})
}

func TestCollectFromOpenAIMessageContent_nil(t *testing.T) {
	if got := collectFromOpenAIMessageContent(0, "content", nil); len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}

func TestOpenaiImageReplaceJob_JSONParserPath(t *testing.T) {
	j := openaiImageReplaceJob{MsgIndex: 1, Field: "content", PartIndex: 2, URL: "https://x", IsObjectForm: true}
	got := j.jsonParserPath()
	want := []string{"messages", "[1]", "content", "[2]", "image_url", "url"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("jsonParserPath() = %v, want %v", got, want)
		}
	}
	j2 := openaiImageReplaceJob{MsgIndex: 0, Field: "reasoning_content", PartIndex: 0, IsObjectForm: false}
	got2 := j2.jsonParserPath()
	want2 := []string{"messages", "[0]", "reasoning_content", "[0]", "image_url"}
	for i := range want2 {
		if i >= len(got2) || got2[i] != want2[i] {
			t.Fatalf("string form = %v, want %v", got2, want2)
		}
	}
}

func TestClaudeImageReplaceJob_JSONParserPath(t *testing.T) {
	ref := claudeImageReplaceJob{MsgIndex: 1, ContentIndices: []int{2}, URL: "https://x"}
	got := ref.jsonParserPathToSource()
	want := []string{"messages", "[1]", "content", "[2]", "source"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("jsonParserPathToSource() = %v, want %v", got, want)
		}
	}

	ref2 := claudeImageReplaceJob{MsgIndex: 0, ContentIndices: []int{4, 1}, URL: "https://nested"}
	got2 := ref2.jsonParserPathToSource()
	want2 := []string{"messages", "[0]", "content", "[4]", "content", "[1]", "source"}
	for i := range want2 {
		if i >= len(got2) || got2[i] != want2[i] {
			t.Fatalf("nested jsonParserPathToSource() = %v, want %v", got2, want2)
		}
	}
}

func TestCollectClaudeImageReplaceJobs_topLevelAndToolResult(t *testing.T) {
	raw := `{
		"model": "m",
		"max_tokens": 100,
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "hi"},
				{"type": "image", "source": {"type": "url", "url": "https://a.example/top.png"}},
				{
					"type": "tool_result",
					"tool_use_id": "tu_1",
					"content": [
						{"type": "text", "text": "nested"},
						{"type": "image", "source": {"type": "url", "url": "https://b.example/in-tool.png"}}
					]
				}
			]
		}]
	}`
	var req anthropic.ClaudeMessagesRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	jobs := collectClaudeImageReplaceJobs(&req)
	if len(jobs) != 2 {
		t.Fatalf("len(jobs) = %d, want 2: %#v", len(jobs), jobs)
	}
	job0, ok := jobs[0].(claudeImageReplaceJob)
	if !ok || job0.URL != "https://a.example/top.png" {
		t.Fatalf("job0 %#v", jobs[0])
	}
	job1, ok := jobs[1].(claudeImageReplaceJob)
	if !ok || job1.URL != "https://b.example/in-tool.png" {
		t.Fatalf("job1 %#v", jobs[1])
	}
	topPath := job0.jsonParserPathToSource()
	if topPath[3] != "[1]" || topPath[len(topPath)-1] != "source" {
		t.Fatalf("top path: %v", topPath)
	}
	nestedPath := job1.jsonParserPathToSource()
	if nestedPath[3] != "[2]" || nestedPath[5] != "[1]" || nestedPath[len(nestedPath)-1] != "source" {
		t.Fatalf("nested path: %v", nestedPath)
	}
}
