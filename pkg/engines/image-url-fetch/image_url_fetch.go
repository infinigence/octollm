package image_url_fetch

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/buger/jsonparser"
	"github.com/infinigence/octollm/pkg/engines/image-url-fetch/cache"
	"github.com/infinigence/octollm/pkg/engines/image-url-fetch/limits"
	"github.com/infinigence/octollm/pkg/engines/image-url-fetch/metrics"
	"github.com/infinigence/octollm/pkg/engines/image-url-fetch/readbody"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
)

var ErrImageURLFetch = errors.New("image url fetch engine")

// ImageURLFetchEngine downloads remote images and inlines them in the raw JSON body via jsonparser.Set.
// OpenAI Chat Completions: image_url fields become object-form data:...;base64,... URLs.
// Anthropic Messages: image source.type "url" is replaced by the official base64 source object (type, media_type, data).
// Array indices in paths must use bracket syntax ("[0]", "[1]"); see jsonparser.searchKeys.
// URL discovery uses collectOpenAIImageReplaceJobs / collectClaudeImageReplaceJobs (extract.go).
type ImageURLFetchEngine struct {
	Next       octollm.Engine
	HTTPClient *http.Client

	retryCount int
	timeout    time.Duration

	// MaxBytesPerURL is the maximum decoded image size per remote URL; 0 disables.
	maxBytesPerURL int64
	// MaxBytesPerRequest is the maximum sum of decoded sizes for unique remote URLs in one request; 0 disables.
	maxBytesPerRequest int64
	notifier           limits.ImageFetchLimitNotifier

	store   cache.Store
	metrics *metrics.M
}

// CacheMode selects how remote image bytes are cached when Cache is nil.
type CacheMode int

const (
	// CacheModeNone disables caching (always fetch over HTTP).
	CacheModeNone CacheMode = iota
	// CacheModeTelemetry uses IndexHTTPStore: only an in-memory index; Get refetches over HTTP.
	CacheModeTelemetry
	// CacheModeFile persists payloads under CacheFileRoot on disk.
	CacheModeFile
)

var _ octollm.Engine = (*ImageURLFetchEngine)(nil)

// ImageURLFetchConfig configures ImageURLFetchEngine. Zero values get defaults in NewImageURLFetchEngine.
type ImageURLFetchConfig struct {
	Next octollm.Engine
	// HTTPClient fetches remote images; nil uses http.DefaultClient.
	HTTPClient *http.Client
	// RetryCount is extra attempts after the first (e.g. 1 => up to 2 tries). Negative values are clamped to 0.
	RetryCount int
	// Timeout is per fetch attempt. Zero defaults to 10s.
	Timeout time.Duration
	// Limits configures per-URL / per-request byte caps and optional notifier. Zero value disables limits.
	Limits limits.ImageURLFetchLimits

	// Cache, if non-nil, is used as the cache store; CacheMode and CacheFileRoot are ignored.
	Cache cache.Store
	// CacheMode selects a built-in store when Cache is nil. Default is CacheModeNone.
	CacheMode CacheMode
	// CacheFileRoot is the on-disk root when CacheMode is CacheModeFile (required for that mode).
	CacheFileRoot string

	// Metrics is optional Prometheus instrumentation; nil disables.
	Metrics *metrics.M
}

