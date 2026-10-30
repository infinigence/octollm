package usage

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
	"github.com/infinigence/octollm/pkg/types/rerank"
	"github.com/infinigence/octollm/pkg/types/systemone"
	"github.com/infinigence/octollm/pkg/types/vertex"
)

func newReq() *octollm.Request {
	return octollm.NewEmptyRequest(context.Background())
}

func nonStreamEngine[T any](body string) octollm.Engine {
	return octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		return octollm.NewNonStreamResponse(http.StatusOK, http.Header{},
			octollm.NewBodyFromBytes([]byte(body), &octollm.JSONParser[T]{})), nil
	})
}

func streamEngine[T any](datas ...string) octollm.Engine {
	return octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		ch := make(chan *octollm.StreamChunk, len(datas))
		for _, d := range datas {
			ch <- &octollm.StreamChunk{Body: octollm.NewBodyFromBytes([]byte(d), &octollm.JSONParser[T]{})}
		}
		close(ch)
		return octollm.NewStreamResponse(http.StatusOK, http.Header{}, octollm.NewStreamChan(ch, func() {})), nil
	})
}

func drain(t *testing.T, resp *octollm.Response) int {
	t.Helper()
	require.NotNil(t, resp.Stream)
	n := 0
	for range resp.Stream.Chan() {
		n++
	}
	resp.Stream.Close()
	return n
}

func TestCollector_NonStream(t *testing.T) {
	tests := []struct {
		name string
		next octollm.Engine
		want Usage
	}{
		{
			name: "chat completions",
			next: nonStreamEngine[openai.ChatCompletionResponse](
				`{"id":"c1","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"cached_tokens":30,"cache_write_tokens":10}}}`),
			want: &ChatCompletions{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120,
				PromptTokensDetails: &openai.PromptTokensDetails{CachedTokens: ptr(30), CacheWriteTokens: ptr(10)}},
		},
		{
			name: "completions",
			next: nonStreamEngine[openai.CompletionResponse](
				`{"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":4}}`),
			want: &ChatCompletions{PromptTokens: 3, CompletionTokens: 4},
		},
		{
			name: "claude",
			next: nonStreamEngine[anthropic.ClaudeMessagesResponse](
				`{"id":"m1","type":"message","role":"assistant","content":[],"usage":{"input_tokens":5,"output_tokens":7,"cache_creation_input_tokens":40,"cache_read_input_tokens":50,"cache_creation":{"ephemeral_5m_input_tokens":15,"ephemeral_1h_input_tokens":25}}}`),
			want: &Claude{InputTokens: ptr[int64](5), OutputTokens: ptr[int64](7),
				CacheCreationInputTokens: ptr[int64](40), CacheReadInputTokens: ptr[int64](50),
				CacheCreation: &anthropic.CacheCreation{Ephemeral5mInputTokens: ptr[int64](15), Ephemeral1hInputTokens: ptr[int64](25)}},
		},
		{
			name: "responses",
			next: nonStreamEngine[openai.ResponsesResponse](
				`{"id":"r1","object":"response","output":[],"usage":{"input_tokens":50,"output_tokens":5,"total_tokens":55,"input_tokens_details":{"cached_tokens":20}}}`),
			want: &Responses{InputTokens: 50, OutputTokens: 5, TotalTokens: 55,
				InputTokensDetails: &openai.ResponsesInputTokenDetails{CachedTokens: ptr(20)}},
		},
		{
			name: "vertex",
			next: nonStreamEngine[vertex.GenerateContentResponse](
				`{"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":3,"thoughtsTokenCount":4,"cachedContentTokenCount":2}}`),
			want: &Vertex{PromptTokenCount: 12, CandidatesTokenCount: 3, ThoughtsTokenCount: 4, CachedContentTokenCount: 2},
		},
		{
			name: "embeddings",
			next: nonStreamEngine[openai.EmbeddingResponse](
				`{"data":[],"usage":{"prompt_tokens":8,"total_tokens":8}}`),
			want: &Embedding{PromptTokens: 8, TotalTokens: 8},
		},
		{
			name: "rerank",
			next: nonStreamEngine[rerank.RerankResponse](
				`{"results":[],"usage":{"total_tokens":9}}`),
			want: &Rerank{TotalTokens: 9},
		},
		{
			name: "decisions",
			next: nonStreamEngine[openai.DecisionsResponse](
				`{"id":"d1","model":"m","answers":[],"usage":{"input_tokens":30,"output_tokens":2,"total_tokens":32}}`),
			want: &Responses{InputTokens: 30, OutputTokens: 2, TotalTokens: 32},
		},
		{
			name: "systemone",
			next: nonStreamEngine[systemone.SystemOneResponse](
				`{"model":"m","answers":{},"usage":{"input_tokens":125,"output_tokens":20}}`),
			want: &SystemOne{InputTokens: 125, OutputTokens: ptr(20)},
		},
		{
			name: "systemone without usage",
			next: nonStreamEngine[systemone.SystemOneResponse](`{"model":"m","answers":{}}`),
			want: nil,
		},
		{
			name: "no usage reported",
			next: nonStreamEngine[openai.ChatCompletionResponse](`{"id":"c1","choices":[]}`),
			want: nil,
		},
		{
			name: "unknown body type",
			next: nonStreamEngine[map[string]any](`{"usage":{"prompt_tokens":1}}`),
			want: nil,
		},
		{
			name: "unparsable body",
			next: nonStreamEngine[openai.ChatCompletionResponse](`not json`),
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newReq()
			resp, err := NewCollectorEngine(tt.next).Process(req)
			require.NoError(t, err)
			require.NotNil(t, resp)
			assert.Equal(t, tt.want, Get(req))
		})
	}
}

