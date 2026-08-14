package loadbalancer

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/infinigence/octollm/pkg/octollm"
)

// ShardKeyWeightedRoundRobin implements weighted round-robin with optional backend affinity. It:
//   - Uses AffinityProvider to resolve ordered backend candidates.
//   - Tries eligible affinity candidates before normal WRR selection.
//   - Falls back to smooth WRR after affinity candidates are exhausted.
type ShardKeyWeightedRoundRobin struct {
	mu sync.Mutex

	backends         []*wrrBackend
	affinityProvider AffinityProvider
	retryTimeout     time.Duration
	retryMaxCount    int
}

var _ octollm.Engine = (*ShardKeyWeightedRoundRobin)(nil)

// NewShardKeyWeightedRoundRobin creates an affinity-aware weighted round-robin load balancer.
//
// Negative weights are rejected. If every configured weight is zero, all backends are normalized
// to the same positive weight. A nil affinityProvider disables affinity and uses normal WRR.
func NewShardKeyWeightedRoundRobin(
	backends []BackendItem,
	retryTimeout time.Duration,
	retryMaxCount int,
	affinityProvider AffinityProvider,
) (*ShardKeyWeightedRoundRobin, error) {
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

	wrrBackends := make([]*wrrBackend, len(backends))
	for i, backend := range backends {
		w := backend.Weight
		if allZero {
			w = 100
		}
		b := &wrrBackend{
			name:          backend.Name,
			weight:        w,
			engine:        backend.Engine,
			currentWeight: rand.Intn(w + 1),
		}
		wrrBackends[i] = b
	}

	return &ShardKeyWeightedRoundRobin{
		backends:         wrrBackends,
		affinityProvider: affinityProvider,
		retryTimeout:     retryTimeout,
		retryMaxCount:    retryMaxCount,
	}, nil
}

// resolveAffinity maps provider candidates to the existing WRR backends so currentWeight state
// remains shared across requests.
func (l *ShardKeyWeightedRoundRobin) resolveAffinity(req *octollm.Request) ([]*wrrBackend, AffinityCommitFunc, error) {
	if l.affinityProvider == nil {
		return nil, nil, nil
	}

	providerBackends, commit, err := l.affinityProvider.Resolve(req)
	if err != nil {
		return nil, nil, err
	}

	backendByName := make(map[string]*wrrBackend, len(l.backends))
	for _, b := range l.backends {
		if b.name != "" {
			backendByName[b.name] = b
		}
	}

	prioritized := make([]*wrrBackend, 0, len(providerBackends))
	for _, pb := range providerBackends {
		if pb == nil {
			continue
		}
		backend, ok := backendByName[pb.Name]
		if !ok || backend == nil || backend.weight <= 0 {
			continue
		}
		prioritized = append(prioritized, backend)
	}
	return prioritized, commit, nil
}

