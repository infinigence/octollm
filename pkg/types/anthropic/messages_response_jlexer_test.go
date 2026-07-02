package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mailru/easyjson/jlexer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeMessagesResponse_ParseJLexer(t *testing.T) {
	testCases := []struct {
		Name string
		JSON string
	}{
		// Basic responses
		{
			Name: "Empty",
			JSON: `{}`,
		},
		{
			Name: "SimpleText",
			JSON: `{"id":"msg_01ABC","type":"message","role":"assistant","model":"claude-3-5-sonnet-20241022","content":[{"type":"text","text":"Hello, world!"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":3}}`,
		},
		{
			Name: "ToolUseResponse",
			JSON: `{"id":"msg_02","type":"message","role":"assistant","model":"claude-3-5-sonnet-20241022","content":[{"type":"text","text":"Let me check"},{"type":"tool_use","id":"tool_1","name":"get_weather","input":{"city":"NYC"}}],"stop_reason":"tool_use","usage":{"input_tokens":20,"output_tokens":15}}`,
		},
		{
			Name: "ThinkingResponse",
			JSON: `{"id":"msg_03","type":"message","role":"assistant","model":"claude-3-5-sonnet-20241022","content":[{"type":"thinking","thinking":"thinking text"},{"type":"text","text":"answer"}],"stop_reason":"stop_sequence","stop_sequence":"END","usage":{"input_tokens":7,"output_tokens":4,"cache_creation_input_tokens":2}}`,
		},
		{
			Name: "NullContent",
			JSON: `{"id":"msg_04","type":"message","role":"assistant","model":"claude-3-5-sonnet-20241022","content":null}`,
		},
		{
			Name: "NullUsage",
			JSON: `{"id":"msg_05","type":"message","role":"assistant","model":"claude-3-5-sonnet-20241022","content":[],"usage":null}`,
		},
		{
			Name: "NullStopSequence",
			JSON: `{"id":"msg_06","type":"message","role":"assistant","model":"claude-3-5-sonnet-20241022","content":[],"stop_sequence":null}`,
		},
		// Real API non-stream responses (with extra non-standard fields)
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
		},
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
		},
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
		},
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
		},
		{
			Name: "NullStringFields",
			JSON: `{"id":null,"type":null,"role":null,"model":null,"content":[]}`,
		},
		{
			Name: "NullStopReason",
			JSON: `{"id":"msg_07","type":"message","role":"assistant","model":"claude-3-5-sonnet","content":[],"stop_reason":null}`,
		},
		{
			Name: "EmptyContent",
			JSON: `{"id":"msg_08","type":"message","role":"assistant","model":"claude-3-5-sonnet","content":[]}`,
		},
		{
			Name: "UsageWithAllNullFields",
			JSON: `{"id":"msg_09","type":"message","role":"assistant","model":"claude-3-5-sonnet","content":[],"usage":{"input_tokens":null,"output_tokens":null,"cache_creation_input_tokens":null,"cache_read_input_tokens":null}}`,
		},
		{
			Name: "UsageWithCacheCreationOnly",
			JSON: `{"id":"msg_10","type":"message","role":"assistant","model":"claude-3-5-sonnet","content":[],"usage":{"input_tokens":5,"cache_creation_input_tokens":3}}`,
		},
		{
			Name: "UsageWithCacheReadOnly",
			JSON: `{"id":"msg_11","type":"message","role":"assistant","model":"claude-3-5-sonnet","content":[],"usage":{"input_tokens":5,"cache_read_input_tokens":2}}`,
		},
		{
			Name: "TopLevelNull",
			JSON: `null`,
		},
		{
			Name: "UnknownFieldsIgnored",
			JSON: `{"id":"msg_12","type":"message","role":"assistant","model":"claude-3-5-sonnet","content":[],"extra":"field","nested":{"a":1}}`,
		},
		{
			Name: "RedactedThinkingContent",
			JSON: `{"id":"msg_13","type":"message","role":"assistant","model":"claude-3-5-sonnet","content":[{"type":"redacted_thinking","data":"base64data"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`,
		},
		{
			Name: "DocumentContent",
			JSON: `{"id":"msg_14","type":"message","role":"assistant","model":"claude-3-5-sonnet","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="},"title":"doc.pdf","context":"A doc"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`,
		},
		{
			Name: "ToolResultContent",
			JSON: `{"id":"msg_15","type":"message","role":"assistant","model":"claude-3-5-sonnet","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"result text"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			jsonBytes := []byte(strings.TrimSpace(tc.JSON))

			var stdResp ClaudeMessagesResponse
			err := json.Unmarshal(jsonBytes, &stdResp)
			require.NoError(t, err, "standard json.Unmarshal should not error")

			in := jlexer.Lexer{Data: jsonBytes}
			jResp := &ClaudeMessagesResponse{}
			jResp.ParseJLexer(&in)
			require.NoError(t, in.Error(), "jlexer parser should not error")
			assert.Equal(t, &stdResp, jResp, "jlexer parser output should match standard json.Unmarshal")
		})
	}
}

func TestClaudeMessagesStreamEvent_ParseJLexer(t *testing.T) {
	testCases := []struct {
		Name string
		JSON string
	}{
		// Basic stream events
		{
			Name: "ContentBlockStartText",
			JSON: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		},
		{
			Name: "ContentBlockStartToolUse",
			JSON: `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_123","name":"get_weather","input":{}}}`,
		},
		{
			Name: "ContentBlockDelta",
			JSON: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		},
		{
			Name: "MessageStart",
			JSON: `{"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","content":[],"model":"claude-3-5-sonnet-20241022","stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`,
		},
		{
			Name: "MessageStop",
			JSON: `{"type":"message_stop"}`,
		},
		{
			Name: "TopLevelNull",
			JSON: `null`,
		},
		{
			Name: "Ping",
			JSON: `{"type":"ping"}`,
		},
		// Real API stream events
		{
			Name: "GLM51_MessageStart",
			JSON: `{"message":{"model":"glm-5.1","id":"msg_60a10c20-c51e-4eec-8f05-07216fbb1ab5","role":"assistant","type":"message","content":[],"usage":{"input_tokens":63,"output_tokens":0}},"type":"message_start"}`,
		},
		{
			Name: "GLM51_ContentBlockStart_Thinking",
			JSON: `{"type":"content_block_start","content_block":{"type":"thinking","signature":"","thinking":""},"index":0}`,
		},
		{
			Name: "GLM51_ContentBlockDelta_ThinkingDelta",
			JSON: `{"delta":{"type":"thinking_delta","thinking":"The user wants"},"type":"content_block_delta","index":0}`,
		},
		{
			Name: "GLM51_ContentBlockDelta_SignatureDelta",
			JSON: `{"delta":{"type":"signature_delta","signature":""},"type":"content_block_delta","index":0}`,
		},
		{
			Name: "GLM51_ContentBlockStop",
			JSON: `{"type":"content_block_stop","index":0}`,
		},
		{
			Name: "GLM51_ContentBlockStart_ToolUse",
			JSON: `{"type":"content_block_start","content_block":{"name":"get_weather","input":{},"id":"toolu_d138668b2bd4467abd640a53","type":"tool_use"},"index":1}`,
		},
		{
			Name: "GLM51_ContentBlockDelta_InputJsonDelta_Empty",
			JSON: `{"delta":{"partial_json":"","type":"input_json_delta"},"type":"content_block_delta","index":1}`,
		},
		{
			Name: "GLM51_ContentBlockDelta_InputJsonDelta_Partial",
			JSON: `{"delta":{"partial_json":"{\"location\": \"Beijing\"}","type":"input_json_delta"},"type":"content_block_delta","index":1}`,
		},
		{
			Name: "GLM51_MessageDelta",
			JSON: `{"delta":{"stop_reason":"tool_use"},"type":"message_delta","usage":{"output_tokens":39,"cache_creation_input_tokens":0,"input_tokens":175,"cache_read_input_tokens":0,"prompt_tokens_details":{"cached_tokens":0}}}`,
		},
		{
			Name: "GLM51_MessageStop",
			JSON: `{"type":"message_stop"}`,
		},
		{
			Name: "ClaudeSonnet46_MessageStart",
			JSON: `{"type":"message_start","message":{"model":"mcs-5","id":"msg_bdrk_01AegxDeE11veXgd878SUjFX","type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"stop_details":null,"usage":{"input_tokens":680,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":5}}}`,
		},
		{
			Name: "ClaudeSonnet46_ContentBlockStart_ToolUse",
			JSON: `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_bdrk_0193mV9uwy5TFF8p8sTGi3hC","name":"get_weather","input":{}}}`,
		},
		{
			Name: "ClaudeSonnet46_ContentBlockDelta_InputJsonDelta_Empty",
			JSON: `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`,
		},
		{
			Name: "ClaudeSonnet46_ContentBlockDelta_InputJsonDelta_Partial",
			JSON: `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"lo"}}`,
		},
		{
			Name: "ClaudeSonnet46_ContentBlockStop",
			JSON: `{"type":"content_block_stop","index":0}`,
		},
		{
			Name: "ClaudeSonnet46_MessageDelta",
			JSON: `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null,"stop_details":null},"usage":{"input_tokens":680,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":38}}`,
		},
		{
			Name: "ClaudeSonnet46_MessageStop",
			JSON: `{"type":"message_stop","amazon-bedrock-invocationMetrics":{"inputTokenCount":680,"outputTokenCount":38,"invocationLatency":1307,"firstByteLatency":1100}}`,
		},
		{
			Name: "NullStringFields",
			JSON: `{"type":null,"index":0}`,
		},
		{
			Name: "ErrorEvent",
			JSON: `{"type":"error","error":{"type":"invalid_request_error","message":"Invalid request"}}`,
		},
		{
			Name: "ErrorEventNullFields",
			JSON: `{"type":"error","error":{"type":null,"message":null}}`,
		},
		{
			Name: "NullMessage",
			JSON: `{"type":"message_start","message":null}`,
		},
		{
			Name: "NullDelta",
			JSON: `{"type":"content_block_delta","index":0,"delta":null}`,
		},
		{
			Name: "NullUsage",
			JSON: `{"type":"message_delta","usage":null}`,
		},
		{
			Name: "NullError",
			JSON: `{"type":"error","error":null}`,
		},
		{
			Name: "NullIndex",
			JSON: `{"type":"content_block_start","index":null}`,
		},
		{
			Name: "DeltaWithAllContentBlockFields",
			JSON: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello","partial_json":"{}","thinking":"think","signature":"sig"}}`,
		},
		{
			Name: "DeltaWithNullStringFields",
			JSON: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":null,"partial_json":null,"thinking":null,"signature":null}}`,
		},
		{
			Name: "DeltaMessageDeltaWithStopSequence",
			JSON: `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":"END"}}`,
		},
		{
			Name: "DeltaMessageDeltaWithNullFields",
			JSON: `{"type":"message_delta","delta":{"stop_reason":null,"stop_sequence":null}}`,
		},
		{
			Name: "UnknownFieldsIgnored",
			JSON: `{"type":"ping","extra":"field","nested":{"a":1}}`,
		},
		{
			Name: "ContentBlockStartImage",
			JSON: `{"type":"content_block_start","index":0,"content_block":{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR="}}}`,
		},
		{
			Name: "ContentBlockStartDocument",
			JSON: `{"type":"content_block_start","index":0,"content_block":{"type":"document","source":{"type":"url","url":"https://example.com/doc.pdf"},"title":"doc.pdf","context":"A doc","citations":{"enabled":true}}}`,
		},
		{
			Name: "ContentBlockStartThinking",
			JSON: `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"think text","signature":"sig_abc"}}`,
		},
		{
			Name: "ContentBlockStartRedactedThinking",
			JSON: `{"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"base64data"}}`,
		},
		{
			Name: "ContentBlockStartToolResult",
			JSON: `{"type":"content_block_start","index":0,"content_block":{"type":"tool_result","tool_use_id":"toolu_1","content":"result text"}}`,
		},
		{
			Name: "ContentBlockStartGeneralBlock",
			JSON: `{"type":"content_block_start","index":0,"content_block":{"type":"unknown_type","extra":"field"}}`,
		},
		{
			Name: "NullContentBlock",
			JSON: `{"type":"content_block_start","index":0,"content_block":null}`,
		},
		{
			Name: "UsageWithAllFields",
			JSON: `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":10,"cache_read_input_tokens":5}}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			jsonBytes := []byte(strings.TrimSpace(tc.JSON))

			var stdEvent ClaudeMessagesStreamEvent
			err := json.Unmarshal(jsonBytes, &stdEvent)
			require.NoError(t, err, "standard json.Unmarshal should not error")

			in := jlexer.Lexer{Data: jsonBytes}
			jEvent := &ClaudeMessagesStreamEvent{}
			jEvent.ParseJLexer(&in)
			require.NoError(t, in.Error(), "jlexer parser should not error")
			assert.Equal(t, &stdEvent, jEvent, "jlexer parser output should match standard json.Unmarshal")
		})
	}
}
