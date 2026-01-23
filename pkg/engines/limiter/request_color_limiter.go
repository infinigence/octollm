package limiter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

// RequestColorLimiterEngine is a request rate-based priority rate limiter.
// Performs rate limiting checks based on priority and configured rates.
type RequestColorLimiterEngine struct {
	redisClient *redis.Client
	key         string
	rates       []int
	nameSpace   string
	next        octollm.Engine
}

var _ octollm.Engine = (*RequestColorLimiterEngine)(nil)

var errRequestRateLimitReached = fmt.Errorf("request rate limit reached")

// NewRequestColorLimiterEngine creates a request rate color limiter engine
// redisClient: Redis client
// key: Redis key for storing request rate count
// rates: Request rate threshold array, must be strictly decreasing
// nameSpace: Namespace for isolating priority across different namespaces, marker and limiter within the same nameSpace can communicate
// next: Next engine
func NewRequestColorLimiterEngine(redisClient *redis.Client, key string, rates []int, nameSpace string, next octollm.Engine) (*RequestColorLimiterEngine, error) {
	if next == nil {
		return nil, fmt.Errorf("next engine must not be nil")
	}

	if len(rates) == 0 {
		return &RequestColorLimiterEngine{
			redisClient: redisClient,
			key:         key,
			rates:       nil,
			nameSpace:   nameSpace,
			next:        next,
		}, nil
	}

	filteredRates, filtered := filterDecreasingRates(rates)
	if filtered {
		logrus.Warnf("request_color_limiter_rates must be strictly decreasing, filtered from %v to %v (removed %d non-decreasing values)", rates, filteredRates, len(rates)-len(filteredRates))
	}

	return &RequestColorLimiterEngine{
		redisClient: redisClient,
		key:         key,
		rates:       filteredRates,
		nameSpace:   nameSpace,
		next:        next,
	}, nil
}

// allow attempts to allow the request to pass through, performing RPM rate limiting check
func (e *RequestColorLimiterEngine) allow(ctx context.Context) (done func(), err error) {
	// If rates is empty, directly pass through
	if len(e.rates) == 0 || e.redisClient == nil {
		return func() {}, nil
	}

	// Get priority
	priority := 0
	if p, ok := e.getPriorityFromContext(ctx); ok {
		priority = p
	}
	if priority < 0 {
		logrus.WithContext(ctx).Errorf("[RequestColorLimiterEngine] invalid priority: %d, must be in range [0, %d)", priority, len(e.rates))
		return func() {}, fmt.Errorf("invalid priority: %d, must be in range [0, %d)", priority, len(e.rates))
	}

	var priorityLimit int
	if priority >= len(e.rates) {
		priority = len(e.rates) - 1
		priorityLimit = e.rates[0]
	} else {
		priorityLimit = e.rates[len(e.rates)-1-priority]
	}
	maxRate := e.rates[0]
	gap := len(e.rates) - 1 - priority
	if gap < 0 {
		gap = 0
	}
	totalLimit := maxRate - gap

	// Check current request rate usage
	val, err := e.redisClient.Get(ctx, e.key).Int()
	if err != nil && err != redis.Nil {
		logrus.WithContext(ctx).Errorf("[RequestColorLimiterEngine] failed to get request rate value for %s: %v", e.key, err)
		return func() {}, fmt.Errorf("failed to get request rate value: %w", err)
	}

	currentRemaining := priorityLimit
	if err != redis.Nil {
		currentRemaining = val
	}

	if currentRemaining <= 0 {
		logrus.WithContext(ctx).Errorf("[RequestColorLimiterEngine] request rate limit %d reached, current: %d, key: %s, priority: %d", priorityLimit, currentRemaining, e.key, priority)
		return func() {}, errRequestRateLimitReached
	}

	// Check if total limit is exceeded (simple check)
	if totalLimit > 0 && (priorityLimit-currentRemaining+1) > totalLimit {
		logrus.WithContext(ctx).Errorf("[RequestColorLimiterEngine] total request rate limit %d reached, key: %s, priority: %d", totalLimit, e.key, priority)
		return func() {}, errRequestRateLimitReached
	}

	logrus.WithContext(ctx).Debugf("[RequestColorLimiterEngine] request rate allow: priority=%d, remaining=%d/%d, key=%s", priority, currentRemaining, priorityLimit, e.key)

	done = func() {
		// Deduct request rate
		if e.redisClient != nil {
			c1 := context.WithoutCancel(ctx)
			pip := e.redisClient.Pipeline()
			pip.DecrBy(c1, e.key, 1)
			ttl := pip.TTL(c1, e.key)
			_, err := pip.Exec(c1)
			if err != nil && !errors.Is(err, redis.Nil) {
				logrus.WithContext(ctx).Errorf("[RequestColorLimiterEngine] failed to deduct request rate for %s: %v", e.key, err)
				return
			}

			// If TTL doesn't exist, set initial value
			if ttl.Val() <= 0 {
				err := e.redisClient.SetEx(c1, e.key, priorityLimit-1, 120*time.Second).Err()
				if err != nil {
					logrus.WithContext(ctx).Errorf("[RequestColorLimiterEngine] failed to set request rate TTL for %s: %v", e.key, err)
				}
			}
		}
	}

	return done, nil
}

func (e *RequestColorLimiterEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	ctx := req.Context()

	// Use allow method to perform rate limiting
	done, err := e.allow(ctx)
	if err != nil {
		if err == errRequestRateLimitReached {
			logrus.WithContext(ctx).Warnf("[RequestColorLimiterEngine] request rate limit reached, key: %s", e.key)
			return nil, &errutils.UpstreamRespError{
				StatusCode: 429,
				Body:       []byte("request rate limit reached"),
			}
		}
		logrus.WithContext(ctx).Errorf("[RequestColorLimiterEngine] request rate limiter error: %v, key: %s", err, e.key)
		return nil, &errutils.UpstreamRespError{
			StatusCode: 500,
			Body:       []byte("internal server error"),
		}
	}

	// Process request
	resp, err := e.next.Process(req)

	// Call done to cleanup regardless of success or failure (deduct quota)
	done()

	return resp, err
}

func (e *RequestColorLimiterEngine) getPriorityFromContext(ctx context.Context) (int, bool) {
	contextKey := contextKey(fmt.Sprintf("%s:%s", e.nameSpace, ContextKeyPriority))
	priorityStr, ok := ctx.Value(contextKey).(string)
	if !ok {
		return 0, false
	}
	var priority int
	_, err := fmt.Sscanf(priorityStr, ContextValuePrefixPriority+"%d", &priority)
	if err != nil {
		return 0, false
	}

	return priority, true
}
