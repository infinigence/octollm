package converter

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
)

func testChatCompletionToResponses_NonStream(t *testing.T, responsesReqJSON, expectedChatReqJSON, chatRespJSON, expectedResponsesRespJSON string) {
	ctx := context.Background()

	httpReq, err := http.NewRequestWithContext(ctx, "POST", "http://localhost/v1/responses", nil)
	require.NoError(t, err)

	req := octollm.NewRequest(httpReq, octollm.APIFormatResponses)
	req.Body = octollm.NewBodyFromBytes(
		[]byte(responsesReqJSON),
		&octollm.JSONParser[openai.ResponsesRequest]{},
	)

	mockEng := newMockEngine(t)
	mockEng.expectedRequestCheck = func(t *testing.T, req *octollm.Request) {
		assert.Equal(t, octollm.APIFormatChatCompletions, req.Format)
		bytes, err := req.Body.Bytes()
		require.NoError(t, err)
		assert.JSONEq(t, expectedChatReqJSON, string(bytes))
	}
	mockEng.responseToReturn = &octollm.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body: octollm.NewBodyFromBytes(
			[]byte(chatRespJSON),
			&octollm.JSONParser[openai.ChatCompletionResponse]{},
		),
	}

	converter := NewChatCompletionToResponses(mockEng)
	resp, err := converter.Process(req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	respBytes, err := resp.Body.Bytes()
	require.NoError(t, err)
	assert.JSONEq(t, expectedResponsesRespJSON, string(respBytes))
}

func TestChatCompletionToResponses_NonStream_SimpleText(t *testing.T) {
	responsesReqJSON := `{
		"model": "gpt-4o",
		"instructions": "You are helpful.",
		"input": [{"role": "user", "content": [{"type": "input_text", "text": "Hello"}]}]
	}`
	expectedChatReqJSON := `{
		"model": "gpt-4o",
		"messages": [
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": [{"type": "text", "text": "Hello"}]}
		]
	}`
	chatRespJSON := `{
		"id": "c1", "object": "chat.completion", "created": 1, "model": "gpt-4o",
		"choices": [{"index": 0, "finish_reason": "stop", "message": {"role": "assistant", "content": "Hi!"}}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12}
	}`
	expectedResponsesRespJSON := `{
		"id": "c1", "object": "response", "created_at": 1, "status": "completed", "model": "gpt-4o",
		"output": [{"id": "msg_0", "type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "Hi!"}]}],
		"usage": {"input_tokens": 10, "output_tokens": 2, "total_tokens": 12}
	}`

	testChatCompletionToResponses_NonStream(t, responsesReqJSON, expectedChatReqJSON, chatRespJSON, expectedResponsesRespJSON)
}

func testChatCompletionToResponses_convertRequestBody(t *testing.T, responsesReqJSON, expectedChatReqJSON string) {
	ctx := context.Background()
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
	req := octollm.NewRequest(httpReq, octollm.APIFormatResponses)
	req.Body = octollm.NewBodyFromBytes([]byte(responsesReqJSON), &octollm.JSONParser[openai.ResponsesRequest]{})

	converter := NewChatCompletionToResponses(nil)
	newBody, err := converter.convertRequestBody(ctx, req.Body)
	require.NoError(t, err)

	bytes, err := newBody.Bytes()
	require.NoError(t, err)
	assert.JSONEq(t, expectedChatReqJSON, string(bytes))
}

func TestChatCompletionToResponses_convertRequestBody_FunctionCallAndOutput(t *testing.T) {
	responsesReqJSON := `{
		"model": "gpt-4o",
		"input": [
			{"type": "function_call", "call_id": "call_1", "name": "get_weather", "arguments": "{\"city\":\"Tokyo\"}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "Sunny"}
		]
	}`
	expectedChatReqJSON := `{
		"model": "gpt-4o",
		"messages": [
			{"role": "assistant", "tool_calls": [{"id": "call_1", "index": 0, "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"Tokyo\"}"}}]},
			{"role": "tool", "tool_call_id": "call_1", "content": "Sunny"}
		]
	}`

	testChatCompletionToResponses_convertRequestBody(t, responsesReqJSON, expectedChatReqJSON)
}

