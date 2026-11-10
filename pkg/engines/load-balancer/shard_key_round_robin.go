package loadbalancer

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/engines/load-balancer/common"
	"github.com/infinigence/octollm/pkg/octollm"
)

type shardKeyRoundRobinBackend struct {
	name          string
	weight        int
	layer         int
	engine        octollm.Engine
	currentWeight int
}

type prioritizedWrrBackend struct {
	backend        *shardKeyRoundRobinBackend
	strongCacheHit bool
}

// ShardKeyWeightedRoundRobin implements layered weighted round-robin with optional backend affinity. It:
//   - Uses AffinityProvider to resolve ordered backend candidates.
//   - Tries every affinity candidate first, in provider order and whatever its layer.
//   - Then falls back to smooth WRR in the highest layer that still has untried backends.
//   - Writes CacheHitKind onto each attempt request; Color Limiter enforces headroom.
type ShardKeyWeightedRoundRobin struct {
	mu sync.Mutex

	backends         []*shardKeyRoundRobinBackend
	affinityProvider AffinityProvider
	retryTimeout     time.Duration
	retryMaxCount    int
	backendAdmission BackendAdmission
	redisClient      *redis.Client
}

var _ octollm.Engine = (*ShardKeyWeightedRoundRobin)(nil)

// NewShardKeyWeightedRoundRobin creates an affinity-aware weighted round-robin load balancer.
//
// Negative weights and layers are rejected. A nil affinityProvider disables affinity and uses
// normal WRR. A nil backendAdmission skips admission checks and preserves current balancer
// behavior.
// redisClient is required by the constructor for call-site compatibility.
func NewShardKeyWeightedRoundRobin(
	backends []BackendItem,
	retryTimeout time.Duration,
	retryMaxCount int,
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

	wrrBackends := make([]*shardKeyRoundRobinBackend, len(backends))
	for i, backend := range backends {
		wrrBackends[i] = &shardKeyRoundRobinBackend{
			name:          backend.Name,
			weight:        backend.Weight,
			layer:         backend.Layer,
			engine:        backend.Engine,
			currentWeight: rand.Intn(backend.Weight + 1),
		}
	}

	return &ShardKeyWeightedRoundRobin{
		backends:         wrrBackends,
		affinityProvider: affinityProvider,
		retryTimeout:     retryTimeout,
		retryMaxCount:    retryMaxCount,
		backendAdmission: backendAdmission,
		redisClient:      redisClient,
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

	backendByName := make(map[string]*shardKeyRoundRobinBackend, len(l.backends))
	for _, b := range l.backends {
		if b != nil && b.name != "" {
			backendByName[b.name] = b
		}
	}

	prioritized := make([]*prioritizedWrrBackend, 0, len(providerBackends))
	for _, pb := range providerBackends {
		if pb == nil || !pb.StrongCacheHit {
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

	excludeNames := make(map[string]bool)

	start := time.Now()
	retryCount := 0
	expectKind := common.CacheMiss
	var lastRealResp *octollm.Response
	var lastRealErr error
	var lastN string
	var lastActualKind common.CacheHitKind
	for {
		// Affinity candidates go first, in provider order and whatever their layer. A tried
		// backend is always excluded, so each candidate is offered at most once.
		var prioritized *prioritizedWrrBackend
		for _, cand := range prioritizedBackends {
			if !excludeNames[cand.backend.name] {
				prioritized = cand
				break
			}
		}

		prioritizedName := ""
		if prioritized != nil {
			prioritizedName = prioritized.backend.name
			if prioritized.strongCacheHit {
				expectKind = common.CacheHitStrong
			} else if expectKind != common.CacheHitStrong {
				expectKind = common.CacheHitWeak
			}
		}
		selection := l.selectNextEngine(req.Context(), prioritizedName, excludeNames)
		if selection == nil || selection.shardKeyRoundRobinBackend == nil || selection.shardKeyRoundRobinBackend.engine == nil {
			if lastRealErr != nil {
				slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] no backend engine available on failover, returning previous error: %v", lastRealErr))
				cacheAwareRouteCounter.WithLabelValues(string(expectKind), string(lastActualKind), modelName, lastN).Inc()
				return lastRealResp, lastRealErr
			}
			return nil, fmt.Errorf("no backend engine available")
		}
		selected := selection.shardKeyRoundRobinBackend
		n, eng := selected.name, selected.engine
		actualKind := common.CacheMiss
		if prioritized != nil {
			actualKind = common.CacheHitWeak
			if prioritized.strongCacheHit {
				actualKind = common.CacheHitStrong
			}
			slog.InfoContext(req.Context(),
				fmt.Sprintf("[ShardKey WRR load balancer] prioritized backend hit (strongCacheHit=%t): %s (layer %d, %d prioritized)", prioritized.strongCacheHit, n, selected.layer, len(prioritizedBackends)),
				slog.String("backend_name", n),
			)
		} else {
			slog.InfoContext(req.Context(),
				fmt.Sprintf("[ShardKey WRR load balancer] no prioritized backend left (%d prioritized), fallback to WRR: %s (layer %d)", len(prioritizedBackends), n, selected.layer),
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
			} else if isBackendFailureError(err) {
				done(false)
			}
		}

		if err == nil {
			if commit != nil {
				if commitErr := commit(n); commitErr != nil {
					slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] failed to commit shard key mapping: %v", commitErr))
				}
			}
			cacheAwareRouteCounter.WithLabelValues(string(expectKind), string(actualKind), modelName, n).Inc()
			return resp, nil
		}

		if isNotRetriableError(err) {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] error is not retriable, return without retry: %v", err))
			cacheAwareRouteCounter.WithLabelValues(string(expectKind), string(actualKind), modelName, n).Inc()
			return resp, err
		}

		lastRealResp, lastRealErr = resp, err
		lastN, lastActualKind = n, actualKind
		retryCount++
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
			return lastRealResp, lastRealErr
		}
		slog.InfoContext(req.Context(),
			fmt.Sprintf("[ShardKey WRR load balancer] backend %s failed, will retry, count %d, time %v", n, retryCount, time.Since(start)),
			slog.String("backend_name", n),
		)
		totalFailoverRequestsCounter.WithLabelValues(modelName, n).Inc()
	}
}

