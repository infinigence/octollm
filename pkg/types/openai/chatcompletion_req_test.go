package openai

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPtr(i int) *int {
	return &i
}

func floatPtr(f float64) *float64 {
	return &f
}

func TestMessage_Marshal_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name   string
		JSON   string
		Object *Message
	}{
		{
			Name: "String",
			JSON: `{"role":"user","content":"Hello, world!"}`,
			Object: &Message{
				Role:    "user",
				Content: MessageContentString("Hello, world!"),
			},
		},
		{
			Name: "Array",
			JSON: `{"role":"user","content":[{"type":"text","text":"Hello"},{"type":"text","text":" world!"}]}`,
			Object: &Message{
				Role: "user",
				Content: MessageContentArray{
					{
						Type: "text",
						Text: "Hello",
					},
					{
						Type: "text",
						Text: " world!",
					},
				},
			},
		},
		{
			Name: "ImageURL",
			JSON: `{
				"role": "user",
				"content": [
					{"type": "text", "text": "Describe this image"},
					{"type": "image_url", "image_url": "https://example.com/img.png"}
				]
			}`,
			Object: &Message{
				Role: "user",
				Content: MessageContentArray{
					{
						Type: "text",
						Text: "Describe this image",
					},
					{
						Type:     "image_url",
						ImageURL: MessageContentItemImageURLString("https://example.com/img.png"),
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			var msg Message
			err := json.Unmarshal([]byte(tc.JSON), &msg)
			require.NoError(t, err)
			assert.Equal(t, tc.Object, &msg)
		})
		t.Run("Marshal_"+tc.Name, func(t *testing.T) {
			data, err := json.Marshal(tc.Object)
			require.NoError(t, err)
			assert.JSONEq(t, tc.JSON, string(data))
		})
	}
}

func TestApiChatCompletionsRequest_Marshal_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name   string
		JSON   string
		Object *ChatCompletionRequest
	}{
		{
			Name: "String",
			JSON: `{
				"model": "gpt-4",
				"messages": [
					{"role": "system", "content": "You are a helpful assistant."},
					{"role": "user", "content": "Hello!"}
				],
				"max_tokens": 100,
				"stop": "stop",
				"temperature": 0.7
			}`,
			Object: &ChatCompletionRequest{
				Model: "gpt-4",
				Messages: []*Message{
					{Role: "system", Content: MessageContentString("You are a helpful assistant.")},
					{Role: "user", Content: MessageContentString("Hello!")},
				},
				MaxTokens:   intPtr(100),
				Stop:        StopString("stop"),
				Temperature: floatPtr(0.7),
			},
		},
	}

	for _, tc := range testCases {
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			var req ChatCompletionRequest
			err := json.Unmarshal([]byte(tc.JSON), &req)
			require.NoError(t, err)
			assert.Equal(t, tc.Object, &req)
		})
		t.Run("Marshal_"+tc.Name, func(t *testing.T) {
			data, err := json.Marshal(tc.Object)
			require.NoError(t, err)
			assert.JSONEq(t, tc.JSON, string(data))
		})
	}
}

func TestMessageContentItem_Marhsal_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name   string
		JSON   string
		Object *MessageContentItem
	}{
		{
			Name: "String",
			JSON: `{"type": "image_url", "image_url": "https://example.com/image.png"}`,
			Object: &MessageContentItem{
				Type:     "image_url",
				ImageURL: MessageContentItemImageURLString("https://example.com/image.png"),
			},
		},
		{
			Name: "String",
			JSON: `{"type": "image_url", "image_url": {"url": "https://example.com/img.jpg", "detail": "high"}}`,
			Object: &MessageContentItem{
				Type: "image_url",
				ImageURL: &MessageContentItemImageURL{
					URL:    "https://example.com/img.jpg",
					Detail: "high",
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			var item MessageContentItem
			err := json.Unmarshal([]byte(tc.JSON), &item)
			require.NoError(t, err)
			assert.Equal(t, tc.Object, &item)
		})
		t.Run("Marshal_"+tc.Name, func(t *testing.T) {
			data, err := json.Marshal(tc.Object)
			require.NoError(t, err)
			assert.JSONEq(t, tc.JSON, string(data))
		})
	}
}

func TestImageURLString_GetImageUrl(t *testing.T) {
	s := MessageContentItemImageURLString("https://test.com/a.png")
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

func TestStopValue_Marshal_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name          string
		Object        StopValue
		JSON          string
		MarshalOnly   bool
		UnmarshalOnly bool
	}{
		{
			name:   "string",
			Object: StopString("stop"),
			JSON:   `"stop"`,
		},
		{
			name:   "array",
			Object: StopArray{"stop", "end"},
			JSON:   `["stop","end"]`,
		},
	}

	for _, tt := range tests {
		if !tt.UnmarshalOnly {
			t.Run("Marshal_"+tt.name, func(t *testing.T) {
				data, err := json.Marshal(tt.Object)
				assert.NoError(t, err)
				assert.JSONEq(t, tt.JSON, string(data))
			})
		}
		if !tt.MarshalOnly {
			t.Run("Unmarshal_"+tt.name, func(t *testing.T) {
				var sf stopField
				err := json.Unmarshal([]byte(tt.JSON), &sf)
				assert.NoError(t, err)
				assert.Equal(t, tt.Object, sf.Value)
			})
		}
	}
}
