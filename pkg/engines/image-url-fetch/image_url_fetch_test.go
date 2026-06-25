package image_url_fetch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/infinigence/octollm/pkg/engines/image-url-fetch/metrics"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

var testChatBodyParser = &octollm.JSONParser[openai.ChatCompletionRequest]{}

var testClaudeBodyParser = &octollm.JSONParser[anthropic.ClaudeMessagesRequest]{}

func TestImageURLFetchEngine_inlineObjectForm(t *testing.T) {
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	t.Cleanup(srv.Close)

	raw := []byte(fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":%q}}]}]}`, srv.URL+"/x"))

	var nextBody []byte
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		b, err := req.Body.Bytes()
		require.NoError(t, err)
		nextBody = b
		return octollm.NewNonStreamResponse(200, nil, octollm.NewBodyFromBytes([]byte(`{}`), nil)), nil
	})

	eng, err := NewImageURLFetchEngine(ImageURLFetchConfig{
		Next:       next,
		HTTPClient: srv.Client(),
		Timeout:    5 * time.Second,
	})
	require.NoError(t, err)
	u := octollm.NewRequest(httptest.NewRequest(http.MethodPost, "/", nil), octollm.APIFormatChatCompletions)
	u.Body = octollm.NewBodyFromBytes(raw, testChatBodyParser)

	_, err = eng.Process(u)
	require.NoError(t, err)

	var out openai.ChatCompletionRequest
	require.NoError(t, json.Unmarshal(nextBody, &out))
	arr, ok := out.Messages[0].Content.(openai.MessageContentArray)
	require.True(t, ok)
	url := arr[0].ImageURL.GetImageUrl()
	require.True(t, len(url) > 20)
	require.Contains(t, url, "data:image/png;base64,")
	require.Equal(t, base64.StdEncoding.EncodeToString(pngBytes), url[len("data:image/png;base64,"):])
}

func TestImageURLFetchEngine_inlineStringForm(t *testing.T) {
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	t.Cleanup(srv.Close)

	raw := []byte(fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":%q}]}]}`, srv.URL+"/pic"))

	var nextBody []byte
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		b, err := req.Body.Bytes()
		require.NoError(t, err)
		nextBody = b
		return octollm.NewNonStreamResponse(200, nil, octollm.NewBodyFromBytes([]byte(`{}`), nil)), nil
	})

	eng, err := NewImageURLFetchEngine(ImageURLFetchConfig{
		Next:       next,
		HTTPClient: srv.Client(),
		Timeout:    5 * time.Second,
	})
	require.NoError(t, err)
	u := octollm.NewRequest(httptest.NewRequest(http.MethodPost, "/", nil), octollm.APIFormatChatCompletions)
	u.Body = octollm.NewBodyFromBytes(raw, testChatBodyParser)

	_, err = eng.Process(u)
	require.NoError(t, err)

	var out openai.ChatCompletionRequest
	require.NoError(t, json.Unmarshal(nextBody, &out))
	arr, ok := out.Messages[0].Content.(openai.MessageContentArray)
	require.True(t, ok)
	url := arr[0].ImageURL.GetImageUrl()
	require.Contains(t, url, "data:image/png;base64,")
	require.Contains(t, string(nextBody), `"image_url":{"url":`)
}

// String-form image_url (including data URLs) is normalized to {"url":...} in serialized body.
func TestImageURLFetchEngine_normalizesStringImageURLToObjectForm_dataURL(t *testing.T) {
	embeddedPNG := "data:image/png;base64,iVBORw0KGgo="
	raw := []byte(fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":%q}]}]}`, embeddedPNG))

	var nextBody []byte
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		b, err := req.Body.Bytes()
		require.NoError(t, err)
		nextBody = b
		return octollm.NewNonStreamResponse(200, nil, octollm.NewBodyFromBytes([]byte(`{}`), nil)), nil
	})

	eng, err := NewImageURLFetchEngine(ImageURLFetchConfig{Next: next})
	require.NoError(t, err)
	u := octollm.NewRequest(httptest.NewRequest(http.MethodPost, "/", nil), octollm.APIFormatChatCompletions)
	u.Body = octollm.NewBodyFromBytes(raw, testChatBodyParser)

	_, err = eng.Process(u)
	require.NoError(t, err)

	require.Contains(t, string(nextBody), `"image_url":{"url":`)
	require.Contains(t, string(nextBody), embeddedPNG)
}