func TestChatCompletionToResponses_convertRequestBody_MultipleParallelToolCalls(t *testing.T) {
	responsesReqJSON := `{
		"model": "gpt-4o",
		"input": [
			{"type": "function_call", "call_id": "call_1", "name": "get_weather", "arguments": "{\"city\":\"Tokyo\"}"},
			{"type": "function_call", "call_id": "call_2", "name": "get_weather", "arguments": "{\"city\":\"Osaka\"}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "Sunny"},
			{"type": "function_call_output", "call_id": "call_2", "output": "Rainy"}
		]
	}`
	expectedChatReqJSON := `{
		"model": "gpt-4o",
		"messages": [
			{"role": "assistant", "tool_calls": [
				{"id": "call_1", "index": 0, "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"Tokyo\"}"}},
				{"id": "call_2", "index": 1, "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"Osaka\"}"}}
			]},
			{"role": "tool", "tool_call_id": "call_1", "content": "Sunny"},
			{"role": "tool", "tool_call_id": "call_2", "content": "Rainy"}
		]
	}`

	testChatCompletionToResponses_convertRequestBody(t, responsesReqJSON, expectedChatReqJSON)
}

func TestChatCompletionToResponses_convertRequestBody_ToolsAndToolChoice(t *testing.T) {
	responsesReqJSON := `{
		"model": "gpt-4o",
		"input": "Weather?",
		"tools": [{"type": "function", "name": "get_weather", "description": "Get weather", "parameters": {"type": "object"}}],
		"tool_choice": "required"
	}`
	expectedChatReqJSON := `{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "Weather?"}],
		"tools": [{"type": "function", "function": {"name": "get_weather", "description": "Get weather", "parameters": {"type": "object"}}}],
		"tool_choice": "required"
	}`

	testChatCompletionToResponses_convertRequestBody(t, responsesReqJSON, expectedChatReqJSON)
}

func TestChatCompletionToResponses_convertRequestBody_ToolChoiceObject(t *testing.T) {
	responsesReqJSON := `{
		"model": "gpt-4o",
		"input": "Weather?",
		"tool_choice": {"type": "function", "name": "get_weather"}
	}`
	expectedChatReqJSON := `{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "Weather?"}],
		"tool_choice": {"type": "function", "function": {"name": "get_weather"}}
	}`

	testChatCompletionToResponses_convertRequestBody(t, responsesReqJSON, expectedChatReqJSON)
}

func TestChatCompletionToResponses_convertRequestBody_SamplingAndReasoning(t *testing.T) {
	responsesReqJSON := `{
		"model": "gpt-5",
		"max_output_tokens": 1024,
		"temperature": 0.7,
		"top_p": 0.9,
		"reasoning": {"effort": "high"},
		"input": "Tell me a story"
	}`
	expectedChatReqJSON := `{
		"model": "gpt-5",
		"max_tokens": 1024,
		"temperature": 0.7,
		"top_p": 0.9,
		"reasoning_effort": "high",
		"messages": [{"role": "user", "content": "Tell me a story"}]
	}`

	testChatCompletionToResponses_convertRequestBody(t, responsesReqJSON, expectedChatReqJSON)
}

func testChatCompletionToResponses_convertNonStreamResponseBody(t *testing.T, chatRespJSON, expectedResponsesRespJSON string) {
	ctx := context.Background()
	srcBody := octollm.NewBodyFromBytes([]byte(chatRespJSON), &octollm.JSONParser[openai.ChatCompletionResponse]{})

	converter := NewChatCompletionToResponses(nil)
	newBody, err := converter.convertNonStreamResponseBody(ctx, srcBody)
	require.NoError(t, err)

	bytes, err := newBody.Bytes()
	require.NoError(t, err)
	assert.JSONEq(t, expectedResponsesRespJSON, string(bytes))
}