// NewImageURLFetchEngine wraps cfg.Next. Applies defaults: HTTPClient -> DefaultClient, Timeout -> 10s, RetryCount >= 0.
// Returns an error when CacheModeFile has an empty CacheFileRoot, or when NewFileStore / NewIndexHTTPStore fails.
func NewImageURLFetchEngine(cfg ImageURLFetchConfig) (*ImageURLFetchEngine, error) {
	retry := cfg.RetryCount
	if retry < 0 {
		retry = 0
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	var store cache.Store
	if cfg.Cache != nil {
		store = cfg.Cache
	} else if cfg.CacheMode == CacheModeTelemetry {
		ix, err := cache.NewIndexHTTPStore(cache.IndexHTTPStoreConfig{
			HTTPClient:     client,
			MaxBytesPerURL: cfg.Limits.MaxBytesPerURL,
			Metrics:        cfg.Metrics,
		})
		if err != nil {
			return nil, err
		}
		store = ix
	} else if cfg.CacheMode == CacheModeFile {
		root := strings.TrimSpace(cfg.CacheFileRoot)
		if root == "" {
			return nil, fmt.Errorf("%w: CacheModeFile requires non-empty CacheFileRoot", ErrImageURLFetch)
		}
		fs, err := cache.NewFileStore(root)
		if err != nil {
			return nil, err
		}
		store = fs
	}

	return &ImageURLFetchEngine{
		Next:               cfg.Next,
		HTTPClient:         client,
		retryCount:         retry,
		timeout:            timeout,
		maxBytesPerURL:     cfg.Limits.MaxBytesPerURL,
		maxBytesPerRequest: cfg.Limits.MaxBytesPerRequest,
		notifier:           cfg.Limits.Notifier,
		store:              store,
		metrics:            cfg.Metrics,
	}, nil
}

func (e *ImageURLFetchEngine) notifyLimit(ctx context.Context, ev limits.ImageLimitEvent) {
	if e == nil || e.notifier == nil {
		return
	}
	_ = e.notifier.OnLimitExceeded(ctx, ev)
}

func (e *ImageURLFetchEngine) Store() cache.Store {
	return e.store
}

// extractImageReplaceJobsFromBody inspects the unified body like moderator.OpenAIAdapter.ExtractTextFromBody:
// it uses Parsed() and switches on concrete types; *openai.ChatCompletionRequest and *anthropic.ClaudeMessagesRequest yield image jobs.
// A nil body or unsupported parsed type is logged and yields (nil, nil) so the engine passes the request through.
// The body must have a non-nil Parser (e.g. JSONParser[openai.ChatCompletionRequest]); Parsed() calls parser.Parse and a nil parser panics.
func extractImageReplaceJobsFromBody(ctx context.Context, body *octollm.UnifiedBody) ([]imageReplaceJob, error) {
	if body == nil {
		slog.WarnContext(ctx, "[ImageURLFetchEngine] request body is nil; skip image inlining")
		return nil, nil
	}

	parsed, err := body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("%w: parse body error: %w", ErrImageURLFetch, err)
	}
	switch p := parsed.(type) {
	case *openai.ChatCompletionRequest:
		return collectOpenAIImageReplaceJobs(p), nil
	case *anthropic.ClaudeMessagesRequest:
		return collectClaudeImageReplaceJobs(p), nil
	default:
		slog.WarnContext(ctx, "[ImageURLFetchEngine] unsupported body type for image inlining; skip",
			slog.String("type", fmt.Sprintf("%T", parsed)))
		return nil, nil
	}
}

func (e *ImageURLFetchEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	jobs, err := extractImageReplaceJobsFromBody(req.Context(), req.Body)
	if err != nil {
		return nil, err
	}
	if len(jobs) == 0 {
		return e.Next.Process(req)
	}

	body, err := req.Body.Bytes()
	if err != nil {
		return nil, fmt.Errorf("%w: get request body bytes error: %w", ErrImageURLFetch, err)
	}

	unique := make(map[string]struct{})
	out := body
	modified := false
	for _, j := range jobs {
		u := j.remoteURL()
		if u == "" {
			continue
		}
		if isDataURL(u) {
			path, encoded, ok, nerr := j.normalizeExistingURL()
			if nerr != nil {
				return nil, fmt.Errorf("%w: marshal existing image URL: %w", ErrImageURLFetch, nerr)
			}
			if ok {
				var setErr error
				out, setErr = jsonparser.Set(out, encoded, path...)
				if setErr != nil {
					return nil, fmt.Errorf("%w: json normalize at %v: %w", ErrImageURLFetch, path, setErr)
				}
				modified = true
			}
			continue
		}
		unique[u] = struct{}{}
	}
	if len(unique) == 0 {
		if modified {
			req.Body.SetBytes(out)
		}
		return e.Next.Process(req)
	}

	result := make(map[string]fetchImageResult, len(unique))
	var mu sync.Mutex
	var wg sync.WaitGroup
	ctx := req.Context()
	for u := range unique {
		wg.Add(1)
		go func(imageURL string) {
			defer wg.Done()
			mediaType, rawB64, n, ferr := e.fetchRemoteImage(ctx, imageURL)
			mu.Lock()
			result[imageURL] = fetchImageResult{mediaType: mediaType, rawBase64: rawB64, decodedBytes: n, err: ferr}
			mu.Unlock()
		}(u)
	}
	wg.Wait()

	for u, r := range result {
		if r.err != nil {
			if e.metrics != nil {
				e.metrics.IncRejectedDueToImageDownload()
			}
			return nil, fmt.Errorf("%w: fetch image %q: %w", ErrImageURLFetch, u, r.err)
		}
	}

	var sumDecoded int64
	for u := range unique {
		sumDecoded += result[u].decodedBytes
	}
	if maxReq := e.maxBytesPerRequest; maxReq > 0 && sumDecoded > maxReq {
		e.notifyLimit(ctx, limits.ImageLimitEvent{
			Kind:        limits.ImageLimitPerRequest,
			LimitBytes:  maxReq,
			ActualBytes: sumDecoded,
		})
		return nil, fmt.Errorf("%w: total image bytes %d exceeds limit %d: %w", ErrImageURLFetch, sumDecoded, maxReq, limits.ErrTotalImageSizeExceeded)
	}
	if e.metrics != nil {
		e.metrics.ObserveRequestSumBytes(sumDecoded)
	}

	for _, job := range jobs {
		u := job.remoteURL()
		if u == "" || isDataURL(u) {
			continue
		}
		fr := result[u]
		if fr.mediaType == "" && fr.rawBase64 == "" {
			continue
		}
		path, encoded, mErr := job.inlineReplacement(fr.mediaType, fr.rawBase64)
		if mErr != nil {
			return nil, fmt.Errorf("%w: marshal inline payload: %w", ErrImageURLFetch, mErr)
		}
		var setErr error
		out, setErr = jsonparser.Set(out, encoded, path...)
		if setErr != nil {
			return nil, fmt.Errorf("%w: json replace at %v: %w", ErrImageURLFetch, path, setErr)
		}
	}

	req.Body.SetBytes(out)
	slog.DebugContext(req.Context(), "[ImageURLFetchEngine] inlined remote image parts")

	resp, err := e.Next.Process(req)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

type fetchImageResult struct {
	mediaType    string
	rawBase64    string
	decodedBytes int64
	err          error
}

func isDataURL(u string) bool {
	return strings.HasPrefix(strings.ToLower(u), "data:")
}

func (e *ImageURLFetchEngine) fetchRemoteImage(ctx context.Context, url string) (mediaType string, rawBase64 string, decodedBytes int64, err error) {
	maxAttempts := e.retryCount + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		mt, b64, n, err := e.fetchRemoteImageOnce(ctx, url)
		if err == nil {
			return mt, b64, n, nil
		}
		lastErr = err
	}
	return "", "", 0, lastErr
}

