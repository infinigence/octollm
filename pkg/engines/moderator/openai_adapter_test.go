package moderator

import (
	"context"
	"testing"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
)

func TestOpenAIAdapter_ExtractTextFromRequest(t *testing.T) {
	adapter := &OpenAIAdapter{}

	tests := []struct {
		name    string
		req     *openai.ChatCompletionRequest
		want    string
		wantErr bool
	}{
		{
			name: "simple string content",
			req: &openai.ChatCompletionRequest{
				Model: "gpt-4",
				Messages: []*openai.Message{
					{
						Role:    "system",
						Content: openai.MessageContentString("You are a helpful assistant."),
					},
					{
						Role:    "user",
						Content: openai.MessageContentString("Hello, world!"),
					},
				},
			},
			want:    "You are a helpful assistant.Hello, world!",
			wantErr: false,
		},
		{
			name: "array content",
			req: &openai.ChatCompletionRequest{
				Model: "gpt-4",
				Messages: []*openai.Message{
					{
						Role: "user",
						Content: openai.MessageContentArray([]*openai.MessageContentItem{
							{Type: "text", Text: "Hello"},
							{Type: "text", Text: " world!"},
						}),
					},
				},
			},
			want:    "Hello world!",
			wantErr: false,
		},
		{
			name: "with reasoning content",
			req: &openai.ChatCompletionRequest{
				Model: "gpt-4",
				Messages: []*openai.Message{
					{
						Role:             "assistant",
						Content:          openai.MessageContentString("Final answer"),
						ReasoningContent: openai.MessageContentString("Let me think..."),
					},
				},
			},
			want:    "Final answerLet me think...",
			wantErr: false,
		},
		{
			name: "with reasoning content and array content",
			req: &openai.ChatCompletionRequest{
				Model: "gpt-4",
				Messages: []*openai.Message{
					{
						Role: "assistant",
						Content: openai.MessageContentArray([]*openai.MessageContentItem{
							{Type: "text", Text: "Final answer"},
							{Type: "text", Text: " world!"},
						}),
						ReasoningContent: openai.MessageContentArray([]*openai.MessageContentItem{
							{Type: "text", Text: "Let me think..."},
							{Type: "text", Text: " world!"},
						}),
					},
				},
			},
			want:    "Final answer world!Let me think... world!",
			wantErr: false,
		},
		{
			name: "with tool calls",
			req: &openai.ChatCompletionRequest{
				Model: "gpt-4",
				Messages: []*openai.Message{
					{
						Role:    "assistant",
						Content: openai.MessageContentString("I'll use a tool"),
						ToolCalls: []*openai.ToolCall{
							{
								ID:   "call_123",
								Type: "function",
								Function: &openai.ToolCallFunction{
									Name:      "search",
									Arguments: `{"query":"test"}`,
								},
							},
						},
					},
				},
			},
			want:    `I'll use a tool{"query":"test"}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionRequest]{})
			body.SetParsed(tt.req)

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

func TestOpenAIAdapter_ExtractTextFromResponse_NonStreaming(t *testing.T) {
	adapter := &OpenAIAdapter{}

	resp := &openai.ChatCompletionResponse{
		ID:      "chatcmpl-123",
		Object:  "chat.completion",
		Created: 1234567890,
		Model:   "gpt-4",
		Choices: []*openai.ChatCompletionChoice{
			{
				Index: 0,
				Message: &openai.Message{
					Role:    "assistant",
					Content: openai.MessageContentString("This is a test response."),
				},
				FinishReason: "stop",
			},
		},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionResponse]{})
	body.SetParsed(resp)

	got, err := adapter.ExtractTextFromBody(context.Background(), body)
	if err != nil {
		t.Fatalf("ExtractTextFromBody() error = %v", err)
	}

	want := "This is a test response."
	if string(got) != want {
		t.Errorf("ExtractTextFromBody() = %v, want %v", string(got), want)
	}
}

func TestOpenAIAdapter_ExtractTextFromResponse_Streaming(t *testing.T) {
	adapter := &OpenAIAdapter{}

	resp := &openai.ChatCompletionStreamChunk{
		ID:      "chatcmpl-123",
		Object:  "chat.completion.chunk",
		Created: 1234567890,
		Model:   "gpt-4",
		Choices: []*openai.ChatCompletionStreamChoice{
			{
				Index: 0,
				Delta: &openai.Message{
					Role:    "assistant",
					Content: openai.MessageContentString("Hello"),
				},
			},
		},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionStreamChunk]{})
	body.SetParsed(resp)

	got, err := adapter.ExtractTextFromBody(context.Background(), body)
	if err != nil {
		t.Fatalf("ExtractTextFromBody() error = %v", err)
	}

	want := "Hello"
	if string(got) != want {
		t.Errorf("ExtractTextFromBody() = %v, want %v", string(got), want)
	}
}

func TestOpenAIAdapter_ExtractTextFromResponse_WithToolCalls(t *testing.T) {
	adapter := &OpenAIAdapter{}

	resp := &openai.ChatCompletionResponse{
		ID:      "chatcmpl-123",
		Object:  "chat.completion",
		Created: 1234567890,
		Model:   "gpt-4",
		Choices: []*openai.ChatCompletionChoice{
			{
				Index: 0,
				Message: &openai.Message{
					Role:    "assistant",
					Content: openai.MessageContentString("I'll search for that."),
					ToolCalls: []*openai.ToolCall{
						{
							ID:   "call_456",
							Type: "function",
							Function: &openai.ToolCallFunction{
								Name:      "search",
								Arguments: `{"query":"golang"}`,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
		},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionResponse]{})
	body.SetParsed(resp)

	got, err := adapter.ExtractTextFromBody(context.Background(), body)
	if err != nil {
		t.Fatalf("ExtractTextFromBody() error = %v", err)
	}

	want := `I'll search for that.{"query":"golang"}`
	if string(got) != want {
		t.Errorf("ExtractTextFromBody() = %v, want %v", string(got), want)
	}
}

func TestOpenAIAdapter_GetReplacementBody_NonStreaming(t *testing.T) {
	adapter := &OpenAIAdapter{
		ReplacementTextForNonStreaming: "Content has been blocked.",
		ReplacementFinishReason:        "content_filter",
	}

	originalResp := &openai.ChatCompletionResponse{
		ID:      "chatcmpl-123",
		Object:  "chat.completion",
		Created: 1234567890,
		Model:   "gpt-4",
		Choices: []*openai.ChatCompletionChoice{
			{
				Index: 0,
				Message: &openai.Message{
					Role:    "assistant",
					Content: openai.MessageContentString("Original content"),
				},
				FinishReason: "stop",
			},
		},
		Usage: &openai.Usage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionResponse]{})
	body.SetParsed(originalResp)

	replacementBody := adapter.GetReplacementBody(context.Background(), body)
	if replacementBody == nil {
		t.Fatal("GetReplacementBody() returned nil")
	}

	parsed, err := replacementBody.Parsed()
	if err != nil {
		t.Fatalf("Failed to parse replacement body: %v", err)
	}

	replacement := parsed.(*openai.ChatCompletionResponse)

	// 验证基本字段
	if replacement.ID != "chatcmpl-123" {
		t.Errorf("ID = %v, want chatcmpl-123", replacement.ID)
	}
	if replacement.Model != "gpt-4" {
		t.Errorf("Model = %v, want gpt-4", replacement.Model)
	}

	// 验证替换内容
	if len(replacement.Choices) != 1 {
		t.Fatalf("Expected 1 choice, got %d", len(replacement.Choices))
	}

	choice := replacement.Choices[0]
	if choice.Message == nil {
		t.Fatal("Choice.Message is nil")
	}

	content := choice.Message.Content.ExtractText()
	if content != "Content has been blocked." {
		t.Errorf("Content = %v, want 'Content has been blocked.'", content)
	}

	if choice.FinishReason != "content_filter" {
		t.Errorf("FinishReason = %v, want 'content_filter'", choice.FinishReason)
	}

	// 验证 Usage 保留
	if replacement.Usage == nil {
		t.Fatal("Usage should be preserved")
	}
	if replacement.Usage.TotalTokens != 30 {
		t.Errorf("TotalTokens = %v, want 30", replacement.Usage.TotalTokens)
	}
}

func TestOpenAIAdapter_GetReplacementBody_Streaming(t *testing.T) {
	adapter := &OpenAIAdapter{
		ReplacementTextForStreaming: "[Blocked]",
		ReplacementFinishReason:     "content_filter",
	}

	originalResp := &openai.ChatCompletionStreamChunk{
		ID:      "chatcmpl-456",
		Object:  "chat.completion.chunk",
		Created: 1234567890,
		Model:   "gpt-4",
		Choices: []*openai.ChatCompletionStreamChoice{
			{
				Index: 0,
				Delta: &openai.Message{
					Content: openai.MessageContentString("Original chunk"),
				},
			},
		},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionStreamChunk]{})
	body.SetParsed(originalResp)

	replacementBody := adapter.GetReplacementBody(context.Background(), body)
	if replacementBody == nil {
		t.Fatal("GetReplacementBody() returned nil")
	}

	parsed, err := replacementBody.Parsed()
	if err != nil {
		t.Fatalf("Failed to parse replacement body: %v", err)
	}

	replacement := parsed.(*openai.ChatCompletionStreamChunk)

	// 验证替换内容
	if len(replacement.Choices) != 1 {
		t.Fatalf("Expected 1 choice, got %d", len(replacement.Choices))
	}

	choice := replacement.Choices[0]
	if choice.Delta == nil {
		t.Fatal("Choice.Delta is nil")
	}

	content := choice.Delta.Content.ExtractText()
	if content != "[Blocked]" {
		t.Errorf("Content = %v, want '[Blocked]'", content)
	}

	if choice.FinishReason != "content_filter" {
		t.Errorf("FinishReason = %v, want 'content_filter'", choice.FinishReason)
	}
}

