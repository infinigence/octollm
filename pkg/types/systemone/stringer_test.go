package systemone

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSystemOneRequest_String(t *testing.T) {
	testCases := []struct {
		Name     string
		JSON     string
		Expected string
	}{
		{
			Name: "QuestionsSortedByName",
			JSON: `{"model":"jev-latest","state":{"content":"payment not received"},"questions":{` +
				`"severity":{"type":"score","instructions":"How severe?","criteria":["minor","major"]},` +
				`"escalate":{"type":"noul","instructions":"Notify on-call?","criteria":{"true":"outage"}},` +
				`"department":{"type":"choice","instructions":"Which team?","criteria":{"technical":null,"billing":"payments"}},` +
				`"mug":{"type":"box","instructions":"the mug"},` +
				`"x":{"type":"rank"}}}`,
			Expected: `(SystemOneRequest) {
  Model: "jev-latest"
  State: object(len=34)
  Questions: len(5)
    {name="department", type=choice, instructions=len(11), criteria=["billing", "technical"]}
    {name="escalate", type=noul, instructions=len(15), criteria={true=len(6), false=<nil>}}
    {name="mug", type=box, instructions=len(7)}
    {name="severity", type=score, instructions=len(11), criteria=len(2)}
    {name="x", type=rank}
}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			var req SystemOneRequest
			require.NoError(t, json.Unmarshal([]byte(tc.JSON), &req))
			assert.Equal(t, tc.Expected, req.String())
		})
	}
}

func TestSystemOneResponse_String(t *testing.T) {
	testCases := []struct {
		Name     string
		JSON     string
		Expected string
	}{
		{
			Name: "AllAnswerTypes",
			JSON: `{"model":"decision-model-preview","request_id":"req-1","answers":{` +
				`"severity":{"type":"score","score":2.25,"legend":{"0":"minor","1":"major"},"probabilities":{"1":0.73,"0":0.27},"confidence":0.91},` +
				`"escalate":{"type":"noul","noul":0.99},` +
				`"department":{"type":"choice","choice":"billing","confidence":0.88,"probabilities":{"technical":0.06,"billing":0.94}}},` +
				`"usage":{"input_tokens":125,"output_tokens":20}}`,
			Expected: `(SystemOneResponse) {
  RequestID: "req-1"
  Model: "decision-model-preview"
  Answers: len(3)
    {name="department", type=choice, choice="billing", confidence=0.88, probabilities={"billing": 0.94, "technical": 0.06}}
    {name="escalate", type=noul, noul=0.99}
    {name="severity", type=score, score=2.25, confidence=0.91, probabilities={"0": 0.27, "1": 0.73}, legend=object(len=25)}
  Usage: input=125, output=20
}`,
		},
		{
			Name: "MissingOutputTokens",
			JSON: `{"id":"gen-1","model":"jev-1.13.0","answers":{"is_urgent":{"type":"noul","noul":0.95}},"usage":{"input_tokens":296}}`,
			Expected: `(SystemOneResponse) {
  ID: "gen-1"
  Model: "jev-1.13.0"
  Answers: len(1)
    {name="is_urgent", type=noul, noul=0.95}
  Usage: input=296
}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			var resp SystemOneResponse
			require.NoError(t, json.Unmarshal([]byte(tc.JSON), &resp))
			assert.Equal(t, tc.Expected, resp.String())
		})
	}
}
