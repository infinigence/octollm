package streammerge

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
)

// runResponsesMerge feeds the raw Responses API SSE events through the merger
// (parsed as the client emits them) and asserts the assembled non-stream
// response equals expectedJSON.
func runResponsesMerge(t *testing.T, events []string, expectedJSON string) {
	t.Helper()
	m := NewResponsesMerger()
	for _, e := range events {
		body := octollm.NewBodyFromBytes([]byte(e), &octollm.JSONParser[openai.ResponseStreamChunk]{})
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

// Event fixtures mirror the expectedResponsesChunks streams from
// pkg/engines/converter/responses_test.go: the Responses SSE sequences the
// merger must fold back into a single response.

func TestResponsesMerger_SimpleText(t *testing.T) {
	runResponsesMerge(t, []string{
		`{"type":"response.created","response":{"id":"c1","object":"response","status":"in_progress","model":"gpt-4o","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_0","type":"message","role":"assistant"}}`,
		`{"type":"response.content_part.added","output_index":0,"content_index":0,"part":{"type":"output_text"}}`,
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"Hello"}`,
		`{"type":"response.output_text.done","output_index":0,"content_index":0,"text":"Hello"}`,
		`{"type":"response.content_part.done","output_index":0,"content_index":0}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_0","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}}`,
		`{"type":"response.completed","response":{"id":"c1","object":"response","status":"completed","model":"gpt-4o","output":[{"id":"msg_0","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11}}}`,
	}, `{"id":"c1","object":"response","status":"completed","model":"gpt-4o","output":[{"id":"msg_0","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11}}`)
}

func TestResponsesMerger_ToolCall(t *testing.T) {
	runResponsesMerge(t, []string{
		`{"type":"response.created","response":{"id":"c2","object":"response","status":"in_progress","model":"gpt-4o","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_0","type":"function_call","call_id":"call_1","name":"get_weather"}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_0","output_index":0,"delta":"{\"city\":"}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_0","output_index":0,"delta":"\"Tokyo\"}"}`,
		`{"type":"response.function_call_arguments.done","item_id":"fc_0","output_index":0,"arguments":"{\"city\":\"Tokyo\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"fc_0","type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Tokyo\"}"}}`,
		`{"type":"response.completed","response":{"id":"c2","object":"response","status":"completed","model":"gpt-4o","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`,
	}, `{"id":"c2","object":"response","status":"completed","model":"gpt-4o","output":[{"id":"fc_0","type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Tokyo\"}"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`)
}

func TestResponsesMerger_ReasoningThenText(t *testing.T) {
	runResponsesMerge(t, []string{
		`{"type":"response.created","response":{"id":"c3","object":"response","status":"in_progress","model":"o3","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_0","type":"reasoning"}}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_0","output_index":0,"delta":"Think"}`,
		`{"type":"response.reasoning_summary_text.done","item_id":"rs_0","output_index":0,"text":"Think"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_0","type":"reasoning","summary":[{"type":"summary_text","text":"Think"}]}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant"}}`,
		`{"type":"response.content_part.added","output_index":1,"content_index":0,"part":{"type":"output_text"}}`,
		`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"42"}`,
		`{"type":"response.output_text.done","output_index":1,"content_index":0,"text":"42"}`,
		`{"type":"response.content_part.done","output_index":1,"content_index":0}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"42"}]}}`,
		`{"type":"response.completed","response":{"id":"c3","object":"response","status":"completed","model":"o3","usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8,"output_tokens_details":{"reasoning_tokens":2}}}}`,
	}, `{"id":"c3","object":"response","status":"completed","model":"o3","output":[{"id":"rs_0","type":"reasoning","summary":[{"type":"summary_text","text":"Think"}]},{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"42"}]}],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8,"output_tokens_details":{"reasoning_tokens":2}}}`)
}

// A gateway that streams deltas but never emits output_item.done must still
// merge, using the accumulated fragments instead of the finished item.
func TestResponsesMerger_TextWithoutOutputItemDone(t *testing.T) {
	runResponsesMerge(t, []string{
		`{"type":"response.created","response":{"id":"c4","object":"response","status":"in_progress","model":"gpt-4o"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_0","type":"message","role":"assistant"}}`,
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"Hel"}`,
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"lo"}`,
		`{"type":"response.completed","response":{"id":"c4","object":"response","status":"completed","model":"gpt-4o","usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}}}`,
	}, `{"id":"c4","object":"response","status":"completed","model":"gpt-4o","output":[{"id":"msg_0","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}}`)
}

func TestResponsesMerger_ToolCallWithoutOutputItemDone(t *testing.T) {
	runResponsesMerge(t, []string{
		`{"type":"response.created","response":{"id":"c5","object":"response","status":"in_progress","model":"gpt-4o"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_0","type":"function_call","call_id":"call_1","name":"get_weather"}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_0","output_index":0,"delta":"{\"city\":"}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_0","output_index":0,"delta":"\"Tokyo\"}"}`,
		`{"type":"response.completed","response":{"id":"c5","object":"response","status":"completed","model":"gpt-4o"}}`,
	}, `{"id":"c5","object":"response","status":"completed","model":"gpt-4o","output":[{"id":"fc_0","type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Tokyo\"}"}]}`)
}

// Gateways that skip the deltas and only deliver the whole value on the done
// event must merge to the same result.
func TestResponsesMerger_DoneEventsWithoutDeltas(t *testing.T) {
	runResponsesMerge(t, []string{
		`{"type":"response.created","response":{"id":"c6","object":"response","status":"in_progress","model":"gpt-4o"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_0","type":"message","role":"assistant"}}`,
		`{"type":"response.output_text.done","output_index":0,"content_index":0,"text":"Hello"}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"ping"}}`,
		`{"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":1,"arguments":"{}"}`,
		`{"type":"response.completed","response":{"id":"c6","object":"response","status":"completed","model":"gpt-4o"}}`,
	}, `{"id":"c6","object":"response","status":"completed","model":"gpt-4o","output":[{"id":"msg_0","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]},{"id":"fc_1","type":"function_call","call_id":"call_1","name":"ping","arguments":"{}"}]}`)
}

// Events that identify their item only by item_id (no output_index) must land on
// the item the added event registered.
func TestResponsesMerger_ResolvesItemByItemID(t *testing.T) {
	runResponsesMerge(t, []string{
		`{"type":"response.created","response":{"id":"c7","object":"response","status":"in_progress","model":"gpt-4o"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_0","type":"function_call","call_id":"call_1","name":"get_weather"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_2","name":"get_time"}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"tz\":\"JST\"}"}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_0","delta":"{\"city\":\"Tokyo\"}"}`,
		`{"type":"response.completed","response":{"id":"c7","object":"response","status":"completed","model":"gpt-4o"}}`,
	}, `{"id":"c7","object":"response","status":"completed","model":"gpt-4o","output":[{"id":"fc_0","type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Tokyo\"}"},{"id":"fc_1","type":"function_call","call_id":"call_2","name":"get_time","arguments":"{\"tz\":\"JST\"}"}]}`)
}

func TestResponsesMerger_Refusal(t *testing.T) {
	runResponsesMerge(t, []string{
		`{"type":"response.created","response":{"id":"c8","object":"response","status":"in_progress","model":"gpt-4o"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_0","type":"message","role":"assistant"}}`,
		`{"type":"response.content_part.added","output_index":0,"content_index":0,"part":{"type":"refusal"}}`,
		`{"type":"response.refusal.delta","output_index":0,"content_index":0,"delta":"I can't"}`,
		`{"type":"response.refusal.delta","output_index":0,"content_index":0,"delta":" help."}`,
		`{"type":"response.completed","response":{"id":"c8","object":"response","status":"completed","model":"gpt-4o"}}`,
	}, `{"id":"c8","object":"response","status":"completed","model":"gpt-4o","output":[{"id":"msg_0","type":"message","role":"assistant","content":[{"type":"refusal","refusal":"I can't help."}]}]}`)
}

// The zeroed usage on response.created must not clobber the real counts that
// arrive on the terminal event.
func TestResponsesMerger_UsageSplitAcrossEvents(t *testing.T) {
	runResponsesMerge(t, []string{
		`{"type":"response.created","response":{"id":"c9","object":"response","status":"in_progress","model":"gpt-4o","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`,
		`{"type":"response.in_progress","response":{"usage":{"input_tokens":12,"output_tokens":0,"total_tokens":0,"input_tokens_details":{"cached_tokens":8,"cache_write_tokens":2}}}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_0","type":"message","role":"assistant"}}`,
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"ok"}`,
		`{"type":"response.completed","response":{"id":"c9","object":"response","status":"completed","model":"gpt-4o","usage":{"output_tokens":3,"total_tokens":15,"output_tokens_details":{"reasoning_tokens":1}}}}`,
	}, `{"id":"c9","object":"response","status":"completed","model":"gpt-4o","output":[{"id":"msg_0","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15,"input_tokens_details":{"cached_tokens":8,"cache_write_tokens":2},"output_tokens_details":{"reasoning_tokens":1}}}`)
}

// A stream that carries no per-item events at all still yields the output the
// terminal snapshot reported.
func TestResponsesMerger_SnapshotOnlyOutput(t *testing.T) {
	runResponsesMerge(t, []string{
		`{"type":"response.created","response":{"id":"c10","object":"response","status":"in_progress","model":"gpt-4o"}}`,
		`{"type":"response.completed","response":{"id":"c10","object":"response","status":"completed","model":"gpt-4o","output":[{"id":"msg_0","type":"message","role":"assistant","content":[{"type":"output_text","text":"snapshot"}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}`,
	}, `{"id":"c10","object":"response","status":"completed","model":"gpt-4o","output":[{"id":"msg_0","type":"message","role":"assistant","content":[{"type":"output_text","text":"snapshot"}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`)
}

// A terminal failure must preserve the upstream status and error envelope rather
// than merging into a silently successful empty response.
func TestResponsesMerger_FailedStatusPreservesError(t *testing.T) {
	runResponsesMerge(t, []string{
		`{"type":"response.created","response":{"id":"c11","object":"response","status":"in_progress","model":"gpt-4o"}}`,
		`{"type":"response.failed","response":{"id":"c11","object":"response","status":"failed","model":"gpt-4o","error":{"type":"server_error","message":"backend exploded"}}}`,
	}, `{"id":"c11","object":"response","status":"failed","model":"gpt-4o","error":{"type":"server_error","message":"backend exploded"}}`)
}

func TestResponsesMerger_IncompleteStatus(t *testing.T) {
	runResponsesMerge(t, []string{
		`{"type":"response.created","response":{"id":"c12","object":"response","status":"in_progress","model":"gpt-4o"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_0","type":"message","role":"assistant"}}`,
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"partial"}`,
		`{"type":"response.incomplete","response":{"id":"c12","object":"response","status":"incomplete","model":"gpt-4o","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":10,"output_tokens":7,"total_tokens":17}}}`,
	}, `{"id":"c12","object":"response","status":"incomplete","model":"gpt-4o","incomplete_details":{"reason":"max_output_tokens"},"output":[{"id":"msg_0","type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}],"usage":{"input_tokens":10,"output_tokens":7,"total_tokens":17}}`)
}

// The [DONE] terminator is consumed silently, like the other mergers.
func TestResponsesMerger_StreamDoneTerminator(t *testing.T) {
	runResponsesMerge(t, []string{
		`{"type":"response.created","response":{"id":"c13","object":"response","status":"in_progress","model":"gpt-4o"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_0","type":"message","role":"assistant"}}`,
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hi"}`,
		`{"type":"response.completed","response":{"id":"c13","object":"response","status":"completed","model":"gpt-4o"}}`,
		`[DONE]`,
	}, `{"id":"c13","object":"response","status":"completed","model":"gpt-4o","output":[{"id":"msg_0","type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`)
}

func TestFor_Responses(t *testing.T) {
	m, err := For(octollm.APIFormatResponses)
	require.NoError(t, err)
	require.NotNil(t, m)
}
