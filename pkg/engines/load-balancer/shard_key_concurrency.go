package loadbalancer

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/octollm"
)

type concurrencyBackend struct {
	name           string
	engine         octollm.Engine
	maxConcurrency int
}

// ShardKeyConcurrency implements a load balancer that:
//   - When shardKeyListGetter is set: prioritizes backends resolved from shard keys (Redis ZSETs),
//     falling back to concurrency-based selection once all prioritized backends are exhausted.
//   - When shardKeyListGetter is nil: selects the backend with the lowest
//     currentConcurrency/maxConcurrency ratio via Redis ZCard.
type ShardKeyConcurrency struct {
	backends []*concurrencyBackend

	// shardKeyListGetter and redisClient are used to resolve prioritized backends in Process.
	shardKeyListGetter func(req *octollm.Request) []string
	redisClient        *redis.Client
	shardKeyTTL        time.Duration
	keyPrefix          string

	// concurrencyKeyFn returns the Redis key used to ZCard current concurrency for a backend.
	concurrencyKeyFn func(req *octollm.Request, backendName string) string

	retryTimeout  time.Duration
	retryMaxCount int
}

var _ octollm.Engine = (*ShardKeyConcurrency)(nil)

// NewShardKeyConcurrency creates a load balancer that selects backends by shard-key affinity
// (when shardKeyListGetter is non-nil) or by lowest concurrency ratio (when nil).
//
// Parameters:
//   - backends: backend items with name/concurrency/engine
//   - retryTimeout: maximum time to retry failed requests
//   - retryMaxCount: maximum number of retries
//   - shardKeyTTL: expiration time for shard key -> backend mapping in Redis
//   - shardKeyListGetter: extracts shard keys from the request; if nil, concurrency-based LB is used
//   - redisClient: Redis client for shard-key ZSETs and concurrency ZCard
//   - keyPrefix: prefix prepended to all Redis keys
//   - concurrencyKeyFn: required; returns the Redis key used to track per-backend concurrency
func NewShardKeyConcurrency(
	backends []BackendItem,
	retryTimeout time.Duration,
	retryMaxCount int,
	shardKeyTTL time.Duration,
	shardKeyListGetter func(req *octollm.Request) []string,
	redisClient *redis.Client,
	keyPrefix string,
	concurrencyKeyFn func(req *octollm.Request, backendName string) string,
) (*ShardKeyConcurrency, error) {
	cb := make([]*concurrencyBackend, 0, len(backends))
	for _, b := range backends {
		if b.Weight <= 0 {
			slog.Warn(fmt.Sprintf("[ShardKey Concurrency load balancer] backend %s has weight 0, it will be ignored in concurrency-based selection", b.Name))
			continue
		}
		cb = append(cb, &concurrencyBackend{
			name:           b.Name,
			engine:         b.Engine,
			maxConcurrency: b.Weight,
		})
	}

	if len(cb) == 0 {
		return nil, fmt.Errorf("backends must have at least one item")
	}
	if concurrencyKeyFn == nil {
		return nil, fmt.Errorf("concurrencyKeyFn must be provided")
	}

	return &ShardKeyConcurrency{
		backends:           cb,
		shardKeyListGetter: shardKeyListGetter,
		redisClient:        redisClient,
		shardKeyTTL:        shardKeyTTL,
		keyPrefix:          keyPrefix,
		concurrencyKeyFn:   concurrencyKeyFn,
		retryTimeout:       retryTimeout,
		retryMaxCount:      retryMaxCount,
	}, nil
}

func (l *ShardKeyConcurrency) redisKey(shardKey string) string {
	if l.keyPrefix == "" {
		return shardKey
	}
	return l.keyPrefix + ":" + shardKey
}

// resolvePrioritizedBackends resolves shardKeyList -> backendName via Redis ZSETs and returns
// backend pointers ordered from high to low priority (later keys have higher priority).
func (l *ShardKeyConcurrency) resolvePrioritizedBackends(
	ctx context.Context,
	shardKeyList []string,
) []*concurrencyBackend {
	if l.redisClient == nil || len(shardKeyList) == 0 {
		return nil
	}

	readPipe := l.redisClient.Pipeline()
	cmds := make([]*redis.StringSliceCmd, len(shardKeyList))
	for i, shardKey := range shardKeyList {
		if shardKey == "" {
			continue
		}
		cmds[i] = readPipe.ZRevRange(ctx, l.redisKey(shardKey), 0, -1)
	}
	if _, err := readPipe.Exec(ctx); err != nil && err != redis.Nil {
		slog.WarnContext(ctx, fmt.Sprintf("[ShardKey Concurrency load balancer] failed to exec Redis pipeline for shard keys: %v", err))
	}

	backendByName := make(map[string]*concurrencyBackend, len(l.backends))
	for _, b := range l.backends {
		if b.name != "" {
			backendByName[b.name] = b
		}
	}

	seen := make(map[string]bool)
	trimPipe := l.redisClient.Pipeline()

	var prioritized []*concurrencyBackend
	for i := len(shardKeyList) - 1; i >= 0; i-- {
		cmd := cmds[i]
		if cmd == nil {
			continue
		}
		backendNames, err := cmd.Result()
		if err != nil && err != redis.Nil {
			slog.DebugContext(ctx, fmt.Sprintf("[ShardKey Concurrency load balancer] Redis ZSET error for shard key %s: %v", shardKeyList[i], err))
			continue
		}
		if len(backendNames) > 3 {
			stop := int64(len(backendNames) - 4)
			if stop >= 0 {
				trimPipe.ZRemRangeByRank(ctx, l.redisKey(shardKeyList[i]), 0, stop)
			}
		}
		for _, name := range backendNames {
			if name == "" || seen[name] {
				continue
			}
			if b, ok := backendByName[name]; ok {
				prioritized = append(prioritized, b)
				seen[name] = true
			}
		}
	}

	if _, err := trimPipe.Exec(ctx); err != nil && err != redis.Nil {
		slog.WarnContext(ctx, fmt.Sprintf("[ShardKey Concurrency load balancer] failed to trim Redis ZSET for shard keys: %v", err))
	}

	return prioritized
}

