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

// ShardKeyWeightedRoundRobin implements a weighted round-robin load balancer with shard key support.
// It follows the same structure and algorithm as WeightedRoundRobin, but:
//   - Uses Redis pipeline to resolve shard_key_list -> backendName
//   - Prioritizes backends corresponding to shard_key_list (later keys have higher priority)
//   - Once all shard-key backends are used, falls back to normal WRR by weight.
type ShardKeyWeightedRoundRobin struct {
	mu sync.Mutex

	backends []*wrrBackend

	// shardKeyListGetter and redisClient are used to resolve prioritized backends in Process.
	shardKeyListGetter func(req *octollm.Request) []string
	redisClient        *redis.Client
	cacheTTL           time.Duration

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
//   - cacheTTL: expiration time for shard key -> backend mapping in Redis
//   - shardKeyList: shard keys for this request (string array, later elements have higher priority)
//   - redisClient: Redis client, used to resolve shard keys to backend names with pipeline
func NewShardKeyWeightedRoundRobin(
	backends []BackendItem,
	retryTimeout time.Duration,
	retryMaxCount int,
	cacheTTL time.Duration,
	shardKeyListGetter func(req *octollm.Request) []string,
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

	lb := &ShardKeyWeightedRoundRobin{
		backends:           wrrBackends,
		shardKeyListGetter: shardKeyListGetter,
		redisClient:        redisClient,
		cacheTTL:           cacheTTL,
		retryTimeout:       retryTimeout,
		retryMaxCount:      retryMaxCount,
	}

	return lb, nil
}

// resolvePrioritizedBackends resolves shardKeyList -> backendName via Redis and returns
// backend pointers ordered from high to low priority (later keys have higher priority).
// Note: This is computed per-request; backend weight state (currentWeight) stays on l.backends and is reused.
func (l *ShardKeyWeightedRoundRobin) resolvePrioritizedBackends(
	ctx context.Context,
	shardKeyList []string,
) (prioritizedBackends []*wrrBackend) {
	if l.redisClient == nil || len(shardKeyList) == 0 {
		return nil
	}

	pipe := l.redisClient.Pipeline()
	cmds := make([]*redis.StringCmd, len(shardKeyList))
	for i, shardKey := range shardKeyList {
		if shardKey == "" {
			continue
		}
		cmds[i] = pipe.Get(ctx, shardKey)
	}

	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		slog.WarnContext(ctx, fmt.Sprintf("[ShardKey WRR load balancer] failed to exec Redis pipeline for shard keys: %v", err))
	}

	backendByName := make(map[string]*wrrBackend, len(l.backends))
	for _, b := range l.backends {
		if b.name != "" {
			backendByName[b.name] = b
		}
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
				slog.DebugContext(ctx, fmt.Sprintf("[ShardKey WRR load balancer] Redis get error for shard key %s: %v", shardKeyList[i], err))
			}
			continue
		}
		if backendName == "" || seen[backendName] {
			continue
		}
		if backend, ok := backendByName[backendName]; ok {
			prioritizedBackends = append(prioritizedBackends, backend)
			seen[backendName] = true
		}
	}

	return prioritizedBackends
}

func (l *ShardKeyWeightedRoundRobin) Process(req *octollm.Request) (*octollm.Response, error) {
	// cache request body for retries
	if _, err := req.Body.Bytes(); err != nil {
		return nil, fmt.Errorf("failed to cache request body for retries: %w", err)
	}

	var shardKeyList []string
	if l.shardKeyListGetter != nil {
		shardKeyList = l.shardKeyListGetter(req)
	}
	prioritizedBackends := l.resolvePrioritizedBackends(req.Context(), shardKeyList)
	prioritizedIndex := 0

	start := time.Now()
	retryCount := 0
	for {
		var prioritizedBackend string
		if prioritizedIndex < len(prioritizedBackends) {
			if b := prioritizedBackends[prioritizedIndex]; b != nil {
				prioritizedBackend = b.name
				prioritizedIndex++
			}
		}

		n, eng := l.GetNextEngine(prioritizedBackend)
		if eng == nil {
			return nil, fmt.Errorf("no backend engine available")
		}
		slog.InfoContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] will use engine name: %s", n))
		resp, err := eng.Process(req)
		if err == nil {
			// Reuse same metadata key as normal WRR.
			resp.SetMetadataValue(backendName, n)

			// Update Redis: shardKeyList -> selected backend name with configured TTL.
			if l.redisClient != nil && len(shardKeyList) > 0 && l.cacheTTL > 0 {
				pipe := l.redisClient.Pipeline()
				for _, shardKey := range shardKeyList {
					if shardKey == "" {
						continue
					}
					pipe.Set(req.Context(), shardKey, n, l.cacheTTL)
				}
				if _, err := pipe.Exec(req.Context()); err != nil && err != redis.Nil {
					slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] failed to update shard key mapping in Redis: %v", err))
				}
			}

			return resp, nil
		}
		retryCount++
		if time.Since(start) >= l.retryTimeout {
			// retry period reached, return last resp and err
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] retry period %v reached, return last resp and err", l.retryTimeout))
			return resp, err
		}
		if retryCount >= l.retryMaxCount {
			// retry max count reached, return last resp and err
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] retry max count %d reached, return last resp and err", l.retryMaxCount))
			return resp, err
		}
		slog.InfoContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] will retry, count %d, time %v", retryCount, time.Since(start)))
	}
}

// GetNextEngine applies the shard-key aware WRR selection:
//   - all backends first add their own weight once
//   - if prioritizedBackend is non-empty and its backend's currentWeight >= -5 * totalWeight, pick it
//   - otherwise, pick among all backends with currentWeight >= -5 * totalWeight by max currentWeight
func (l *ShardKeyWeightedRoundRobin) GetNextEngine(prioritizedBackend string) (string, octollm.Engine) {
	l.mu.Lock()
	defer l.mu.Unlock()

	totalWeight := 0
	for _, backend := range l.backends {
		if backend == nil || backend.weight <= 0 {
			continue
		}
		totalWeight += backend.weight
	}
	if totalWeight <= 0 {
		return "", nil
	}
	threshold := -5 * totalWeight

	// First, all backends add their own weight once (matches the example's "随后每个端点增加一次自己对应的权重值").
	for _, backend := range l.backends {
		if backend == nil || backend.weight <= 0 {
			continue
		}
		backend.currentWeight += backend.weight
	}

	// Try shard-hit backend first (if any) and if it hasn't dropped below -5 * totalWeight.
	if prioritizedBackend != "" {
		var hitBackend *wrrBackend
		for _, backend := range l.backends {
			if backend == nil || backend.weight <= 0 {
				continue
			}
			if backend.name == prioritizedBackend {
				hitBackend = backend
				break
			}
		}
		if hitBackend != nil && hitBackend.currentWeight >= threshold {
			hitBackend.currentWeight -= totalWeight
			return hitBackend.name, hitBackend.engine
		}
	}

	// Fallback to normal WRR among backends whose currentWeight >= threshold.
	var maxWeightBackend *wrrBackend
	for _, backend := range l.backends {
		if backend == nil || backend.weight <= 0 {
			continue
		}
		if backend.currentWeight < threshold {
			// below -5 * totalWeight, temporarily not selectable
			continue
		}
		if maxWeightBackend == nil || backend.currentWeight > maxWeightBackend.currentWeight {
			maxWeightBackend = backend
		}
	}
	if maxWeightBackend == nil {
		return "", nil
	}
	maxWeightBackend.currentWeight -= totalWeight
	return maxWeightBackend.name, maxWeightBackend.engine
}