// Non–chat-completion parsed body yields no jobs; request is passed to next unchanged.
func TestImageURLFetchEngine_unsupportedParsedTypePassesThrough(t *testing.T) {
	called := false
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		called = true
		b, err := req.Body.Bytes()
		require.NoError(t, err)
		require.JSONEq(t, `{"model":"ada","input":"x"}`, string(b))
		return octollm.NewNonStreamResponse(200, nil, octollm.NewBodyFromBytes([]byte(`{}`), nil)), nil
	})
	eng, err := NewImageURLFetchEngine(ImageURLFetchConfig{Next: next})
	require.NoError(t, err)
	raw := []byte(`{"model":"ada","input":"x"}`)
	u := octollm.NewRequest(httptest.NewRequest(http.MethodPost, "/", nil), octollm.APIFormatChatCompletions)
	u.Body = octollm.NewBodyFromBytes(raw, &octollm.JSONParser[openai.EmbeddingRequest]{})

	_, err = eng.Process(u)
	require.NoError(t, err)
	require.True(t, called)
}

func TestImageURLFetchEngine_nilBodyPassesThrough(t *testing.T) {
	called := false
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		called = true
		return octollm.NewNonStreamResponse(200, nil, octollm.NewBodyFromBytes([]byte(`{}`), nil)), nil
	})
	eng, err := NewImageURLFetchEngine(ImageURLFetchConfig{Next: next})
	require.NoError(t, err)
	u := octollm.NewRequest(httptest.NewRequest(http.MethodPost, "/", nil), octollm.APIFormatChatCompletions)
	u.Body = nil

	_, err = eng.Process(u)
	require.NoError(t, err)
	require.True(t, called)
}

func TestImageURLFetchEngine_configDefaultsHTTPClient(t *testing.T) {
	// Timeout 0 -> 10s default inside constructor; HTTPClient nil -> http.DefaultClient
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		return octollm.NewNonStreamResponse(200, nil, octollm.NewBodyFromBytes([]byte(`{}`), nil)), nil
	})
	eng, err := NewImageURLFetchEngine(ImageURLFetchConfig{Next: next})
	require.NoError(t, err)
	require.NotNil(t, eng.HTTPClient)
	require.Equal(t, 0, eng.retryCount)
	require.Equal(t, 10*time.Second, eng.timeout)
}

func TestImageURLFetchEngine_negativeRetryClamped(t *testing.T) {
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		return octollm.NewNonStreamResponse(200, nil, octollm.NewBodyFromBytes([]byte(`{}`), nil)), nil
	})
	eng, err := NewImageURLFetchEngine(ImageURLFetchConfig{Next: next, RetryCount: -3})
	require.NoError(t, err)
	require.Equal(t, 0, eng.retryCount)
}

func TestImageURLFetchEngine_CacheModeFile_requiresRoot(t *testing.T) {
	t.Parallel()
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		return octollm.NewNonStreamResponse(200, nil, octollm.NewBodyFromBytes([]byte(`{}`), nil)), nil
	})
	_, err := NewImageURLFetchEngine(ImageURLFetchConfig{Next: next, CacheMode: CacheModeFile})
	require.Error(t, err)
}

