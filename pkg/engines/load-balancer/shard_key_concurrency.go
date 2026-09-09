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
	name             string
	engine           octollm.Engine
	headroom         backendHeadroom
	concurrencyKeyFn func(*octollm.Request) string
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
// supply ConcurrencyKeyFn, which returns the Redis ZSET key whose cardinality represents that
// backend's current concurrency. The per-backend cache-miss ceiling is configured through
// BackendItem.CacheMissMaxUtilization. A nil backendAdmission skips admission checks
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
		cb = append(cb, &concurrencyBackend{
			name:             b.Name,
			engine:           b.Engine,
			headroom:         newBackendHeadroom(b),
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
// using Redis ZCard. Backends in excludeNames are skipped. counted is false when Redis Exec fails
// and a random positive-capacity backend is returned; callers must not apply headroom in that case.
// ratio is that selected count/denom when counted is true, otherwise 0.
func (l *ShardKeyConcurrency) selectByConcurrency(req *octollm.Request, excludeNames map[string]bool) (*concurrencyBackend, int64, bool, float64) {
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
		return nil, 0, false, 0
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
			if b.headroom.denom(req) > 0 {
				usable = append(usable, b)
			}
		}
		if len(usable) == 0 {
			return nil, 0, false, 0
		}
		return usable[0], 0, false, 0
	}

	minRatio := math.MaxFloat64
	var selected *concurrencyBackend
	var selectedCount int64
	for i, b := range candidates {
		count, err := cmds[i].Result()
		if err != nil {
			continue
		}
		d := b.headroom.denom(req)
		if d <= 0 {
			continue
		}
		ratio := float64(count) / float64(d)
		if ratio < minRatio {
			minRatio = ratio
			selected = b
			selectedCount = count
		}
	}
	if selected == nil {
		return nil, 0, false, 0
	}
	return selected, selectedCount, true, minRatio
}

// concurrencyCount reads the backend's in-flight cardinality. ok is false on Redis
// errors so callers can fail open and not treat the request as over headroom.
func (l *ShardKeyConcurrency) concurrencyCount(req *octollm.Request, b *concurrencyBackend) (int64, bool) {
	if b == nil || b.concurrencyKeyFn == nil {
		return 0, false
	}
	key := b.concurrencyKeyFn(req)
	if key == "" {
		return 0, false
	}
	count, err := l.redisClient.ZCard(req.Context(), key).Result()
	if err != nil {
		slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] failed to ZCard concurrency for headroom check on %s: %v", b.name, err))
		return 0, false
	}
	return count, true
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
	headroomSkipCount := 0
	expectKind := affinityKindMiss
	var lastResp *octollm.Response
	var lastErr error
	var lastN, lastActualKind string
	for {
		var n string
		var eng octollm.Engine
		var actualKind string

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
			// Determine the expected affinity kind for metrics. If a strong cache hit is found, it takes precedence.
			if strongCacheHit {
				expectKind = affinityKindStrongCacheAware
			} else if expectKind != affinityKindStrongCacheAware {
				expectKind = affinityKindWeakCacheAware
			}

			if b.headroom.denom(req) <= 0 {
				slog.InfoContext(req.Context(),
					fmt.Sprintf("[ShardKey Concurrency load balancer] skip prioritized backend with no effective capacity: %s (index %d/%d)", b.name, prioritizedIndex, len(prioritized)),
					slog.String("backend_name", b.name),
				)
				excludeNames[b.name] = true
				continue
			}
			// Preserve reserved capacity for strong cache hits; weak hits obey the ceiling.
			if !strongCacheHit {
				count, ok := l.concurrencyCount(req, b)
				if ok && b.headroom.exceeded(req, count) {
					slog.InfoContext(req.Context(),
						fmt.Sprintf("[ShardKey Concurrency load balancer] skip weak-cache-hit prioritized backend over headroom %.2f: %s (index %d/%d)", b.headroom.cacheMissMaxUtilization, b.name, prioritizedIndex, len(prioritized)),
						slog.String("backend_name", b.name),
					)
					excludeNames[b.name] = true
					headroomSkipCount++
					continue
				}
			}
			n, eng = b.name, b.engine
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
			selected, count, counted, ratio := l.selectByConcurrency(req, excludeNames)
			if selected == nil {
				// Exit when no engine can be selected. Candidates may already be
				// exhausted by headroom/admission excludes, or skipped for non-positive
				// capacity without being marked. Prefer the previous backend error after a
				// real failover; otherwise ErrCacheMissHeadroom if any headroom skip
				// occurred; otherwise a generic selection failure.
				if retryCount > 0 {
					slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] no backend engine available on failover, returning previous error: %v", lastErr))
					cacheAwareRouteCounter.WithLabelValues(expectKind, lastActualKind, modelName, lastN).Inc()
					return lastResp, lastErr
				}

				if headroomSkipCount > 0 {
					return nil, ErrCacheMissHeadroom
				}

				return nil, fmt.Errorf("no backend engine available")
			}

			// Headroom is per backend, so exclude an over-limit cache miss and try the next candidate.
			if counted && selected.headroom.exceeded(req, count) {
				slog.InfoContext(req.Context(),
					fmt.Sprintf("[ShardKey Concurrency load balancer] skip cache-miss backend over headroom %.2f: %s (ratio=%.2f), candidates: %v", selected.headroom.cacheMissMaxUtilization, selected.name, ratio, candidates),
					slog.String("backend_name", selected.name),
				)
				excludeNames[selected.name] = true
				headroomSkipCount++
				continue
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

		req.SetMetadataValue(backendName, n)
		resp, err := eng.Process(req)
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
		lastResp, lastErr = resp, err
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
			return resp, err
		}
		slog.InfoContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] will retry, count %d, time %v", retryCount, time.Since(start)))
		totalFailoverRequestsCounter.WithLabelValues(modelName, n).Inc()
	}
}
