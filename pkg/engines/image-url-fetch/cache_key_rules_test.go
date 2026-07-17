package image_url_fetch

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/infinigence/octollm/pkg/engines/image-url-fetch/cache"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/stretchr/testify/require"
)

func TestCompileCacheKeyRules_invalidPattern(t *testing.T) {
	t.Parallel()
	_, err := compileCacheKeyRules([]CacheKeyRule{{
		Name:        "bad",
		Pattern:     "[",
		KeyTemplate: "x",
	}})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrImageInternal)
}

func TestNewRegexKeyDeriver_invalidCacheKeyRule(t *testing.T) {
	t.Parallel()
	_, err := NewRegexKeyDeriver([]CacheKeyRule{{
		Name:        "bad",
		Pattern:     "(",
		KeyTemplate: "x",
	}})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrImageInternal)
}

func TestRegexKeyDeriver_Key(t *testing.T) {
	t.Parallel()

	moonshotRule := CacheKeyRule{
		Name:        "moonshot_blobproxy",
		Pattern:     `^https://blobproxy\.moonshot\.cn/blobs/([^/?#]+)(?:[?#].*)?$`,
		KeyTemplate: `moonshot_blobproxy:$1`,
	}
	d, err := NewRegexKeyDeriver([]CacheKeyRule{moonshotRule})
	require.NoError(t, err)

	urlA := "https://blobproxy.moonshot.cn/blobs/k3.FlYqzKjeGN0Eh1bfo14_4zUceppK?sig=pGj&exp=1783500617&data=Tpq"
	urlB := "https://blobproxy.moonshot.cn/blobs/k3.FlYqzKjeGN0Eh1bfo14_4zUceppK?sig=PQP&exp=1783498614&data=QD9"
	urlC := "https://blobproxy.moonshot.cn/blobs/k3.FlYqzKjeGN0Eh1bfo14_4zUceppK?exp=1783498614&sig=PQP&data=QD9"
	urlOtherBlob := "https://blobproxy.moonshot.cn/blobs/other_blob_id?sig=xxx"
	nonWhitelist := "https://example.com/img.png?token=1"

	require.Equal(t, d.Key(urlA), d.Key(urlB))
	require.Equal(t, d.Key(urlA), d.Key(urlC), "query param order should not affect cache key")
	require.NotEqual(t, d.Key(urlA), d.Key(urlOtherBlob))
	require.Equal(t, cache.KeyForURL(nonWhitelist), d.Key(nonWhitelist))

	expected := cache.KeyForURL("moonshot_blobproxy:k3.FlYqzKjeGN0Eh1bfo14_4zUceppK")
	require.Equal(t, expected, d.Key(urlA))
}

func TestRegexKeyDeriver_firstMatchWins(t *testing.T) {
	t.Parallel()

	d, err := NewRegexKeyDeriver([]CacheKeyRule{
		{Name: "first", Pattern: `^https://example\.com/(.+)$`, KeyTemplate: "first:$1"},
		{Name: "second", Pattern: `^https://example\.com/(.+)$`, KeyTemplate: "second:$1"},
	})
	require.NoError(t, err)
	raw := "https://example.com/path/to/img.png"
	require.Equal(t, cache.KeyForURL("first:path/to/img.png"), d.Key(raw))
}

func TestImageURLFetchEngine_cacheKeyRules_signedURLsShareFileCache(t *testing.T) {
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

	blobID := "k3.FlYqzKjeGN0Eh1bfo14_4zUceppK"
	url1 := fmt.Sprintf("%s/blobs/%s?sig=aaa&exp=1", srv.URL, blobID)
	url2 := fmt.Sprintf("%s/blobs/%s?sig=bbb&exp=2", srv.URL, blobID)

	pattern := fmt.Sprintf(`^%s/blobs/([^/?#]+)(?:[?#].*)?$`, regexp.QuoteMeta(srv.URL))
	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		return octollm.NewNonStreamResponse(200, nil, octollm.NewBodyFromBytes([]byte(`{}`), nil)), nil
	})

	deriver, err := NewRegexKeyDeriver([]CacheKeyRule{{
		Name:        "test_blobproxy",
		Pattern:     pattern,
		KeyTemplate: "test_blobproxy:$1",
	}})
	require.NoError(t, err)

	eng, err := NewImageURLFetchEngine(ImageURLFetchConfig{
		Next:          next,
		HTTPClient:    srv.Client(),
		CacheMode:     CacheModeFile,
		CacheFileRoot: root,
		Deriver:       deriver,
	})
	require.NoError(t, err)

	raw1 := []byte(fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":%q}}]}]}`, url1))
	u1 := octollm.NewRequest(httptest.NewRequest(http.MethodPost, "/", nil), octollm.APIFormatChatCompletions)
	u1.Body = octollm.NewBodyFromBytes(raw1, testChatBodyParser)
	_, err = eng.Process(u1)
	require.NoError(t, err)
	require.Equal(t, 1, httpCalls)

	raw2 := []byte(fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":%q}}]}]}`, url2))
	u2 := octollm.NewRequest(httptest.NewRequest(http.MethodPost, "/", nil), octollm.APIFormatChatCompletions)
	u2.Body = octollm.NewBodyFromBytes(raw2, testChatBodyParser)
	_, err = eng.Process(u2)
	require.NoError(t, err)
	require.Equal(t, 1, httpCalls, "different signed URLs for same blob should share cache key")
}

func TestImageURLFetchEngine_cacheKeyRules_differentQueryWithoutRule(t *testing.T) {
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

	url1 := srv.URL + "/img.png?token=1"
	url2 := srv.URL + "/img.png?token=2"

	next := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		return octollm.NewNonStreamResponse(200, nil, octollm.NewBodyFromBytes([]byte(`{}`), nil)), nil
	})

	eng, err := NewImageURLFetchEngine(ImageURLFetchConfig{
		Next:          next,
		HTTPClient:    srv.Client(),
		CacheMode:     CacheModeFile,
		CacheFileRoot: root,
	})
	require.NoError(t, err)

	raw1 := []byte(fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":%q}}]}]}`, url1))
	u1 := octollm.NewRequest(httptest.NewRequest(http.MethodPost, "/", nil), octollm.APIFormatChatCompletions)
	u1.Body = octollm.NewBodyFromBytes(raw1, testChatBodyParser)
	_, err = eng.Process(u1)
	require.NoError(t, err)

	raw2 := []byte(fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":%q}}]}]}`, url2))
	u2 := octollm.NewRequest(httptest.NewRequest(http.MethodPost, "/", nil), octollm.APIFormatChatCompletions)
	u2.Body = octollm.NewBodyFromBytes(raw2, testChatBodyParser)
	_, err = eng.Process(u2)
	require.NoError(t, err)
	require.Equal(t, 2, httpCalls, "without cache key rules, different query strings should not share cache")
}