func TestImageURLFetchEngine_telemetryMode_noPayloadCache(t *testing.T) {
	t.Parallel()
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	var httpCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	t.Cleanup(srv.Close)

	raw := []byte(fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":%q}}]}]}`, srv.URL+"/x"))

	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		return octollm.NewNonStreamResponse(200, nil, octollm.NewBodyFromBytes([]byte(`{}`), nil)), nil
	})
	reg := prometheus.NewRegistry()
	met, err := metrics.New(reg)
	require.NoError(t, err)

	eng, err := NewImageURLFetchEngine(ImageURLFetchConfig{
		Next:       next,
		HTTPClient: srv.Client(),
		Timeout:    5 * time.Second,
		CacheMode:  CacheModeTelemetry,
		Metrics:    met,
	})
	require.NoError(t, err)

	u := octollm.NewRequest(httptest.NewRequest(http.MethodPost, "/", nil), octollm.APIFormatChatCompletions)
	u.Body = octollm.NewBodyFromBytes(raw, testChatBodyParser)
	_, err = eng.Process(u)
	require.NoError(t, err)
	u2 := octollm.NewRequest(httptest.NewRequest(http.MethodPost, "/", nil), octollm.APIFormatChatCompletions)
	u2.Body = octollm.NewBodyFromBytes(raw, testChatBodyParser)
	_, err = eng.Process(u2)
	require.NoError(t, err)
	require.Equal(t, 2, httpCalls, "telemetry mode still uses HTTP each time; no payload cache")

	families, err := reg.Gather()
	require.NoError(t, err)
	var hits, fetches float64
	for _, fam := range families {
		switch fam.GetName() {
		case "image_url_fetch_cache_hits_total":
			for _, m := range fam.GetMetric() {
				hits += m.GetCounter().GetValue()
			}
		case "image_url_fetch_http_fetches_total":
			for _, m := range fam.GetMetric() {
				fetches += m.GetCounter().GetValue()
			}
		}
	}
	require.Equal(t, 1.0, hits, "second request is repeat URL → cache_hits_total without storing bytes")
	require.Equal(t, 1.0, fetches, "only first Process uses engine HTTPClient; second is store hit (IndexHTTPStore still GETs origin for telemetry)")
}

func TestImageURLFetchEngine_fileCache_avoidsSecondHTTP(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	var httpCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	t.Cleanup(srv.Close)

	raw := []byte(fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":%q}}]}]}`, srv.URL+"/x"))

	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		return octollm.NewNonStreamResponse(200, nil, octollm.NewBodyFromBytes([]byte(`{}`), nil)), nil
	})
	reg := prometheus.NewRegistry()
	met, err := metrics.New(reg)
	require.NoError(t, err)

	eng, err := NewImageURLFetchEngine(ImageURLFetchConfig{
		Next:          next,
		HTTPClient:    srv.Client(),
		Timeout:       5 * time.Second,
		CacheMode:     CacheModeFile,
		CacheFileRoot: root,
		Metrics:       met,
	})
	require.NoError(t, err)

	u := octollm.NewRequest(httptest.NewRequest(http.MethodPost, "/", nil), octollm.APIFormatChatCompletions)
	u.Body = octollm.NewBodyFromBytes(raw, testChatBodyParser)
	_, err = eng.Process(u)
	require.NoError(t, err)
	require.Equal(t, 1, httpCalls)

	u2 := octollm.NewRequest(httptest.NewRequest(http.MethodPost, "/", nil), octollm.APIFormatChatCompletions)
	u2.Body = octollm.NewBodyFromBytes(raw, testChatBodyParser)
	_, err = eng.Process(u2)
	require.NoError(t, err)
	require.Equal(t, 1, httpCalls, "second request should hit on-disk cache")

	families, err := reg.Gather()
	require.NoError(t, err)
	var hits, fetches float64
	for _, fam := range families {
		switch fam.GetName() {
		case "image_url_fetch_cache_hits_total":
			for _, m := range fam.GetMetric() {
				hits += m.GetCounter().GetValue()
			}
		case "image_url_fetch_http_fetches_total":
			for _, m := range fam.GetMetric() {
				fetches += m.GetCounter().GetValue()
			}
		}
	}
	require.Equal(t, 1.0, fetches)
	require.Equal(t, 1.0, hits)
}

func TestExtractImageReplaceJobsFromBody_embeddingTypeNoJobs(t *testing.T) {
	raw := []byte(`{"model":"ada","input":"hi"}`)
	body := octollm.NewBodyFromBytes(raw, &octollm.JSONParser[openai.EmbeddingRequest]{})
	jobs, err := extractImageReplaceJobsFromBody(context.Background(), body)
	require.NoError(t, err)
	require.Empty(t, jobs)
}

