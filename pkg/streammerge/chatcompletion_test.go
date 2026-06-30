package streammerge

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
)

// runChatMerge feeds the raw chat.completion.chunk SSE payloads through the
// merger (parsed exactly as the client emits them) and asserts the assembled
// non-stream response equals expectedJSON.
func runChatMerge(t *testing.T, chunks []string, expectedJSON string) {
	t.Helper()
	m := NewChatCompletionMerger()
	for _, c := range chunks {
		body := octollm.NewBodyFromBytes([]byte(c), &octollm.JSONParser[openai.ChatCompletionStreamChunk]{})
		if err := m.Merge(&octollm.StreamChunk{Body: body}); err != nil {
			t.Fatalf("Merge(%s): %v", c, err)
		}
	}
	merged, err := m.Merged()
	require.NoError(t, err)
	got, err := merged.Bytes()
	require.NoError(t, err)
	require.JSONEq(t, expectedJSON, string(got))
}

// Stream fixtures mirror the openaiRespJSON inputs in
// pkg/engines/converter/claude_test.go, so the merger is exercised against the
// same real upstream payloads the protocol converter handles.

func TestChatCompletionMerger_SimpleText(t *testing.T) {
	runChatMerge(t, []string{
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"I'm","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" Kim","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"i","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":",","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" a","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" large","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" language","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" model","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" trained","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" by","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" Moon","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"shot","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":" AI","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":".","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":null},"logprobs":null,"finish_reason":"stop","matched_stop":163586}]}`,
		`{"id":"c91dda37dc3c4018ac0fecf81cf0052d","object":"chat.completion.chunk","created":1765734075,"model":"Kimi-K2-Instruct","choices":[],"usage":{"prompt_tokens":31,"total_tokens":46,"completion_tokens":15,"prompt_tokens_details":null,"reasoning_tokens":0}}`,
		`[DONE]`,
	}, `{"id":"c91dda37dc3c4018ac0fecf81cf0052d","created":1765734075,"object":"chat.completion","model":"Kimi-K2-Instruct","usage":{"completion_tokens":15,"prompt_tokens":31,"total_tokens":46},"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"I'm Kimi, a large language model trained by Moonshot AI."}}]}`)
}

