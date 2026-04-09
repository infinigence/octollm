package loadbalancer

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/octollm"
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
	shardKeyTTL        time.Duration
	keyPrefix          string

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
//   - shardKeyTTL: expiration time for shard key -> backend mapping in Redis
//   - shardKeyList: shard keys for this request (string array, later elements have higher priority)
//   - redisClient: Redis client, used to resolve shard keys to backend names with pipeline
//   - keyPrefix: prefix prepended to all Redis keys to namespace shard keys per model/instance
func NewShardKeyWeightedRoundRobin(
	backends []BackendItem,
	retryTimeout time.Duration,
	retryMaxCount int,
	shardKeyTTL time.Duration,
	shardKeyListGetter func(req *octollm.Request) []string,
	redisClient *redis.Client,
	keyPrefix string,
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
		shardKeyTTL:        shardKeyTTL,
		keyPrefix:          keyPrefix,
		retryTimeout:       retryTimeout,
		retryMaxCount:      retryMaxCount,
	}

	return lb, nil
}

func (l *ShardKeyWeightedRoundRobin) redisKey(shardKey string) string {
	if l.keyPrefix == "" {
		return shardKey
	}
	return l.keyPrefix + ":" + shardKey
}

// resolvePrioritizedBackends resolves shardKeyList -> backendName via Redis ZSETs and returns
// backend pointers ordered from high to low priority (later keys have higher priority).
// Note: This is computed per-request; backend weight state (currentWeight) stays on l.backends and is reused.
func (l *ShardKeyWeightedRoundRobin) resolvePrioritizedBackends(
	ctx context.Context,
	shardKeyList []string,
) (prioritizedBackends []*wrrBackend) {
	if l.redisClient == nil || len(shardKeyList) == 0 {
		return nil
	}

	// First pipeline: read all members for each shard key once via ZREVRANGE.
	readPipe := l.redisClient.Pipeline()
	cmds := make([]*redis.StringSliceCmd, len(shardKeyList))
	for i, shardKey := range shardKeyList {
		if shardKey == "" {
			continue
		}
		// Get all members ordered from newest to oldest (by score).
		cmds[i] = readPipe.ZRevRange(ctx, l.redisKey(shardKey), 0, -1)
	}

	if _, err := readPipe.Exec(ctx); err != nil && err != redis.Nil {
		slog.WarnContext(ctx, fmt.Sprintf("[ShardKey WRR load balancer] failed to exec Redis pipeline for shard keys: %v", err))
	}

	backendByName := make(map[string]*wrrBackend, len(l.backends))
	for _, b := range l.backends {
		if b.name != "" {
			backendByName[b.name] = b
		}
	}

	seen := make(map[string]bool)
	// Second pipeline: for keys whose ZSET size > 3, trim older entries.
	trimPipe := l.redisClient.Pipeline()

	// Iterate from last to first so later shard keys have higher priority.
	for i := len(shardKeyList) - 1; i >= 0; i-- {
		cmd := cmds[i]
		if cmd == nil {
			continue
		}
		backendNames, err := cmd.Result()
		if err != nil && err != redis.Nil {
			slog.DebugContext(ctx, fmt.Sprintf("[ShardKey WRR load balancer] Redis ZSET error for shard key %s: %v", shardKeyList[i], err))
			continue
		}
		// If more than 3 members exist, trim the oldest ones (keep latest 3).
		if len(backendNames) > 3 {
			// In ascending order, oldest elements have lowest rank.
			// Remove ranks [0, len-4] so that only the latest 3 remain.
			stop := int64(len(backendNames) - 4)
			if stop >= 0 {
				trimPipe.ZRemRangeByRank(ctx, l.redisKey(shardKeyList[i]), 0, stop)
			}
		}
		// backendNames are already ordered from most recent to oldest
		for _, backendName := range backendNames {
			if backendName == "" || seen[backendName] {
				continue
			}
			if backend, ok := backendByName[backendName]; ok {
				prioritizedBackends = append(prioritizedBackends, backend)
				seen[backendName] = true
			}
		}
	}

	if _, err := trimPipe.Exec(ctx); err != nil && err != redis.Nil {
		slog.WarnContext(ctx, fmt.Sprintf("[ShardKey WRR load balancer] failed to trim Redis ZSET for shard keys: %v", err))
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

		n, eng := l.GetNextEngine(req.Context(), prioritizedBackend, excludeNames)
		if eng == nil {
			// should not happen: the loop exits before all backends are excluded (line 264 above); panic guard only
			return nil, fmt.Errorf("no backend engine available")
		}
		if prioritizedBackend != "" && n == prioritizedBackend {
			slog.InfoContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] prioritized backend hit: %s (index %d/%d), shardKeys: %v", n, prioritizedIndex, len(prioritizedBackends), shardKeyList))
		} else {
			slog.InfoContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] no prioritized backend available (exhausted %d), fallback to WRR: %s, shardKeys: %v", len(prioritizedBackends), n, shardKeyList))
		}
		req.SetMetadataValue(backendName, n)
		resp, err := eng.Process(req)
		if err == nil {
			// Update Redis: shardKeyList -> ZSET of backend names (score = current timestamp), with configured TTL.
			if l.redisClient != nil && len(shardKeyList) > 0 && l.shardKeyTTL > 0 {
				pipe := l.redisClient.Pipeline()
				for _, shardKey := range shardKeyList {
					if shardKey == "" {
						continue
					}
					pipe.ZAdd(req.Context(), l.redisKey(shardKey), redis.Z{
						Score:  float64(time.Now().Unix()),
						Member: n,
					})
					pipe.Expire(req.Context(), l.redisKey(shardKey), l.shardKeyTTL)
				}
				if _, err := pipe.Exec(req.Context()); err != nil && err != redis.Nil {
					slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] failed to update shard key mapping in Redis: %v", err))
				}
			}

			return resp, nil
		}
		excludeNames[n] = true
		retryCount++
		if req.Context().Err() != nil {
			// if context is done, return immediately without retrying
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] request context error: %v", req.Context().Err()))
			return resp, err
		}
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
		if len(excludeNames) >= len(l.backends) {
			slog.WarnContext(req.Context(), "[ShardKey WRR load balancer] all backends have been tried, return last resp and err")
			return resp, err
		}
		slog.InfoContext(req.Context(), fmt.Sprintf("[ShardKey WRR load balancer] will retry, count %d, time %v", retryCount, time.Since(start)))
		modelName, _ := octollm.GetCtxValue[string](req, octollm.ContextKeyModelName)
		totalFailoverRequestsCounter.WithLabelValues(modelName, n).Inc()
	}
}