// Multiple messages; per-message content mixes text, data URLs (no fetch), object and string image_url,
// reasoning_content, and a duplicate HTTP URL (fetched once, replaced in two places).
func TestImageURLFetchEngine_multiMessagesMixedImageParts(t *testing.T) {
	png1 := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00}
	png2 := []byte{0x89, 0x50, 0x4e, 0x47, 0x01, 0x02}
	png3 := []byte{0x89, 0x50, 0x4e, 0x47, 0x03, 0x04, 0x05}

	mux := http.NewServeMux()
	mux.HandleFunc("/a.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png1)
	})
	mux.HandleFunc("/b.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png2)
	})
	mux.HandleFunc("/c.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png3)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	urlA := srv.URL + "/a.png"
	urlB := srv.URL + "/b.png"
	urlC := srv.URL + "/c.png"

	// 1x1 PNG already inlined; engine must skip fetch.
	embeddedPNG := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	embeddedJPEG := "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQEASABIAAD/2wBDAP//////////////////////////////////////////////////////////////////////////////////////2wBDAf//////////////////////////////////////////////////////////////////////////////////////wAARCAABAAEDASIAAhEBAxEB/8QAFQABAQAAAAAAAAAAAAAAAAAAAAn/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/8QAFAEBAAAAAAAAAAAAAAAAAAAAAP/EABQRAQAAAAAAAAAAAAAAAAAAAAD/2gAMAwEAAhEDEQA/AL+AD//Z"

	payload := map[string]interface{}{
		"model":       "gpt-4o-mini",
		"temperature": 0.2,
		"stream":      false,
		"messages": []interface{}{
			map[string]interface{}{
				"role": "system",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "sys"},
				},
			},
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "hello"},
					map[string]interface{}{"type": "image_url", "image_url": embeddedPNG},
					map[string]interface{}{
						"type": "image_url",
						"image_url": map[string]interface{}{
							"url":    urlA,
							"detail": "auto",
						},
					},
					map[string]interface{}{"type": "text", "text": "mid"},
					map[string]interface{}{"type": "image_url", "image_url": urlB},
				},
			},
			map[string]interface{}{
				"role":    "assistant",
				"content": "only string body",
			},
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "image_url", "image_url": urlC},
					map[string]interface{}{"type": "text", "text": "after image"},
					map[string]interface{}{"type": "image_url", "image_url": embeddedJPEG},
				},
				"reasoning_content": []interface{}{
					map[string]interface{}{"type": "text", "text": "think"},
					map[string]interface{}{
						"type":      "image_url",
						"image_url": map[string]interface{}{"url": urlA},
					},
					map[string]interface{}{"type": "image_url", "image_url": embeddedJPEG},
				},
			},
		},
	}

	raw, err := json.Marshal(payload)
	require.NoError(t, err)

	var nextBody []byte
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		b, err := req.Body.Bytes()
		require.NoError(t, err)
		nextBody = b
		return octollm.NewNonStreamResponse(200, nil, octollm.NewBodyFromBytes([]byte(`{}`), nil)), nil
	})

	eng, err := NewImageURLFetchEngine(ImageURLFetchConfig{
		Next:       next,
		HTTPClient: srv.Client(),
		Timeout:    10 * time.Second,
	})
	require.NoError(t, err)
	u := octollm.NewRequest(httptest.NewRequest(http.MethodPost, "/", nil), octollm.APIFormatChatCompletions)
	u.Body = octollm.NewBodyFromBytes(raw, testChatBodyParser)

	_, err = eng.Process(u)
	require.NoError(t, err)

	var out openai.ChatCompletionRequest
	require.NoError(t, json.Unmarshal(nextBody, &out))
	require.Len(t, out.Messages, 4)

	// Message 1 — user: array indices 1,2,4 are image parts; index 1 is data URL and must be unchanged.
	u0 := out.Messages[1].Content.(openai.MessageContentArray)
	require.Equal(t, embeddedPNG, u0[1].ImageURL.GetImageUrl())
	wantB64A := base64.StdEncoding.EncodeToString(png1)
	require.Contains(t, u0[2].ImageURL.GetImageUrl(), "data:image/png;base64,"+wantB64A)
	require.Contains(t, u0[4].ImageURL.GetImageUrl(), base64.StdEncoding.EncodeToString(png2))

	// Message 2 — assistant plain string content.
	asStr, ok := out.Messages[2].Content.(openai.MessageContentString)
	require.True(t, ok)
	require.Contains(t, string(asStr), "only string")

	// Message 3 — user: content[0] is /c.png (fetched); content[2] embedded JPEG unchanged;
	// reasoning: [1] same urlA as above should be inlined; [2] data URL unchanged.
	u3 := out.Messages[3].Content.(openai.MessageContentArray)
	require.Contains(t, u3[0].ImageURL.GetImageUrl(), base64.StdEncoding.EncodeToString(png3))
	require.Equal(t, embeddedJPEG, u3[2].ImageURL.GetImageUrl())

	// reasoning_content
	reason, ok := out.Messages[3].ReasoningContent.(openai.MessageContentArray)
	require.True(t, ok)
	require.Contains(t, reason[1].ImageURL.GetImageUrl(), "data:image/png;base64,"+wantB64A)
	require.Equal(t, embeddedJPEG, reason[2].ImageURL.GetImageUrl())
}