func TestCollector_Stream(t *testing.T) {
	tests := []struct {
		name  string
		next  octollm.Engine
		want  Usage
		count int
	}{
		{
			name: "chat completions usage on final chunk",
			next: streamEngine[openai.ChatCompletionStreamChunk](
				`{"id":"c1","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
				`{"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":4}}}`,
				`[DONE]`,
			),
			want: &ChatCompletions{PromptTokens: 10, CompletionTokens: 2,
				PromptTokensDetails: &openai.PromptTokensDetails{CachedTokens: ptr(4)}},
			count: 3,
		},
		{
			name: "chat completions continuous usage accumulates",
			next: streamEngine[openai.ChatCompletionStreamChunk](
				`{"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":4}}}`,
				`{"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
				`[DONE]`,
			),
			// cached_tokens omitted by the later chunk keeps its earlier value
			want: &ChatCompletions{PromptTokens: 10, CompletionTokens: 5,
				PromptTokensDetails: &openai.PromptTokensDetails{CachedTokens: ptr(4)}},
			count: 3,
		},
		{
			name: "claude usage split across message_start and message_delta",
			next: streamEngine[anthropic.ClaudeMessagesStreamEvent](
				`{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","content":[],"usage":{"input_tokens":5,"output_tokens":1,"cache_read_input_tokens":50,"cache_creation_input_tokens":40,"cache_creation":{"ephemeral_5m_input_tokens":40}}}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`,
				`{"type":"message_stop"}`,
			),
			want: &Claude{InputTokens: ptr[int64](5), OutputTokens: ptr[int64](9),
				CacheCreationInputTokens: ptr[int64](40), CacheReadInputTokens: ptr[int64](50),
				CacheCreation: &anthropic.CacheCreation{Ephemeral5mInputTokens: ptr[int64](40)}},
			count: 4,
		},
		{
			name: "responses usage on response.completed",
			next: streamEngine[openai.ResponseStreamChunk](
				`{"type":"response.output_text.delta","delta":"hi"}`,
				`{"type":"response.completed","response":{"id":"r1","object":"response","output":[],"usage":{"input_tokens":20,"output_tokens":3,"total_tokens":23,"input_tokens_details":{"cache_write_tokens":6}}}}`,
			),
			want: &Responses{InputTokens: 20, OutputTokens: 3, TotalTokens: 23,
				InputTokensDetails: &openai.ResponsesInputTokenDetails{CacheWriteTokens: ptr(6)}},
			count: 2,
		},
		{
			name: "vertex keeps the latest cumulative report",
			next: streamEngine[vertex.StreamGenerateContentResponse](
				`{"candidates":[],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":1}}`,
				`{"candidates":[],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":3,"totalTokenCount":15}}`,
			),
			want:  &Vertex{PromptTokenCount: 12, CandidatesTokenCount: 3, TotalTokenCount: 15},
			count: 2,
		},
		{
			name: "unparsable chunk is forwarded and skipped",
			next: streamEngine[openai.ChatCompletionStreamChunk](
				`garbage`,
				`{"id":"c1","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			),
			want:  &ChatCompletions{PromptTokens: 1, CompletionTokens: 1},
			count: 2,
		},
		{
			name: "no usage reported",
			next: streamEngine[openai.ChatCompletionStreamChunk](
				`{"id":"c1","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
				`[DONE]`,
			),
			want:  nil,
			count: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newReq()
			resp, err := NewCollectorEngine(tt.next).Process(req)
			require.NoError(t, err)
			assert.Equal(t, tt.count, drain(t, resp))
			assert.Equal(t, tt.want, Get(req))
		})
	}
}