// GetNextEngine applies the shard-key aware WRR selection.
// Step for each pick:
//  1. Compute totalWeight.
//  2. If prioritizedBackend is non-empty, pick it directly;
//     otherwise pick the backend with the largest currentWeight among non-excluded backends.
//  3. For the selected backend, subtract totalWeight from its currentWeight.
//  4. Then, for all backends, add their own weight once (currentWeight += weight).
func (l *ShardKeyWeightedRoundRobin) GetNextEngine(ctx context.Context, prioritizedBackend string, excludeNames map[string]bool) (string, octollm.Engine) {
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

	// Try shard-hit backend first (if any).
	var selected *wrrBackend
	if prioritizedBackend != "" {
		for _, backend := range l.backends {
			if backend == nil || backend.weight <= 0 {
				continue
			}
			if backend.name == prioritizedBackend {
				selected = backend
				break
			}
		}
	}

	// Fallback to normal WRR among non-excluded backends whose currentWeight >= threshold.
	if selected == nil {
		for _, backend := range l.backends {
			if backend == nil || backend.weight <= 0 {
				continue
			}
			if excludeNames[backend.name] {
				continue
			}
			if selected == nil || backend.currentWeight > selected.currentWeight {
				selected = backend
			}
		}
	}

	if selected == nil {
		return "", nil
	}

	// Capture weight snapshot of remaining candidates (inside lock, before weight update).
	type snapshot struct {
		name          string
		currentWeight int
	}
	candidates := make([]snapshot, 0, len(l.backends))
	for _, backend := range l.backends {
		if backend == nil || excludeNames[backend.name] {
			continue
		}
		candidates = append(candidates, snapshot{name: backend.name, currentWeight: backend.currentWeight})
	}
	slog.InfoContext(ctx, fmt.Sprintf("[ShardKey WRR load balancer] selected: %s (currentWeight=%d), candidates: %v", selected.name, selected.currentWeight, candidates))

	// Step 3: subtract totalWeight from selected backend.
	selected.currentWeight -= totalWeight

	// Step 4: then every backend adds its own weight once.
	for _, backend := range l.backends {
		if backend == nil || backend.weight <= 0 {
			continue
		}
		backend.currentWeight += backend.weight
	}

	return selected.name, selected.engine
}