func TestImageURLFetchEngine_claudeTopLevelAndToolResult(t *testing.T) {
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	t.Cleanup(srv.Close)

	topURL := srv.URL + "/top.png"
	nestedURL := srv.URL + "/nested.png"
	secondMsgURL := srv.URL + "/msg1.png"
	raw := []byte(fmt.Sprintf(`{"model":"m","max_tokens":100,"messages":[
		{"role":"user","content":[
			{"type":"text","text":"x"},
			{"type":"image","source":{"type":"url","url":%q}},
			{"type":"tool_result","tool_use_id":"tu_1","content":[
				{"type":"image","source":{"type":"url","url":%q}}
			]}
		]},
		{"role":"user","content":[
			{"type":"image","source":{"type":"url","url":%q}}
		]}
	]}`, topURL, nestedURL, secondMsgURL))

	var nextBody []byte
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		b, err := req.Body.Bytes()
		require.NoError(t, err)
		nextBody = b
		return octollm.NewNonStreamResponse(200, nil, octollm.NewBodyFromBytes([]byte(`{}`), nil)), nil
	})

	eng, err := NewImageURLFetchEngine(ImageURLFetchConfig{
		Next:       next,
		HTTPClient: srv.Client(),
		Timeout:    5 * time.Second,
	})
	require.NoError(t, err)
	u := octollm.NewRequest(httptest.NewRequest(http.MethodPost, "/", nil), octollm.APIFormatClaudeMessages)
	u.Body = octollm.NewBodyFromBytes(raw, testClaudeBodyParser)

	_, err = eng.Process(u)
	require.NoError(t, err)

	var out anthropic.ClaudeMessagesRequest
	require.NoError(t, json.Unmarshal(nextBody, &out))
	require.Len(t, out.Messages, 2)

	msg0Content, ok := out.Messages[0].Content.(anthropic.MessageContentBlockArray)
	require.True(t, ok)
	require.Len(t, msg0Content, 3)

	msg1Content, ok := out.Messages[1].Content.(anthropic.MessageContentBlockArray)
	require.True(t, ok)
	require.Len(t, msg1Content, 1)

	topImg, ok := msg0Content[1].(*anthropic.ImageBlockParam)
	require.True(t, ok)
	require.NotNil(t, topImg.Source)
	require.Equal(t, "base64", topImg.Source.Type)
	require.Equal(t, "image/png", topImg.Source.MediaType)

	nestedTool, ok := msg0Content[2].(*anthropic.ToolResultBlockParam)
	require.True(t, ok)
	nestedContent, ok := nestedTool.Content.(anthropic.MessageContentBlockArray)
	require.True(t, ok)
	require.Len(t, nestedContent, 1)
	nestedImg, ok := nestedContent[0].(*anthropic.ImageBlockParam)
	require.True(t, ok)
	wantB64 := base64.StdEncoding.EncodeToString(pngBytes)
	var topData, nestedData string
	require.NoError(t, json.Unmarshal(topImg.Source.Data, &topData))
	require.NoError(t, json.Unmarshal(nestedImg.Source.Data, &nestedData))
	require.Equal(t, wantB64, topData)
	require.Equal(t, "base64", nestedImg.Source.Type)
	require.Equal(t, "image/png", nestedImg.Source.MediaType)
	require.Equal(t, wantB64, nestedData)

	secondImg, ok := msg1Content[0].(*anthropic.ImageBlockParam)
	require.True(t, ok)
	require.NotNil(t, secondImg.Source)
	require.Equal(t, "base64", secondImg.Source.Type)
	require.Equal(t, "image/png", secondImg.Source.MediaType)
	var secondData string
	require.NoError(t, json.Unmarshal(secondImg.Source.Data, &secondData))
	require.Equal(t, wantB64, secondData)
}