// An outer engine that has received a chunk must already see that chunk's usage,
// without waiting for the stream to end.
func TestCollector_StreamUsageVisibleBeforeChunkForwarded(t *testing.T) {
	req := newReq()
	resp, err := NewCollectorEngine(streamEngine[openai.ChatCompletionStreamChunk](
		`{"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
		`[DONE]`,
	)).Process(req)
	require.NoError(t, err)

	<-resp.Stream.Chan()
	assert.Equal(t, &ChatCompletions{PromptTokens: 10, CompletionTokens: 2}, Get(req))

	drain(t, resp)
}

func TestCollector_ResetsOnEachAttempt(t *testing.T) {
	attempt := 0
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		attempt++
		if attempt == 1 {
			return nonStreamEngine[openai.ChatCompletionResponse](
				`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20}}`).Process(req)
		}
		return nil, errors.New("upstream down")
	})
	collector := NewCollectorEngine(next)
	req := newReq()

	_, err := collector.Process(req)
	require.NoError(t, err)
	require.NotNil(t, Get(req))

	// A retry on the same request must not report the earlier attempt's usage.
	_, err = collector.Process(req)
	require.Error(t, err)
	assert.Nil(t, Get(req))
}

func TestCollector_UsageSharedWithDerivedRequests(t *testing.T) {
	type ctxKey struct{}
	req := newReq()
	derived := req.WithContext(context.WithValue(req.Context(), ctxKey{}, 1))

	_, err := NewCollectorEngine(nonStreamEngine[openai.ChatCompletionResponse](
		`{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":4}}`)).Process(derived)
	require.NoError(t, err)

	assert.Equal(t, &ChatCompletions{PromptTokens: 3, CompletionTokens: 4}, Get(req))
}

func TestCollector_CloseReleasesGoroutine(t *testing.T) {
	src := make(chan *octollm.StreamChunk, 1)
	src <- &octollm.StreamChunk{Body: octollm.NewBodyFromBytes([]byte(`{"choices":[]}`),
		&octollm.JSONParser[openai.ChatCompletionStreamChunk]{})}
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		return octollm.NewStreamResponse(http.StatusOK, http.Header{},
			octollm.NewStreamChan(src, func() { close(src) })), nil
	})
	resp, err := NewCollectorEngine(next).Process(newReq())
	require.NoError(t, err)

	// Nobody reads the collector's output; Close must still release its goroutine
	// even though the request context is never canceled.
	resp.Stream.Close()
	done := make(chan struct{})
	go func() {
		for range resp.Stream.Chan() {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("collector output was not closed after Close")
	}
}

func TestGet_WithoutCollector(t *testing.T) {
	assert.Nil(t, Get(newReq()))
	assert.Nil(t, Get(nil))
}
