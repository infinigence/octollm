package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/stretchr/testify/assert"
)

func TestMessageNewParams_MarshalJSON(t *testing.T) {
	p := MessageNewParams{
		MessageNewParams: anthropic.MessageNewParams{
			Model: anthropic.ModelClaude3_5HaikuLatest,
			Messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(
					anthropic.NewTextBlock("Hello, I am a user."),
					anthropic.NewImageBlock(anthropic.URLImageSourceParam{
						URL: "https://example.com/image.jpg",
					}),
				),
			},
			System: []anthropic.TextBlockParam{
				{Text: "Be very serious at all times."},
			},
			Tools: []anthropic.ToolUnionParam{
				{
					OfTool: &anthropic.ToolParam{
						Name:        "get_coordinates",
						Description: anthropic.String("Accepts a place as an address, then returns the latitude and longitude coordinates."),
						InputSchema: anthropic.ToolInputSchemaParam{
							Type: "object",
							Properties: map[string]any{
								"location": map[string]string{
									"type": "string",
								},
							},
							Required: []string{"location"},
						},
					},
				},
			},
			MaxTokens: 1024,
		},
		Stream: param.NewOpt(false),
	}

	jsonStr, err := json.Marshal(p)
	assert.NoError(t, err)
	t.Logf("jsonStr: %s", string(jsonStr))
	assert.JSONEq(t, `{
		"model":"claude-3-5-haiku-latest",
		"messages":[
			{"role":"user","content":[
				{"type":"text","text":"Hello, I am a user."},
				{"type":"image","source":{"type":"url","url":"https://example.com/image.jpg"}}
			]}
		],
		"system": [{"type":"text","text":"Be very serious at all times."}],
		"tools":[{
			"name":"get_coordinates",
			"description":"Accepts a place as an address, then returns the latitude and longitude coordinates.",
			"input_schema":{
				"type":"object",
				"properties":{
					"location":{"type":"string"}
				},
				"required":["location"]
			}
		}],
		"max_tokens":1024,
		"stream":false
	}`, string(jsonStr))
}

func TestMessageNewParams_UnmarshalJSON(t *testing.T) {
	p := MessageNewParams{}
	jStrOriginal := `{
		"model":"claude-3-5-haiku-latest",
		"messages":[
			{"role":"user","content":[
				{"type":"text","text":"Hello, I am a user."},
				{"type":"image","source":{"type":"url","url":"https://example.com/image.jpg"}}
			]}
		],
		"system": [{"type":"text","text":"Be very serious at all times."}],
		"tools":[{
			"name":"get_coordinates",
			"description":"Accepts a place as an address, then returns the latitude and longitude coordinates.",
			"input_schema":{
				"type":"object",
				"properties":{
					"location":{"type":"string"}
				},
				"required":["location"]
			}
		}],
		"max_tokens":1024,
		"stream":false
	}`

	err := json.Unmarshal([]byte(jStrOriginal), &p)
	assert.NoError(t, err)

	// spew.Dump(p)

	jsonStr, err := json.Marshal(p)
	assert.NoError(t, err)
	// t.Logf("jsonStr: %s", string(jsonStr))
	assert.JSONEq(t, jStrOriginal, string(jsonStr))
	assert.True(t, p.Stream.Valid())
	assert.False(t, p.Stream.Value)
}
