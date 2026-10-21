package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/infinigence/octollm/pkg/engines/limiter"
	"github.com/infinigence/octollm/pkg/engines/load-balancer/common"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/redis/go-redis/v9"
)

type sharedKeyRoundRobinBackend struct {
	name          string
	weight        int
	engine        octollm.Engine
	currentWeight int
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
// Negative weights are rejected. If every configured weight is zero, all backends are normalized
// to the same positive weight. A nil affinityProvider disables affinity and uses normal WRR.
// A nil backendAdmission skips admission checks and preserves current balancer behavior.
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
	}

	wrrBackends := make([]*sharedKeyRoundRobinBackend, len(backends))
	for i, backend := range backends {
		w := backend.Weight
		if allZero {
			w = 100
		}
		wrrBackends[i] = &sharedKeyRoundRobinBackend{
			name:          backend.Name,
			weight:        w,
			engine:        backend.Engine,
			currentWeight: rand.Intn(w + 1),
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
	expectKind := common.CacheMiss
	var lastRealResp *octollm.Response
	var lastRealErr error
	var lastN string
	var lastActualKind common.CacheHitKind
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
				expectKind = common.CacheHitStrong
			} else if expectKind != common.CacheHitStrong {
				expectKind = common.CacheHitWeak
			}
		}
		selection := l.selectNextEngine(req.Context(), prioritizedName, excludeNames)
		if selection == nil || selection.sharedKeyRoundRobinBackend == nil || selection.sharedKeyRoundRobinBackend.engine == nil {
			if retryCount > 0 {
				slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] no backend engine available on failover, returning previous error: %v", lastRealErr))
				cacheAwareRouteCounter.WithLabelValues(string(expectKind), string(lastActualKind), modelName, lastN).Inc()
			}
			if lastRealErr != nil {
				return lastRealResp, lastRealErr
			}
			return nil, fmt.Errorf("no backend engine available")
		}
		selected := selection.sharedKeyRoundRobinBackend
		n, eng, isAllZero := selected.name, selected.engine, selection.isAllZero
		isPrioritizedHit := prioritizedBackend != nil && n == prioritizedName
		var actualKind common.CacheHitKind
		if isPrioritizedHit {
			if strongCacheHit {
				actualKind = common.CacheHitStrong
			} else {
				actualKind = common.CacheHitWeak
			}
			slog.InfoContext(req.Context(),
				fmt.Sprintf("[ShardKey WRR load balancer] prioritized backend hit (strongCacheHit=%t): %s (index %d/%d)", strongCacheHit, n, prioritizedIndex, len(prioritizedBackends)),
				slog.String("backend_name", n),
			)
		} else {
			actualKind = common.CacheMiss
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
				excludeNames[n] = true
				continue
			}
		}

		attemptReq := req.WithContext(context.WithValue(req.Context(), common.ContextKeyCacheHitKind, actualKind))
		attemptReq.SetMetadataValue(backendName, n)
		resp, err := eng.Process(attemptReq)
		if done != nil {
			if err == nil {
				done(true)
			} else {
				if isBackendFailureError(err) {
					done(false)
				}
			}
		}

		if err == nil {
			if commit != nil && !isAllZero {
				if commitErr := commit(n); commitErr != nil {
					slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] failed to commit shard key mapping: %v", commitErr))
				}
			}
			cacheAwareRouteCounter.WithLabelValues(string(expectKind), string(actualKind), modelName, n).Inc()
			return resp, nil
		}

		// The following logic handles the case where err is non-nil.
		if isNotRetriableError(err) {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] error is not retriable, return without retry: %v", err))
			cacheAwareRouteCounter.WithLabelValues(string(expectKind), string(actualKind), modelName, n).Inc()
			return resp, err
		}

		if errors.Is(err, limiter.ErrRateLimitReached) {
			slog.InfoContext(req.Context(),
				fmt.Sprintf("[ShardKey WRR load balancer] limiter rejected backend %s, failover", n),
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
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] request context error: %v", req.Context().Err()))
			cacheAwareRouteCounter.WithLabelValues(string(expectKind), string(actualKind), modelName, n).Inc()
			return lastRealResp, lastRealErr
		}
		if time.Since(start) >= l.retryTimeout {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] retry period %v reached, return last resp and err", l.retryTimeout))
			cacheAwareRouteCounter.WithLabelValues(string(expectKind), string(actualKind), modelName, n).Inc()
			return lastRealResp, lastRealErr
		}
		if retryCount >= l.retryMaxCount {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] retry max count %d reached, return last resp and err", l.retryMaxCount))
			cacheAwareRouteCounter.WithLabelValues(string(expectKind), string(actualKind), modelName, n).Inc()
			return lastRealResp, lastRealErr
		}
		if len(excludeNames) >= len(l.backends) {
			slog.WarnContext(req.Context(), "[ShardKey WRR load balancer] all backends have been tried, return last resp and err")
			cacheAwareRouteCounter.WithLabelValues(string(expectKind), string(actualKind), modelName, n).Inc()
			if lastRealErr != nil {
				return lastRealResp, lastRealErr
			}
			return nil, fmt.Errorf("no backend engine available")
		}
		slog.InfoContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] will retry, count %d, time %v", retryCount, time.Since(start)))
		totalFailoverRequestsCounter.WithLabelValues(modelName, n).Inc()
	}
}

type wrrSelection struct {
	sharedKeyRoundRobinBackend *sharedKeyRoundRobinBackend
	isAllZero                  bool
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
// Steps 5–6 are applied immediately and are never rolled back.
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
	for _, backend := range candidates {
		if backend == nil || backend.weight <= 0 {
			continue
		}
		backend.currentWeight += backend.weight
	}

	return &wrrSelection{
		sharedKeyRoundRobinBackend: selected,
	}
}
