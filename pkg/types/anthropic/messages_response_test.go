package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeMessagesStreamEvent_Marshal_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name          string
		JSON          string
		Object        ClaudeMessagesStreamEvent
		UnmarshalOnly bool
	}{
		{
			Name: "ContentBlockStartText",
			JSON: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			Object: ClaudeMessagesStreamEvent{
				Type:         "content_block_start",
				Index:        intPtr(0),
				ContentBlock: &TextBlockParam{Type: "text", Text: ""},
			},
		},
		{
			Name: "ContentBlockStartToolUse",
			JSON: `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_123","name":"get_weather","input":{}}}`,
			Object: ClaudeMessagesStreamEvent{
				Type:  "content_block_start",
				Index: intPtr(1),
				ContentBlock: &ToolUseBlockParam{
					Type:  "tool_use",
					ID:    "toolu_123",
					Name:  "get_weather",
					Input: json.RawMessage("{}"),
				},
			},
		},
		{
			Name: "ContentBlockDelta",
			JSON: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
			Object: ClaudeMessagesStreamEvent{
				Type:  "content_block_delta",
				Index: intPtr(0),
				Delta: &DeltaUnion{
					ContentBlockDelta: ContentBlockDelta{
						Type: "text_delta",
						Text: strPtr("Hello"),
					},
				},
			},
		},
		{
			Name: "MessageStart",
			JSON: `{"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","content":[],"model":"claude-3-5-sonnet-20241022","stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`,
			Object: ClaudeMessagesStreamEvent{
				Type: "message_start",
				Message: &ClaudeMessagesResponse{
					ID:      "msg_123",
					Type:    "message",
					Role:    "assistant",
					Model:   "claude-3-5-sonnet-20241022",
					Content: MessageContentBlockArray{},
					Usage:   &Usage{InputTokens: int64Ptr(10), OutputTokens: int64Ptr(0)},
				},
			},
			UnmarshalOnly: true, // stop_reason:null → empty string with omitempty doesn't round-trip
		},
		{
			Name:   "MessageStop",
			JSON:   `{"type":"message_stop"}`,
			Object: ClaudeMessagesStreamEvent{Type: "message_stop"},
		},
		{
			Name:          "TopLevelNull",
			JSON:          `null`,
			Object:        ClaudeMessagesStreamEvent{},
			UnmarshalOnly: true,
		},
	}

	for _, tc := range testCases {
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			var event ClaudeMessagesStreamEvent
			err := json.Unmarshal([]byte(tc.JSON), &event)
			require.NoError(t, err)
			assert.Equal(t, tc.Object, event)
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

// TestRealAPI_NonStreamResponses_Unmarshal tests unmarshaling of real non-streaming API
// responses captured from Infini-AI's GLM-5.1 and Claude-Sonnet-4-6 models. These responses
// contain extra non-standard fields (e.g. prompt_tokens_details, caller, stop_details,
// cache_creation) that our types don't model, so only Unmarshal is tested.
func TestRealAPI_NonStreamResponses_Unmarshal(t *testing.T) {
	testCases := []struct {
		Name   string
		JSON   string
		Object ClaudeMessagesResponse
	}{
		// ── GLM-5.1 Non-Streaming: tool call ──
		{
			Name: "GLM51_ToolCall",
			JSON: `{
				"content": [
					{"signature":"","thinking":"The user wants to know the weather in Beijing. I'll use the get_weather tool with location \"Beijing\".","type":"thinking"},
					{"id":"toolu_80f1f820bbbe4e38b4c68078","input":{"location":"Beijing"},"name":"get_weather","type":"tool_use"}
				],
				"id":"msg_c6282afd-3205-482d-a813-14382d94d00f",
				"model":"glm-5.1",
				"role":"assistant",
				"stop_reason":"tool_use",
				"type":"message",
				"usage":{"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"input_tokens":175,"output_tokens":36,"prompt_tokens_details":{"cached_tokens":0}}
			}`,
			Object: ClaudeMessagesResponse{
				ID:         "msg_c6282afd-3205-482d-a813-14382d94d00f",
				Type:       "message",
				Role:       "assistant",
				Model:      "glm-5.1",
				StopReason: "tool_use",
				Content: MessageContentBlockArray{
					&ThinkingBlockParam{
						Type:      "thinking",
						Thinking:  `The user wants to know the weather in Beijing. I'll use the get_weather tool with location "Beijing".`,
						Signature: "",
					},
					&ToolUseBlockParam{
						Type:  "tool_use",
						ID:    "toolu_80f1f820bbbe4e38b4c68078",
						Name:  "get_weather",
						Input: json.RawMessage(`{"location":"Beijing"}`),
					},
				},
				Usage: &Usage{
					InputTokens:              int64Ptr(175),
					OutputTokens:             int64Ptr(36),
					CacheCreationInputTokens: int64Ptr(0),
					CacheReadInputTokens:     int64Ptr(0),
				},
			},
		},
		// ── GLM-5.1 Non-Streaming: tool_result follow-up → text answer ──
		{
			Name: "GLM51_ToolResultFollowUp",
			JSON: `{
				"content": [
					{"signature":"","thinking":"The weather in Beijing is sunny, 25°C, with 40% humidity. I'll present this information to the user.","type":"thinking"},
					{"text":"The current weather in **Beijing** is:\n\n- ☀️ **Sunny**\n- 🌡️ **Temperature:** 25°C\n- 💧 **Humidity:** 40%\n\nIt looks like a pleasant day in Beijing!","type":"text"}
				],
				"id":"msg_f4dc3793-e936-446f-80f7-27498f73f617",
				"model":"glm-5.1",
				"role":"assistant",
				"stop_reason":"end_turn",
				"type":"message",
				"usage":{"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"input_tokens":229,"output_tokens":77,"prompt_tokens_details":{"cached_tokens":0}}
			}`,
			Object: ClaudeMessagesResponse{
				ID:         "msg_f4dc3793-e936-446f-80f7-27498f73f617",
				Type:       "message",
				Role:       "assistant",
				Model:      "glm-5.1",
				StopReason: "end_turn",
				Content: MessageContentBlockArray{
					&ThinkingBlockParam{
						Type:      "thinking",
						Thinking:  "The weather in Beijing is sunny, 25°C, with 40% humidity. I'll present this information to the user.",
						Signature: "",
					},
					&TextBlockParam{
						Type: "text",
						Text: "The current weather in **Beijing** is:\n\n- ☀️ **Sunny**\n- 🌡️ **Temperature:** 25°C\n- 💧 **Humidity:** 40%\n\nIt looks like a pleasant day in Beijing!",
					},
				},
				Usage: &Usage{
					InputTokens:              int64Ptr(229),
					OutputTokens:             int64Ptr(77),
					CacheCreationInputTokens: int64Ptr(0),
					CacheReadInputTokens:     int64Ptr(0),
				},
			},
		},
		// ── Claude-Sonnet-4-6 Non-Streaming: tool call ──
		{
			Name: "ClaudeSonnet46_ToolCall",
			JSON: `{
				"model":"mcs-5",
				"id":"msg_01NmTdg7V6NJNPw3PrMau6kL",
				"type":"message",
				"role":"assistant",
				"content":[{"type":"tool_use","id":"toolu_019FRbRb6d4YugSfgG1BYk47","name":"get_weather","input":{"location":"Beijing"},"caller":{"type":"direct"}}],
				"stop_reason":"tool_use",
				"stop_sequence":null,
				"stop_details":{"type":"tool_use"},
				"usage":{"input_tokens":680,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":38,"service_tier":"standard","inference_geo":"global"}
			}`,
			Object: ClaudeMessagesResponse{
				ID:         "msg_01NmTdg7V6NJNPw3PrMau6kL",
				Type:       "message",
				Role:       "assistant",
				Model:      "mcs-5",
				StopReason: "tool_use",
				Content: MessageContentBlockArray{
					&ToolUseBlockParam{
						Type:  "tool_use",
						ID:    "toolu_019FRbRb6d4YugSfgG1BYk47",
						Name:  "get_weather",
						Input: json.RawMessage(`{"location":"Beijing"}`),
					},
				},
				Usage: &Usage{
					InputTokens:              int64Ptr(680),
					OutputTokens:             int64Ptr(38),
					CacheCreationInputTokens: int64Ptr(0),
					CacheReadInputTokens:     int64Ptr(0),
				},
			},
		},
		// ── Claude-Sonnet-4-6 Non-Streaming: tool_result follow-up → text answer ──
		{
			Name: "ClaudeSonnet46_ToolResultFollowUp",
			JSON: `{
				"model":"mcs-5",
				"id":"msg_bdrk_01DdxdcXt2r11qNHHPm2Cgpf",
				"type":"message",
				"role":"assistant",
				"content":[{"type":"text","text":"The current weather in **Beijing** is as follows:\n\n- ☀️ **Condition:** Sunny\n- 🌡️ **Temperature:** 25°C\n- 💧 **Humidity:** 40%\n\nIt looks like a pleasant day in Beijing! Great weather for outdoor activities."}],
				"stop_reason":"end_turn",
				"stop_sequence":null,
				"stop_details":null,
				"usage":{"input_tokens":666,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":71}
			}`,
			Object: ClaudeMessagesResponse{
				ID:         "msg_bdrk_01DdxdcXt2r11qNHHPm2Cgpf",
				Type:       "message",
				Role:       "assistant",
				Model:      "mcs-5",
				StopReason: "end_turn",
				Content: MessageContentBlockArray{
					&TextBlockParam{
						Type: "text",
						Text: "The current weather in **Beijing** is as follows:\n\n- ☀️ **Condition:** Sunny\n- 🌡️ **Temperature:** 25°C\n- 💧 **Humidity:** 40%\n\nIt looks like a pleasant day in Beijing! Great weather for outdoor activities.",
					},
				},
				Usage: &Usage{
					InputTokens:              int64Ptr(666),
					OutputTokens:             int64Ptr(71),
					CacheCreationInputTokens: int64Ptr(0),
					CacheReadInputTokens:     int64Ptr(0),
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			var resp ClaudeMessagesResponse
			err := json.Unmarshal([]byte(strings.TrimSpace(tc.JSON)), &resp)
			require.NoError(t, err, "unmarshal should succeed even with extra fields")
			assert.Equal(t, tc.Object, resp)
		})
	}
}

// TestRealAPI_StreamEvents_Unmarshal tests unmarshaling of real streaming SSE event data
// captured from Infini-AI's GLM-5.1 and Claude-Sonnet-4-6 models. These events contain extra
// non-standard fields (e.g. prompt_tokens_details, stop_details, amazon-bedrock-*) that our
// types don't model, so only Unmarshal is tested.
func TestRealAPI_StreamEvents_Unmarshal(t *testing.T) {
	testCases := []struct {
		Name   string
		JSON   string
		Object ClaudeMessagesStreamEvent
	}{
		// ── GLM-5.1 Streaming events ──
		{
			Name: "GLM51_MessageStart",
			JSON: `{"message":{"model":"glm-5.1","id":"msg_60a10c20-c51e-4eec-8f05-07216fbb1ab5","role":"assistant","type":"message","content":[],"usage":{"input_tokens":63,"output_tokens":0}},"type":"message_start"}`,
			Object: ClaudeMessagesStreamEvent{
				Type: "message_start",
				Message: &ClaudeMessagesResponse{
					ID:      "msg_60a10c20-c51e-4eec-8f05-07216fbb1ab5",
					Type:    "message",
					Role:    "assistant",
					Model:   "glm-5.1",
					Content: MessageContentBlockArray{},
					Usage:   &Usage{InputTokens: int64Ptr(63), OutputTokens: int64Ptr(0)},
				},
			},
		},
		{
			Name:   "GLM51_Ping",
			JSON:   `{"type":"ping"}`,
			Object: ClaudeMessagesStreamEvent{Type: "ping"},
		},
		{
			Name: "GLM51_ContentBlockStart_Thinking",
			JSON: `{"type":"content_block_start","content_block":{"type":"thinking","signature":"","thinking":""},"index":0}`,
			Object: ClaudeMessagesStreamEvent{
				Type:  "content_block_start",
				Index: intPtr(0),
				ContentBlock: &ThinkingBlockParam{
					Type:      "thinking",
					Thinking:  "",
					Signature: "",
				},
			},
		},
		{
			Name: "GLM51_ContentBlockDelta_ThinkingDelta",
			JSON: `{"delta":{"type":"thinking_delta","thinking":"The user wants"},"type":"content_block_delta","index":0}`,
			Object: ClaudeMessagesStreamEvent{
				Type:  "content_block_delta",
				Index: intPtr(0),
				Delta: &DeltaUnion{
					ContentBlockDelta: ContentBlockDelta{
						Type:     "thinking_delta",
						Thinking: strPtr("The user wants"),
					},
				},
			},
		},
		{
			Name: "GLM51_ContentBlockDelta_SignatureDelta",
			JSON: `{"delta":{"type":"signature_delta","signature":""},"type":"content_block_delta","index":0}`,
			Object: ClaudeMessagesStreamEvent{
				Type:  "content_block_delta",
				Index: intPtr(0),
				Delta: &DeltaUnion{
					ContentBlockDelta: ContentBlockDelta{
						Type:      "signature_delta",
						Signature: strPtr(""),
					},
				},
			},
		},
		{
			Name:   "GLM51_ContentBlockStop",
			JSON:   `{"type":"content_block_stop","index":0}`,
			Object: ClaudeMessagesStreamEvent{Type: "content_block_stop", Index: intPtr(0)},
		},
		{
			Name: "GLM51_ContentBlockStart_ToolUse",
			JSON: `{"type":"content_block_start","content_block":{"name":"get_weather","input":{},"id":"toolu_d138668b2bd4467abd640a53","type":"tool_use"},"index":1}`,
			Object: ClaudeMessagesStreamEvent{
				Type:  "content_block_start",
				Index: intPtr(1),
				ContentBlock: &ToolUseBlockParam{
					Type:  "tool_use",
					ID:    "toolu_d138668b2bd4467abd640a53",
					Name:  "get_weather",
					Input: json.RawMessage(`{}`),
				},
			},
		},
		{
			Name: "GLM51_ContentBlockDelta_InputJsonDelta_Empty",
			JSON: `{"delta":{"partial_json":"","type":"input_json_delta"},"type":"content_block_delta","index":1}`,
			Object: ClaudeMessagesStreamEvent{
				Type:  "content_block_delta",
				Index: intPtr(1),
				Delta: &DeltaUnion{
					ContentBlockDelta: ContentBlockDelta{
						Type:        "input_json_delta",
						PartialJSON: strPtr(""),
					},
				},
			},
		},
		{
			Name: "GLM51_ContentBlockDelta_InputJsonDelta_Partial",
			JSON: `{"delta":{"partial_json":"{\"location\": \"Beijing\"}","type":"input_json_delta"},"type":"content_block_delta","index":1}`,
			Object: ClaudeMessagesStreamEvent{
				Type:  "content_block_delta",
				Index: intPtr(1),
				Delta: &DeltaUnion{
					ContentBlockDelta: ContentBlockDelta{
						Type:        "input_json_delta",
						PartialJSON: strPtr(`{"location": "Beijing"}`),
					},
				},
			},
		},
		{
			Name: "GLM51_MessageDelta",
			JSON: `{"delta":{"stop_reason":"tool_use"},"type":"message_delta","usage":{"output_tokens":39,"cache_creation_input_tokens":0,"input_tokens":175,"cache_read_input_tokens":0,"prompt_tokens_details":{"cached_tokens":0}}}`,
			Object: ClaudeMessagesStreamEvent{
				Type: "message_delta",
				Delta: &DeltaUnion{
					MessageDelta: MessageDelta{
						StopReason: strPtr("tool_use"),
					},
				},
				Usage: &Usage{
					InputTokens:              int64Ptr(175),
					OutputTokens:             int64Ptr(39),
					CacheCreationInputTokens: int64Ptr(0),
					CacheReadInputTokens:     int64Ptr(0),
				},
			},
		},
		{
			Name:   "GLM51_MessageStop",
			JSON:   `{"type":"message_stop"}`,
			Object: ClaudeMessagesStreamEvent{Type: "message_stop"},
		},
		// ── Claude-Sonnet-4-6 Streaming events ──
		{
			Name: "ClaudeSonnet46_MessageStart",
			JSON: `{"type":"message_start","message":{"model":"mcs-5","id":"msg_bdrk_01AegxDeE11veXgd878SUjFX","type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"stop_details":null,"usage":{"input_tokens":680,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":5}}}`,
			Object: ClaudeMessagesStreamEvent{
				Type: "message_start",
				Message: &ClaudeMessagesResponse{
					ID:      "msg_bdrk_01AegxDeE11veXgd878SUjFX",
					Type:    "message",
					Role:    "assistant",
					Model:   "mcs-5",
					Content: MessageContentBlockArray{},
					Usage: &Usage{
						InputTokens:              int64Ptr(680),
						OutputTokens:             int64Ptr(5),
						CacheCreationInputTokens: int64Ptr(0),
						CacheReadInputTokens:     int64Ptr(0),
					},
				},
			},
		},
		{
			Name: "ClaudeSonnet46_ContentBlockStart_ToolUse",
			JSON: `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_bdrk_0193mV9uwy5TFF8p8sTGi3hC","name":"get_weather","input":{}}}`,
			Object: ClaudeMessagesStreamEvent{
				Type:  "content_block_start",
				Index: intPtr(0),
				ContentBlock: &ToolUseBlockParam{
					Type:  "tool_use",
					ID:    "toolu_bdrk_0193mV9uwy5TFF8p8sTGi3hC",
					Name:  "get_weather",
					Input: json.RawMessage(`{}`),
				},
			},
		},
		{
			Name: "ClaudeSonnet46_ContentBlockDelta_InputJsonDelta_Empty",
			JSON: `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`,
			Object: ClaudeMessagesStreamEvent{
				Type:  "content_block_delta",
				Index: intPtr(0),
				Delta: &DeltaUnion{
					ContentBlockDelta: ContentBlockDelta{
						Type:        "input_json_delta",
						PartialJSON: strPtr(""),
					},
				},
			},
		},
		{
			Name: "ClaudeSonnet46_ContentBlockDelta_InputJsonDelta_Partial",
			JSON: `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"lo"}}`,
			Object: ClaudeMessagesStreamEvent{
				Type:  "content_block_delta",
				Index: intPtr(0),
				Delta: &DeltaUnion{
					ContentBlockDelta: ContentBlockDelta{
						Type:        "input_json_delta",
						PartialJSON: strPtr(`{"lo`),
					},
				},
			},
		},
		{
			Name:   "ClaudeSonnet46_ContentBlockStop",
			JSON:   `{"type":"content_block_stop","index":0}`,
			Object: ClaudeMessagesStreamEvent{Type: "content_block_stop", Index: intPtr(0)},
		},
		{
			Name: "ClaudeSonnet46_MessageDelta",
			JSON: `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null,"stop_details":null},"usage":{"input_tokens":680,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":38}}`,
			Object: ClaudeMessagesStreamEvent{
				Type: "message_delta",
				Delta: &DeltaUnion{
					MessageDelta: MessageDelta{
						StopReason: strPtr("tool_use"),
					},
				},
				Usage: &Usage{
					InputTokens:              int64Ptr(680),
					OutputTokens:             int64Ptr(38),
					CacheCreationInputTokens: int64Ptr(0),
					CacheReadInputTokens:     int64Ptr(0),
				},
			},
		},
		{
			Name:   "ClaudeSonnet46_MessageStop",
			JSON:   `{"type":"message_stop","amazon-bedrock-invocationMetrics":{"inputTokenCount":680,"outputTokenCount":38,"invocationLatency":1307,"firstByteLatency":1100}}`,
			Object: ClaudeMessagesStreamEvent{Type: "message_stop"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			var event ClaudeMessagesStreamEvent
			err := json.Unmarshal([]byte(strings.TrimSpace(tc.JSON)), &event)
			require.NoError(t, err, "unmarshal should succeed even with extra fields")
			assert.Equal(t, tc.Object, event)
		})
	}
}
