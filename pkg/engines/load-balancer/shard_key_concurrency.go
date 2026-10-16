package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/engines/limiter"
	"github.com/infinigence/octollm/pkg/octollm"
)

// ShardMaxConcurrencyFn returns the capacity denominator for concurrency ratio (count/denom).
// When non-nil, each value—including 0—is used directly (no fallback to BackendItem.Weight).
// Denominators <= 0 exclude that backend from ratio-based selection. When a BackendItem has no
// function, NewShardKeyConcurrency wraps its Weight as a static denominator.
type ShardMaxConcurrencyFn func(req *octollm.Request) int

type concurrencyBackend struct {
	name             string
	engine           octollm.Engine
	maxConcurrencyFn ShardMaxConcurrencyFn
	concurrencyKeyFn func(*octollm.Request) string
}

func (b *concurrencyBackend) denom(req *octollm.Request) int {
	if b == nil || b.maxConcurrencyFn == nil {
		return 0
	}
	return b.maxConcurrencyFn(req)
}

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
//   - Writes CacheHitKind onto each attempt request; Color Limiter enforces headroom.
type ShardKeyConcurrency struct {
	backends         []*concurrencyBackend
	affinityProvider AffinityProvider
	redisClient      *redis.Client
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
// disables backend affinity. redisClient is required for selection. Each kept BackendItem must
// supply ConcurrencyKeyFn. A nil backendAdmission skips admission checks
// and preserves current balancer behavior.
func NewShardKeyConcurrency(
	backends []BackendItem,
	retryTimeout time.Duration,
	retryMaxCount int,
	affinityProvider AffinityProvider,
	redisClient *redis.Client,
	backendAdmission BackendAdmission,
) (*ShardKeyConcurrency, error) {
	if redisClient == nil {
		return nil, fmt.Errorf("redisClient must be provided")
	}

	cb := make([]*concurrencyBackend, 0, len(backends))
	for _, b := range backends {
		if b.Weight <= 0 && b.MaxConcurrencyFn == nil {
			slog.Warn(fmt.Sprintf("[ShardKey Concurrency load balancer] backend %s has weight<=0 and no MaxConcurrencyFn, skipping", b.Name))
			continue
		}
		if b.ConcurrencyKeyFn == nil {
			return nil, fmt.Errorf("concurrencyKeyFn must be provided")
		}
		fn := b.MaxConcurrencyFn
		if fn == nil {
			w := b.Weight
			fn = func(*octollm.Request) int { return w }
		}
		cb = append(cb, &concurrencyBackend{
			name:             b.Name,
			engine:           b.Engine,
			maxConcurrencyFn: fn,
			concurrencyKeyFn: b.ConcurrencyKeyFn,
		})
	}
	if len(cb) == 0 {
		return nil, fmt.Errorf("backends must have at least one item")
	}

	return &ShardKeyConcurrency{
		backends:         cb,
		affinityProvider: affinityProvider,
		redisClient:      redisClient,
		retryTimeout:     retryTimeout,
		retryMaxCount:    retryMaxCount,
		backendAdmission: backendAdmission,
	}, nil
}

func (l *ShardKeyConcurrency) resolveAffinity(req *octollm.Request) ([]*prioritizedBackend, AffinityCommitFunc, error) {
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

	prioritized := make([]*prioritizedBackend, 0, len(providerBackends))
	for _, pb := range providerBackends {
		if pb == nil {
			continue
		}
		b, ok := backendByName[pb.Name]
		if !ok || b == nil {
			continue
		}
		prioritized = append(prioritized, &prioritizedBackend{
			backend:        b,
			strongCacheHit: pb.StrongCacheHit,
		})
	}
	return prioritized, commit, nil
}

// selectByConcurrency picks the backend with the lowest currentConcurrency/maxConcurrency ratio
// using Redis ZCard. Backends in excludeNames are skipped. Redis Exec failure falls back to a
// random positive-capacity backend.
func (l *ShardKeyConcurrency) selectByConcurrency(req *octollm.Request, excludeNames map[string]bool) *concurrencyBackend {
	ctx := req.Context()
	candidates := make([]*concurrencyBackend, 0, len(l.backends))
	for _, b := range l.backends {
		if b == nil || excludeNames[b.name] {
			continue
		}
		if b.concurrencyKeyFn == nil || b.concurrencyKeyFn(req) == "" {
			continue
		}
		candidates = append(candidates, b)
	}
	if len(candidates) == 0 {
		return nil
	}
	rand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })

	pipe := l.redisClient.Pipeline()
	cmds := make([]*redis.IntCmd, len(candidates))
	for i, b := range candidates {
		cmds[i] = pipe.ZCard(ctx, b.concurrencyKeyFn(req))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		slog.WarnContext(ctx, fmt.Sprintf("[ShardKey Concurrency load balancer] failed to get concurrency counts from Redis: %v", err))
		usable := candidates[:0]
		for _, b := range candidates {
			if b.denom(req) > 0 {
				usable = append(usable, b)
			}
		}
		if len(usable) == 0 {
			return nil
		}
		return usable[0]
	}

	minRatio := math.MaxFloat64
	var selected *concurrencyBackend
	for i, b := range candidates {
		count, err := cmds[i].Result()
		if err != nil {
			continue
		}
		d := b.denom(req)
		if d <= 0 {
			continue
		}
		ratio := float64(count) / float64(d)
		if ratio < minRatio {
			minRatio = ratio
			selected = b
		}
	}
	return selected
}

