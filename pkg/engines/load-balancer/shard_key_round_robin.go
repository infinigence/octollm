package loadbalancer

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/redis/go-redis/v9"
)

type sharedKeyRoundRobinBackend struct {
	name             string
	weight           int
	engine           octollm.Engine
	currentWeight    int
	headroom         backendHeadroom
	concurrencyKeyFn func(*octollm.Request) string
}

type prioritizedWrrBackend struct {
	backend        *sharedKeyRoundRobinBackend
	strongCacheHit bool
}

const defaultMinWeightMultiple = 5

// ShardKeyWeightedRoundRobin implements weighted round-robin with optional backend affinity. It:
//   - Uses AffinityProvider to resolve ordered backend candidates.
//   - Tries eligible affinity candidates before normal WRR selection.
//   - Falls back to smooth WRR after affinity candidates are exhausted.
type ShardKeyWeightedRoundRobin struct {
	mu sync.Mutex

	backends         []*sharedKeyRoundRobinBackend
	affinityProvider AffinityProvider
	retryTimeout     time.Duration
	retryMaxCount    int
	backendAdmission BackendAdmission
	redisClient      *redis.Client
	// minWeightMultiple is the floor for affinity hint only:
	// a prioritized backend is skipped while currentWeight < -minWeightMultiple * totalWeight.
	// Fallback WRR still picks the highest currentWeight among live candidates.
	// Values <= 0 use defaultMinWeightMultiple.
	minWeightMultiple int
}

var _ octollm.Engine = (*ShardKeyWeightedRoundRobin)(nil)

// NewShardKeyWeightedRoundRobin creates an affinity-aware weighted round-robin load balancer.
//
// Negative weights are rejected. If every configured weight is zero, all backends are normalized
// to the same positive weight. A nil affinityProvider disables affinity and uses normal WRR.
// A nil backendAdmission skips admission checks and preserves current balancer behavior.
// redisClient is required for cache-miss headroom checks. Each BackendItem must supply
// ConcurrencyKeyFn. minWeightMultiple <= 0 uses defaultMinWeightMultiple.
func NewShardKeyWeightedRoundRobin(
	backends []BackendItem,
	retryTimeout time.Duration,
	retryMaxCount int,
	minWeightMultiple int,
	affinityProvider AffinityProvider,
	backendAdmission BackendAdmission,
	redisClient *redis.Client,
) (*ShardKeyWeightedRoundRobin, error) {
	if redisClient == nil {
		return nil, fmt.Errorf("redisClient must be provided")
	}

	if len(backends) == 0 {
		return nil, fmt.Errorf("backends must have at least one item")
	}

	// Normalize an all-zero configuration to equal positive weights so affinity routing and smooth
	// WRR remain active.
	allZero := true
	for _, backend := range backends {
		if backend.Weight < 0 {
			return nil, fmt.Errorf("weight must be >= 0")
		}
		if backend.Weight != 0 {
			allZero = false
		}
		if backend.ConcurrencyKeyFn == nil {
			return nil, fmt.Errorf("concurrencyKeyFn must be provided")
		}
	}

	wrrBackends := make([]*sharedKeyRoundRobinBackend, len(backends))
	for i, backend := range backends {
		w := backend.Weight
		if allZero {
			w = 100
		}
		b := &sharedKeyRoundRobinBackend{
			name:             backend.Name,
			weight:           w,
			engine:           backend.Engine,
			currentWeight:    rand.Intn(w + 1),
			headroom:         newBackendHeadroom(backend),
			concurrencyKeyFn: backend.ConcurrencyKeyFn,
		}
		wrrBackends[i] = b
	}

	if minWeightMultiple <= 0 {
		minWeightMultiple = defaultMinWeightMultiple
	}

	return &ShardKeyWeightedRoundRobin{
		backends:          wrrBackends,
		affinityProvider:  affinityProvider,
		retryTimeout:      retryTimeout,
		retryMaxCount:     retryMaxCount,
		backendAdmission:  backendAdmission,
		redisClient:       redisClient,
		minWeightMultiple: minWeightMultiple,
	}, nil
}

func (l *ShardKeyWeightedRoundRobin) concurrencyCount(req *octollm.Request, b *sharedKeyRoundRobinBackend) (int64, bool) {
	if b == nil || b.concurrencyKeyFn == nil {
		return 0, false
	}
	key := b.concurrencyKeyFn(req)
	if key == "" {
		return 0, false
	}
	count, err := l.redisClient.ZCard(req.Context(), key).Result()
	if err != nil {
		slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] failed to ZCard concurrency for headroom check on %s: %v", b.name, err))
		return 0, false
	}
	return count, true
}