func TestOpenAIAdapter_GetReplacementBody_NoReplacement(t *testing.T) {
	adapter := &OpenAIAdapter{
		// 没有设置替换文本
	}

	originalResp := &openai.ChatCompletionResponse{
		ID:    "chatcmpl-123",
		Model: "gpt-4",
		Choices: []*openai.ChatCompletionChoice{
			{
				Message: &openai.Message{
					Content: openai.MessageContentString("Original content"),
				},
			},
		},
	}

	body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionResponse]{})
	body.SetParsed(originalResp)

	replacementBody := adapter.GetReplacementBody(context.Background(), body)
	if replacementBody != nil {
		t.Error("GetReplacementBody() should return nil when no replacement text is set")
	}
}

func TestOpenAIAdapter_ExtractTextFromMessage(t *testing.T) {
	adapter := &OpenAIAdapter{}

	tests := []struct {
		name string
		msg  *openai.Message
		want string
	}{
		{
			name: "string content only",
			msg: &openai.Message{
				Role:    "user",
				Content: openai.MessageContentString("Hello"),
			},
			want: "Hello",
		},
		{
			name: "array content",
			msg: &openai.Message{
				Role: "user",
				Content: openai.MessageContentArray([]*openai.MessageContentItem{
					{Type: "text", Text: "Part 1"},
					{Type: "text", Text: " Part 2"},
				}),
			},
			want: "Part 1 Part 2",
		},
		{
			name: "with reasoning content",
			msg: &openai.Message{
				Role:             "assistant",
				Content:          openai.MessageContentString("Answer"),
				ReasoningContent: openai.MessageContentString("Thinking..."),
			},
			want: "AnswerThinking...",
		},
		{
			name: "with tool calls",
			msg: &openai.Message{
				Role:    "assistant",
				Content: openai.MessageContentString("Using tool"),
				ToolCalls: []*openai.ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: &openai.ToolCallFunction{
							Name:      "func1",
							Arguments: `{"arg":"value"}`,
						},
					},
				},
			},
			want: `Using tool{"arg":"value"}`,
		},
		{
			name: "nil content",
			msg: &openai.Message{
				Role: "user",
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := adapter.extractTextFromMessage(tt.msg)
			if string(got) != tt.want {
				t.Errorf("extractTextFromMessage() = %v, want %v", string(got), tt.want)
			}
		})
	}
}

