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
				Input: ResponsesInputItemArray{
					&ResponsesInputMessage{
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
				Input: ResponsesInputItemArray{
					&ResponsesInputMessage{
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
				Input: ResponsesInputItemArray{
					&ResponsesInputMessage{
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
				Input: ResponsesInputItemArray{
					&ResponsesInputMessage{Role: "user", Content: ResponsesInputMessageContentString("plain text message")},
				},
			},
		},
		{
			Name: "FunctionCallAndOutputItems",
			JSON: `{"model":"gpt-5.4","instructions":"You are helpful","input":[{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Tokyo\"}"},{"type":"function_call_output","call_id":"call_1","output":"Sunny"}]}`,
			Object: ResponsesRequest{
				Model:        "gpt-5.4",
				Instructions: "You are helpful",
				Input: ResponsesInputItemArray{
					&ResponsesInputFunctionCall{Type: "function_call", CallID: "call_1", Name: "get_weather", Arguments: `{"city":"Tokyo"}`},
					&ResponsesInputFunctionCallOutput{Type: "function_call_output", CallID: "call_1", Output: ResponsesFunctionCallOutputString("Sunny")},
				},
			},
		},
		{
			Name: "ToolChoiceStringAndTools",
			JSON: `{"model":"gpt-5.4","tool_choice":"required","tools":[{"type":"function","name":"get_weather","description":"Get weather","parameters":{"type":"object"}}]}`,
			Object: ResponsesRequest{
				Model:      "gpt-5.4",
				ToolChoice: ResponsesToolChoiceString("required"),
				Tools: []*ResponseTool{
					{
						Type:        "function",
						Name:        "get_weather",
						Description: "Get weather",
						Parameters:  json.RawMessage(`{"type":"object"}`),
					},
				},
			},
		},
		{
			Name: "ToolChoiceObject",
			JSON: `{"model":"gpt-5.4","tool_choice":{"type":"function","name":"get_weather"}}`,
			Object: ResponsesRequest{
				Model:      "gpt-5.4",
				ToolChoice: ResponsesToolChoiceObject{Type: "function", Name: "get_weather"},
			},
		},
		{
			Name: "ScalarParams",
			JSON: `{"model":"gpt-5.4","input":"hi","max_output_tokens":100,"temperature":0.5,"top_p":0.9,"reasoning":{"effort":"high"},"parallel_tool_calls":true}`,
			Object: ResponsesRequest{
				Model:             "gpt-5.4",
				Input:             ResponsesInputString("hi"),
				MaxOutputTokens:   intPtr(100),
				Temperature:       floatPtr(0.5),
				TopP:              floatPtr(0.9),
				Reasoning:         &ResponseReasoning{Effort: "high"},
				ParallelToolCalls: boolPtr(true),
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

func TestResponsesToolChoiceValue_Marshal_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name   string
		JSON   string
		Object ResponsesToolChoiceValue
	}{
		{
			Name:   "String",
			JSON:   `"auto"`,
			Object: ResponsesToolChoiceString("auto"),
		},
		{
			Name:   "Object",
			JSON:   `{"type":"function","name":"get_weather"}`,
			Object: ResponsesToolChoiceObject{Type: "function", Name: "get_weather"},
		},
		{
			Name:   "Null",
			JSON:   `null`,
			Object: nil,
		},
	}

	for _, tc := range testCases {
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			var sf responsesToolChoiceField
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
			Object: ResponsesInputItemArray{&ResponsesInputMessage{Role: "user", Content: ResponsesInputMessageContentString("hi")}},
		},
		{
			Name:   "Null",
			JSON:   `null`,
			Object: nil,
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

func TestResponsesInputValue_ExtractText(t *testing.T) {
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
			Name:          "MixedItemArray",
			JSON:          `[{"role":"user","content":"hi "},{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Tokyo\"}"},{"type":"function_call_output","call_id":"call_1","output":"Sunny"},{"type":"web_search_call","id":"ws_1"}]`,
			ExtractedText: `hi {"city":"Tokyo"}Sunny`,
		},
	}

	for _, tc := range testCases {
		t.Run("ExtractText_"+tc.Name, func(t *testing.T) {
			var sf responsesInputField
			err := json.Unmarshal([]byte(tc.JSON), &sf)
			require.NoError(t, err)
			assert.Equal(t, tc.ExtractedText, sf.Value.ExtractText())
		})
	}
}

func TestResponsesInputItem_Marshal_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name          string
		JSON          string
		Object        ResponsesInputItem
		UnmarshalOnly bool
	}{
		{
			Name:   "MessageWithoutType",
			JSON:   `{"role":"user","content":"hi"}`,
			Object: &ResponsesInputMessage{Role: "user", Content: ResponsesInputMessageContentString("hi")},
		},
		{
			Name:          "MessageWithType",
			JSON:          `{"type":"message","role":"user","content":"hi"}`,
			Object:        &ResponsesInputMessage{Role: "user", Content: ResponsesInputMessageContentString("hi")},
			UnmarshalOnly: true, // ResponsesInputMessage has no Type field, so "type" is dropped on marshal
		},
		{
			Name:   "FunctionCall",
			JSON:   `{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Tokyo\"}"}`,
			Object: &ResponsesInputFunctionCall{Type: "function_call", CallID: "call_1", Name: "get_weather", Arguments: `{"city":"Tokyo"}`},
		},
		{
			Name:   "FunctionCallOutput",
			JSON:   `{"type":"function_call_output","call_id":"call_1","output":"Sunny"}`,
			Object: &ResponsesInputFunctionCallOutput{Type: "function_call_output", CallID: "call_1", Output: ResponsesFunctionCallOutputString("Sunny")},
		},
		{
			Name: "Reasoning",
			JSON: `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"think"}],"encrypted_content":"enc"}`,
			Object: &ResponsesInputReasoning{
				Type:             "reasoning",
				ID:               "rs_1",
				Summary:          []*ResponsesReasoningSummaryPart{{Type: "summary_text", Text: "think"}},
				EncryptedContent: "enc",
			},
		},
		{
			Name:   "UnknownTypePreservedRaw",
			JSON:   `{"type":"web_search_call","id":"ws_1","status":"completed"}`,
			Object: &ResponsesInputRawItem{Raw: json.RawMessage(`{"type":"web_search_call","id":"ws_1","status":"completed"}`)},
		},
	}

	for _, tc := range testCases {
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			item, err := unmarshalResponsesInputItem([]byte(tc.JSON))
			require.NoError(t, err)
			assert.Equal(t, tc.Object, item)
		})
		if !tc.UnmarshalOnly {
			t.Run("Marshal_"+tc.Name, func(t *testing.T) {
				data, err := json.Marshal(tc.Object)
				require.NoError(t, err)
				assert.JSONEq(t, tc.JSON, string(data))
			})
		}
	}
}

