package openai

import (
	"encoding/json"
	"testing"

	"github.com/mailru/easyjson/jlexer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChatCompletionRequest_ParseJLexer(t *testing.T) {
	testCases := []struct {
		Name string
		JSON string
	}{
		{
			Name: "basic with stop string",
			JSON: `{"model":"gpt-4","messages":[{"role":"system","content":"You are a helpful assistant."},{"role":"user","content":"Hello!"}],"max_tokens":100,"stop":"stop","temperature":0.7}`,
		},
		{
			Name: "array content with image_url string",
			JSON: `{"model":"gpt-4o","messages":[{"role":"user","content":[{"type":"text","text":"Describe this image"},{"type":"image_url","image_url":"https://example.com/img.png"}]}],"stop":["stop1","stop2"]}`,
		},
		{
			Name: "array content with image_url struct",
			JSON: `{"model":"gpt-4o","messages":[{"role":"user","content":[{"type":"text","text":"Describe this image"},{"type":"image_url","image_url":{"url":"https://example.com/img.jpg","detail":"high"}}]}]}`,
		},
		{
			Name: "array content with image_url null",
			JSON: `{"model":"gpt-4o","messages":[{"role":"user","content":[{"type":"text","text":"Describe this image"},{"type":"image_url"}]}],"stop":["stop1","stop2"]}`,
		},
		{
			Name: "tool_choice string auto",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"tool_choice":"auto"}`,
		},
		{
			Name: "tool_choice object function",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"function","function":{"name":"get_weather"}}}`,
		},
		{
			Name: "tool_choice object allowed_tools",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"allowed_tools","allowed_tools":{"mode":"auto","tools":[{"type":"function","function":{"name":"get_weather"}}]}}}`,
		},
		{
			Name: "tool_choice object allowed_tools empty",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"allowed_tools","allowed_tools":{"mode":"auto"}}}`,
		},
		{
			Name: "tool_choice object custom",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"custom","custom":{"name":"my_tool"}}}`,
		},
		{
			Name: "with tools and thinking",
			JSON: `{"model":"o1","messages":[{"role":"user","content":"Think"}],"thinking":{"type":"enabled"},"tools":[{"type":"function","function":{"name":"calculator","description":"A calculator","parameters":{"type":"object","properties":{}}}}]}`,
		},
		{
			Name: "empty request",
			JSON: `{}`,
		},
		{
			Name: "model only",
			JSON: `{"model":"gpt-4"}`,
		},
		{
			Name: "null fields",
			JSON: `{"model":"gpt-4","messages":null,"max_tokens":null,"temperature":null,"stop":null,"tool_choice":null,"stream":null}`,
		},
		{
			Name: "stop array",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stop":["end","quit"]}`,
		},
		{
			Name: "empty messages array",
			JSON: `{"model":"gpt-4","messages":[]}`,
		},
		{
			Name: "with all pointer fields",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"max_tokens":200,"max_completion_tokens":300,"temperature":0.5,"top_p":0.9,"top_k":40,"stream":true}`,
		},
		{
			Name: "custom tool",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"custom","custom":{"name":"my_tool","description":"A custom tool","format":{"type":"object"}}}]}`,
		},
		{
			Name: "null string fields in message",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":"hi","name":null,"tool_call_id":null}],"tools":[{"type":null,"function":{"name":"calc","description":null,"parameters":null}}]}`,
		},
		{
			Name: "null model field",
			JSON: `{"model":null,"messages":[]}`,
		},
		{
			Name: "null content item string fields",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":[{"type":null,"text":null}]}]}`,
		},
		{
			Name: "no content item type",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":[{"text":"hello"}]}]}`,
		},
		{
			Name: "null image_url detail",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/img.png","detail":null}}]}]}`,
		},
		{
			Name: "null content field in message",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":null}]}`,
		},
		{
			Name: "no content field in message",
			JSON: `{"model":"gpt-4","messages":[{"role":"user"}]}`,
		},
		{
			Name: "video_url content item",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"https://example.com/video.mp4"}}]}]}`,
		},
		{
			Name: "audio_url content item",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"audio_url","audio_url":{"url":"https://example.com/audio.mp3"}}]}]}`,
		},
		{
			Name: "input_audio content item",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"base64audiodata","format":"wav"}}]}]}`,
		},
		{
			Name: "file content item",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"file","file":{"file_uri":"file-abc123","mime_type":"application/pdf"}}]}]}`,
		},
		{
			Name: "null video_url in content item",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"video_url","video_url":null}]}]}`,
		},
		{
			Name: "null audio_url in content item",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"audio_url","audio_url":null}]}]}`,
		},
		{
			Name: "null input_audio in content item",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":null}]}]}`,
		},
		{
			Name: "null file in content item",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"file","file":null}]}]}`,
		},
		{
			Name: "null url in video_url",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":null}}]}]}`,
		},
		{
			Name: "null url in audio_url",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"audio_url","audio_url":{"url":null}}]}]}`,
		},
		{
			Name: "null data and format in input_audio",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":null,"format":null}}]}]}`,
		},
		{
			Name: "null file_uri and mime_type in file",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"file","file":{"file_uri":null,"mime_type":null}}]}]}`,
		},
		{
			Name: "message with tool_calls",
			JSON: `{"model":"gpt-4","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","index":0,"type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"NYC\"}"}}]}]}`,
		},
		{
			Name: "message with null tool_calls",
			JSON: `{"model":"gpt-4","messages":[{"role":"assistant","content":null,"tool_calls":null}]}`,
		},
		{
			Name: "message with reasoning_content string",
			JSON: `{"model":"o1","messages":[{"role":"assistant","content":"answer","reasoning_content":"thinking about it"}]}`,
		},
		{
			Name: "message with reasoning_content array",
			JSON: `{"model":"o1","messages":[{"role":"assistant","content":"answer","reasoning_content":[{"type":"text","text":"thinking"}]}]}`,
		},
		{
			Name: "message with null reasoning_content",
			JSON: `{"model":"o1","messages":[{"role":"assistant","content":"answer","reasoning_content":null}]}`,
		},
		{
			Name: "tool with null function and custom",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":null,"custom":null}]}`,
		},
		{
			Name: "tool_choice object with null function allowed_tools custom",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"function","function":null,"allowed_tools":null,"custom":null}}`,
		},
		{
			Name: "tool_choice allowed_tools with null tools",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"allowed_tools","allowed_tools":{"mode":"auto","tools":null}}}`,
		},
		{
			Name: "custom tool with null format",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"custom","custom":{"name":"tool1","description":"desc","format":null}}]}`,
		},
		{
			Name: "top-level null",
			JSON: `null`,
		},
		{
			Name: "unknown fields ignored",
			JSON: `{"model":"gpt-4","messages":[],"unknown_field":"value","nested":{"a":1}}`,
		},
		{
			Name: "stop empty array",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stop":[]}`,
		},
		{
			Name: "image_url null in content item",
			JSON: `{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"image_url","image_url":null}]}]}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			var stdReq ChatCompletionRequest
			err := json.Unmarshal([]byte(tc.JSON), &stdReq)
			require.NoError(t, err, "standard json.Unmarshal should not error")

			in := jlexer.Lexer{Data: []byte(tc.JSON)}
			jReq := &ChatCompletionRequest{}
			jReq.ParseJLexer(&in)
			require.NoError(t, in.Error(), "jlexer parser should not error")
			assert.Equal(t, &stdReq, jReq, "jlexer parser output should match standard json.Unmarshal")
		})
	}
}
