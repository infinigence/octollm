package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"time"

	"github.com/infinigence/octollm/pkg/engines/limiter"
	"github.com/infinigence/octollm/pkg/engines/load-balancer/common"
	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/octollm"
)

type concurrencyBackend struct {
	name             string
	engine           octollm.Engine
	maxConcurrencyFn func(req *octollm.Request) int
	concurrencyKeyFn func(req *octollm.Request) string
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
			if b.maxConcurrencyFn(req) > 0 {
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
		d := b.maxConcurrencyFn(req)
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
	// Materialize the request body once so every retry can read it again.
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
	expectKind := common.CacheMiss
	var lastRealResp *octollm.Response
	var lastRealErr error
	var lastN string
	var lastActualKind common.CacheHitKind
	for {
		var n string
		var eng octollm.Engine
		var actualKind common.CacheHitKind

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
			strongCacheHit := pb.isStrongCacheHit()
			if strongCacheHit {
				expectKind = common.CacheHitStrong
			} else if expectKind != common.CacheHitStrong {
				expectKind = common.CacheHitWeak
			}
			n, eng = b.name, b.engine
			if strongCacheHit {
				actualKind = common.CacheHitStrong
			} else {
				actualKind = common.CacheHitWeak
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
					cacheAwareRouteCounter.WithLabelValues(string(expectKind), string(lastActualKind), modelName, lastN).Inc()
				}
				if lastRealErr != nil {
					return lastRealResp, lastRealErr
				}
				return nil, fmt.Errorf("no backend engine available")
			}

			n, eng = selected.name, selected.engine
			actualKind = common.CacheMiss
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

		attemptReq := req.WithContext(context.WithValue(req.Context(), common.ContextKeyCacheHitKind, actualKind))
		attemptReq.SetMetadataValue(backendName, n)
		resp, err := eng.Process(attemptReq)
		if admissionDone != nil {
			if err == nil {
				admissionDone(true)
			} else {
				if isBackendFailureError(err) {
					admissionDone(false)
				}
			}
		}
		if err == nil {
			if commitAffinity != nil {
				if commitErr := commitAffinity(n); commitErr != nil {
					slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] failed to commit shard key mapping: %v", commitErr))
				}
			}
			cacheAwareRouteCounter.WithLabelValues(string(expectKind), string(actualKind), modelName, n).Inc()
			return resp, nil
		}

		// The following logic handles the case where err is non-nil.
		if isNotRetriableError(err) {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] error is not retriable, return without retry: %v", err))
			cacheAwareRouteCounter.WithLabelValues(string(expectKind), string(actualKind), modelName, n).Inc()
			return resp, err
		}

		if errors.Is(err, limiter.ErrRateLimitReached) {
			slog.InfoContext(req.Context(),
				fmt.Sprintf("[ShardKey Concurrency load balancer] limiter rejected backend %s, failover", n),
				slog.String("backend_name", n),
			)
			// When lastRealErr is nil, lastRealResp must also be nil: the response is written only in the non-rate-limit error branch, paired with the error.
			if lastRealErr == nil {
				lastRealErr = err
			}
		} else {
			lastRealResp, lastRealErr = resp, err
			lastN, lastActualKind = n, actualKind
			retryCount++
		}
		excludeNames[n] = true
		if req.Context().Err() != nil {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] request context error: %v", req.Context().Err()))
			cacheAwareRouteCounter.WithLabelValues(string(expectKind), string(actualKind), modelName, n).Inc()
			return lastRealResp, lastRealErr
		}
		if time.Since(start) >= l.retryTimeout {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] retry period %v reached, return last resp and err", l.retryTimeout))
			cacheAwareRouteCounter.WithLabelValues(string(expectKind), string(actualKind), modelName, n).Inc()
			return lastRealResp, lastRealErr
		}
		if retryCount >= l.retryMaxCount {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] retry max count %d reached, return last resp and err", l.retryMaxCount))
			cacheAwareRouteCounter.WithLabelValues(string(expectKind), string(actualKind), modelName, n).Inc()
			return lastRealResp, lastRealErr
		}
		if len(excludeNames) >= len(l.backends) {
			slog.WarnContext(req.Context(), "[ShardKey Concurrency load balancer] all backends have been tried, return last resp and err")
			cacheAwareRouteCounter.WithLabelValues(string(expectKind), string(actualKind), modelName, n).Inc()
			if lastRealErr != nil {
				return lastRealResp, lastRealErr
			}
			return nil, fmt.Errorf("no backend engine available")
		}
		slog.InfoContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] will retry, count %d, time %v", retryCount, time.Since(start)))
		totalFailoverRequestsCounter.WithLabelValues(modelName, n).Inc()
	}
}
