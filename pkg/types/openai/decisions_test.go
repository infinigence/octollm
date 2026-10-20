package openai

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func float64Ptr(f float64) *float64 {
	return &f
}

func TestDecisionsRequest_Marshal_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name   string
		JSON   string
		Object DecisionsRequest
	}{
		{
			Name: "StringInput_Predicate",
			JSON: `{"model":"gpt-6-luna","input":"The package arrived with a broken screen.","questions":[{"type":"predicate","name":"damaged","instructions":"Does the customer report a damaged item?"}]}`,
			Object: DecisionsRequest{
				Model: "gpt-6-luna",
				Input: ResponsesInputString("The package arrived with a broken screen."),
				Questions: []DecisionsQuestion{
					{Type: "predicate", Name: "damaged", Instructions: "Does the customer report a damaged item?"},
				},
			},
		},
		{
			Name: "MessageInputWithImage_ChoiceAndScore",
			JSON: `{"model":"gpt-6-luna","input":[{"role":"user","content":[` +
				`{"type":"input_text","text":"Inspect the product in this photo."},` +
				`{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="}]}],"questions":[` +
				`{"type":"choice","name":"department","instructions":"Which team?","choices":[{"value":"billing","description":"payments"},{"value":true}]},` +
				`{"type":"score","name":"severity","instructions":"How severe?","levels":[{"label":"minor"},{"label":"critical","description":"outage"}]}]}`,
			Object: DecisionsRequest{
				Model: "gpt-6-luna",
				Input: ResponsesInputItemArray{
					&ResponsesInputMessage{
						Role: "user",
						Content: ResponsesInputMessageContentArray{
							{Type: "input_text", Text: "Inspect the product in this photo."},
							{Type: "input_image", ImageURL: MessageContentItemImageURLString("data:image/png;base64,iVBORw0KGgo=")},
						},
					},
				},
				Questions: []DecisionsQuestion{
					{
						Type:         "choice",
						Name:         "department",
						Instructions: "Which team?",
						Choices: []DecisionsChoice{
							{Value: json.RawMessage(`"billing"`), Description: "payments"},
							{Value: json.RawMessage(`true`)},
						},
					},
					{
						Type:         "score",
						Name:         "severity",
						Instructions: "How severe?",
						Levels: []DecisionsLevel{
							{Label: "minor"},
							{Label: "critical", Description: "outage"},
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			var got DecisionsRequest
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

func TestDecisionsResponse_Marshal_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name   string
		JSON   string
		Object DecisionsResponse
	}{
		{
			Name: "AllAnswerTypes",
			JSON: `{"id":"dec_1","model":"gpt-6-luna","answers":[` +
				`{"type":"predicate","name":"damaged","probability":0.95},` +
				`{"type":"choice","name":"department","choice":"billing","confidence":0.88,"probabilities":[{"value":"billing","probability":0.94},{"value":true,"probability":0.06}]},` +
				`{"type":"score","name":"severity","score":1.73,"confidence":0.91,"probabilities":[{"value":0,"label":"minor","probability":0.27},{"value":1,"label":"critical","probability":0.73}]},` +
				`{"type":"refusal","name":"unsafe"}],` +
				`"usage":{"input_tokens":42,"input_tokens_details":{"cached_tokens":8,"cache_write_tokens":3},"output_tokens":5,"output_tokens_details":{"reasoning_tokens":2},"total_tokens":47}}`,
			Object: DecisionsResponse{
				ID:    "dec_1",
				Model: "gpt-6-luna",
				Answers: []DecisionsAnswer{
					{Type: "predicate", Name: "damaged", Probability: float64Ptr(0.95)},
					{
						Type:       "choice",
						Name:       "department",
						Choice:     json.RawMessage(`"billing"`),
						Confidence: float64Ptr(0.88),
						Probabilities: []DecisionsProbability{
							{Value: json.RawMessage(`"billing"`), Probability: 0.94},
							{Value: json.RawMessage(`true`), Probability: 0.06},
						},
					},
					{
						Type:       "score",
						Name:       "severity",
						Score:      float64Ptr(1.73),
						Confidence: float64Ptr(0.91),
						Probabilities: []DecisionsProbability{
							{Value: json.RawMessage(`0`), Label: "minor", Probability: 0.27},
							{Value: json.RawMessage(`1`), Label: "critical", Probability: 0.73},
						},
					},
					{Type: "refusal", Name: "unsafe"},
				},
				Usage: &ResponsesUsage{
					InputTokens:         42,
					OutputTokens:        5,
					TotalTokens:         47,
					InputTokensDetails:  &ResponsesInputTokenDetails{CachedTokens: intPtr(8), CacheWriteTokens: intPtr(3)},
					OutputTokensDetails: &ResponsesOutputTokenDetails{ReasoningTokens: intPtr(2)},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			var got DecisionsResponse
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

func TestDecisionsRequest_String(t *testing.T) {
	testCases := []struct {
		Name     string
		JSON     string
		Expected string
	}{
		{
			Name: "MessageInputWithImage",
			JSON: `{"model":"gpt-6-luna","input":[{"role":"user","content":[` +
				`{"type":"input_text","text":"Inspect the product."},` +
				`{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="}]}],"questions":[` +
				`{"type":"predicate","name":"damaged","instructions":"Is it damaged?"},` +
				`{"type":"choice","name":"department","instructions":"Which team?","choices":[{"value":"billing","description":"payments"},{"value":true}]},` +
				`{"type":"score","name":"severity","instructions":"How severe?","levels":[{"label":"minor"},{"label":"critical"}]}]}`,
			Expected: `(DecisionsRequest) {
  Model: "gpt-6-luna"
  Input: len(1)
    {type=message, role=user, content=[input_text(len=20), input_image(len=34), ]}
  Questions: len(3)
    {name="damaged", type=predicate, instructions=len(14)}
    {name="department", type=choice, instructions=len(11), choices=["billing", true]}
    {name="severity", type=score, instructions=len(11), levels=["minor", "critical"]}
}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			var req DecisionsRequest
			require.NoError(t, json.Unmarshal([]byte(tc.JSON), &req))
			assert.Equal(t, tc.Expected, req.String())
		})
	}
}

func TestDecisionsResponse_String(t *testing.T) {
	testCases := []struct {
		Name     string
		JSON     string
		Expected string
	}{
		{
			Name: "AllAnswerTypes",
			JSON: `{"id":"dec_1","model":"gpt-6-luna","answers":[` +
				`{"type":"predicate","name":"damaged","probability":0.95},` +
				`{"type":"choice","name":"department","choice":"billing","confidence":0.88,"probabilities":[{"value":"billing","probability":0.94},{"value":true,"probability":0.06}]},` +
				`{"type":"score","name":"severity","score":1.73,"confidence":0.91,"probabilities":[{"value":0,"label":"minor","probability":0.27},{"value":1,"label":"critical","probability":0.73}]},` +
				`{"type":"refusal","name":"unsafe"}],` +
				`"usage":{"input_tokens":42,"input_tokens_details":{"cached_tokens":8},"output_tokens":5,"total_tokens":47}}`,
			Expected: `(DecisionsResponse) {
  ID: "dec_1"
  Model: "gpt-6-luna"
  Answers: len(4)
    {name="damaged", type=predicate, probability=0.95}
    {name="department", type=choice, choice="billing", confidence=0.88, probabilities=["billing": 0.94, true: 0.06]}
    {name="severity", type=score, score=1.73, confidence=0.91, probabilities=[0("minor"): 0.27, 1("critical"): 0.73]}
    {name="unsafe", type=refusal}
  Usage: input=42, output=5, total=47, cached=8
}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			var resp DecisionsResponse
			require.NoError(t, json.Unmarshal([]byte(tc.JSON), &resp))
			assert.Equal(t, tc.Expected, resp.String())
		})
	}
}