func (l *ShardKeyWeightedRoundRobin) Process(req *octollm.Request) (*octollm.Response, error) {
	// Materialize the request body once so every retry can read it again.
	if _, err := req.Body.Bytes(); err != nil {
		return nil, fmt.Errorf("failed to cache request body for retries: %w", err)
	}

	prioritizedBackends, commit, err := l.resolveAffinity(req)
	if err != nil {
		return nil, err
	}

	prioritizedIndex := 0
	excludeNames := make(map[string]bool)

	start := time.Now()
	retryCount := 0
	for {
		var prioritizedBackend string
		if prioritizedIndex < len(prioritizedBackends) {
			if b := prioritizedBackends[prioritizedIndex]; b != nil && !excludeNames[b.name] {
				prioritizedBackend = b.name
			}
			prioritizedIndex++
		}

		n, eng, isAllZero := l.GetNextEngine(req.Context(), prioritizedBackend, excludeNames)
		if eng == nil {
			return nil, fmt.Errorf("no backend engine available")
		}
		if prioritizedBackend != "" && n == prioritizedBackend {
			slog.InfoContext(req.Context(),
				fmt.Sprintf("[ShardKey WRR load balancer] prioritized backend hit: %s (index %d/%d)", n, prioritizedIndex, len(prioritizedBackends)),
				slog.String("backend_name", n),
			)
		} else {
			slog.InfoContext(req.Context(),
				fmt.Sprintf("[ShardKey WRR load balancer] no prioritized backend available (exhausted %d), fallback to WRR: %s", len(prioritizedBackends), n),
				slog.String("backend_name", n),
			)
		}
		req.SetMetadataValue(backendName, n)
		resp, err := eng.Process(req)
		if err == nil {
			// Do not persist affinity for the stateless all-zero fallback; its random selection
			// should not become a future affinity decision.
			if commit != nil && !isAllZero {
				if commitErr := commit(n); commitErr != nil {
					slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] failed to commit shard key mapping: %v", commitErr))
				}
			}
			return resp, nil
		}
		if isNotRetriableError(err) {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] error is not retriable, return without retry: %v", err))
			return resp, err
		}
		excludeNames[n] = true
		retryCount++
		if req.Context().Err() != nil {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] request context error: %v", req.Context().Err()))
			return resp, err
		}
		if time.Since(start) >= l.retryTimeout {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] retry period %v reached, return last resp and err", l.retryTimeout))
			return resp, err
		}
		if retryCount >= l.retryMaxCount {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] retry max count %d reached, return last resp and err", l.retryMaxCount))
			return resp, err
		}
		if len(excludeNames) >= len(l.backends) {
			slog.WarnContext(req.Context(), "[ShardKey WRR load balancer] all backends have been tried, return last resp and err")
			return resp, err
		}
		slog.InfoContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] will retry, count %d, time %v", retryCount, time.Since(start)))
		modelName, _ := octollm.GetCtxValue[string](req, octollm.ContextKeyModelName)
		totalFailoverRequestsCounter.WithLabelValues(modelName, n).Inc()
	}
}

// GetNextEngine applies affinity-aware smooth WRR selection.
//
// Per-pick algorithm:
//  1. Build non-excluded candidates with non-negative weights and compute totalWeight.
//  2. If totalWeight is zero, pick a random candidate without mutating WRR state.
//  3. Otherwise, prefer an eligible positive-weight affinity backend; fall back to the
//     positive-weight candidate with the highest currentWeight.
//  4. Penalize the selected backend by subtracting totalWeight.
//  5. Add each positive-weight candidate's weight to its currentWeight.
func (l *ShardKeyWeightedRoundRobin) GetNextEngine(ctx context.Context, prioritizedBackend string, excludeNames map[string]bool) (string, octollm.Engine, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	totalWeight := 0
	var candidates []*wrrBackend
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
			return "", nil, true
		}
		selected := candidates[rand.Intn(len(candidates))]
		slog.InfoContext(ctx,
			fmt.Sprintf("[ShardKey WRR load balancer] all-zero weights, random pick: %s, candidates: %v", selected.name, candidates),
			slog.String("backend_name", selected.name),
		)
		return selected.name, selected.engine, true
	}

	var selected *wrrBackend
	if prioritizedBackend != "" {
		for _, backend := range candidates {
			if backend.name == prioritizedBackend && backend.weight > 0 {
				selected = backend
				break
			}
		}
	}

	if selected == nil {
		for _, backend := range candidates {
			if backend.weight == 0 {
				continue
			}
			if selected == nil || backend.currentWeight > selected.currentWeight {
				selected = backend
			}
		}
	}

	if selected == nil {
		return "", nil, false
	}

	slog.InfoContext(ctx,
		fmt.Sprintf("[ShardKey WRR load balancer] selected: %s (currentWeight=%d), candidates: %v", selected.name, selected.currentWeight, candidates),
		slog.String("backend_name", selected.name),
	)

	selected.currentWeight -= totalWeight
	for _, backend := range candidates {
		if backend == nil || backend.weight <= 0 {
			continue
		}
		backend.currentWeight += backend.weight
	}

	return selected.name, selected.engine, false
}
