package loadbalancer

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/octollm"
)

// ErrCacheMissHeadroom is returned when a cache-miss request cannot be served because every
// candidate backend is above cacheMissMaxUtilization — only the reserved headroom remains,
// and it is restricted to strong-cache-hit requests.
var ErrCacheMissHeadroom = errors.New("cache-miss request rejected to preserve cache-hit headroom")

// ShardMaxConcurrencyFn returns the capacity denominator for concurrency ratio (count/denom).
// When non-nil, each value—including 0—is used directly (no fallback to BackendItem.Weight).
// Denominators <= 0 exclude that backend from ratio-based selection. When a BackendItem has no
// function, NewShardKeyConcurrency wraps its Weight as a static denominator.
type ShardMaxConcurrencyFn func(req *octollm.Request) int

type concurrencyBackend struct {
	name   string
	engine octollm.Engine
	// maxConcurrencyFn supplies the ratio denominator per request. It is always set by construction:
	// dynamic backends use BackendItem.MaxConcurrencyFn, static backends use a closure over Weight.
	maxConcurrencyFn ShardMaxConcurrencyFn
	// cacheMissMaxUtilization is the per-backend headroom ceiling; see BackendItem.CacheMissMaxUtilization.
	cacheMissMaxUtilization float64
}

// prioritizedBackend pairs an affinity-resolved backend with whether it is a strong cache hit.
type prioritizedBackend struct {
	backend        *concurrencyBackend
	strongCacheHit bool
}

func (pb *prioritizedBackend) isStrongCacheHit() bool {
	return pb.strongCacheHit
}

// ShardKeyConcurrency implements a load balancer that:
//   - When affinityProvider is set: prioritizes backends resolved by the provider,
//     falling back to concurrency-based selection once all prioritized backends are exhausted.
//   - When affinityProvider is nil: selects the backend with the lowest
//     currentConcurrency/maxConcurrency ratio via Redis ZCard.
type ShardKeyConcurrency struct {
	backends         []*concurrencyBackend
	affinityProvider AffinityProvider
	redisClient      *redis.Client
	concurrencyKeyFn func(req *octollm.Request, backendName string) string
	retryTimeout     time.Duration
	retryMaxCount    int
	backendAdmission BackendAdmission
}

var _ octollm.Engine = (*ShardKeyConcurrency)(nil)

// NewShardKeyConcurrency creates a load balancer that selects backends by backend affinity
// (when affinityProvider is non-nil) or by lowest concurrency ratio (when nil).
//
// Backends with Weight <= 0 are skipped unless MaxConcurrencyFn is set; a static Weight is wrapped
// as the request-time maximum concurrency when MaxConcurrencyFn is nil. A nil affinityProvider
// disables backend affinity. redisClient and concurrencyKeyFn are required for selection;
// concurrencyKeyFn must return the Redis ZSET key whose cardinality represents the backend's
// current concurrency. The per-backend cache-miss ceiling is configured through
// BackendItem.CacheMissMaxUtilization. A nil backendAdmission skips admission checks
// and preserves current balancer behavior.
func NewShardKeyConcurrency(
	backends []BackendItem,
	retryTimeout time.Duration,
	retryMaxCount int,
	affinityProvider AffinityProvider,
	redisClient *redis.Client,
	concurrencyKeyFn func(req *octollm.Request, backendName string) string,
	backendAdmission BackendAdmission,
) (*ShardKeyConcurrency, error) {
	if redisClient == nil {
		return nil, fmt.Errorf("redisClient must be provided")
	}
	if concurrencyKeyFn == nil {
		return nil, fmt.Errorf("concurrencyKeyFn must be provided")
	}

	cb := make([]*concurrencyBackend, 0, len(backends))
	for _, b := range backends {
		if b.Weight <= 0 && b.MaxConcurrencyFn == nil {
			slog.Warn(fmt.Sprintf("[ShardKey Concurrency load balancer] backend %s has weight<=0 and no MaxConcurrencyFn, skipping", b.Name))
			continue
		}
		maxConcurrencyFn := b.MaxConcurrencyFn
		if maxConcurrencyFn == nil {
			static := b.Weight
			maxConcurrencyFn = func(req *octollm.Request) int {
				return static
			}
		}

		cb = append(cb, &concurrencyBackend{
			name:                    b.Name,
			engine:                  b.Engine,
			maxConcurrencyFn:        maxConcurrencyFn,
			cacheMissMaxUtilization: b.CacheMissMaxUtilization,
		})
	}
	if len(cb) == 0 {
		return nil, fmt.Errorf("backends must have at least one item")
	}

	return &ShardKeyConcurrency{
		backends:         cb,
		affinityProvider: affinityProvider,
		redisClient:      redisClient,
		concurrencyKeyFn: concurrencyKeyFn,
		retryTimeout:     retryTimeout,
		retryMaxCount:    retryMaxCount,
		backendAdmission: backendAdmission,
	}, nil
}