func TestChatCompletionMerger_ToolCall(t *testing.T) {
	runChatMerge(t, []string{
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"我来","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"帮你","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"查","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"一下","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"。","tool_calls":null},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":"functions.search:0","index":0,"type":"function","function":{"name":"search","arguments":""}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":0,"type":"function","function":{"name":null,"arguments":"{\"queries"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":0,"type":"function","function":{"name":null,"arguments":"\":"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124074,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":0,"type":"function","function":{"name":null,"arguments":"1"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124075,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":[{"id":null,"index":0,"type":"function","function":{"name":null,"arguments":"}"}}]},"logprobs":null,"finish_reason":null,"matched_stop":null}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124076,"model":"Kimi-K2-Instruct","choices":[{"index":0,"delta":{"content":"","tool_calls":null},"logprobs":null,"finish_reason":"tool_calls","matched_stop":163586}]}`,
		`{"id":"1480f67c3ad545f580a0742339bb391b","object":"chat.completion.chunk","created":1756124076,"model":"Kimi-K2-Instruct","choices":[],"usage":{"prompt_tokens":177,"total_tokens":246,"completion_tokens":69,"prompt_tokens_details":null}}`,
		`[DONE]`,
	}, `{"id":"1480f67c3ad545f580a0742339bb391b","created":1756124076,"object":"chat.completion","model":"Kimi-K2-Instruct","usage":{"completion_tokens":69,"prompt_tokens":177,"total_tokens":246},"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"我来帮你查一下。","tool_calls":[{"id":"functions.search:0","index":0,"type":"function","function":{"name":"search","arguments":"{\"queries\":1}"}}]}}]}`)
}

func TestChatCompletionMerger_Reasoning(t *testing.T) {
	runChatMerge(t, []string{
		`{"id":"2026021010253871cb1b6ec9e54d6b","created":1770690338,"object":"chat.completion.chunk","model":"glm-4.6","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"\n"}}]}`,
		`{"id":"2026021010253871cb1b6ec9e54d6b","created":1770690338,"object":"chat.completion.chunk","model":"glm-4.6","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"1"}}]}`,
		`{"id":"2026021010253871cb1b6ec9e54d6b","created":1770690338,"object":"chat.completion.chunk","model":"glm-4.6","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"."}}]}`,
		`{"id":"2026021010253871cb1b6ec9e54d6b","created":1770690338,"object":"chat.completion.chunk","model":"glm-4.6","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":" "}}]}`,
		`{"id":"2026021010253871cb1b6ec9e54d6b","created":1770690338,"object":"chat.completion.chunk","model":"glm-4.6","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":" **"}}]}`,
		`{"id":"2026021010253871cb1b6ec9e54d6b","created":1770690338,"object":"chat.completion.chunk","model":"glm-4.6","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"分析"}}]}`,
		`{"id":"2026021010253871cb1b6ec9e54d6b","created":1770690338,"object":"chat.completion.chunk","model":"glm-4.6","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"用户的"}}]}`,
		`{"id":"2026021010253871cb1b6ec9e54d6b","created":1770690338,"object":"chat.completion.chunk","model":"glm-4.6","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"查询"}}]}`,
		`{"id":"2026021010253871cb1b6ec9e54d6b","created":1770690338,"object":"chat.completion.chunk","model":"glm-4.6","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"："}}]}`,
		`{"id":"2026021010253871cb1b6ec9e54d6b","created":1770690338,"object":"chat.completion.chunk","model":"glm-4.6","choices":[{"index":0,"delta":{"role":"assistant","content":"\n"}}]}`,
		`{"id":"2026021010253871cb1b6ec9e54d6b","created":1770690338,"object":"chat.completion.chunk","model":"glm-4.6","choices":[{"index":0,"delta":{"role":"assistant","content":"你好"}}]}`,
		`{"id":"2026021010253871cb1b6ec9e54d6b","created":1770690338,"object":"chat.completion.chunk","model":"glm-4.6","choices":[{"index":0,"delta":{"role":"assistant","content":"！\n\n"}}]}`,
		`{"id":"2026021010253871cb1b6ec9e54d6b","created":1770690338,"object":"chat.completion.chunk","model":"glm-4.6","choices":[{"index":0,"delta":{"role":"assistant","content":"我是一个"}}]}`,
		`{"id":"2026021010253871cb1b6ec9e54d6b","created":1770690338,"object":"chat.completion.chunk","model":"glm-4.6","choices":[{"index":0,"delta":{"role":"assistant","content":"大型"}}]}`,
		`{"id":"2026021010253871cb1b6ec9e54d6b","created":1770690338,"object":"chat.completion.chunk","model":"glm-4.6","choices":[{"index":0,"delta":{"role":"assistant","content":"语言"}}]}`,
		`{"id":"2026021010253871cb1b6ec9e54d6b","created":1770690338,"object":"chat.completion.chunk","model":"glm-4.6","choices":[{"index":0,"delta":{"role":"assistant","content":"模型"}}]}`,
		`{"id":"2026021010253871cb1b6ec9e54d6b","created":1770690338,"object":"chat.completion.chunk","model":"glm-4.6","choices":[{"index":0,"finish_reason":"stop","delta":{"role":"assistant","content":""}}]}`,
		`{"id":"2026021010253871cb1b6ec9e54d6b","created":1770690338,"model":"glm-4.6","choices":[],"usage":{"completion_tokens":1024,"prompt_tokens":9,"total_tokens":1033,"prompt_tokens_details":{"cached_tokens":7}},"object":"chat.completion.chunk"}`,
		`[DONE]`,
	}, `{"id":"2026021010253871cb1b6ec9e54d6b","created":1770690338,"object":"chat.completion","model":"glm-4.6","usage":{"completion_tokens":1024,"prompt_tokens":9,"total_tokens":1033,"prompt_tokens_details":{"cached_tokens":7}},"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"\n你好！\n\n我是一个大型语言模型","reasoning_content":"\n1.  **分析用户的查询："}}]}`)
}

// TestChatCompletionMerger_UsageSplitAcrossChunks covers the corner case where
// usage is dribbled out across chunks instead of arriving whole in the final
// one: prompt_tokens_details (cached_tokens) shows up in the very first chunk,
// while the closing usage chunk carries only the token totals and a nil
// prompt_tokens_details. A whole-pointer assignment would let the final chunk
// clobber the early cached_tokens; the field-by-field merge must preserve it.
func TestChatCompletionMerger_UsageSplitAcrossChunks(t *testing.T) {
	runChatMerge(t, []string{
		`{"id":"u","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":0,"total_tokens":0,"completion_tokens":0,"prompt_tokens_details":{"cached_tokens":7}}}`,
		`{"id":"u","model":"m","choices":[{"index":0,"finish_reason":"stop","delta":{"content":""}}]}`,
		`{"id":"u","model":"m","choices":[],"usage":{"prompt_tokens":31,"total_tokens":46,"completion_tokens":15,"prompt_tokens_details":null}}`,
		`[DONE]`,
	}, `{"id":"u","created":0,"object":"chat.completion","model":"m","usage":{"completion_tokens":15,"prompt_tokens":31,"total_tokens":46,"prompt_tokens_details":{"cached_tokens":7}},"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"hi"}}]}`)
}

// TestChatCompletionMerger_MultipleChoices checks out-of-order choice indexes
// are accumulated independently and emitted sorted by index.
func TestChatCompletionMerger_MultipleChoices(t *testing.T) {
	runChatMerge(t, []string{
		`{"id":"x","model":"m","choices":[{"index":1,"delta":{"role":"assistant","content":"second"}}]}`,
		`{"id":"x","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"first"}}]}`,
		`[DONE]`,
	}, `{"id":"x","created":0,"object":"chat.completion","model":"m","usage":null,"choices":[{"index":0,"finish_reason":"","message":{"role":"assistant","content":"first"}},{"index":1,"finish_reason":"","message":{"role":"assistant","content":"second"}}]}`)
}
