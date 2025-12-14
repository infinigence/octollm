package openai

import (
	"encoding/json"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/stretchr/testify/assert"
)

func TestChatCompletionNewParams_MarshalJSON(t *testing.T) {
	p := ChatCompletionNewParams{
		ChatCompletionNewParams: openai.ChatCompletionNewParams{
			Model: openai.ChatModelGPT3_5Turbo,
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.SystemMessage("Hello, I am an assistant."),
				openai.UserMessage([]openai.ChatCompletionContentPartUnionParam{
					openai.TextContentPart("Hello, I am a user."),
					openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
						URL: "https://example.com/image.jpg",
					}),
				}),
			},
			Tools: []openai.ChatCompletionToolUnionParam{
				openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
					Name:        "get_weather",
					Description: openai.String("Get weather at the given location"),
					Parameters: openai.FunctionParameters{
						"type": "object",
						"properties": map[string]interface{}{
							"location": map[string]string{
								"type": "string",
							},
						},
						"required": []string{"location"},
					},
				}),
			},
		},
		Stream: param.NewOpt(false),
	}

	jsonStr, err := json.Marshal(p)
	assert.NoError(t, err)
	// t.Logf("jsonStr: %s", string(jsonStr))
	assert.JSONEq(t, `{
		"model":"gpt-3.5-turbo",
		"messages":[
			{"role":"system","content":"Hello, I am an assistant."},
			{"role":"user","content":[
				{"type":"text","text":"Hello, I am a user."},
				{"type":"image_url","image_url":{"url":"https://example.com/image.jpg"}}
			]}
		],
		"tools":[{
			"type":"function",
			"function":{
				"name":"get_weather",
				"description":"Get weather at the given location",
				"parameters":{
					"type":"object",
					"properties":{
						"location":{"type":"string"}
					},
					"required":["location"]
				}
			}
		}],
		"stream":false
	}`, string(jsonStr))
}

func TestChatCompletionNewParams_UnmarshalJSON(t *testing.T) {
	p := ChatCompletionNewParams{}
	jStrOriginal := `{
		"model":"gpt-3.5-turbo",
		"messages":[
			{"role":"system","content":"Hello, I am an assistant."},
			{"role":"user","content":[
				{"type":"text","text":"Hello, I am a user."},
				{"type":"image_url","image_url":{"url":"https://example.com/image.jpg"}}
			]}
		],
		"tools":[{
			"type":"function",
			"function":{
				"name":"get_weather",
				"description":"Get weather at the given location",
				"parameters":{
					"type":"object",
					"properties":{
						"location":{"type":"string"}
					},
					"required":["location"]
				}
			}
		}],
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
