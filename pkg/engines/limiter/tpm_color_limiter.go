package limiter

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/engines/load-balancer/common"
	"github.com/infinigence/octollm/pkg/octollm"
)

// TPMColorLimiterEngine admits a request on a physical backend by the TPM that
// backend served over the last 60 seconds, at the priority a color marker of
// the same nameSpace assigned.
//
// It is the TPM counterpart of ConcurrencyColorLimiterEngine and shares its
// QoSLimits semantics, with TPM in place of inflight requests: the request's
// QoS index i owns the TPM of key:own_{i+1}, capped by PriorityLevelMax[i],
// and every index shares the TPM of key:public, capped by AvailableTotalMax[i].
// A request is admitted while both are below their caps, which HeadroomRatio
// raises for strong cache hits. Over a cap returns ErrTPMRateLimitReached, or
// ErrTPMStrongCacheRateLimitReached for a strong cache hit.
//
// An admitted request is counted in both windows: with WithTokenEstimate, its
// estimated tokens are reserved right away, so concurrent requests see one
// another before any has finished. The reservation is settled to the usage a
// usage.CollectorEngine below it recorded once the response is closed; it is
// refunded when the next engine fails, and kept when the response closes
// without usage recorded. Wrap each backend with its own limiter below the
// load balancer, so every attempt, retries and replicated requests included,
// is counted against the backend that served it.
type TPMColorLimiterEngine struct {
	redisClient *redis.Client
	key         string
	limitsFunc  LimitsFunc
	nameSpace   string
	estimate    TokenEstimateFunc
	next        octollm.Engine

	// tpmWindowCollector, when non-nil, opts this limiter into the TPM window
	// metrics; tpmWindowLabelValues are its values for the labels the host
	// declared through InitTPMWindowCollector. Both are set together by
	// WithTPMWindowLabelValues before the engine serves requests.
	tpmWindowCollector   *TPMWindowCollector
	tpmWindowLabelValues []string
}

var _ octollm.Engine = (*TPMColorLimiterEngine)(nil)

// TPM window metrics.
//
// The collector is owned by octollm: the "priority" label (the QoS band the
// request was evaluated at, or "public" for the window every band shares) and
// every observed window value come from inside the limiter, at
// engine.Process time. The embedding host declares any additional labels
// through InitTPMWindowCollector — their names are opaque to this package —
// and each limiter opts in and supplies its values with
// WithTPMWindowLabelValues, which binds the collector to that limiter.
// Metrics are off unless both happen: no InitTPMWindowCollector call, or a
// limiter that never called WithTPMWindowLabelValues, records nothing.
//
// Instead of a GaugeVec, the metric is a custom Collector whose series carry
// a lastUpdate stamp: Collect reports a series only when it was observed
// within tpmWindowSeriesTTL, and forgets it entirely once it goes stale. A
// GaugeVec would report a deleted svc's series at its last value forever;
// this way the series disappears from the scrape shortly after the svc's
// last admission, which is how a scrape-time redis read behaves once its key
// expires.
//
// There is one collector per process, held in a package global. It must be
// initialized before limiters attach their label values.
var (
	tpmWindowMu        sync.RWMutex
	tpmWindowCollector *TPMWindowCollector
)

// tpmWindowSeriesTTL is how long a series keeps being reported after its last
// observation. Trade-off: shorter means a deleted svc's series disappears
// sooner, but a sparse-traffic svc also flickers in and out of the scrape.
const tpmWindowSeriesTTL = 15 * time.Second

const (
	// tpmWindowPriorityLabel is the label octollm appends to the host's
	// labels. Its value is the priority band the request was evaluated at
	// (larger = higher priority), or "public" for the window every band
	// shares.
	tpmWindowPriorityLabel = "priority"

	// tpmWindowPriorityLabelPublic is the priority label value of the public
	// window, which all bands share.
	tpmWindowPriorityLabelPublic = "public"
)

// TPMWindowCollector is a gauge with fixed label names whose series go stale:
// only series observed within tpmWindowSeriesTTL are reported, and stale ones
// are dropped from memory at Collect time.
type TPMWindowCollector struct {
	extraLabels []string
	mu          sync.Mutex
	desc        *prometheus.Desc
	series      map[string]*tpmSeries
}

type tpmSeries struct {
	labels     []string // full label values: host labels + priority
	value      float64
	lastUpdate time.Time
}

