package ruleengine

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/expr-lang/expr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/exprenv"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
)

func parseResponsesRequest(t *testing.T, body string) *openai.ResponsesRequest {
	t.Helper()
	var req openai.ResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))
	return &req
}

// TestReplayResponsesMessages covers the structure decisions the replay has to reproduce.
// The expectations are written by hand from the rules in convertRequestBody
// (pkg/engines/converter/responses.go); nothing cross-checks them against the real converter,
// so they need rereading whenever it changes.
func TestReplayResponsesMessages(t *testing.T) {
	testCases := []struct {
		name     string
		body     string
		expected []replayedMessage
	}{
		{
			name:     "Empty",
			body:     `{"model":"gpt-4o"}`,
			expected: nil,
		},
		{
			name:     "InstructionsBecomeTheFirstMessage",
			body:     `{"instructions":"sys","input":"hi"}`,
			expected: []replayedMessage{{contentText: "sys"}, {contentText: "hi"}},
		},
		{
			name:     "BlankInstructionsAreNotAMessage",
			body:     `{"instructions":"","input":"hi"}`,
			expected: []replayedMessage{{contentText: "hi"}},
		},
		{
			name:     "StringContent",
			body:     `{"input":[{"role":"user","content":"hello"}]}`,
			expected: []replayedMessage{{contentText: "hello"}},
		},
		{
			name:     "TextPartsAreConcatenated",
			body:     `{"input":[{"role":"user","content":[{"type":"input_text","text":"one"},{"type":"input_text","text":"two"}]}]}`,
			expected: []replayedMessage{{contentText: "onetwo"}},
		},
		{
			name:     "ImagePartRendersLikeChat",
			body:     `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.com/a.png"}]}]}`,
			expected: []replayedMessage{{contentText: "[img:https://example.com/a.png]"}},
		},
		{
			name:     "MessageWithNoConvertiblePartsIsDropped",
			body:     `{"input":[{"role":"user","content":[{"type":"input_file","file_url":"u"}]},{"role":"user","content":"after"}]}`,
			expected: []replayedMessage{{contentText: "after"}},
		},
		{
			name:     "ImagePartWithoutURLIsNotConvertible",
			body:     `{"input":[{"role":"user","content":[{"type":"input_image"}]},{"role":"user","content":"after"}]}`,
			expected: []replayedMessage{{contentText: "after"}},
		},
		{
			name:     "EmptyContentArrayIsDropped",
			body:     `{"input":[{"role":"user","content":[]},{"role":"user","content":"after"}]}`,
			expected: []replayedMessage{{contentText: "after"}},
		},
		{
			name:     "EmptyTextPartKeepsTheMessage",
			body:     `{"input":[{"role":"user","content":[{"type":"input_text","text":""}]},{"role":"user","content":"after"}]}`,
			expected: []replayedMessage{{contentText: ""}, {contentText: "after"}},
		},
		{
			name: "ConsecutiveFunctionCallsMergeIntoOne",
			body: `{"input":[
				{"type":"function_call","call_id":"c1","arguments":"{\"x\":1}"},
				{"type":"function_call","call_id":"c2","arguments":"{\"y\":2}"},
				{"type":"function_call","call_id":"c3","arguments":"{\"z\":3}"}
			]}`,
			expected: []replayedMessage{{firstToolCallArgs: `{"x":1}`}},
		},
		{
			name: "AMessageBreaksTheFunctionCallRun",
			body: `{"input":[
				{"type":"function_call","call_id":"c1","arguments":"{\"x\":1}"},
				{"role":"user","content":"middle"},
				{"type":"function_call","call_id":"c2","arguments":"{\"y\":2}"}
			]}`,
			expected: []replayedMessage{
				{firstToolCallArgs: `{"x":1}`},
				{contentText: "middle"},
				{firstToolCallArgs: `{"y":2}`},
			},
		},
		{
			name: "AFunctionCallOutputBreaksTheRun",
			body: `{"input":[
				{"type":"function_call","call_id":"c1","arguments":"{\"x\":1}"},
				{"type":"function_call_output","call_id":"c1","output":"done"},
				{"type":"function_call","call_id":"c2","arguments":"{\"y\":2}"}
			]}`,
			expected: []replayedMessage{
				{firstToolCallArgs: `{"x":1}`},
				{contentText: "done"},
				{firstToolCallArgs: `{"y":2}`},
			},
		},
		{
			name: "ReasoningAndUnknownItemsAreDropped",
			body: `{"input":[
				{"role":"user","content":"q"},
				{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"thinking"}]},
				{"type":"web_search_call","id":"ws_1"},
				{"role":"assistant","content":"a"}
			]}`,
			expected: []replayedMessage{{contentText: "q"}, {contentText: "a"}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := replayResponsesMessages(parseResponsesRequest(t, tc.body))
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestReplayResponsesMessages_NilRequest(t *testing.T) {
	assert.Nil(t, replayResponsesMessages(nil))
}

// TestResponsesShardKeyExpr exercises the path a cache-aware load-balancing shard key takes:
// the expression is compiled against exprenv.Sentinel and run on the live request, and the
// caller requires a []string back. Before Responses support this produced nil, silently
// dropping the shard key.
func TestResponsesShardKeyExpr(t *testing.T) {
	exprenv.RegisterDefaultExtractor("message5HashArrayV2", &MessageNHashArrayV2Extractor{N: 5})
	t.Cleanup(func() { exprenv.UnregisterDefaultExtractor("message5HashArrayV2") })

	prog, err := expr.Compile(`req.Feature("message5HashArrayV2")`, expr.Env(exprenv.Sentinel))
	require.NoError(t, err)

	httpReq, err := http.NewRequest("POST", "http://localhost/v1/responses", nil)
	require.NoError(t, err)
	req := octollm.NewRequest(httpReq, octollm.APIFormatResponses)
	req.Body = octollm.NewBodyFromBytes(
		[]byte(`{"model":"gpt-4o","instructions":"sys","input":[{"role":"user","content":"hello"}]}`),
		&octollm.JSONParser[openai.ResponsesRequest]{},
	)

	output, err := expr.Run(prog, exprenv.Get(req))
	require.NoError(t, err)

	keys, ok := output.([]string)
	require.True(t, ok, "shard key expr must yield []string, got %T", output)
	require.Len(t, keys, 5)
	assert.NotEmpty(t, keys[0])
	assert.NotEmpty(t, keys[1])
	assert.Equal(t, []string{"", "", ""}, keys[2:])
}
