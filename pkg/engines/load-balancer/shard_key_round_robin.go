package loadbalancer

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

// ShardKeyWeightedRoundRobin implements a weighted round-robin load balancer with shard key support.
// It follows the same structure and algorithm as WeightedRoundRobin, but:
//   - Uses Redis pipeline to resolve shard_key_list -> backendName
//   - Prioritizes backends corresponding to shard_key_list (later keys have higher priority)
//   - Once all shard-key backends are used, falls back to normal WRR by weight.
type ShardKeyWeightedRoundRobin struct {
	mu sync.Mutex

	backends []*wrrBackend

	// prioritizedBackends are resolved from shardKeyList via Redis.
	// Requests will be routed to these first (from high to low priority),
	// then fall back to standard weighted round-robin.
	prioritizedBackends []*wrrBackend
	prioritizedIndex    int
	prioritizedSet      map[*wrrBackend]struct{}

	retryTimeout  time.Duration
	retryMaxCount int
}

var _ octollm.Engine = (*ShardKeyWeightedRoundRobin)(nil)

// NewShardKeyWeightedRoundRobin creates a shard-key-aware weighted round-robin load balancer.
//
// Parameters:
//   - backends: backend items with name/weight/engine (same as WeightedRoundRobin)
//   - retryTimeout: maximum time to retry failed requests
//   - retryMaxCount: maximum number of retries
//   - shardKeyList: shard keys for this request (string array, later elements have higher priority)
//   - redisClient: Redis client, used to resolve shard keys to backend names with pipeline
func NewShardKeyWeightedRoundRobin(
	backends []BackendItem,
	retryTimeout time.Duration,
	retryMaxCount int,
	shardKeyList []string,
	redisClient *redis.Client,
) (*ShardKeyWeightedRoundRobin, error) {
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
	backendByName := make(map[string]*wrrBackend, len(backends))
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
		if backend.Name != "" {
			backendByName[backend.Name] = b
		}
	}

	lb := &ShardKeyWeightedRoundRobin{
		backends:       wrrBackends,
		retryTimeout:   retryTimeout,
		retryMaxCount:  retryMaxCount,
		prioritizedSet: make(map[*wrrBackend]struct{}),
	}

	// Resolve prioritized backends via Redis pipeline if shardKeyList and redisClient are provided.
	if len(shardKeyList) > 0 && redisClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		pipe := redisClient.Pipeline()
		cmds := make([]*redis.StringCmd, len(shardKeyList))
		for i, shardKey := range shardKeyList {
			if shardKey == "" {
				continue
			}
			cmds[i] = pipe.Get(ctx, shardKey)
		}

		if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
			logrus.WithContext(ctx).Warnf("[ShardKey WRR load balancer] failed to exec Redis pipeline for shard keys: %v", err)
		}

		seen := make(map[string]bool)
		// Iterate from last to first so later shard keys have higher priority.
		for i := len(shardKeyList) - 1; i >= 0; i-- {
			cmd := cmds[i]
			if cmd == nil {
				continue
			}
			backendName, err := cmd.Result()
			if err != nil {
				if err != redis.Nil {
					logrus.WithContext(ctx).Debugf("[ShardKey WRR load balancer] Redis get error for shard key %s: %v", shardKeyList[i], err)
				}
				continue
			}
			if backendName == "" || seen[backendName] {
				continue
			}
			if backend, ok := backendByName[backendName]; ok {
				lb.prioritizedBackends = append(lb.prioritizedBackends, backend)
				lb.prioritizedSet[backend] = struct{}{}
				seen[backendName] = true
			}
		}
	}

	return lb, nil
}

func (l *ShardKeyWeightedRoundRobin) Process(req *octollm.Request) (*octollm.Response, error) {
	// cache request body for retries
	if _, err := req.Body.Bytes(); err != nil {
		return nil, fmt.Errorf("failed to cache request body for retries: %w", err)
	}

	start := time.Now()
	retryCount := 0
	for {
		n, eng := l.GetNextEngine()
		logrus.WithContext(req.Context()).Infof("[ShardKey WRR load balancer] will use engine name: %s", n)
		resp, err := eng.Process(req)
		if err == nil {
			// Reuse same metadata key as normal WRR.
			resp.SetMetadataValue(backendName, n)
			return resp, nil
		}
		retryCount++
		if time.Since(start) >= l.retryTimeout {
			// retry period reached, return last resp and err
			logrus.WithContext(req.Context()).Warnf("[ShardKey WRR load balancer] retry period %v reached, return last resp and err", l.retryTimeout)
			return resp, err
		}
		if retryCount >= l.retryMaxCount {
			// retry max count reached, return last resp and err
			logrus.WithContext(req.Context()).Warnf("[ShardKey WRR load balancer] retry max count %d reached, return last resp and err", l.retryMaxCount)
			return resp, err
		}
		logrus.WithContext(req.Context()).Infof("[ShardKey WRR load balancer] will retry, count %d, time %v", retryCount, time.Since(start))
	}
}

// GetNextEngine selects the next backend engine based on shard key matching and weighted round-robin.
// It prioritizes backends mapped from shardKeyList (for cache hit).
// Once all prioritized backends are used, it falls back to normal WRR by weight.
func (l *ShardKeyWeightedRoundRobin) GetNextEngine() (string, octollm.Engine) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// First, consume prioritized backends resolved from shardKeyList.
	if l.prioritizedIndex < len(l.prioritizedBackends) {
		backend := l.prioritizedBackends[l.prioritizedIndex]
		l.prioritizedIndex++
		return backend.name, backend.engine
	}

	// Fallback to standard weighted round-robin (same algorithm as WeightedRoundRobin),
	// but excluding backends that have already been used via prioritizedBackends.
	totalWeight := 0
	maxWeight := 0
	var maxWeightBackend *wrrBackend
	for _, backend := range l.backends {
		if _, used := l.prioritizedSet[backend]; used {
			// Skip backends that were already selected by shard_key_list.
			continue
		}
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