func TestChatCompletionToResponses_convertNonStreamResponseBody_ToolCall(t *testing.T) {
	chatRespJSON := `{
		"id": "c2", "object": "chat.completion", "created": 1, "model": "gpt-4o",
		"choices": [{"index": 0, "finish_reason": "tool_calls", "message": {"role": "assistant", "tool_calls": [{"id": "call_1", "index": 0, "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"Tokyo\"}"}}]}}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
	}`
	expectedResponsesRespJSON := `{
		"id": "c2", "object": "response", "created_at": 1, "status": "completed", "model": "gpt-4o",
		"output": [{"id": "fc_0", "type": "function_call", "call_id": "call_1", "name": "get_weather", "arguments": "{\"city\":\"Tokyo\"}"}],
		"usage": {"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}
	}`

	testChatCompletionToResponses_convertNonStreamResponseBody(t, chatRespJSON, expectedResponsesRespJSON)
}

func TestChatCompletionToResponses_convertNonStreamResponseBody_Reasoning(t *testing.T) {
	chatRespJSON := `{
		"id": "c3", "object": "chat.completion", "created": 1, "model": "o3",
		"choices": [{"index": 0, "finish_reason": "stop", "message": {"role": "assistant", "content": "Answer", "reasoning_content": "Thinking..."}}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 3, "total_tokens": 13, "completion_tokens_details": {"reasoning_tokens": 2}}
	}`
	expectedResponsesRespJSON := `{
		"id": "c3", "object": "response", "created_at": 1, "status": "completed", "model": "o3",
		"output": [
			{"id": "rs_0", "type": "reasoning", "summary": [{"type": "summary_text", "text": "Thinking..."}]},
			{"id": "msg_1", "type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "Answer"}]}
		],
		"usage": {"input_tokens": 10, "output_tokens": 3, "total_tokens": 13, "output_tokens_details": {"reasoning_tokens": 2}}
	}`

	testChatCompletionToResponses_convertNonStreamResponseBody(t, chatRespJSON, expectedResponsesRespJSON)
}

func TestChatCompletionToResponses_convertNonStreamResponseBody_Length(t *testing.T) {
	chatRespJSON := `{
		"id": "c4", "object": "chat.completion", "created": 1, "model": "gpt-4o",
		"choices": [{"index": 0, "finish_reason": "length", "message": {"role": "assistant", "content": "partial"}}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 7, "total_tokens": 17}
	}`
	expectedResponsesRespJSON := `{
		"id": "c4", "object": "response", "created_at": 1, "status": "incomplete", "model": "gpt-4o",
		"incomplete_details": {"reason": "max_output_tokens"},
		"output": [{"id": "msg_0", "type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "partial"}]}],
		"usage": {"input_tokens": 10, "output_tokens": 7, "total_tokens": 17}
	}`

	testChatCompletionToResponses_convertNonStreamResponseBody(t, chatRespJSON, expectedResponsesRespJSON)
}

func testChatCompletionToResponses_convertStreamResponse(t *testing.T, chatChunks, expectedResponsesChunks []string) {
	httpReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil)
	req := octollm.NewRequest(httpReq, octollm.APIFormatResponses)
	converter := NewChatCompletionToResponses(nil)

	inCh := make(chan *octollm.StreamChunk)
	closed := false
	closeFunc := func() { closed = true }

	go func() {
		defer close(inCh)
		for _, chunk := range chatChunks {
			body := octollm.NewBodyFromBytes([]byte(chunk), &octollm.JSONParser[openai.ChatCompletionStreamChunk]{})
			inCh <- &octollm.StreamChunk{Body: body}
		}
	}()
	inStream := octollm.NewStreamChan(inCh, closeFunc)

	dstStream, err := converter.convertStreamResponse(req, inStream)
	require.NoError(t, err)

	i := 0
	for dstChunk := range dstStream.Chan() {
		bodyBytes, err := dstChunk.Body.Bytes()
		require.NoError(t, err)

		typeStr := gjson.GetBytes(bodyBytes, "type").String()
		require.Equal(t, typeStr, dstChunk.Metadata["event"])

		require.JSONEq(t, expectedResponsesChunks[i], string(bodyBytes))
		i++
	}
	dstStream.Close()

	require.Equal(t, i, len(expectedResponsesChunks))
	require.True(t, closed)
}