func (l *ShardKeyConcurrency) resolveAffinity(req *octollm.Request) ([]prioritizedBackend, AffinityCommitFunc, error) {
	if l.affinityProvider == nil {
		return nil, nil, nil
	}

	providerBackends, commit, err := l.affinityProvider.Resolve(req)
	if err != nil {
		return nil, nil, err
	}

	backendByName := make(map[string]*concurrencyBackend, len(l.backends))
	for _, b := range l.backends {
		if b.name != "" {
			backendByName[b.name] = b
		}
	}

	prioritized := make([]prioritizedBackend, 0, len(providerBackends))
	for _, pb := range providerBackends {
		if pb == nil {
			continue
		}
		b, ok := backendByName[pb.Name]
		if !ok || b == nil {
			continue
		}
		prioritized = append(prioritized, prioritizedBackend{
			backend:        b,
			strongCacheHit: pb.StrongCacheHit,
		})
	}
	return prioritized, commit, nil
}

// selectByConcurrency picks the backend with the lowest currentConcurrency/maxConcurrency ratio
// using Redis ZCard. Backends in excludeNames are skipped. The returned float is the selected
// backend's utilization ratio (count/maxConcurrency); it is 0 on the random fallback path below.
// Falls back to uniform random selection among backends with positive effective capacity if Redis Exec fails.
func (l *ShardKeyConcurrency) selectByConcurrency(req *octollm.Request, excludeNames map[string]bool) (*concurrencyBackend, float64) {
	ctx := req.Context()
	candidates := make([]*concurrencyBackend, 0, len(l.backends))
	for _, b := range l.backends {
		if b == nil || excludeNames[b.name] {
			continue
		}
		candidates = append(candidates, b)
	}
	if len(candidates) == 0 {
		return nil, 0
	}
	rand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })

	pipe := l.redisClient.Pipeline()
	cmds := make([]*redis.IntCmd, len(candidates))
	for i, b := range candidates {
		cmds[i] = pipe.ZCard(ctx, l.concurrencyKeyFn(req, b.name))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		slog.WarnContext(ctx, fmt.Sprintf("[ShardKey Concurrency load balancer] failed to get concurrency counts from Redis: %v", err))
		usable := candidates[:0]
		for _, b := range candidates {
			if b.maxConcurrencyFn(req) > 0 {
				usable = append(usable, b)
			}
		}
		if len(usable) == 0 {
			return nil, 0
		}
		return usable[0], 0
	}

	minRatio := math.MaxFloat64
	var selected *concurrencyBackend
	for i, b := range candidates {
		count, err := cmds[i].Result()
		if err != nil {
			continue
		}
		denom := b.maxConcurrencyFn(req)
		if denom <= 0 {
			continue
		}
		ratio := float64(count) / float64(denom)
		if ratio < minRatio {
			minRatio = ratio
			selected = b
		}
	}
	if selected == nil {
		return nil, 0
	}
	return selected, minRatio
}

// headroomEnabled reports whether cache-miss headroom reservation is active. A configured
// utilization of <= 0 or >= 1 disables it.
func (b *concurrencyBackend) headroomEnabled() bool {
	return b.cacheMissMaxUtilization > 0 && b.cacheMissMaxUtilization < 1.0
}

// overHeadroom fails open on Redis errors so transient Redis failures do not reject requests.
// Callers must first check headroomEnabled.
func (l *ShardKeyConcurrency) overHeadroom(req *octollm.Request, b *concurrencyBackend) bool {
	denom := b.maxConcurrencyFn(req)
	if denom <= 0 {
		return true
	}
	count, err := l.redisClient.ZCard(req.Context(), l.concurrencyKeyFn(req, b.name)).Result()
	if err != nil {
		slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] failed to ZCard concurrency for headroom check on %s: %v", b.name, err))
		return false
	}
	return float64(count)/float64(denom) > b.cacheMissMaxUtilization
}