func (e *ImageURLFetchEngine) fetchRemoteImageOnce(ctx context.Context, url string) (mediaType string, rawBase64 string, decodedBytes int64, err error) {
	attemptTimeout := e.timeout
	if attemptTimeout <= 0 {
		attemptTimeout = 10 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()

	if e.store != nil {
		key := cache.KeyForURL(url)
		if data, meta, ok, gerr := e.store.Get(reqCtx, key); gerr != nil {
			return "", "", 0, gerr
		} else if ok {
			// Cache/index hit: do not ObserveDecodedBytes/IncHTTPFetches here to avoid double-counting size
			// and to keep http_fetches_total as engine-origin HTTP only (see metrics package comment).
			if e.metrics != nil {
				e.metrics.IncCacheHits()
			}
			ct := readbody.NormalizeImageContentType(meta.ContentType)
			b64 := base64.StdEncoding.EncodeToString(data)
			return ct, b64, int64(len(data)), nil
		}
	}

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", 0, err
	}

	httpStart := time.Now()
	resp, err := e.HTTPClient.Do(httpReq)
	if err != nil {
		return "", "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	maxBytesPerURL := e.maxBytesPerURL
	if maxBytesPerURL > 0 {
		if cl := readbody.ParseContentLength(resp); cl >= 0 && cl > maxBytesPerURL {
			e.notifyLimit(ctx, limits.ImageLimitEvent{
				Kind:        limits.ImageLimitPerURL,
				ImageURL:    url,
				LimitBytes:  maxBytesPerURL,
				ActualBytes: cl,
			})
			return "", "", 0, fmt.Errorf("Content-Length %d exceeds limit %d: %w", cl, maxBytesPerURL, limits.ErrPerImageSizeExceeded)
		}
	}

	contentType := readbody.NormalizeImageContentType(resp.Header.Get("Content-Type"))

	data, err := readbody.ReadLimited(resp.Body, maxBytesPerURL)
	if err != nil {
		return "", "", 0, err
	}
	if maxBytesPerURL > 0 && int64(len(data)) > maxBytesPerURL {
		n := int64(len(data))
		e.notifyLimit(ctx, limits.ImageLimitEvent{
			Kind:        limits.ImageLimitPerURL,
			ImageURL:    url,
			LimitBytes:  maxBytesPerURL,
			ActualBytes: n,
		})
		return "", "", 0, fmt.Errorf("body size %d exceeds limit %d: %w", len(data), maxBytesPerURL, limits.ErrPerImageSizeExceeded)
	}

	if e.metrics != nil {
		e.metrics.ObserveHTTPFetchDuration(time.Since(httpStart))
		e.metrics.ObserveDecodedBytes(int64(len(data)))
		e.metrics.IncHTTPFetches()
	}
	k := cache.KeyForURL(url)
	if e.store != nil {
		if putErr := e.store.Put(reqCtx, k, data, cache.Meta{ContentType: contentType, SourceURL: url}); putErr != nil {
			slog.WarnContext(ctx, "[ImageURLFetchEngine] cache Put failed; continuing without persisting this image",
				slog.String("err", putErr.Error()),
				slog.String("key", k),
				slog.String("url", url))
		}
	}

	b64 := base64.StdEncoding.EncodeToString(data)
	return contentType, b64, int64(len(data)), nil
}
