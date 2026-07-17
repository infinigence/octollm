// Package metrics exposes Prometheus instruments for the image URL fetch engine.
// Units: bytes for sizes; HTTP fetch wall time in milliseconds (typical for download latency dashboards).
// Callers may wrap the Registerer (namespace, labels, buckets) before passing it to New.
//
// Metric layering (important for dashboards):
//   - Counters/histograms updated from ImageURLFetchEngine.fetchRemoteImageOnce describe the engine path only:
//     IncHTTPFetches and ObserveDecodedBytes run only when the engine loads the image body itself (store Get miss,
//     then HTTPClient.Do to the remote URL). They do not run on store Get hits, so decoded_bytes is not double-counted
//     when serving from cache.
//   - IncCacheHits counts successful Store.Get from the engine’s perspective (including IndexHTTPStore index hits).
//     IndexHTTPStore may still perform HTTP GET inside Get for telemetry/refetch; that is not counted on
//     image_url_fetch_http_fetches_total. Use IndexHTTPStore’s own metrics (duration/decoded bytes there) if you need
//     origin HTTP volume for that mode.
//   - ObserveHTTPFetchDuration records per-URL cold HTTP GET wall time (store miss path only).
//   - ObserveRequestFetchDuration records request-level wall time from starting concurrent fetches
//     until all complete (matches Process fetch_duration log; includes cache hits and HTTP).
//   - IncRejectedDueToImageDownload is incremented when Process returns an error after at least one
//     remote image URL failed to load (HTTP error, timeout, per-URL cap, read error, etc.).
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// M holds histograms and counters registered with the provided Registerer.
// A zero M or one built with New(nil) is a no-op safe to call from the engine.
// See package comment for how http_fetches vs cache_hits vs decoded_bytes relate.
type M struct {
	disabled bool

	decodedBytes              prometheus.Histogram
	requestSumBytes           prometheus.Histogram
	httpFetchMilliseconds     prometheus.Histogram
	requestFetchMilliseconds  prometheus.Histogram
	requestCacheHitRatio      prometheus.Histogram
	httpFetchesTotal          prometheus.Counter
	cacheHitsTotal            prometheus.Counter
	fullCacheHitRequestsTotal prometheus.Counter
	// Requests that stop at this engine because a remote image could not be fetched (not size-sum policy after success).
	rejectedDueToImageDownload prometheus.Counter
}

// New registers metrics on reg. If reg is nil, returns a no-op *M and nil error.
// If any collector fails to register, returns that error.
func New(reg prometheus.Registerer) (*M, error) {
	if reg == nil {
		return &M{disabled: true}, nil
	}

	decodedBytes := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "image_url_fetch_decoded_bytes",
		Help: "Decoded image payload size in bytes (raw image, not base64 length) per engine cold load only. " +
			"Omitted when Store.Get hits so the same logical image is not counted again in this histogram.",
		Buckets: prometheus.ExponentialBuckets(1024, 2, 20), // 1KiB .. ~1GiB
	})
	requestSum := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "image_url_fetch_request_decoded_bytes_sum",
		Help:    "Sum of decoded image bytes for unique remote URLs in one engine request (after all fetches succeed).",
		Buckets: prometheus.ExponentialBuckets(4096, 2, 22),
	})
	httpMs := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "image_url_fetch_http_fetch_duration_milliseconds",
		Help: "Wall time in milliseconds for one successful HTTP GET and body read in the engine (store miss path only).",
		// Same shape as DefBuckets (0.005s..10s) expressed in ms.
		Buckets: []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000},
	})
	requestFetchMs := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "image_url_fetch_request_fetch_duration_milliseconds",
		Help: "Wall time in milliseconds to complete all remote image fetches in one request " +
			"(concurrent; includes cache hits and HTTP misses).",
		Buckets: []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000},
	})
	requestCacheHitRatio := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "image_url_fetch_request_cache_hit_ratio",
		Help:    "Fraction of remote, non-data image URLs served from cache in one request; 0 means no image hit and 1 means every remote image hit.",
		Buckets: []float64{0, 0.25, 0.5, 0.75, 0.99, 1},
	})
	httpFetches := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "image_url_fetch_http_fetches_total",
		Help: "Successful remote HTTP GETs issued by the engine after a store miss (not counting HTTP inside IndexHTTPStore.Get).",
	})
	cacheHits := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "image_url_fetch_cache_hits_total",
		Help: "Successful Store.Get from the engine (disk hit, or IndexHTTPStore index hit; latter may still refetch via HTTP inside the store).",
	})
	fullCacheHitRequests := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "image_url_fetch_full_cache_hit_requests_total",
		Help: "Remote-image requests whose every remote image was served from cache and passed request image-size validation.",
	})
	rejectedDownload := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "image_url_fetch_download_failed_rejects_total",
		Help: "Requests not forwarded to the next engine because loading at least one remote image failed " +
			"(non-2xx HTTP, timeout, body read error, per-URL byte cap during fetch, etc.).",
	})

	for _, c := range []prometheus.Collector{
		decodedBytes, requestSum, httpMs, requestFetchMs, requestCacheHitRatio, httpFetches, cacheHits, fullCacheHitRequests, rejectedDownload,
	} {
		if err := reg.Register(c); err != nil {
			return nil, err
		}
	}

	return &M{
		decodedBytes:               decodedBytes,
		requestSumBytes:            requestSum,
		httpFetchMilliseconds:      httpMs,
		requestFetchMilliseconds:   requestFetchMs,
		requestCacheHitRatio:       requestCacheHitRatio,
		httpFetchesTotal:           httpFetches,
		cacheHitsTotal:             cacheHits,
		fullCacheHitRequestsTotal:  fullCacheHitRequests,
		rejectedDueToImageDownload: rejectedDownload,
	}, nil
}

