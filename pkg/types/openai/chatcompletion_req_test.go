package openai

import (
	"encoding/json"
	"testing"
)

func TestMessage_UnmarshalJSON_String(t *testing.T) {
	jsonData := `{
		"role": "user",
		"content": "Hello, world!"
	}`

	var msg Message
	err := json.Unmarshal([]byte(jsonData), &msg)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if msg.Role != "user" {
		t.Errorf("Expected role 'user', got '%s'", msg.Role)
	}

	contentStr, ok := msg.Content.(MessageContentString)
	if !ok {
		t.Fatalf("Expected MessageContentString, got %T", msg.Content)
	}

	if string(contentStr) != "Hello, world!" {
		t.Errorf("Expected content 'Hello, world!', got '%s'", contentStr)
	}
}

func TestMessage_UnmarshalJSON_Array(t *testing.T) {
	jsonData := `{
		"role": "user",
		"content": [
			{"type": "text", "text": "Hello"},
			{"type": "text", "text": " world!"}
		]
	}`

	var msg Message
	err := json.Unmarshal([]byte(jsonData), &msg)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if msg.Role != "user" {
		t.Errorf("Expected role 'user', got '%s'", msg.Role)
	}

	contentArr, ok := msg.Content.(MessageContentArray)
	if !ok {
		t.Fatalf("Expected MessageContentArray, got %T", msg.Content)
	}

	if len(contentArr) != 2 {
		t.Errorf("Expected 2 items, got %d", len(contentArr))
	}

	if contentArr[0].Text != "Hello" {
		t.Errorf("Expected first item text 'Hello', got '%s'", contentArr[0].Text)
	}
}

