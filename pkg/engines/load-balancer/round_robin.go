package loadbalancer

import (
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/infinigence/octollm/pkg/octollm"
)

type BackendItem struct {
	Name   string // optional
	Weight int
	Engine octollm.Engine
}

type wrrBackend struct {
	name          string
	weight        int
	engine        octollm.Engine
	currentWeight int
}

func (b *wrrBackend) String() string {
	return fmt.Sprintf("{%s w=%d cw=%d}", b.name, b.weight, b.currentWeight)
}

type WeightedRoundRobin struct {
	mu       sync.Mutex
	backends []*wrrBackend

	retryTimeout  time.Duration
	retryMaxCount int
}

var _ octollm.Engine = (*WeightedRoundRobin)(nil)

type loadBalancerMetadataKey string

const (
	// backendName stores the name of the backend selected by the load balancer.
	backendName loadBalancerMetadataKey = "backend_name"
)

// GetSelectedBackendName retrieves the selected backend name from request metadata.
func GetSelectedBackendName(req *octollm.Request) (string, bool) {
	val, ok := req.GetMetadataValue(backendName)
	if !ok {
		return "", false
	}
	name, ok := val.(string)
	return name, ok
}

func NewWeightedRoundRobin(backends []BackendItem, retryTimeout time.Duration, retryMaxCount int) (*WeightedRoundRobin, error) {
	if len(backends) == 0 {
		return nil, fmt.Errorf("backends must have at least one item")
	}
	// if all weights are 0, set all weights to 1
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
		wrrBackends[i] = &wrrBackend{
			name:          backend.Name,
			weight:        w,
			engine:        backend.Engine,
			currentWeight: rand.Intn(w + 1),
		}
	}
	return &WeightedRoundRobin{
		backends:      wrrBackends,
		retryTimeout:  retryTimeout,
		retryMaxCount: retryMaxCount,
	}, nil
}

func (l *WeightedRoundRobin) Process(req *octollm.Request) (*octollm.Response, error) {
	// cache request body for retries
	if _, err := req.Body.Bytes(); err != nil {
		return nil, fmt.Errorf("failed to cache request body for retries: %w", err)
	}

	start := time.Now()
	retryCount := 0
	for {
		n, eng := l.GetNextEngine()
		slog.InfoContext(req.Context(), fmt.Sprintf("[WRR load balancer] will use engine name: %s", n))
		req.SetMetadataValue(backendName, n)
		resp, err := eng.Process(req)
		if err == nil {
			return resp, nil
		}
		retryCount++
		if req.Context().Err() != nil {
			slog.WarnContext(req.Context(), fmt.Sprintf("[WRR load balancer] request context error: %v", req.Context().Err()))
			return resp, err
		}
		if time.Since(start) >= l.retryTimeout {
			slog.WarnContext(req.Context(), fmt.Sprintf("[WRR load balancer] retry peroid %v reached, return last resp and err", l.retryTimeout))
			return resp, err
		}
		if retryCount >= l.retryMaxCount {
			slog.WarnContext(req.Context(), fmt.Sprintf("[WRR load balancer] retry max count %d reached, return last resp and err", l.retryMaxCount))
			return resp, err
		}
		slog.InfoContext(req.Context(), fmt.Sprintf("[WRR load balancer] will retry, count %d, time %v", retryCount, time.Since(start)))
		modelName, _ := octollm.GetCtxValue[string](req, octollm.ContextKeyModelName)
		totalFailoverRequestsCounter.WithLabelValues(modelName, n).Inc()
	}
}

func (l *WeightedRoundRobin) GetNextEngine() (string, octollm.Engine) {
	l.mu.Lock()
	defer l.mu.Unlock()

	totalWeight := 0
	maxWeight := 0
	var maxWeightBackend *wrrBackend = nil
	for _, backend := range l.backends {
		backend.currentWeight += backend.weight
		totalWeight += backend.weight
		if backend.currentWeight > maxWeight {
			maxWeight = backend.currentWeight
			maxWeightBackend = backend
		}
	}
	if maxWeightBackend == nil {
		return "", nil
	}
	maxWeightBackend.currentWeight -= totalWeight
	return maxWeightBackend.name, maxWeightBackend.engine
}