// ObserveDecodedBytes records payload size for one engine cold load (store miss then HTTP). Cache hits skip this.
func (m *M) ObserveDecodedBytes(n int64) {
	if m == nil || m.disabled {
		return
	}
	m.decodedBytes.Observe(float64(n))
}

// ObserveRequestSumBytes records the sum of decoded bytes for unique URLs in one request.
func (m *M) ObserveRequestSumBytes(sum int64) {
	if m == nil || m.disabled {
		return
	}
	m.requestSumBytes.Observe(float64(sum))
}

// ObserveHTTPFetchDuration records duration for the engine’s successful HTTP GET + body read (cold path).
func (m *M) ObserveHTTPFetchDuration(d time.Duration) {
	if m == nil || m.disabled {
		return
	}
	m.httpFetchMilliseconds.Observe(d.Seconds() * 1000)
}

// ObserveRequestFetchDuration records wall time to finish all concurrent remote image fetches in one request.
func (m *M) ObserveRequestFetchDuration(d time.Duration) {
	if m == nil || m.disabled {
		return
	}
	m.requestFetchMilliseconds.Observe(d.Seconds() * 1000)
}

// ObserveRequestCacheHitRatio records the cache-hit fraction across remote, non-data images in one request.
func (m *M) ObserveRequestCacheHitRatio(ratio float64) {
	if m == nil || m.disabled {
		return
	}
	m.requestCacheHitRatio.Observe(ratio)
}

// IncHTTPFetches increments successful remote fetches performed by the engine HTTPClient (cold path after store miss).
func (m *M) IncHTTPFetches() {
	if m == nil || m.disabled {
		return
	}
	m.httpFetchesTotal.Inc()
}

// IncCacheHits increments when the engine’s Store.Get returns ok (disk or index hit; not an all-network counter).
func (m *M) IncCacheHits() {
	if m == nil || m.disabled {
		return
	}
	m.cacheHitsTotal.Inc()
}

// IncFullCacheHitRequests increments once when every remote image in the request was served from cache.
func (m *M) IncFullCacheHitRequests() {
	if m == nil || m.disabled {
		return
	}
	m.fullCacheHitRequestsTotal.Inc()
}

// IncRejectedDueToImageDownload records one request that failed before Next because a remote image could not be fetched.
func (m *M) IncRejectedDueToImageDownload() {
	if m == nil || m.disabled {
		return
	}
	m.rejectedDueToImageDownload.Inc()
}