// resolveAffinity maps provider candidates to the existing WRR backends so
// currentWeight state remains shared across requests. Weak hits are kept and
// obey cache-miss headroom; strong hits may bypass the ceiling.
func (l *ShardKeyWeightedRoundRobin) resolveAffinity(req *octollm.Request) ([]*prioritizedWrrBackend, AffinityCommitFunc, error) {
	if l.affinityProvider == nil {
		return nil, nil, nil
	}

	providerBackends, commit, err := l.affinityProvider.Resolve(req)
	if err != nil {
		return nil, nil, err
	}

	backendByName := make(map[string]*sharedKeyRoundRobinBackend, len(l.backends))
	for _, b := range l.backends {
		if b != nil && b.name != "" {
			backendByName[b.name] = b
		}
	}

	prioritized := make([]*prioritizedWrrBackend, 0, len(providerBackends))
	for _, pb := range providerBackends {
		if pb == nil {
			continue
		}
		backend, ok := backendByName[pb.Name]
		if !ok || backend == nil || backend.weight <= 0 {
			continue
		}
		prioritized = append(prioritized, &prioritizedWrrBackend{
			backend:        backend,
			strongCacheHit: pb.StrongCacheHit,
		})
	}
	return prioritized, commit, nil
}

func (l *ShardKeyWeightedRoundRobin) Process(req *octollm.Request) (*octollm.Response, error) {
	// Materialize the request body once so every retry can read it again.
	if _, err := req.Body.Bytes(); err != nil {
		return nil, fmt.Errorf("failed to cache request body for retries: %w", err)
	}

	modelName, _ := octollm.GetCtxValue[string](req, octollm.ContextKeyModelName)

	prioritizedBackends, commit, err := l.resolveAffinity(req)
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
		var prioritizedBackend *prioritizedWrrBackend
		if prioritizedIndex < len(prioritizedBackends) {
			cand := prioritizedBackends[prioritizedIndex]
			prioritizedIndex++
			if cand == nil || cand.backend == nil || excludeNames[cand.backend.name] {
				continue
			}
			prioritizedBackend = cand
		}

		prioritizedName := ""
		strongCacheHit := false
		if prioritizedBackend != nil {
			prioritizedName = prioritizedBackend.backend.name
			strongCacheHit = prioritizedBackend.strongCacheHit
			if strongCacheHit {
				expectKind = affinityKindStrongCacheAware
			} else if expectKind != affinityKindStrongCacheAware {
				expectKind = affinityKindWeakCacheAware
			}
		}
		selection := l.selectNextEngine(req.Context(), prioritizedName, excludeNames)
		if selection == nil || selection.sharedKeyRoundRobinBackend == nil || selection.sharedKeyRoundRobinBackend.engine == nil {
			if retryCount > 0 {
				slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] no backend engine available on failover, returning previous error: %v", lastErr))
				cacheAwareRouteCounter.WithLabelValues(expectKind, lastActualKind, modelName, lastN).Inc()
				return lastResp, lastErr
			}
			if headroomSkipCount > 0 {
				return nil, ErrCacheMissHeadroom
			}
			return nil, fmt.Errorf("no backend engine available")
		}
		selected := selection.sharedKeyRoundRobinBackend
		n, eng, isAllZero := selected.name, selected.engine, selection.isAllZero
		isPrioritizedHit := prioritizedBackend != nil && n == prioritizedName
		// MaxConcurrencyFn is optional on WRR. denom is only a concurrency cap when
		// headroom is enabled; do not treat denom<=0 as no capacity otherwise (it may just be Weight).
		if isPrioritizedHit {
			if selected.headroom.enabled() && selected.headroom.denom(req) <= 0 {
				slog.InfoContext(req.Context(),
					fmt.Sprintf("[ShardKey WRR load balancer] skip prioritized backend with no effective capacity: %s (index %d/%d)", n, prioritizedIndex, len(prioritizedBackends)),
					slog.String("backend_name", n),
				)
				excludeNames[n] = true
				headroomSkipCount++
				selection.Rollback()
				continue
			}
			// Strong hits may use reserved capacity; weak hits obey the ceiling.
			if !strongCacheHit && selected.headroom.enabled() {
				count, ok := l.concurrencyCount(req, selected)
				if ok && selected.headroom.exceeded(req, count) {
					slog.InfoContext(req.Context(),
						fmt.Sprintf("[ShardKey WRR load balancer] skip weak-cache-hit prioritized backend over headroom %.2f: %s (index %d/%d)", selected.headroom.cacheMissMaxUtilization, n, prioritizedIndex, len(prioritizedBackends)),
						slog.String("backend_name", n),
					)
					excludeNames[n] = true
					headroomSkipCount++
					selection.Rollback()
					continue
				}
			}
		} else if selected.headroom.enabled() {
			if selected.headroom.denom(req) <= 0 {
				slog.InfoContext(req.Context(),
					fmt.Sprintf("[ShardKey WRR load balancer] skip cache-miss backend with no effective capacity: %s", n),
					slog.String("backend_name", n),
				)
				excludeNames[n] = true
				headroomSkipCount++
				selection.Rollback()
				continue
			}
			count, ok := l.concurrencyCount(req, selected)
			if ok && selected.headroom.exceeded(req, count) {
				slog.InfoContext(req.Context(),
					fmt.Sprintf("[ShardKey WRR load balancer] skip cache-miss backend over headroom %.2f: %s", selected.headroom.cacheMissMaxUtilization, n),
					slog.String("backend_name", n),
				)
				excludeNames[n] = true
				headroomSkipCount++
				selection.Rollback()
				continue
			}
		}
		var actualKind string
		if isPrioritizedHit {
			if strongCacheHit {
				actualKind = affinityKindStrongCacheAware
			} else {
				actualKind = affinityKindWeakCacheAware
			}
			slog.InfoContext(req.Context(),
				fmt.Sprintf("[ShardKey WRR load balancer] prioritized backend hit (strongCacheHit=%t): %s (index %d/%d)", strongCacheHit, n, prioritizedIndex, len(prioritizedBackends)),
				slog.String("backend_name", n),
			)
		} else {
			actualKind = affinityKindMiss
			slog.InfoContext(req.Context(),
				fmt.Sprintf("[ShardKey WRR load balancer] no prioritized backend available (exhausted %d), fallback to WRR: %s", len(prioritizedBackends), n),
				slog.String("backend_name", n),
			)
		}

		var done AttemptDoneFunc
		if l.backendAdmission != nil {
			var allowed bool
			done, allowed = l.backendAdmission.BeforeAttempt(req, n)
			if !allowed {
				slog.InfoContext(req.Context(),
					fmt.Sprintf("[ShardKey WRR load balancer] skip backend denied by admission: %s", n),
					slog.String("backend_name", n),
				)
				selection.Rollback()
				excludeNames[n] = true
				continue
			}
		}

		req.SetMetadataValue(backendName, n)
		resp, err := eng.Process(req)
		if done != nil && !isIgnoredAttemptError(err) {
			done(err == nil)
		}
		if err == nil {
			// Do not persist affinity for the stateless all-zero fallback; its random selection
			// should not become a future affinity decision.
			if commit != nil && !isAllZero {
				if commitErr := commit(n); commitErr != nil {
					slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] failed to commit shard key mapping: %v", commitErr))
				}
			}
			cacheAwareRouteCounter.WithLabelValues(expectKind, actualKind, modelName, n).Inc()
			return resp, nil
		}
		if isNotRetriableError(err) {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] error is not retriable, return without retry: %v", err))
			cacheAwareRouteCounter.WithLabelValues(expectKind, actualKind, modelName, n).Inc()
			return resp, err
		}
		excludeNames[n] = true
		lastResp, lastErr = resp, err
		lastN, lastActualKind = n, actualKind
		retryCount++
		if req.Context().Err() != nil {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] request context error: %v", req.Context().Err()))
			cacheAwareRouteCounter.WithLabelValues(expectKind, actualKind, modelName, n).Inc()
			return resp, err
		}
		if time.Since(start) >= l.retryTimeout {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] retry period %v reached, return last resp and err", l.retryTimeout))
			cacheAwareRouteCounter.WithLabelValues(expectKind, actualKind, modelName, n).Inc()
			return resp, err
		}
		if retryCount >= l.retryMaxCount {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] retry max count %d reached, return last resp and err", l.retryMaxCount))
			cacheAwareRouteCounter.WithLabelValues(expectKind, actualKind, modelName, n).Inc()
			return resp, err
		}
		if len(excludeNames) >= len(l.backends) {
			slog.WarnContext(req.Context(), "[ShardKey WRR load balancer] all backends have been tried, return last resp and err")
			cacheAwareRouteCounter.WithLabelValues(expectKind, actualKind, modelName, n).Inc()
			return resp, err
		}
		slog.InfoContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] will retry, count %d, time %v", retryCount, time.Since(start)))
		totalFailoverRequestsCounter.WithLabelValues(modelName, n).Inc()
	}
}