// selectByConcurrency picks the backend with the lowest currentConcurrency/maxConcurrency ratio
// using Redis ZCard. Backends in excludeNames are skipped.
// Falls back to uniform random selection if Redis is unavailable or no backend has maxConcurrency set.
func (l *ShardKeyConcurrency) selectByConcurrency(req *octollm.Request, excludeNames map[string]bool) (string, octollm.Engine) {
	ctx := req.Context()
	candidates := make([]*concurrencyBackend, 0, len(l.backends))
	for _, b := range l.backends {
		if b == nil || excludeNames[b.name] {
			continue
		}
		candidates = append(candidates, b)
	}
	if len(candidates) == 0 {
		return "", nil
	}
	rand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })

	if l.redisClient == nil {
		b := candidates[rand.Intn(len(candidates))]
		return b.name, b.engine
	}

	pipe := l.redisClient.Pipeline()
	cmds := make([]*redis.IntCmd, len(candidates))
	for i, b := range candidates {
		cmds[i] = pipe.ZCard(ctx, l.concurrencyKeyFn(req, b.name))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		slog.WarnContext(ctx, fmt.Sprintf("[ShardKey Concurrency load balancer] failed to get concurrency counts from Redis: %v", err))
		b := candidates[rand.Intn(len(candidates))]
		return b.name, b.engine
	}

	minRatio := math.MaxFloat64
	var selected *concurrencyBackend
	for i, b := range candidates {
		count, err := cmds[i].Result()
		if err != nil {
			continue
		}
		ratio := float64(count) / float64(b.maxConcurrency)
		if ratio < minRatio {
			minRatio = ratio
			selected = b
		}
	}
	if selected == nil {
		return "", nil
	}
	return selected.name, selected.engine
}

func (l *ShardKeyConcurrency) Process(req *octollm.Request) (*octollm.Response, error) {
	if _, err := req.Body.Bytes(); err != nil {
		return nil, fmt.Errorf("failed to cache request body for retries: %w", err)
	}

	var shardKeyList []string
	if l.shardKeyListGetter != nil {
		shardKeyList = l.shardKeyListGetter(req)
	}
	prioritized := l.resolvePrioritizedBackends(req.Context(), shardKeyList)
	prioritizedIndex := 0
	excludeNames := make(map[string]bool)

	start := time.Now()
	retryCount := 0
	for {
		var n string
		var eng octollm.Engine

		if prioritizedIndex < len(prioritized) {
			b := prioritized[prioritizedIndex]
			prioritizedIndex++
			n, eng = b.name, b.engine
			slog.InfoContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] prioritized backend hit: %s (index %d/%d), shardKeys: %v", n, prioritizedIndex, len(prioritized), shardKeyList))
		} else {
			candidates := make([]string, 0, len(l.backends))
			for _, b := range l.backends {
				if b != nil && !excludeNames[b.name] {
					candidates = append(candidates, b.name)
				}
			}
			n, eng = l.selectByConcurrency(req, excludeNames)
			slog.InfoContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] no prioritized backend available (exhausted %d), fallback to concurrency-based selection: %s, shardKeys: %v, candidates: %v", len(prioritized), n, shardKeyList, candidates))
		}

		if eng == nil {
			return nil, fmt.Errorf("no backend engine available")
		}
		req.SetMetadataValue(backendName, n)
		resp, err := eng.Process(req)
		if err == nil {
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
					slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] failed to update shard key mapping in Redis: %v", err))
				}
			}
			return resp, nil
		}
		excludeNames[n] = true
		retryCount++
		if req.Context().Err() != nil {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] request context error: %v", req.Context().Err()))
			return resp, err
		}
		if time.Since(start) >= l.retryTimeout {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] retry period %v reached, return last resp and err", l.retryTimeout))
			return resp, err
		}
		if retryCount >= l.retryMaxCount {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] retry max count %d reached, return last resp and err", l.retryMaxCount))
			return resp, err
		}
		if len(excludeNames) >= len(l.backends) {
			slog.WarnContext(req.Context(), "[ShardKey Concurrency load balancer] all backends have been tried, return last resp and err")
			return resp, err
		}
		slog.InfoContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] will retry, count %d, time %v", retryCount, time.Since(start)))
	}
}
