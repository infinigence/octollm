package systemone

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPtr(i int) *int {
	return &i
}

func float64Ptr(f float64) *float64 {
	return &f
}

func stringPtr(s string) *string {
	return &s
}

func TestSystemOneRequest_Marshal_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name          string
		JSON          string
		Object        SystemOneRequest
		UnmarshalOnly bool
	}{
		{
			Name: "StringState_Noul",
			JSON: `{"model":"jev-latest","state":"Help! My payouts have been failing for 3 days.","questions":{"is_urgent":{"type":"noul","instructions":"Does this convey urgency?"}}}`,
			Object: SystemOneRequest{
				Model: "jev-latest",
				State: json.RawMessage(`"Help! My payouts have been failing for 3 days."`),
				Questions: SystemOneQuestions{
					"is_urgent": &SystemOneNoulQuestion{Type: "noul", Instructions: "Does this convey urgency?"},
				},
			},
		},
		{
			Name: "ObjectState_AllQuestionTypes",
			JSON: `{"model":"jev-latest","state":{"ticket_id":"T-1001","content":"payment not received"},"questions":{` +
				`"refund":{"type":"noul","instructions":"Is a refund requested?","criteria":{"true":"asks for money back","false":"only reports the issue"}},` +
				`"department":{"type":"choice","instructions":"Which team?","criteria":{"billing":"payments","technical":null}},` +
				`"severity":{"type":"score","instructions":"How severe?","criteria":["minor","major","critical"]},` +
				`"mug":{"type":"box","instructions":"the coffee mug in photo"}}}`,
			Object: SystemOneRequest{
				Model: "jev-latest",
				State: json.RawMessage(`{"ticket_id":"T-1001","content":"payment not received"}`),
				Questions: SystemOneQuestions{
					"refund": &SystemOneNoulQuestion{
						Type:         "noul",
						Instructions: "Is a refund requested?",
						Criteria:     &SystemOneNoulCriteria{True: stringPtr("asks for money back"), False: stringPtr("only reports the issue")},
					},
					"department": &SystemOneChoiceQuestion{
						Type:         "choice",
						Instructions: "Which team?",
						Criteria:     map[string]*string{"billing": stringPtr("payments"), "technical": nil},
					},
					"severity": &SystemOneScoreQuestion{
						Type:         "score",
						Instructions: "How severe?",
						Criteria:     []string{"minor", "major", "critical"},
					},
					"mug": &SystemOneBoxQuestion{Type: "box", Instructions: "the coffee mug in photo"},
				},
			},
		},
		{
			Name: "NoulCriteriaWithNull",
			JSON: `{"model":"jev-latest","state":"hi","questions":{"greeting":{"type":"noul","criteria":{"true":"says hello","false":null}}}}`,
			Object: SystemOneRequest{
				Model: "jev-latest",
				State: json.RawMessage(`"hi"`),
				Questions: SystemOneQuestions{
					"greeting": &SystemOneNoulQuestion{Type: "noul", Criteria: &SystemOneNoulCriteria{True: stringPtr("says hello")}},
				},
			},
			UnmarshalOnly: true, // a null description is omitted on Marshal
		},
		{
			Name: "UnknownQuestionType",
			JSON: `{"model":"jev-latest","state":"hi","questions":{"x":{"type":"rank","instructions":"Rank them","items":["a","b"]}}}`,
			Object: SystemOneRequest{
				Model:     "jev-latest",
				State:     json.RawMessage(`"hi"`),
				Questions: SystemOneQuestions{"x": &SystemOneGeneralQuestion{Type: "rank"}},
			},
			UnmarshalOnly: true, // the fallback keeps only the type
		},
	}

	for _, tc := range testCases {
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			var got SystemOneRequest
			require.NoError(t, json.Unmarshal([]byte(tc.JSON), &got))
			assert.Equal(t, tc.Object, got)
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

func TestSystemOneResponse_Marshal_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name   string
		JSON   string
		Object SystemOneResponse
	}{
		{
			Name: "AllAnswerTypes",
			JSON: `{"model":"decision-model-preview","request_id":"50671916-0d60-9ce0-9527-7fe1680b4f62","answers":{` +
				`"department":{"type":"choice","choice":"billing","confidence":0.88,"probabilities":{"billing":0.94,"technical":0.06}},` +
				`"escalate":{"type":"noul","noul":0.99},` +
				`"severity":{"type":"score","score":2.25,"legend":{"0":"minor","1":"major"},"probabilities":{"0":0.27,"1":0.73},"confidence":0.91}},` +
				`"usage":{"input_tokens":125,"output_tokens":20}}`,
			Object: SystemOneResponse{
				Model:     "decision-model-preview",
				RequestID: "50671916-0d60-9ce0-9527-7fe1680b4f62",
				Answers: map[string]SystemOneAnswer{
					"department": {
						Type:          "choice",
						Choice:        stringPtr("billing"),
						Confidence:    float64Ptr(0.88),
						Probabilities: map[string]float64{"billing": 0.94, "technical": 0.06},
					},
					"escalate": {Type: "noul", Noul: float64Ptr(0.99)},
					"severity": {
						Type:          "score",
						Score:         float64Ptr(2.25),
						Legend:        json.RawMessage(`{"0":"minor","1":"major"}`),
						Probabilities: map[string]float64{"0": 0.27, "1": 0.73},
						Confidence:    float64Ptr(0.91),
					},
				},
				Usage: &SystemOneUsage{InputTokens: 125, OutputTokens: intPtr(20)},
			},
		},
		{
			Name: "MissingOutputTokens",
			JSON: `{"id":"gen-1","model":"jev-1.13.0","answers":{"is_urgent":{"type":"noul","noul":0.95}},"usage":{"input_tokens":296}}`,
			Object: SystemOneResponse{
				ID:      "gen-1",
				Model:   "jev-1.13.0",
				Answers: map[string]SystemOneAnswer{"is_urgent": {Type: "noul", Noul: float64Ptr(0.95)}},
				Usage:   &SystemOneUsage{InputTokens: 296},
			},
		},
	}

	for _, tc := range testCases {
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			var got SystemOneResponse
			require.NoError(t, json.Unmarshal([]byte(tc.JSON), &got))
			assert.Equal(t, tc.Object, got)
		})
		t.Run("Marshal_"+tc.Name, func(t *testing.T) {
			data, err := json.Marshal(tc.Object)
			require.NoError(t, err)
			assert.JSONEq(t, tc.JSON, string(data))
		})
	}
}
