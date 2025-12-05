package loadbalancer

import (
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/sirupsen/logrus"
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

type WeightedRoundRobin struct {
	mu       sync.Mutex
	backends []*wrrBackend

	retryTimeout  time.Duration
	retryMaxCount int
}

var _ octollm.Engine = (*WeightedRoundRobin)(nil)

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
	start := time.Now()
	retryCount := 0
	for {
		n, eng := l.GetNextEngine()
		logrus.WithContext(req.Context()).Infof("[WRR load balancer] will use engine name: %s", n)
		resp, err := eng.Process(req)
		if err == nil {
			return resp, nil
		}
		retryCount++
		if time.Since(start) >= l.retryTimeout {
			// retry peroid reached, return last resp and err
			logrus.WithContext(req.Context()).Warnf("[WRR load balancer] retry peroid %v reached, return last resp and err", l.retryTimeout)
			return resp, err
		}
		if retryCount >= l.retryMaxCount {
			// retry max count reached, return last resp and err
			logrus.WithContext(req.Context()).Warnf("[WRR load balancer] retry max count %d reached, return last resp and err", l.retryMaxCount)
			return resp, err
		}
		logrus.WithContext(req.Context()).Infof("[WRR load balancer] will retry, count %d, time %v", retryCount, time.Since(start))
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