func (l *ShardKeyConcurrency) Process(req *octollm.Request) (*octollm.Response, error) {
	// Materialize the request body once so every retry can read it again.
	if _, err := req.Body.Bytes(); err != nil {
		return nil, fmt.Errorf("failed to cache request body for retries: %w", err)
	}

	prioritized, commit, err := l.resolveAffinity(req)
	if err != nil {
		return nil, err
	}

	prioritizedIndex := 0
	excludeNames := make(map[string]bool)

	start := time.Now()
	retryCount := 0
	admissionSkipCount := 0
	headroomSkipCount := 0
	var lastResp *octollm.Response
	var lastErr error
	for {
		var n string
		var eng octollm.Engine

		if prioritizedIndex < len(prioritized) {
			pb := prioritized[prioritizedIndex]
			b := pb.backend
			prioritizedIndex++
			if excludeNames[b.name] {
				continue
			}
			if b.maxConcurrencyFn(req) <= 0 {
				slog.InfoContext(req.Context(),
					fmt.Sprintf("[ShardKey Concurrency load balancer] skip prioritized backend with no effective capacity: %s (index %d/%d)", b.name, prioritizedIndex, len(prioritized)),
					slog.String("backend_name", b.name),
				)
				excludeNames[b.name] = true
				continue
			}
			strongCacheHit := pb.isStrongCacheHit()
			// Preserve reserved capacity for strong cache hits; weak hits obey the ceiling.
			if !strongCacheHit && b.headroomEnabled() && l.overHeadroom(req, b) {
				slog.InfoContext(req.Context(),
					fmt.Sprintf("[ShardKey Concurrency load balancer] skip weak-cache-hit prioritized backend over headroom %.2f: %s (index %d/%d)", b.cacheMissMaxUtilization, b.name, prioritizedIndex, len(prioritized)),
					slog.String("backend_name", b.name),
				)
				excludeNames[b.name] = true
				headroomSkipCount++
				continue
			}
			n, eng = b.name, b.engine
			slog.InfoContext(req.Context(),
				fmt.Sprintf("[ShardKey Concurrency load balancer] prioritized backend hit (strongCacheHit=%t): %s (index %d/%d)", strongCacheHit, n, prioritizedIndex, len(prioritized)),
				slog.String("backend_name", n),
			)
		} else {
			candidates := make([]string, 0, len(l.backends))
			for _, b := range l.backends {
				if b != nil && !excludeNames[b.name] {
					candidates = append(candidates, b.name)
				}
			}
			selected, ratio := l.selectByConcurrency(req, excludeNames)
			if selected != nil {
				n, eng = selected.name, selected.engine
			}
			// Headroom is per backend, so exclude an over-limit cache miss and try the next candidate.
			if selected != nil && selected.headroomEnabled() && ratio > selected.cacheMissMaxUtilization {
				excludeNames[selected.name] = true
				headroomSkipCount++
				continue
			}
			slog.InfoContext(req.Context(),
				fmt.Sprintf("[ShardKey Concurrency load balancer] no prioritized backend available (exhausted %d), fallback to concurrency-based selection: %s, candidates: %v", len(prioritized), n, candidates),
				slog.String("backend_name", n),
			)
		}

		if eng == nil {
			// selectByConcurrency may return nil before every backend is in excludeNames
			// (non-positive capacity is skipped without being marked). Skip reasons are
			// counted on the way here; failover keeps the previous backend error.
			if retryCount > 0 {
				slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] no backend engine available on failover, returning previous error: %v", lastErr))
				return lastResp, lastErr
			}
			if admissionSkipCount > 0 {
				return nil, ErrNoBackendPermitted
			}
			if headroomSkipCount > 0 {
				return nil, ErrCacheMissHeadroom
			}
			return nil, fmt.Errorf("no backend engine available")
		}

		var done AttemptDoneFunc
		if l.backendAdmission != nil {
			var allowed bool
			done, allowed = l.backendAdmission.BeforeAttempt(req, n)
			if !allowed {
				excludeNames[n] = true
				admissionSkipCount++
				continue
			}
		}

		req.SetMetadataValue(backendName, n)
		resp, err := eng.Process(req)
		if done != nil && !isIgnoredAttemptError(err) {
			done(err == nil)
		}
		if err == nil {
			if commit != nil {
				if commitErr := commit(n); commitErr != nil {
					slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] failed to commit shard key mapping: %v", commitErr))
				}
			}
			return resp, nil
		}
		if isNotRetriableError(err) {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] error is not retriable, return without retry: %v", err))
			return resp, err
		}
		excludeNames[n] = true
		lastResp, lastErr = resp, err
		retryCount++
		if req.Context().Err() != nil {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] request context error: %v", req.Context().Err()))
			return resp, err
		}
		if time.Since(start) >= l.retryTimeout {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] retry period %v reached, return last resp and err", l.retryTimeout))
			return resp, err
		}
		if retryCount >= l.retryMaxCount {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] retry max count %d reached, return last resp and err", l.retryMaxCount))
			return resp, err
		}
		if len(excludeNames) >= len(l.backends) {
			slog.WarnContext(req.Context(), "[ShardKey Concurrency load balancer] all backends have been tried, return last resp and err")
			return resp, err
		}
		slog.InfoContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] will retry, count %d, time %v", retryCount, time.Since(start)))
		modelName, _ := octollm.GetCtxValue[string](req, octollm.ContextKeyModelName)
		totalFailoverRequestsCounter.WithLabelValues(modelName, n).Inc()
	}
}