func TestUniversalAdapter_ResponsesFormat(t *testing.T) {
	adapter := NewUniversalAdapterWithConfig("[stream blocked]", "[blocked]", "content_filter")

	t.Run("extract text from responses request", func(t *testing.T) {
		req := &openai.ResponsesRequest{
			Model: "gpt-5.4",
			Input: &openai.ResponsesInput{
				Items: []*openai.ResponsesInputItem{
					{
						Role: "user",
						Content: []*openai.ResponsesInputContentItem{
							{Type: "input_text", Text: "hello responses"},
						},
					},
				},
			},
		}
		body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ResponsesRequest]{})
		body.SetParsed(req)

		got, err := adapter.ExtractTextFromBody(context.Background(), body)
		if err != nil {
			t.Fatalf("ExtractTextFromBody() error = %v", err)
		}
		if string(got) != "hello responses" {
			t.Fatalf("ExtractTextFromBody() = %q, want %q", string(got), "hello responses")
		}
	})

	t.Run("get replacement for responses non-stream", func(t *testing.T) {
		resp := &openai.ResponsesResponse{
			Id: "resp_123",
			Output: []*openai.ResponsesOutputItem{
				{
					Type: "message",
					Role: "assistant",
					Content: []*openai.ResponsesOutputContentItem{
						{Type: "output_text", Text: "origin"},
					},
				},
			},
		}
		body := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ResponsesResponse]{})
		body.SetParsed(resp)

		replaced := adapter.GetReplacementBody(context.Background(), body)
		if replaced == nil {
			t.Fatal("GetReplacementBody() returned nil")
		}

		parsed, err := replaced.Parsed()
		if err != nil {
			t.Fatalf("replacement Parsed() error = %v", err)
		}
		gotResp, ok := parsed.(*openai.ResponsesResponse)
		if !ok {
			t.Fatalf("replacement body type = %T, want *openai.ResponsesResponse", parsed)
		}
		if len(gotResp.Output) != 1 || len(gotResp.Output[0].Content) != 1 {
			t.Fatalf("unexpected replacement output shape: %+v", gotResp.Output)
		}
		if gotResp.Output[0].Content[0].Text != "[blocked]" {
			t.Fatalf("replacement text = %q, want %q", gotResp.Output[0].Content[0].Text, "[blocked]")
		}
	})
}
