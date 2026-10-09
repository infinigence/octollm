package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/engines/limiter"
	"github.com/infinigence/octollm/pkg/octollm"
)

type sharedKeyRoundRobinBackend struct {
	name          string
	weight        int
	layer         int
	engine        octollm.Engine
	currentWeight int
}

type prioritizedWrrBackend struct {
	backend        *sharedKeyRoundRobinBackend
	strongCacheHit bool
}

const defaultMinWeightMultiple = 5

// ShardKeyWeightedRoundRobin implements layered weighted round-robin with optional backend affinity. It:
//   - Restricts each pick to the highest layer that still has untried backends.
//   - Uses AffinityProvider to resolve ordered backend candidates.
//   - Tries eligible affinity candidates in that layer before normal WRR selection.
//   - Falls back to smooth WRR within that layer after its affinity candidates are exhausted.
//   - Writes CacheHitKind onto each attempt request; Color Limiter enforces headroom.
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
// Negative weights and layers are rejected. A nil affinityProvider disables affinity and uses
// normal WRR. A nil backendAdmission skips admission checks and preserves current balancer
// behavior.
// redisClient is required by the constructor for call-site compatibility.
// minWeightMultiple <= 0 uses defaultMinWeightMultiple.
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

	for _, backend := range backends {
		if backend.Weight < 0 {
			return nil, fmt.Errorf("weight must be >= 0")
		}
		if backend.Layer < 0 {
			return nil, fmt.Errorf("layer must be >= 0")
		}
	}

	wrrBackends := make([]*sharedKeyRoundRobinBackend, len(backends))
	for i, backend := range backends {
		wrrBackends[i] = &sharedKeyRoundRobinBackend{
			name:          backend.Name,
			weight:        backend.Weight,
			layer:         backend.Layer,
			engine:        backend.Engine,
			currentWeight: rand.Intn(backend.Weight + 1),
		}
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

// resolveAffinity maps provider candidates to the existing WRR backends so
// currentWeight state remains shared across requests.
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
	if _, err := req.Body.Bytes(); err != nil {
		return nil, fmt.Errorf("failed to cache request body for retries: %w", err)
	}

	modelName, _ := octollm.GetCtxValue[string](req, octollm.ContextKeyModelName)

	prioritizedBackends, commit, err := l.resolveAffinity(req)
	if err != nil {
		return nil, err
	}

	offeredPrioritized := make(map[string]bool)
	excludeNames := make(map[string]bool)

	start := time.Now()
	retryCount := 0
	expectKind := affinityKindMiss
	var lastRealResp *octollm.Response
	var lastRealErr, lastLimiterErr error
	var lastN, lastActualKind string
	for {
		// Offer each affinity candidate at most once, and only while its layer is the active one.
		layer := l.activeLayer(excludeNames)
		var prioritizedBackend *prioritizedWrrBackend
		for _, cand := range prioritizedBackends {
			if cand == nil || cand.backend == nil || cand.backend.layer != layer ||
				offeredPrioritized[cand.backend.name] || excludeNames[cand.backend.name] {
				continue
			}
			offeredPrioritized[cand.backend.name] = true
			prioritizedBackend = cand
			break
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
				slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] no backend engine available on failover, returning previous error: %v", lastRealErr))
				cacheAwareRouteCounter.WithLabelValues(expectKind, lastActualKind, modelName, lastN).Inc()
			}
			return exhaustAfterNoBackend(lastRealResp, lastRealErr, lastLimiterErr)
		}
		selected := selection.sharedKeyRoundRobinBackend
		n, eng := selected.name, selected.engine
		isPrioritizedHit := prioritizedBackend != nil && n == prioritizedName
		var actualKind string
		if isPrioritizedHit {
			if strongCacheHit {
				actualKind = affinityKindStrongCacheAware
			} else {
				actualKind = affinityKindWeakCacheAware
			}
			slog.InfoContext(req.Context(),
				fmt.Sprintf("[ShardKey WRR load balancer] prioritized backend hit (strongCacheHit=%t): %s (layer %d, %d prioritized)", strongCacheHit, n, layer, len(prioritizedBackends)),
				slog.String("backend_name", n),
			)
		} else {
			actualKind = affinityKindMiss
			slog.InfoContext(req.Context(),
				fmt.Sprintf("[ShardKey WRR load balancer] no prioritized backend available in layer %d (%d prioritized), fallback to WRR: %s", layer, len(prioritizedBackends), n),
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
				fmt.Sprintf("[ShardKey WRR load balancer] limiter rejected backend %s, failover", n),
				slog.String("backend_name", n),
			)
			excludeNames[n] = true
			lastLimiterErr = err
			if len(excludeNames) >= len(l.backends) {
				return exhaustAfterNoBackend(lastRealResp, lastRealErr, lastLimiterErr)
			}
			if req.Context().Err() != nil {
				slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] request context error: %v", req.Context().Err()))
				return exhaustAfterNoBackend(lastRealResp, lastRealErr, lastLimiterErr)
			}
			if time.Since(start) >= l.retryTimeout {
				slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] retry period %v reached, return last limiter err", l.retryTimeout))
				return exhaustAfterNoBackend(lastRealResp, lastRealErr, lastLimiterErr)
			}
			continue
		}
		if done != nil && !isIgnoredAttemptError(err) {
			done(err == nil)
		}
		if err == nil {
			if commit != nil {
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
		lastRealResp, lastRealErr = resp, err
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
			return exhaustAfterNoBackend(lastRealResp, lastRealErr, lastLimiterErr)
		}
		slog.InfoContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] will retry, count %d, time %v", retryCount, time.Since(start)))
		totalFailoverRequestsCounter.WithLabelValues(modelName, n).Inc()
	}
}

