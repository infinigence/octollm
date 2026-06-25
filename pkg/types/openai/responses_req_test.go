package openai

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func boolPtr(b bool) *bool {
	return &b
}

func TestResponsesRequest_Marshal_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name   string
		JSON   string
		Object ResponsesRequest
	}{
		{
			Name: "StringInput",
			JSON: `{"model":"gpt-4.1","stream":true,"input":"Tell me a three sentence bedtime story about a unicorn."}`,
			Object: ResponsesRequest{
				Model:  "gpt-4.1",
				Stream: boolPtr(true),
				Input:  ResponsesInputString("Tell me a three sentence bedtime story about a unicorn."),
			},
		},
		{
			Name: "ArrayInputWithStream",
			JSON: `{"model":"gpt-4.1","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
			Object: ResponsesRequest{
				Model:  "gpt-4.1",
				Stream: boolPtr(true),
				Input: ResponsesInputMessageArray{
					{
						Role:    "user",
						Content: ResponsesInputMessageContentArray{{Type: "input_text", Text: "hi"}},
					},
				},
			},
		},
		{
			Name: "ArrayInputWithTextAndImage",
			JSON: `{"model":"gpt-5.4","input":[{"role":"user","content":[{"type":"input_text","text":"what is in this image?"},{"type":"input_image","image_url":"https://example.com/a.jpg"}]}]}`,
			Object: ResponsesRequest{
				Model: "gpt-5.4",
				Input: ResponsesInputMessageArray{
					{
						Role: "user",
						Content: ResponsesInputMessageContentArray{
							{Type: "input_text", Text: "what is in this image?"},
							{Type: "input_image", ImageURL: MessageContentItemImageURLString("https://example.com/a.jpg")},
						},
					},
				},
			},
		},
		{
			Name: "ArrayInputWithImageObject",
			JSON: `{"model":"gpt-5.4","input":[{"role":"user","content":[{"type":"input_image","image_url":{"url":"https://example.com/b.jpg","detail":"high"}}]}]}`,
			Object: ResponsesRequest{
				Model: "gpt-5.4",
				Input: ResponsesInputMessageArray{
					{
						Role: "user",
						Content: ResponsesInputMessageContentArray{
							{Type: "input_image", ImageURL: &MessageContentItemImageURL{URL: "https://example.com/b.jpg", Detail: "high"}},
						},
					},
				},
			},
		},
		{
			Name: "ArrayInputWithStringContent",
			JSON: `{"model":"gpt-5.4","input":[{"role":"user","content":"plain text message"}]}`,
			Object: ResponsesRequest{
				Model: "gpt-5.4",
				Input: ResponsesInputMessageArray{
					{Role: "user", Content: ResponsesInputMessageContentString("plain text message")},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			var req ResponsesRequest
			err := json.Unmarshal([]byte(tc.JSON), &req)
			require.NoError(t, err)
			assert.Equal(t, tc.Object, req)
		})
		t.Run("Marshal_"+tc.Name, func(t *testing.T) {
			data, err := json.Marshal(tc.Object)
			require.NoError(t, err)
			assert.JSONEq(t, tc.JSON, string(data))
		})
	}
}

func TestResponsesInputValue_Marshal_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name   string
		JSON   string
		Object ResponsesInputValue
	}{
		{
			Name:   "String",
			JSON:   `"hello"`,
			Object: ResponsesInputString("hello"),
		},
		{
			Name:   "Array",
			JSON:   `[{"role":"user","content":"hi"}]`,
			Object: ResponsesInputMessageArray{{Role: "user", Content: ResponsesInputMessageContentString("hi")}},
		},
	}

	for _, tc := range testCases {
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			var sf responsesInputField
			err := json.Unmarshal([]byte(tc.JSON), &sf)
			require.NoError(t, err)
			assert.Equal(t, tc.Object, sf.Value)
		})
		t.Run("Marshal_"+tc.Name, func(t *testing.T) {
			data, err := json.Marshal(tc.Object)
			require.NoError(t, err)
			assert.JSONEq(t, tc.JSON, string(data))
		})
	}
}

func TestResponsesInputMessageContent_Marshal_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name   string
		JSON   string
		Object ResponsesInputMessageContent
	}{
		{
			Name:   "String",
			JSON:   `"hello"`,
			Object: ResponsesInputMessageContentString("hello"),
		},
		{
			Name: "Array",
			JSON: `[{"type":"input_text","text":"hi"},{"type":"input_image","image_url":"https://example.com/x.png"}]`,
			Object: ResponsesInputMessageContentArray{
				{Type: "input_text", Text: "hi"},
				{Type: "input_image", ImageURL: MessageContentItemImageURLString("https://example.com/x.png")},
			},
		},
	}

	for _, tc := range testCases {
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			var sf responsesInputMessageContentField
			err := json.Unmarshal([]byte(tc.JSON), &sf)
			require.NoError(t, err)
			assert.Equal(t, tc.Object, sf.Value)
		})
		t.Run("Marshal_"+tc.Name, func(t *testing.T) {
			data, err := json.Marshal(tc.Object)
			require.NoError(t, err)
			assert.JSONEq(t, tc.JSON, string(data))
		})
	}
}

func TestResponsesInputMessageContent_ExtractText(t *testing.T) {
	testCases := []struct {
		Name          string
		JSON          string
		ExtractedText string
	}{
		{
			Name:          "String",
			JSON:          `"hello"`,
			ExtractedText: "hello",
		},
		{
			Name:          "Array",
			JSON:          `[{"type":"input_text","text":"hi"},{"type":"input_image","image_url":"https://example.com/x.png"}]`,
			ExtractedText: "hihttps://example.com/x.png",
		},
	}

	for _, tc := range testCases {
		t.Run("ExtractText_"+tc.Name, func(t *testing.T) {
			var sf responsesInputMessageContentField
			err := json.Unmarshal([]byte(tc.JSON), &sf)
			require.NoError(t, err)
			assert.Equal(t, tc.ExtractedText, sf.Value.ExtractText(), tc.Name)
		})
	}
}

func TestResponsesInputMessage_Marshal_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name   string
		JSON   string
		Object ResponsesInputMessage
	}{
		{
			Name:   "StringContent",
			JSON:   `{"role":"user","content":"Hello, responses!"}`,
			Object: ResponsesInputMessage{Role: "user", Content: ResponsesInputMessageContentString("Hello, responses!")},
		},
		{
			Name: "ArrayContent",
			JSON: `{"role":"user","content":[{"type":"input_text","text":"Hello"},{"type":"input_image","image_url":"https://example.com/a.png"}]}`,
			Object: ResponsesInputMessage{
				Role: "user",
				Content: ResponsesInputMessageContentArray{
					{Type: "input_text", Text: "Hello"},
					{Type: "input_image", ImageURL: MessageContentItemImageURLString("https://example.com/a.png")},
				},
			},
		},
		{
			Name: "ImageURLObject",
			JSON: `{"role":"user","content":[{"type":"input_image","image_url":{"url":"https://example.com/b.jpg","detail":"high"}}]}`,
			Object: ResponsesInputMessage{
				Role: "user",
				Content: ResponsesInputMessageContentArray{
					{Type: "input_image", ImageURL: &MessageContentItemImageURL{URL: "https://example.com/b.jpg", Detail: "high"}},
				},
			},
		},
		{
			Name:   "StringContentInArrayInput",
			JSON:   `{"role":"user","content":"plain text message"}`,
			Object: ResponsesInputMessage{Role: "user", Content: ResponsesInputMessageContentString("plain text message")},
		},
	}

	for _, tc := range testCases {
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			var item ResponsesInputMessage
			err := json.Unmarshal([]byte(tc.JSON), &item)
			require.NoError(t, err)
			assert.Equal(t, tc.Object, item)
		})
		t.Run("Marshal_"+tc.Name, func(t *testing.T) {
			data, err := json.Marshal(tc.Object)
			require.NoError(t, err)
			assert.JSONEq(t, tc.JSON, string(data))
		})
	}
}

func TestResponsesResponse_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name   string
		JSON   string
		Object ResponsesResponse
	}{
		{
			Name: "UsageWithDetails",
			JSON: `{
				"id":"r1","object":"response","created_at":1,"status":"completed","model":"m",
				"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,
					"input_tokens_details":{"cached_tokens":3},
					"output_tokens_details":{"reasoning_tokens":2}
				}
			}`,
			Object: ResponsesResponse{
				Id: "r1",
				Usage: &ResponsesUsage{
					InputTokens:         10,
					OutputTokens:        5,
					TotalTokens:         15,
					InputTokensDetails:  &ResponsesInputTokenDetails{CachedTokens: 3},
					OutputTokensDetails: &ResponsesOutputTokenDetails{ReasoningTokens: 2},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			var resp ResponsesResponse
			err := json.Unmarshal([]byte(tc.JSON), &resp)
			require.NoError(t, err)
			assert.Equal(t, tc.Object, resp)
		})
		// t.Run("Marshal_"+tc.Name, func(t *testing.T) {
		// 	data, err := json.Marshal(tc.Object)
		// 	require.NoError(t, err)
		// 	assert.JSONEq(t, tc.JSON, string(data))
		// })
	}
}

func TestResponseStreamChunk_Marshal_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name   string
		JSON   string
		Object ResponseStreamChunk
	}{
		{
			Name: "ResponseCompleted",
			JSON: `{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`,
			Object: ResponseStreamChunk{
				Type: "response.completed",
				Response: &ResponsesResponse{
					Usage: &ResponsesUsage{
						InputTokens:  1,
						OutputTokens: 2,
						TotalTokens:  3,
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			var ch ResponseStreamChunk
			err := json.Unmarshal([]byte(tc.JSON), &ch)
			require.NoError(t, err)
			assert.Equal(t, tc.Object, ch)
		})
		t.Run("Marshal_"+tc.Name, func(t *testing.T) {
			data, err := json.Marshal(tc.Object)
			require.NoError(t, err)
			assert.JSONEq(t, tc.JSON, string(data))
		})
	}
}