// InitTPMWindowCollector builds the process-wide collector with the host's
// extra labels, e.g. model_id and svc_name, appended with octollm's own
// "priority" label, and registers it on the default registerer. It must be
// called exactly once, before any TPM limiter is built; a second call is an
// error and changes nothing. Calling it with no labels builds a
// priority-only gauge for hosts that want no extra dimensions. The host label
// names are opaque to this package; the matching values are supplied per
// limiter with WithTPMWindowLabelValues.
func InitTPMWindowCollector(labels ...string) (*TPMWindowCollector, error) {
	tpmWindowMu.Lock()
	defer tpmWindowMu.Unlock()
	if tpmWindowCollector != nil {
		return nil, fmt.Errorf("limiter: tpm window collector already built with labels %v", tpmWindowCollector.extraLabels)
	}
	c := &TPMWindowCollector{
		extraLabels: append([]string(nil), labels...),
		series:      make(map[string]*tpmSeries),
	}
	c.desc = prometheus.NewDesc(
		"octollm_tpm_limiter_window",
		"TPM in the 60s window per limiter, band and public window, including the current request's reservation",
		append(append([]string(nil), labels...), tpmWindowPriorityLabel), nil,
	)
	prometheus.MustRegister(c)
	tpmWindowCollector = c
	return c, nil
}

func (c *TPMWindowCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

// Collect emits one gauge per series observed within the TTL and forgets
// series past it, so a deleted svc's series disappears from the scrape once
// no request has refreshed it.
func (c *TPMWindowCollector) Collect(ch chan<- prometheus.Metric) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, s := range c.series {
		if now.Sub(s.lastUpdate) > tpmWindowSeriesTTL {
			delete(c.series, key)
			continue
		}
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, s.value, s.labels...)
	}
}

// set stamps a series with value and now.
func (c *TPMWindowCollector) set(labels []string, value float64) {
	key := tpmWindowSeriesKey(labels)
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.series[key]; ok {
		s.value = value
		s.lastUpdate = now
		return
	}
	c.series[key] = &tpmSeries{labels: labels, value: value, lastUpdate: now}
}

// tpmWindowSeriesKey joins label values; label values never contain a comma
// in practice, and host code can reasonably be expected to keep it that way.
func tpmWindowSeriesKey(labels []string) string {
	return strings.Join(labels, ",")
}

// tpmWindowBandPriority clamps a request priority onto the bands: the own
// window it was evaluated against is owned by band min(max(priority, 0), n-1).
func tpmWindowBandPriority(priority, n int) int {
	return n - 1 - qosIndex(priority, n)
}

// NewTPMColorLimiterEngine creates a limiter with static QoSLimits, in TPM.
// limits must be non-nil, and both limit arrays must be non-empty.
func NewTPMColorLimiterEngine(redisClient *redis.Client, key string, limits *QoSLimits, nameSpace string, next octollm.Engine) (*TPMColorLimiterEngine, error) {
	if err := validateQoSLimits(limits); err != nil {
		return nil, err
	}
	cloned := cloneQoSLimits(*limits)
	return NewTPMColorLimiterEngineWithLimitsFunc(redisClient, key, nameSpace, func(*octollm.Request) (*QoSLimits, error) {
		return &cloned, nil
	}, next)
}

// NewTPMColorLimiterEngineWithLimitsFunc creates a limiter that resolves QoSLimits, in TPM, per request.
// limitsFunc must be non-nil. A nil or invalid QoSLimits from the function fails that request.
func NewTPMColorLimiterEngineWithLimitsFunc(redisClient *redis.Client, key string, nameSpace string, limitsFunc LimitsFunc, next octollm.Engine) (*TPMColorLimiterEngine, error) {
	if limitsFunc == nil {
		return nil, fmt.Errorf("limitsFunc must be provided")
	}
	if redisClient == nil {
		return nil, fmt.Errorf("redisClient must be provided")
	}
	if next == nil {
		return nil, fmt.Errorf("next engine must not be nil")
	}
	return &TPMColorLimiterEngine{
		redisClient: redisClient,
		key:         key,
		limitsFunc:  limitsFunc,
		nameSpace:   nameSpace,
		next:        next,
	}, nil
}

// WithTokenEstimate makes the limiter reserve fn's estimate of each admitted
// request in the backend TPM; without it, a request only counts once its
// response is closed. It must be called before the engine serves requests.
func (e *TPMColorLimiterEngine) WithTokenEstimate(fn TokenEstimateFunc) *TPMColorLimiterEngine {
	e.estimate = fn
	return e
}

// WithTPMWindowLabelValues opts this limiter into the TPM window metrics and
// sets its values for the labels the host declared through
// InitTPMWindowCollector, in declaration order. A shape mismatch is an error,
// so a misconfigured metric surfaces at engine build time. Without this call
// the limiter records nothing. It must be called before the engine serves
// requests.
func (e *TPMColorLimiterEngine) WithTPMWindowLabelValues(values ...string) (*TPMColorLimiterEngine, error) {
	tpmWindowMu.RLock()
	c := tpmWindowCollector
	tpmWindowMu.RUnlock()
	if c == nil {
		return nil, errors.New("limiter: tpm window collector not initialized; call InitTPMWindowCollector first")
	}
	if len(values) != len(c.extraLabels) {
		return nil, fmt.Errorf("limiter: got %d tpm window label values, want %d (see InitTPMWindowCollector)", len(values), len(c.extraLabels))
	}
	e.tpmWindowCollector = c
	e.tpmWindowLabelValues = append([]string(nil), values...)
	return e, nil
}