type wrrSelection struct {
	sharedKeyRoundRobinBackend *sharedKeyRoundRobinBackend
}

// activeLayer returns the highest layer that still has a non-excluded positive-weight backend, or
// -1 when none is left. Backend name, weight and layer never change after construction, so the
// lock is not required.
func (l *ShardKeyWeightedRoundRobin) activeLayer(excludeNames map[string]bool) int {
	layer := -1
	for _, backend := range l.backends {
		if backend == nil || backend.weight <= 0 || excludeNames[backend.name] {
			continue
		}
		if backend.layer > layer {
			layer = backend.layer
		}
	}
	return layer
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

// selectNextEngine applies layered, affinity-aware smooth WRR selection.
//
// Per-pick algorithm:
//  1. Pick the active layer: the highest layer that has a non-excluded positive-weight backend.
//  2. Build non-excluded positive-weight candidates from that layer only and compute its
//     totalWeight. Weight-0 backends are never selected.
//  3. Compute threshold = -minWeightMultiple * totalWeight.
//  4. Prefer the affinity backend if it is a candidate whose currentWeight is at least
//     threshold; otherwise pick the candidate with the highest currentWeight, even if every
//     candidate is below threshold.
//  5. Penalize the selected backend by subtracting totalWeight.
//  6. Add each candidate's weight to its currentWeight.
//
// Steps 5–6 are applied immediately, touch only the active layer, and are never rolled back.
func (l *ShardKeyWeightedRoundRobin) selectNextEngine(ctx context.Context, prioritizedBackend string, excludeNames map[string]bool) *wrrSelection {
	l.mu.Lock()
	defer l.mu.Unlock()

	layer := l.activeLayer(excludeNames)
	if layer < 0 {
		return &wrrSelection{}
	}

	totalWeight := 0
	var candidates []*sharedKeyRoundRobinBackend
	for _, backend := range l.backends {
		if backend == nil || backend.weight <= 0 || backend.layer != layer {
			continue
		}
		if excludeNames[backend.name] {
			continue
		}
		totalWeight += backend.weight
		candidates = append(candidates, backend)
	}

	threshold := l.weightThreshold(totalWeight)

	var selected *sharedKeyRoundRobinBackend
	if prioritizedBackend != "" {
		for _, backend := range candidates {
			if backend.name != prioritizedBackend {
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
		fmt.Sprintf("[ShardKey WRR load balancer] selected: %s (layer=%d, currentWeight=%d, threshold=%d), candidates: %v", selected.name, layer, selected.currentWeight, threshold, candidates),
		slog.String("backend_name", selected.name),
	)

	selected.currentWeight -= totalWeight
	for _, backend := range candidates {
		backend.currentWeight += backend.weight
	}

	return &wrrSelection{
		sharedKeyRoundRobinBackend: selected,
	}
}