func (l *ShardKeyConcurrency) Process(req *octollm.Request) (*octollm.Response, error) {
	if _, err := req.Body.Bytes(); err != nil {
		return nil, fmt.Errorf("failed to cache request body for retries: %w", err)
	}

	modelName, _ := octollm.GetCtxValue[string](req, octollm.ContextKeyModelName)

	prioritized, commitAffinity, err := l.resolveAffinity(req)
	if err != nil {
		return nil, err
	}

	prioritizedIndex := 0
	excludeNames := make(map[string]bool)

	start := time.Now()
	retryCount := 0
	expectKind := affinityKindMiss
	var lastRealResp *octollm.Response
	var lastRealErr, lastLimiterErr error
	var lastN, lastActualKind string
	for {
		var n string
		var eng octollm.Engine
		var actualKind string
		var strongCacheHit bool
		isPrioritizedHit := false

		if prioritizedIndex < len(prioritized) {
			pb := prioritized[prioritizedIndex]
			prioritizedIndex++
			if pb == nil || pb.backend == nil {
				continue
			}
			b := pb.backend
			if excludeNames[b.name] {
				continue
			}
			strongCacheHit = pb.isStrongCacheHit()
			if strongCacheHit {
				expectKind = affinityKindStrongCacheAware
			} else if expectKind != affinityKindStrongCacheAware {
				expectKind = affinityKindWeakCacheAware
			}
			n, eng = b.name, b.engine
			isPrioritizedHit = true
			if strongCacheHit {
				actualKind = affinityKindStrongCacheAware
			} else {
				actualKind = affinityKindWeakCacheAware
			}
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
			selected := l.selectByConcurrency(req, excludeNames)
			if selected == nil {
				if retryCount > 0 {
					slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] no backend engine available on failover, returning previous error: %v", lastRealErr))
					cacheAwareRouteCounter.WithLabelValues(expectKind, lastActualKind, modelName, lastN).Inc()
				}
				return exhaustAfterNoBackend(lastRealResp, lastRealErr, lastLimiterErr)
			}

			n, eng = selected.name, selected.engine
			actualKind = affinityKindMiss
			slog.InfoContext(req.Context(),
				fmt.Sprintf("[ShardKey Concurrency load balancer] no prioritized backend available (exhausted %d), fallback to concurrency-based selection: %s, candidates: %v", len(prioritized), n, candidates),
				slog.String("backend_name", n),
			)
		}

		var admissionDone AttemptDoneFunc
		if l.backendAdmission != nil {
			var allowed bool
			admissionDone, allowed = l.backendAdmission.BeforeAttempt(req, n)
			if !allowed {
				slog.InfoContext(req.Context(),
					fmt.Sprintf("[ShardKey Concurrency load balancer] skip backend denied by admission: %s", n),
					slog.String("backend_name", n),
				)
				excludeNames[n] = true
				continue
			}
		}

		kind := attemptCacheHitKind(isPrioritizedHit, strongCacheHit)
		attemptReq := req.WithContext(context.WithValue(req.Context(), octollm.ContextKeyCacheHitKind, kind))
		attemptReq.SetMetadataValue(backendName, n)
		resp, err := eng.Process(attemptReq)
		if errors.Is(err, limiter.ErrRateLimitReached) {
			slog.InfoContext(req.Context(),
				fmt.Sprintf("[ShardKey Concurrency load balancer] limiter rejected backend %s, failover", n),
				slog.String("backend_name", n),
			)
			excludeNames[n] = true
			lastLimiterErr = err
			if len(excludeNames) >= len(l.backends) {
				return exhaustAfterNoBackend(lastRealResp, lastRealErr, lastLimiterErr)
			}
			if req.Context().Err() != nil {
				slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] request context error: %v", req.Context().Err()))
				return exhaustAfterNoBackend(lastRealResp, lastRealErr, lastLimiterErr)
			}
			if time.Since(start) >= l.retryTimeout {
				slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] retry period %v reached, return last limiter err", l.retryTimeout))
				return exhaustAfterNoBackend(lastRealResp, lastRealErr, lastLimiterErr)
			}
			continue
		}
		if admissionDone != nil && !isIgnoredAttemptError(err) {
			admissionDone(err == nil)
		}
		if err == nil {
			if commitAffinity != nil {
				if commitErr := commitAffinity(n); commitErr != nil {
					slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] failed to commit shard key mapping: %v", commitErr))
				}
			}
			cacheAwareRouteCounter.WithLabelValues(expectKind, actualKind, modelName, n).Inc()
			return resp, nil
		}

		if isNotRetriableError(err) {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] error is not retriable, return without retry: %v", err))
			cacheAwareRouteCounter.WithLabelValues(expectKind, actualKind, modelName, n).Inc()
			return resp, err
		}
		excludeNames[n] = true
		lastRealResp, lastRealErr = resp, err
		lastN, lastActualKind = n, actualKind
		retryCount++
		if req.Context().Err() != nil {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] request context error: %v", req.Context().Err()))
			cacheAwareRouteCounter.WithLabelValues(expectKind, actualKind, modelName, n).Inc()
			return resp, err
		}
		if time.Since(start) >= l.retryTimeout {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] retry period %v reached, return last resp and err", l.retryTimeout))
			cacheAwareRouteCounter.WithLabelValues(expectKind, actualKind, modelName, n).Inc()
			return resp, err
		}
		if retryCount >= l.retryMaxCount {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] retry max count %d reached, return last resp and err", l.retryMaxCount))
			cacheAwareRouteCounter.WithLabelValues(expectKind, actualKind, modelName, n).Inc()
			return resp, err
		}
		if len(excludeNames) >= len(l.backends) {
			slog.WarnContext(req.Context(), "[ShardKey Concurrency load balancer] all backends have been tried, return last resp and err")
			cacheAwareRouteCounter.WithLabelValues(expectKind, actualKind, modelName, n).Inc()
			return exhaustAfterNoBackend(lastRealResp, lastRealErr, lastLimiterErr)
		}
		slog.InfoContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] will retry, count %d, time %v", retryCount, time.Since(start)))
		totalFailoverRequestsCounter.WithLabelValues(modelName, n).Inc()
	}
}