// recordTPMWindows observes the two windows a request's admission read: its
// band's own window and the public window, each with this request's
// reservation already added. bandPriority is the request's priority clamped
// to the bands, i.e. the band that owns the own window. A limiter that did
// not opt in via WithTPMWindowLabelValues records nothing; recording never
// fails the request.
func (e *TPMColorLimiterEngine) recordTPMWindows(bandPriority int, ownTPM, publicTPM, reserved int64, admitted bool) {
	if e.tpmWindowCollector == nil {
		return
	}
	if admitted {
		ownTPM += reserved
		publicTPM += reserved
	}

	// Each series gets its own labels slice: e.tpmWindowLabelValues is shared,
	// so append must never write into its spare capacity.
	bandLabels := append(append([]string(nil), e.tpmWindowLabelValues...), strconv.Itoa(bandPriority))
	e.tpmWindowCollector.set(bandLabels, float64(ownTPM))
	publicLabels := append(append([]string(nil), e.tpmWindowLabelValues...), tpmWindowPriorityLabelPublic)
	e.tpmWindowCollector.set(publicLabels, float64(publicTPM))
}

const tpmLimiterLogPrefix = "[TPMColorLimiterEngine]"

// allow admits the request and returns what it reserved in the backend windows.
func (e *TPMColorLimiterEngine) allow(req *octollm.Request) (res *tpmReservation, priority int, err error) {
	ctx := req.Context()
	limits, err := e.limitsFunc(req)
	if err != nil {
		return nil, 0, err
	}
	if err := validateQoSLimits(limits); err != nil {
		return nil, 0, err
	}

	priority, _ = priorityFromNamespace(ctx, e.nameSpace)
	i := qosIndex(priority, len(limits.AvailableTotalMax))
	kind := common.CacheHitKindFromRequest(req)
	ownBase := limits.PriorityLevelMax[i]
	totalBase := limits.AvailableTotalMax[i]
	effectiveOwn, effectiveTotal := effectiveOwnAndTotalLimits(ownBase, totalBase, limits.HeadroomRatio, kind)

	ownKey := fmt.Sprintf("%s:own_%d", e.key, i+1)
	publicKey := e.key + ":public"
	keys := []string{ownKey, publicKey}
	caps := []int64{int64(effectiveOwn), int64(effectiveTotal)}

	tokens := estimateTokens(req, e.estimate, tpmLimiterLogPrefix)
	res, tpm, err := reserveTPM(ctx, e.redisClient, keys, caps, tokens, tpmLimiterLogPrefix)
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("%s %v, ownKey: %s, publicKey: %s", tpmLimiterLogPrefix, err, ownKey, publicKey))
		return nil, 0, err
	}
	ownTPM, publicTPM := tpm[0], tpm[1]

	// Observe the windows this admission read, reservation included. Never
	// fails the request.
	e.recordTPMWindows(tpmWindowBandPriority(priority, len(limits.AvailableTotalMax)), ownTPM, publicTPM, tokens, res != nil)

	if res == nil {
		slog.WarnContext(ctx, fmt.Sprintf("%s tpm limit reached for priority %d (qosIndex %d), ownTPM: %d/%d, publicTPM: %d/%d, hitKind: %s",
			tpmLimiterLogPrefix, priority, i, ownTPM, effectiveOwn, publicTPM, effectiveTotal, kind))
		limitErr := ErrTPMRateLimitReached
		if kind == common.CacheHitStrong {
			limitErr = ErrTPMStrongCacheRateLimitReached
		}
		return nil, priority, limitErr
	}

	slog.InfoContext(ctx, fmt.Sprintf("%s tpm allowed for priority %d (qosIndex %d), ownTPM: %d/%d, publicTPM: %d/%d, reserved: %d, hitKind: %s",
		tpmLimiterLogPrefix, priority, i, ownTPM, effectiveOwn, publicTPM, effectiveTotal, tokens, kind))
	return res, priority, nil
}

func (e *TPMColorLimiterEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	res, priority, err := e.allow(req)
	if err != nil {
		slog.ErrorContext(req.Context(), fmt.Sprintf("%s tpm rate limiter error: %v, key: %s", tpmLimiterLogPrefix, err, e.key))
		if errors.Is(err, ErrRateLimitReached) {
			recordLimiterDeny(req, priority)
		}
		return nil, err
	}
	recordLimiterAllow(req, priority)

	return processReserved(req, e.next, res)
}