func TestChatCompletionToResponses_convertStreamResponse_SimpleText(t *testing.T) {
	chatChunks := []string{
		`{"id":"c1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}]}`,
		`{"id":"c1","object":"chat.completion.chunk","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}`,
		`[DONE]`,
	}

	expectedResponsesChunks := []string{
		`{"type":"response.created","response":{"id":"c1","object":"response","status":"in_progress","model":"gpt-4o","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_0","type":"message","role":"assistant"}}`,
		`{"type":"response.content_part.added","output_index":0,"content_index":0,"part":{"type":"output_text"}}`,
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"Hello"}`,
		`{"type":"response.output_text.done","output_index":0,"content_index":0,"text":"Hello"}`,
		`{"type":"response.content_part.done","output_index":0,"content_index":0}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_0","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}}`,
		`{"type":"response.completed","response":{"id":"c1","object":"response","status":"completed","model":"gpt-4o","output":[{"id":"msg_0","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11}}}`,
	}

	testChatCompletionToResponses_convertStreamResponse(t, chatChunks, expectedResponsesChunks)
}

func TestChatCompletionToResponses_convertStreamResponse_ToolCall(t *testing.T) {
	chatChunks := []string{
		`{"id":"c2","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`,
		`{"id":"c2","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]},"finish_reason":null}]}`,
		`{"id":"c2","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Tokyo\"}"}}]},"finish_reason":null}]}`,
		`{"id":"c2","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"id":"c2","object":"chat.completion.chunk","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		`[DONE]`,
	}

	expectedResponsesChunks := []string{
		`{"type":"response.created","response":{"id":"c2","object":"response","status":"in_progress","model":"gpt-4o","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_0","type":"function_call","call_id":"call_1","name":"get_weather"}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_0","output_index":0,"delta":"{\"city\":"}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_0","output_index":0,"delta":"\"Tokyo\"}"}`,
		`{"type":"response.function_call_arguments.done","item_id":"fc_0","output_index":0,"arguments":"{\"city\":\"Tokyo\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"fc_0","type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Tokyo\"}"}}`,
		`{"type":"response.completed","response":{"id":"c2","object":"response","status":"completed","model":"gpt-4o","output":[{"id":"fc_0","type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Tokyo\"}"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`,
	}

	testChatCompletionToResponses_convertStreamResponse(t, chatChunks, expectedResponsesChunks)
}

func TestChatCompletionToResponses_convertStreamResponse_ReasoningThenText(t *testing.T) {
	chatChunks := []string{
		`{"id":"c3","object":"chat.completion.chunk","model":"o3","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"Think"},"finish_reason":null}]}`,
		`{"id":"c3","object":"chat.completion.chunk","model":"o3","choices":[{"index":0,"delta":{"content":"42"},"finish_reason":null}]}`,
		`{"id":"c3","object":"chat.completion.chunk","model":"o3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"c3","object":"chat.completion.chunk","model":"o3","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`[DONE]`,
	}

	expectedResponsesChunks := []string{
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
		`{"type":"response.completed","response":{"id":"c3","object":"response","status":"completed","model":"o3","output":[{"id":"rs_0","type":"reasoning","summary":[{"type":"summary_text","text":"Think"}]},{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"42"}]}],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8,"output_tokens_details":{"reasoning_tokens":2}}}}`,
	}

	testChatCompletionToResponses_convertStreamResponse(t, chatChunks, expectedResponsesChunks)
}