type wrrSelection struct {
	shardKeyRoundRobinBackend *shardKeyRoundRobinBackend
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

func pickMaxCurrentWeight(candidates []*shardKeyRoundRobinBackend) *shardKeyRoundRobinBackend {
	var selected *shardKeyRoundRobinBackend
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

// selectNextEngine picks the next backend with layered smooth WRR.
//
// A non-empty prioritizedBackend that names a non-excluded positive-weight backend is selected
// whatever its layer or currentWeight, and the WRR update below runs in its layer. Otherwise the
// active layer is the highest layer that still has a non-excluded positive-weight backend, and its
// candidate with the highest currentWeight is selected. Weight-0 backends are never selected.
//
// The selected backend loses its layer's totalWeight and every candidate in that layer gains its
// own weight. The update touches that layer only, is applied immediately, and is never rolled back.
func (l *ShardKeyWeightedRoundRobin) selectNextEngine(ctx context.Context, prioritizedBackend string, excludeNames map[string]bool) *wrrSelection {
	l.mu.Lock()
	defer l.mu.Unlock()

	var selected *shardKeyRoundRobinBackend
	if prioritizedBackend != "" && !excludeNames[prioritizedBackend] {
		for _, backend := range l.backends {
			if backend != nil && backend.weight > 0 && backend.name == prioritizedBackend {
				selected = backend
				break
			}
		}
	}

	var layer int
	if selected != nil {
		layer = selected.layer
	} else {
		layer = l.activeLayer(excludeNames)
	}
	if layer < 0 {
		return &wrrSelection{}
	}

	totalWeight := 0
	var candidates []*shardKeyRoundRobinBackend
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

	if selected == nil {
		selected = pickMaxCurrentWeight(candidates)
	}
	if selected == nil {
		return &wrrSelection{}
	}

	slog.InfoContext(ctx,
		fmt.Sprintf("[ShardKey WRR load balancer] selected: %s (layer=%d, currentWeight=%d, totalWeight=%d)", selected.name, layer, selected.currentWeight, totalWeight),
		slog.String("backend_name", selected.name),
	)

	selected.currentWeight -= totalWeight
	for _, backend := range candidates {
		backend.currentWeight += backend.weight
	}

	return &wrrSelection{
		shardKeyRoundRobinBackend: selected,
	}
}
