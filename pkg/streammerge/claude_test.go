package streammerge

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/anthropic"
)

// runClaudeMerge feeds the raw Claude Messages SSE events through the merger
// (parsed as the client emits them) and asserts the assembled non-stream
// response equals expectedJSON.
func runClaudeMerge(t *testing.T, events []string, expectedJSON string) {
	t.Helper()
	m := NewClaudeMessagesMerger()
	for _, e := range events {
		body := octollm.NewBodyFromBytes([]byte(e), &octollm.JSONParser[anthropic.ClaudeMessagesStreamEvent]{})
		if err := m.Merge(&octollm.StreamChunk{Body: body}); err != nil {
			t.Fatalf("Merge(%s): %v", e, err)
		}
	}
	merged, err := m.Merged()
	require.NoError(t, err)
	got, err := merged.Bytes()
	require.NoError(t, err)
	require.JSONEq(t, expectedJSON, string(got))
}

// Event fixtures are the expectedClaudeRespJSON streams from
// pkg/engines/converter/claude_test.go: the Claude Messages SSE sequences the
// merger must fold back into a single response.

func TestClaudeMerger_SimpleText(t *testing.T) {
	runClaudeMerge(t, []string{
		`{"type":"message_start","message":{"id":"c91dda37dc3c4018ac0fecf81cf0052d","type":"message","role":"assistant","content":[],"model":"Kimi-K2-Instruct","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I'm"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" Kim"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"i"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":","}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" a"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" large"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" language"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" model"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" trained"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" by"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" Moon"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"shot"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" AI"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":31,"output_tokens":15}}`,
		`{"type":"message_stop"}`,
	}, `{"id":"c91dda37dc3c4018ac0fecf81cf0052d","type":"message","role":"assistant","model":"Kimi-K2-Instruct","stop_reason":"end_turn","usage":{"input_tokens":31,"output_tokens":15},"content":[{"type":"text","text":"I'm Kimi, a large language model trained by Moonshot AI."}]}`)
}

func TestClaudeMerger_ToolCall(t *testing.T) {
	runClaudeMerge(t, []string{
		`{"type":"message_start","message":{"id":"1480f67c3ad545f580a0742339bb391b","type":"message","role":"assistant","content":[],"model":"Kimi-K2-Instruct","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"我来"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"帮你"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"查"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"一下"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"。"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"functions.search:0","name":"search","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"queries"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\":"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"1"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":177,"output_tokens":69}}`,
		`{"type":"message_stop"}`,
	}, `{"id":"1480f67c3ad545f580a0742339bb391b","type":"message","role":"assistant","model":"Kimi-K2-Instruct","stop_reason":"tool_use","usage":{"input_tokens":177,"output_tokens":69},"content":[{"type":"text","text":"我来帮你查一下。"},{"type":"tool_use","id":"functions.search:0","name":"search","input":{"queries":1}}]}`)
}

func TestClaudeMerger_MultipleToolCall(t *testing.T) {
	runClaudeMerge(t, []string{
		`{"type":"message_start","message":{"id":"1480f67c3ad545f580a0742339bb391b","type":"message","role":"assistant","content":[],"model":"Kimi-K2-Instruct","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"我来"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"帮你"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"查"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"一下"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"。"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"functions.search:0","name":"search","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"queries"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\":"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"1"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"functions.fetch:1","name":"fetch","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"queries"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\":"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"2"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":177,"output_tokens":69}}`,
		`{"type":"message_stop"}`,
	}, `{"id":"1480f67c3ad545f580a0742339bb391b","type":"message","role":"assistant","model":"Kimi-K2-Instruct","stop_reason":"tool_use","usage":{"input_tokens":177,"output_tokens":69},"content":[{"type":"text","text":"我来帮你查一下。"},{"type":"tool_use","id":"functions.search:0","name":"search","input":{"queries":1}},{"type":"tool_use","id":"functions.fetch:1","name":"fetch","input":{"queries":2}}]}`)
}

// TestClaudeMerger_Sandwich interleaves tool_use, text, tool_use blocks to
// confirm block order is preserved by index.
func TestClaudeMerger_Sandwich(t *testing.T) {
	runClaudeMerge(t, []string{
		`{"type":"message_start","message":{"id":"1480f67c3ad545f580a0742339bb391b","type":"message","role":"assistant","content":[],"model":"Kimi-K2-Instruct","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"functions.search:0","name":"search","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"queries"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\":"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"1"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"我来"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"帮你"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"查"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"一下"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"。"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"functions.fetch:1","name":"fetch","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"queries"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\":"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"2"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":177,"output_tokens":69}}`,
		`{"type":"message_stop"}`,
	}, `{"id":"1480f67c3ad545f580a0742339bb391b","type":"message","role":"assistant","model":"Kimi-K2-Instruct","stop_reason":"tool_use","usage":{"input_tokens":177,"output_tokens":69},"content":[{"type":"tool_use","id":"functions.search:0","name":"search","input":{"queries":1}},{"type":"text","text":"我来帮你查一下。"},{"type":"tool_use","id":"functions.fetch:1","name":"fetch","input":{"queries":2}}]}`)
}

