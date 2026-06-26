package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/octollm"
)

// ErrCacheMissHeadroom is returned when a cache-miss request cannot be served because every
// candidate backend is at or above cacheMissMaxUtilization — only the reserved headroom remains,
// and it is restricted to cache-hit (last-two-shard-key) requests.
var ErrCacheMissHeadroom = errors.New("cache-miss request rejected to preserve cache-hit headroom")

// ShardMaxConcurrencyFn returns the capacity denominator for concurrency ratio (count/denom).
// When non-nil, each value—including 0—is used directly (no fallback to BackendItem.Weight).
// Denominators <= 0 exclude that backend from ratio-based selection. When a BackendItem has no
// function, NewShardKeyConcurrency wraps its Weight as a static denominator.
type ShardMaxConcurrencyFn func(req *octollm.Request) int

type concurrencyBackend struct {
	name   string
	engine octollm.Engine
	// maxConcurrencyFn supplies the ratio denominator per request. It is always set by construction:
	// dynamic backends use BackendItem.MaxConcurrencyFn, static backends use a closure over Weight.
	maxConcurrencyFn ShardMaxConcurrencyFn
	// cacheMissMaxUtilization is the per-backend headroom ceiling; see BackendItem.CacheMissMaxUtilization.
	cacheMissMaxUtilization float64
}

// prioritizedBackend pairs a shard-key-resolved backend with the shard-key list length and the index
// of the shard key it was resolved from. isStrongCacheHit derives whether it is a high-confidence
// cache hit (resolved from a top-priority shard key), which bypasses the headroom ceiling; weaker
// hits are subject to the backend's cacheMissMaxUtilization.
type prioritizedBackend struct {
	backend          *concurrencyBackend
	shardKeyLen      int
	cacheHitKeyIndex int
}