func TestMessage_MarshalJSON_String(t *testing.T) {
	msg := Message{
		Role:    "assistant",
		Content: MessageContentString("Hello, world!"),
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	expected := `{"role":"assistant","content":"Hello, world!"}`
	var expectedMap, actualMap map[string]interface{}
	json.Unmarshal([]byte(expected), &expectedMap)
	json.Unmarshal(data, &actualMap)

	if expectedMap["role"] != actualMap["role"] {
		t.Errorf("Role mismatch")
	}
	if expectedMap["content"] != actualMap["content"] {
		t.Errorf("Content mismatch: expected '%v', got '%v'", expectedMap["content"], actualMap["content"])
	}
}

func TestMessage_MarshalJSON_Array(t *testing.T) {
	msg := Message{
		Role: "user",
		Content: MessageContentArray([]*MessageContentItem{
			{Type: "text", Text: "Hello"},
			{Type: "text", Text: " world!"},
		}),
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var result map[string]interface{}
	json.Unmarshal(data, &result)

	if result["role"] != "user" {
		t.Errorf("Role mismatch")
	}

	content, ok := result["content"].([]interface{})
	if !ok {
		t.Fatalf("Expected content to be array, got %T", result["content"])
	}

	if len(content) != 2 {
		t.Errorf("Expected 2 items, got %d", len(content))
	}
}

func TestApiChatCompletionsRequest_UnmarshalJSON(t *testing.T) {
	jsonData := `{
		"model": "gpt-4",
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "Hello!"}
		],
		"max_tokens": 100,
		"temperature": 0.7
	}`

	var req ChatCompletionRequest
	err := json.Unmarshal([]byte(jsonData), &req)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if req.Model != "gpt-4" {
		t.Errorf("Expected model 'gpt-4', got '%s'", req.Model)
	}

	if len(req.Messages) != 2 {
		t.Errorf("Expected 2 messages, got %d", len(req.Messages))
	}

	if req.Messages[0].Role != "system" {
		t.Errorf("Expected first message role 'system', got '%s'", req.Messages[0].Role)
	}

	content := req.Messages[0].Content.(MessageContentString)
	if string(content) != "You are a helpful assistant." {
		t.Errorf("Expected content 'You are a helpful assistant.', got '%s'", content)
	}
}

// --- MessageContentItem image_url 支持 string/struct 的测试 ---

func TestMessageContentItem_UnmarshalJSON_ImageURL_String(t *testing.T) {
	jsonData := `{"type": "image_url", "image_url": "https://example.com/image.png"}`
	var item MessageContentItem
	err := json.Unmarshal([]byte(jsonData), &item)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if item.Type != "image_url" {
		t.Errorf("Expected type 'image_url', got '%s'", item.Type)
	}
	if item.ImageURL == nil {
		t.Fatal("Expected ImageURL to be set")
	}
	url := item.ImageURL.GetImageUrl()
	if url != "https://example.com/image.png" {
		t.Errorf("Expected URL 'https://example.com/image.png', got '%s'", url)
	}
	_, ok := item.ImageURL.(ImageURLString)
	if !ok {
		t.Errorf("Expected ImageURL to be ImageURLString, got %T", item.ImageURL)
	}
}

func TestMessageContentItem_UnmarshalJSON_ImageURL_Struct(t *testing.T) {
	jsonData := `{"type": "image_url", "image_url": {"url": "https://example.com/img.jpg", "detail": "high"}}`
	var item MessageContentItem
	err := json.Unmarshal([]byte(jsonData), &item)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if item.Type != "image_url" {
		t.Errorf("Expected type 'image_url', got '%s'", item.Type)
	}
	if item.ImageURL == nil {
		t.Fatal("Expected ImageURL to be set")
	}
	url := item.ImageURL.GetImageUrl()
	if url != "https://example.com/img.jpg" {
		t.Errorf("Expected URL 'https://example.com/img.jpg', got '%s'", url)
	}
	obj, ok := item.ImageURL.(*MessageContentItemImageURL)
	if !ok {
		t.Errorf("Expected ImageURL to be *MessageContentItemImageURL, got %T", item.ImageURL)
	} else if obj.Detail != "high" {
		t.Errorf("Expected detail 'high', got '%s'", obj.Detail)
	}
}

func TestMessageContentItem_MarshalJSON_ImageURL_String(t *testing.T) {
	item := MessageContentItem{
		Type:     "image_url",
		ImageURL: ImageURLString("https://example.com/pic.png"),
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal result failed: %v", err)
	}
	if m["image_url"] != "https://example.com/pic.png" {
		t.Errorf("Expected image_url 'https://example.com/pic.png', got '%v'", m["image_url"])
	}
}

func TestMessageContentItem_MarshalJSON_ImageURL_Struct(t *testing.T) {
	item := MessageContentItem{
		Type: "image_url",
		ImageURL: &MessageContentItemImageURL{
			URL:    "https://example.com/photo.jpg",
			Detail: "low",
		},
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal result failed: %v", err)
	}
	imgURL, ok := m["image_url"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected image_url to be object, got %T", m["image_url"])
	}
	if imgURL["url"] != "https://example.com/photo.jpg" {
		t.Errorf("Expected url 'https://example.com/photo.jpg', got '%v'", imgURL["url"])
	}
	if imgURL["detail"] != "low" {
		t.Errorf("Expected detail 'low', got '%v'", imgURL["detail"])
	}
}

func TestMessageContentItem_ImageURL_RoundTrip(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{"string format", `{"type":"image_url","image_url":"https://a.com/x.png"}`},
		{"struct format", `{"type":"image_url","image_url":{"url":"https://b.com/y.jpg","detail":"auto"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var item MessageContentItem
			if err := json.Unmarshal([]byte(tt.json), &item); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}
			data, err := json.Marshal(item)
			if err != nil {
				t.Fatalf("Marshal failed: %v", err)
			}
			var item2 MessageContentItem
			if err := json.Unmarshal(data, &item2); err != nil {
				t.Fatalf("Round-trip Unmarshal failed: %v", err)
			}
			if item.ImageURL == nil && item2.ImageURL != nil || item.ImageURL != nil && item2.ImageURL == nil {
				t.Error("ImageURL nil mismatch after round-trip")
			}
			if item.ImageURL != nil && item2.ImageURL != nil && item.ImageURL.GetImageUrl() != item2.ImageURL.GetImageUrl() {
				t.Errorf("URL mismatch: %s vs %s", item.ImageURL.GetImageUrl(), item2.ImageURL.GetImageUrl())
			}
		})
	}
}

func TestMessage_UnmarshalJSON_Array_WithImageURL(t *testing.T) {
	jsonData := `{
		"role": "user",
		"content": [
			{"type": "text", "text": "Describe this image"},
			{"type": "image_url", "image_url": "https://example.com/img.png"}
		]
	}`
	var msg Message
	err := json.Unmarshal([]byte(jsonData), &msg)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	contentArr, ok := msg.Content.(MessageContentArray)
	if !ok {
		t.Fatalf("Expected MessageContentArray, got %T", msg.Content)
	}
	if len(contentArr) != 2 {
		t.Fatalf("Expected 2 items, got %d", len(contentArr))
	}
	if contentArr[1].Type != "image_url" || contentArr[1].ImageURL == nil {
		t.Errorf("Expected image_url item with ImageURL set")
	}
	if contentArr[1].ImageURL.GetImageUrl() != "https://example.com/img.png" {
		t.Errorf("Expected URL 'https://example.com/img.png', got '%s'", contentArr[1].ImageURL.GetImageUrl())
	}
}

func TestImageURLString_GetImageUrl(t *testing.T) {
	s := ImageURLString("https://test.com/a.png")
	if s.GetImageUrl() != "https://test.com/a.png" {
		t.Errorf("GetImageUrl() = %s, want https://test.com/a.png", s.GetImageUrl())
	}
}

func TestMessageContentItemImageURL_GetImageUrl(t *testing.T) {
	obj := &MessageContentItemImageURL{URL: "https://test.com/b.jpg", Detail: "high"}
	if obj.GetImageUrl() != "https://test.com/b.jpg" {
		t.Errorf("GetImageUrl() = %s, want https://test.com/b.jpg", obj.GetImageUrl())
	}
}