func TestResponsesInputItem_ExtractText(t *testing.T) {
	testCases := []struct {
		Name          string
		JSON          string
		ExtractedText string
	}{
		{
			Name:          "Message",
			JSON:          `{"role":"user","content":[{"type":"input_text","text":"hi"}]}`,
			ExtractedText: "hi",
		},
		{
			Name:          "FunctionCall",
			JSON:          `{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Tokyo\"}"}`,
			ExtractedText: `{"city":"Tokyo"}`,
		},
		{
			Name:          "FunctionCallOutput",
			JSON:          `{"type":"function_call_output","call_id":"call_1","output":"Sunny"}`,
			ExtractedText: "Sunny",
		},
		{
			Name:          "Reasoning",
			JSON:          `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"step one; "},{"type":"summary_text","text":"step two"}],"encrypted_content":"enc"}`,
			ExtractedText: "step one; step two",
		},
		{
			Name:          "UnknownType",
			JSON:          `{"type":"web_search_call","id":"ws_1"}`,
			ExtractedText: "",
		},
	}

	for _, tc := range testCases {
		t.Run("ExtractText_"+tc.Name, func(t *testing.T) {
			item, err := unmarshalResponsesInputItem([]byte(tc.JSON))
			require.NoError(t, err)
			assert.Equal(t, tc.ExtractedText, item.ExtractText())
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
		{
			Name:   "Null",
			JSON:   `null`,
			Object: nil,
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
			ExtractedText: "hi[img:https://example.com/x.png]",
		},
		{
			Name:          "ArrayImageURLObject",
			JSON:          `[{"type":"input_image","image_url":{"url":"https://example.com/b.jpg","detail":"high"}}]`,
			ExtractedText: "[img:https://example.com/b.jpg]",
		},
		{
			Name:          "AssistantOutputTextPart",
			JSON:          `[{"type":"output_text","annotations":[],"text":"It is sunny."}]`,
			ExtractedText: "It is sunny.",
		},
		{
			Name:          "AssistantRefusalPart",
			JSON:          `[{"type":"refusal","refusal":"I can't help with that."}]`,
			ExtractedText: "I can't help with that.",
		},
		{
			Name:          "ArrayUnsupportedPart",
			JSON:          `[{"type":"input_file","file_url":"https://example.com/a.pdf"}]`,
			ExtractedText: "",
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

func TestResponsesInputFunctionCallOutput_Marshal_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name   string
		JSON   string
		Object ResponsesInputFunctionCallOutput
	}{
		{
			Name: "StringOutput",
			JSON: `{"type":"function_call_output","call_id":"call_1","output":"Sunny"}`,
			Object: ResponsesInputFunctionCallOutput{
				Type:   "function_call_output",
				CallID: "call_1",
				Output: ResponsesFunctionCallOutputString("Sunny"),
			},
		},
		{
			Name: "ArrayOutput",
			JSON: `{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"Sunny"}]}`,
			Object: ResponsesInputFunctionCallOutput{
				Type:   "function_call_output",
				CallID: "call_1",
				Output: ResponsesFunctionCallOutputArray{{Type: "input_text", Text: "Sunny"}},
			},
		},
	}

	for _, tc := range testCases {
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			var item ResponsesInputFunctionCallOutput
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

func TestResponsesFunctionCallOutputValue_Marshal_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name   string
		JSON   string
		Object ResponsesFunctionCallOutputValue
	}{
		{
			Name:   "String",
			JSON:   `"Sunny"`,
			Object: ResponsesFunctionCallOutputString("Sunny"),
		},
		{
			Name: "Array",
			JSON: `[{"type":"input_text","text":"Sunny"},{"type":"input_image","image_url":"https://example.com/w.png"}]`,
			Object: ResponsesFunctionCallOutputArray{
				{Type: "input_text", Text: "Sunny"},
				{Type: "input_image", ImageURL: MessageContentItemImageURLString("https://example.com/w.png")},
			},
		},
		{
			Name:   "Null",
			JSON:   `null`,
			Object: nil,
		},
	}

	for _, tc := range testCases {
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			var sf responsesFunctionCallOutputField
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

func TestResponsesFunctionCallOutputValue_ExtractText(t *testing.T) {
	testCases := []struct {
		Name          string
		JSON          string
		ExtractedText string
	}{
		{
			Name:          "String",
			JSON:          `"Sunny"`,
			ExtractedText: "Sunny",
		},
		{
			Name:          "Array",
			JSON:          `[{"type":"input_text","text":"Sunny"},{"type":"input_image","image_url":"https://example.com/w.png"}]`,
			ExtractedText: "Sunny[img:https://example.com/w.png]",
		},
	}

	for _, tc := range testCases {
		t.Run("ExtractText_"+tc.Name, func(t *testing.T) {
			var sf responsesFunctionCallOutputField
			err := json.Unmarshal([]byte(tc.JSON), &sf)
			require.NoError(t, err)
			assert.Equal(t, tc.ExtractedText, sf.Value.ExtractText())
		})
	}
}

func TestResponsesResponse_Marshal_UnmarshalJSON(t *testing.T) {
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
				Id:      "r1",
				Object:  "response",
				Created: 1,
				Status:  "completed",
				Model:   "m",
				Usage: &ResponsesUsage{
					InputTokens:         10,
					OutputTokens:        5,
					TotalTokens:         15,
					InputTokensDetails:  &ResponsesInputTokenDetails{CachedTokens: intPtr(3)},
					OutputTokensDetails: &ResponsesOutputTokenDetails{ReasoningTokens: intPtr(2)},
				},
			},
		},
		{
			Name: "UsageWithCacheWriteTokens",
			JSON: `{
				"id":"r2","object":"response","created_at":1,"status":"completed","model":"m",
				"usage":{"input_tokens":100,"output_tokens":5,"total_tokens":105,
					"input_tokens_details":{"cached_tokens":20,"cache_write_tokens":30}
				}
			}`,
			Object: ResponsesResponse{
				Id:      "r2",
				Object:  "response",
				Created: 1,
				Status:  "completed",
				Model:   "m",
				Usage: &ResponsesUsage{
					InputTokens:        100,
					OutputTokens:       5,
					TotalTokens:        105,
					InputTokensDetails: &ResponsesInputTokenDetails{CachedTokens: intPtr(20), CacheWriteTokens: intPtr(30)},
				},
			},
		},
		{
			Name: "FunctionCallOutput",
			JSON: `{
				"id":"r3","status":"completed","model":"m",
				"output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Tokyo\"}"}]
			}`,
			Object: ResponsesResponse{
				Id:     "r3",
				Status: "completed",
				Model:  "m",
				Output: []*ResponsesOutputItem{
					{ID: "fc_1", Type: "function_call", CallID: "call_1", Name: "get_weather", Arguments: `{"city":"Tokyo"}`},
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
		t.Run("Marshal_"+tc.Name, func(t *testing.T) {
			data, err := json.Marshal(tc.Object)
			require.NoError(t, err)
			assert.JSONEq(t, tc.JSON, string(data))
		})
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
		{
			Name: "FunctionCallArgumentsDelta",
			JSON: `{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"{\"city\":\"Tokyo\"}"}`,
			Object: ResponseStreamChunk{
				Type:      "response.function_call_arguments.delta",
				ItemID:    "fc_1",
				OutputIdx: intPtr(1),
				Delta:     `{"city":"Tokyo"}`,
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