func TestClaudeMerger_Reasoning(t *testing.T) {
	runClaudeMerge(t, []string{
		`{"type": "message_start", "message": {"id": "2026021010253871cb1b6ec9e54d6b", "type": "message", "role": "assistant", "model": "glm-4.6", "content": [], "usage": {"input_tokens": 0, "output_tokens": 0}}}`,
		`{"type": "content_block_start", "index": 0, "content_block": {"type": "thinking", "thinking": "", "signature": ""}}`,
		`{"type": "content_block_delta", "index": 0, "delta": {"type": "thinking_delta", "thinking": "\n"}}`,
		`{"type": "content_block_delta", "index": 0, "delta": {"type": "thinking_delta", "thinking": "1"}}`,
		`{"type": "content_block_delta", "index": 0, "delta": {"type": "thinking_delta", "thinking": "."}}`,
		`{"type": "content_block_delta", "index": 0, "delta": {"type": "thinking_delta", "thinking": " "}}`,
		`{"type": "content_block_delta", "index": 0, "delta": {"type": "thinking_delta", "thinking": " **"}}`,
		`{"type": "content_block_delta", "index": 0, "delta": {"type": "thinking_delta", "thinking": "分析"}}`,
		`{"type": "content_block_delta", "index": 0, "delta": {"type": "thinking_delta", "thinking": "用户的"}}`,
		`{"type": "content_block_delta", "index": 0, "delta": {"type": "thinking_delta", "thinking": "查询"}}`,
		`{"type": "content_block_delta", "index": 0, "delta": {"type": "thinking_delta", "thinking": "："}}`,
		`{"type": "content_block_stop", "index": 0}`,
		`{"type": "content_block_start", "index": 1, "content_block": {"type": "text", "text": ""}}`,
		`{"type": "content_block_delta", "index": 1, "delta": {"type": "text_delta", "text": "\n"}}`,
		`{"type": "content_block_delta", "index": 1, "delta": {"type": "text_delta", "text": "你好"}}`,
		`{"type": "content_block_delta", "index": 1, "delta": {"type": "text_delta", "text": "！\n\n"}}`,
		`{"type": "content_block_delta", "index": 1, "delta": {"type": "text_delta", "text": "我是一个"}}`,
		`{"type": "content_block_delta", "index": 1, "delta": {"type": "text_delta", "text": "大型"}}`,
		`{"type": "content_block_delta", "index": 1, "delta": {"type": "text_delta", "text": "语言"}}`,
		`{"type": "content_block_delta", "index": 1, "delta": {"type": "text_delta", "text": "模型"}}`,
		`{"type": "content_block_stop", "index": 1}`,
		`{"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"input_tokens": 2, "output_tokens": 1024, "cache_read_input_tokens": 7}}`,
		`{"type": "message_stop"}`,
	}, `{"id":"2026021010253871cb1b6ec9e54d6b","type":"message","role":"assistant","model":"glm-4.6","stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1024,"cache_read_input_tokens":7},"content":[{"type":"thinking","thinking":"\n1.  **分析用户的查询：","signature":""},{"type":"text","text":"\n你好！\n\n我是一个大型语言模型"}]}`)
}

// TestClaudeMerger_EmptyToolInput verifies a tool_use that never receives an
// input_json_delta defaults to a valid empty object rather than invalid JSON.
func TestClaudeMerger_EmptyToolInput(t *testing.T) {
	runClaudeMerge(t, []string{
		`{"type":"message_start","message":{"id":"msg_3","type":"message","role":"assistant","model":"claude"}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_2","name":"noargs","input":{}}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_stop"}`,
	}, `{"id":"msg_3","type":"message","role":"assistant","model":"claude","usage":null,"content":[{"type":"tool_use","id":"toolu_2","name":"noargs","input":{}}]}`)
}

// TestClaudeMerger_UsageSplit reflects real Anthropic streaming token accounting:
// message_start carries input + cache counts (and output_tokens: 1), while
// message_delta carries only the final output_tokens. The merged usage must keep
// the input/cache counts from message_start and take output from message_delta —
// a plain replace on message_delta (as maas-gateway does) would drop them.
func TestClaudeMerger_UsageSplit(t *testing.T) {
	runClaudeMerge(t, []string{
		`{"type":"message_start","message":{"id":"msg_usage","type":"message","role":"assistant","model":"claude-sonnet-4","usage":{"input_tokens":25,"cache_creation_input_tokens":10,"cache_read_input_tokens":4,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":15}}`,
		`{"type":"message_stop"}`,
	}, `{"id":"msg_usage","type":"message","role":"assistant","model":"claude-sonnet-4","stop_reason":"end_turn","usage":{"input_tokens":25,"output_tokens":15,"cache_creation_input_tokens":10,"cache_read_input_tokens":4},"content":[{"type":"text","text":"hi"}]}`)
}