type wrrSelection struct {
	sharedKeyRoundRobinBackend *sharedKeyRoundRobinBackend
	isAllZero                  bool
	rollback                   func()
}

func (s *wrrSelection) Rollback() {
	if s != nil && s.rollback != nil {
		s.rollback()
	}
}

func (l *ShardKeyWeightedRoundRobin) weightThreshold(totalWeight int) int {
	multiple := l.minWeightMultiple
	if multiple <= 0 {
		multiple = defaultMinWeightMultiple
	}
	return -multiple * totalWeight
}

func pickMaxCurrentWeight(candidates []*sharedKeyRoundRobinBackend) *sharedKeyRoundRobinBackend {
	var selected *sharedKeyRoundRobinBackend
	for _, backend := range candidates {
		if backend == nil || backend.weight <= 0 {
			continue
		}
		if selected == nil || backend.currentWeight > selected.currentWeight {
			selected = backend
		}
	}
	return selected
}

// selectNextEngine applies affinity-aware smooth WRR selection.
//
// Per-pick algorithm:
//  1. Build non-excluded candidates with non-negative weights and compute totalWeight.
//  2. If totalWeight is zero, pick a random candidate without mutating WRR state.
//  3. Otherwise compute threshold = -minWeightMultiple * totalWeight.
//  4. Prefer an eligible positive-weight affinity backend whose currentWeight is
//     at least threshold; otherwise pick the positive-weight candidate with the
//     highest currentWeight, even if every candidate is below threshold.
//  5. Penalize the selected backend by subtracting totalWeight.
//  6. Add each positive-weight candidate's weight to its currentWeight.
//
// Steps 5–6 are applied immediately. The returned rollback undoes that delta;
// Process calls it on hook deny and leaves the update in place after a real Process.
func (l *ShardKeyWeightedRoundRobin) selectNextEngine(ctx context.Context, prioritizedBackend string, excludeNames map[string]bool) *wrrSelection {
	l.mu.Lock()
	defer l.mu.Unlock()

	totalWeight := 0
	var candidates []*sharedKeyRoundRobinBackend
	for _, backend := range l.backends {
		if backend == nil || backend.weight < 0 {
			continue
		}
		if excludeNames[backend.name] {
			continue
		}
		totalWeight += backend.weight
		candidates = append(candidates, backend)
	}
	isAllZero := totalWeight == 0

	// Keep the all-zero fallback stateless; no smooth-WRR weight is updated on this path.
	if isAllZero {
		if len(candidates) == 0 {
			return &wrrSelection{isAllZero: true}
		}
		selected := candidates[rand.Intn(len(candidates))]
		slog.InfoContext(ctx,
			fmt.Sprintf("[ShardKey WRR load balancer] all-zero weights, random pick: %s, candidates: %v", selected.name, candidates),
			slog.String("backend_name", selected.name),
		)
		return &wrrSelection{sharedKeyRoundRobinBackend: selected, isAllZero: true}
	}

	threshold := l.weightThreshold(totalWeight)

	var selected *sharedKeyRoundRobinBackend
	if prioritizedBackend != "" {
		for _, backend := range candidates {
			if backend.name != prioritizedBackend || backend.weight <= 0 {
				continue
			}
			if backend.currentWeight >= threshold {
				selected = backend
			} else {
				slog.InfoContext(ctx,
					fmt.Sprintf("[ShardKey WRR load balancer] skip prioritized backend below threshold: %s (currentWeight=%d, threshold=%d)", backend.name, backend.currentWeight, threshold),
					slog.String("backend_name", backend.name),
				)
			}
			break
		}
	}

	if selected == nil {
		selected = pickMaxCurrentWeight(candidates)
	}

	if selected == nil {
		return &wrrSelection{}
	}

	slog.InfoContext(ctx,
		fmt.Sprintf("[ShardKey WRR load balancer] selected: %s (currentWeight=%d, threshold=%d), candidates: %v", selected.name, selected.currentWeight, threshold, candidates),
		slog.String("backend_name", selected.name),
	)

	selected.currentWeight -= totalWeight
	var incremented []*sharedKeyRoundRobinBackend
	var added []int
	for _, backend := range candidates {
		if backend == nil || backend.weight <= 0 {
			continue
		}
		backend.currentWeight += backend.weight
		incremented = append(incremented, backend)
		added = append(added, backend.weight)
	}

	total := totalWeight
	picked := selected
	return &wrrSelection{
		sharedKeyRoundRobinBackend: selected,
		rollback: func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			picked.currentWeight += total
			for i, backend := range incremented {
				backend.currentWeight -= added[i]
			}
		},
	}
}