// isStrongCacheHit reports whether this is a strong (high-confidence) cache hit: one resolved from
// either of the top two (highest-priority) shard keys. The "top two" threshold is fixed and not yet
// configurable.
func (pb *prioritizedBackend) isStrongCacheHit() bool {
	return pb.cacheHitKeyIndex >= pb.shardKeyLen-2
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
//   - backends: backend items; items with Weight<=0 are skipped unless MaxConcurrencyFn is non-nil.
//     Static Weight values are converted into fixed MaxConcurrencyFn closures.
//   - retryTimeout: maximum time to retry failed requests
//   - retryMaxCount: maximum number of retries
//   - shardKeyTTL: expiration time for shard key -> backend mapping in Redis
//   - shardKeyListGetter: extracts shard keys from the request; if nil, concurrency-based LB is used
//   - redisClient: Redis client for shard-key ZSETs and concurrency ZCard
//   - keyPrefix: prefix prepended to all Redis keys
//   - concurrencyKeyFn: required; returns the Redis key used to track per-backend concurrency
//
// The cache-miss headroom ceiling is configured per backend via BackendItem.CacheMissMaxUtilization.
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
	if redisClient == nil {
		return nil, fmt.Errorf("redisClient must be provided")
	}
	cb := make([]*concurrencyBackend, 0, len(backends))
	for _, b := range backends {
		if b.Weight <= 0 && b.MaxConcurrencyFn == nil {
			slog.Warn(fmt.Sprintf("[ShardKey Concurrency load balancer] backend %s has weight<=0 and no MaxConcurrencyFn, skipping", b.Name))
			continue
		}
		maxConcurrencyFn := b.MaxConcurrencyFn
		if maxConcurrencyFn == nil {
			static := b.Weight
			maxConcurrencyFn = func(req *octollm.Request) int {
				return static
			}
		}

		cb = append(cb, &concurrencyBackend{
			name:                    b.Name,
			engine:                  b.Engine,
			maxConcurrencyFn:        maxConcurrencyFn,
			cacheMissMaxUtilization: b.CacheMissMaxUtilization,
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
) []prioritizedBackend {
	if len(shardKeyList) == 0 {
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

	var prioritized []prioritizedBackend
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
				prioritized = append(prioritized, prioritizedBackend{
					backend:          b,
					shardKeyLen:      len(shardKeyList),
					cacheHitKeyIndex: i,
				})
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
// using Redis ZCard. Backends in excludeNames are skipped. The returned float is the selected
// backend's utilization ratio (count/maxConcurrency); it is 0 on the random fallback path below.
// Falls back to uniform random selection among backends with positive effective capacity if Redis Exec fails.
func (l *ShardKeyConcurrency) selectByConcurrency(req *octollm.Request, excludeNames map[string]bool) (*concurrencyBackend, float64) {
	ctx := req.Context()
	candidates := make([]*concurrencyBackend, 0, len(l.backends))
	for _, b := range l.backends {
		if b == nil || excludeNames[b.name] {
			continue
		}
		candidates = append(candidates, b)
	}
	if len(candidates) == 0 {
		return nil, 0
	}
	rand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })

	pipe := l.redisClient.Pipeline()
	cmds := make([]*redis.IntCmd, len(candidates))
	for i, b := range candidates {
		cmds[i] = pipe.ZCard(ctx, l.concurrencyKeyFn(req, b.name))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		slog.WarnContext(ctx, fmt.Sprintf("[ShardKey Concurrency load balancer] failed to get concurrency counts from Redis: %v", err))
		usable := candidates[:0]
		for _, b := range candidates {
			if b.maxConcurrencyFn(req) > 0 {
				usable = append(usable, b)
			}
		}
		if len(usable) == 0 {
			return nil, 0
		}
		return usable[0], 0
	}

	minRatio := math.MaxFloat64
	var selected *concurrencyBackend
	for i, b := range candidates {
		count, err := cmds[i].Result()
		if err != nil {
			continue
		}
		denom := b.maxConcurrencyFn(req)
		if denom <= 0 {
			continue
		}
		ratio := float64(count) / float64(denom)
		if ratio < minRatio {
			minRatio = ratio
			selected = b
		}
	}
	if selected == nil {
		return nil, 0
	}
	return selected, minRatio
}

// headroomEnabled reports whether cache-miss headroom reservation is active for this backend. A
// configured utilization of <=0 or >=1.0 disables it (no headroom is reserved).
func (b *concurrencyBackend) headroomEnabled() bool {
	return b.cacheMissMaxUtilization > 0 && b.cacheMissMaxUtilization < 1.0
}

// overHeadroom reports whether the backend's current concurrency utilization (count/maxConcurrency)
// exceeds its cacheMissMaxUtilization. It fails open: a Redis error returns false so requests are not
// blocked by a transient failure. Callers must guard with b.headroomEnabled.
func (l *ShardKeyConcurrency) overHeadroom(req *octollm.Request, b *concurrencyBackend) bool {
	denom := b.maxConcurrencyFn(req)
	if denom <= 0 {
		return true
	}
	count, err := l.redisClient.ZCard(req.Context(), l.concurrencyKeyFn(req, b.name)).Result()
	if err != nil {
		slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] failed to ZCard concurrency for headroom check on %s: %v", b.name, err))
		return false
	}
	return float64(count)/float64(denom) > b.cacheMissMaxUtilization
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
	var lastResp *octollm.Response
	var lastErr error
	for {
		var n string
		var eng octollm.Engine

		if prioritizedIndex < len(prioritized) {
			pb := prioritized[prioritizedIndex]
			b := pb.backend
			prioritizedIndex++
			if b.maxConcurrencyFn(req) <= 0 {
				slog.InfoContext(req.Context(),
					fmt.Sprintf("[ShardKey Concurrency load balancer] skip prioritized backend with no effective capacity: %s (index %d/%d), shardKeys: %v", b.name, prioritizedIndex, len(prioritized), shardKeyList),
					slog.String("backend_name", b.name),
				)
				excludeNames[b.name] = true
				continue
			}
			// Weak cache hits (not top-two-shard-key) prioritized backends only get headroom up to
			// the backend's cacheMissMaxUtilization; skip them once over so the reserved capacity stays
			// for strong cache hits.
			strongCacheHit := pb.isStrongCacheHit()
			if !strongCacheHit && b.headroomEnabled() && l.overHeadroom(req, b) {
				slog.InfoContext(req.Context(),
					fmt.Sprintf("[ShardKey Concurrency load balancer] skip weak-cache-hit prioritized backend over headroom %.2f: %s (index %d/%d), shardKeys: %v", b.cacheMissMaxUtilization, b.name, prioritizedIndex, len(prioritized), shardKeyList),
					slog.String("backend_name", b.name),
				)
				excludeNames[b.name] = true
				// Excluding this backend may have exhausted every backend (all were weak cache hits
				// over the ceiling). That is a headroom rejection: nothing is left for a cache miss, so
				// reject with the headroom sentinel (429) instead of falling through to an empty
				// concurrency selection. On a failover, surface the previous error rather than masking it.
				if len(excludeNames) >= len(l.backends) {
					if retryCount > 0 {
						slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] all backends over headroom on failover, returning previous error: %v", lastErr))
						return lastResp, lastErr
					} else {
						return nil, ErrCacheMissHeadroom
					}
				}
				continue
			}
			n, eng = b.name, b.engine
			slog.InfoContext(req.Context(),
				fmt.Sprintf("[ShardKey Concurrency load balancer] prioritized backend hit (strongCacheHit=%t): %s (index %d/%d), shardKeys: %v", strongCacheHit, n, prioritizedIndex, len(prioritized), shardKeyList),
				slog.String("backend_name", n),
			)
		} else {
			candidates := make([]string, 0, len(l.backends))
			for _, b := range l.backends {
				if b != nil && !excludeNames[b.name] {
					candidates = append(candidates, b.name)
				}
			}
			selected, ratio := l.selectByConcurrency(req, excludeNames)
			if selected != nil {
				n, eng = selected.name, selected.engine
			}
			// Full cache miss: gate the least-utilized backend on its own headroom ceiling. Because
			// ceilings are per-backend, the lowest-ratio backend being over its ceiling does not imply
			// every backend is over its own (another could sit under a more generous ceiling), so exclude
			// it and continue — the next iteration re-selects among the rest. Only once every backend is
			// excluded is it a true headroom rejection (429); on a failover, surface the previous error.
			if selected != nil && selected.headroomEnabled() && ratio > selected.cacheMissMaxUtilization {
				excludeNames[selected.name] = true
				if len(excludeNames) >= len(l.backends) {
					if retryCount > 0 {
						slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] all backends over headroom on failover, returning previous error: %v", lastErr))
						return lastResp, lastErr
					} else {
						return nil, ErrCacheMissHeadroom
					}
				}
				continue
			}
			slog.InfoContext(req.Context(),
				fmt.Sprintf("[ShardKey Concurrency load balancer] no prioritized backend available (exhausted %d), fallback to concurrency-based selection: %s, shardKeys: %v, candidates: %v", len(prioritized), n, shardKeyList, candidates),
				slog.String("backend_name", n),
			)
		}

		if eng == nil {
			// No selectable backend remains (e.g. the only non-excluded backends have a 0 denominator,
			// so selectByConcurrency cannot pick them and the all-excluded guard below never fires).
			// On a failover, surface the previous error rather than masking it with a generic one.
			if retryCount > 0 {
				slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] no backend engine available on failover, returning previous error: %v", lastErr))
				return lastResp, lastErr
			}
			// On the first attempt this is typically because every candidate backend's maxConcurrencyFn
			// returned 0 (no effective capacity), leaving nothing selectable.
			return nil, fmt.Errorf("no backend engine available")
		}
		req.SetMetadataValue(backendName, n)
		resp, err := eng.Process(req)
		if err == nil {
			if len(shardKeyList) > 0 && l.shardKeyTTL > 0 {
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
		if isNotRetriableError(err) {
			slog.WarnContext(req.Context(), fmt.Sprintf("[ShardKey Concurrency load balancer] error is not retriable, return without retry: %v", err))
			return resp, err
		}
		excludeNames[n] = true
		lastResp, lastErr = resp, err
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
		modelName, _ := octollm.GetCtxValue[string](req, octollm.ContextKeyModelName)
		totalFailoverRequestsCounter.WithLabelValues(modelName, n).Inc()
	}
}
