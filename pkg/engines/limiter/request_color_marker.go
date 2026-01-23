package limiter

import (
	"context"
	"fmt"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

// RequestColorMarkerEngine is a request rate-based coloring logic extension.
// Calculates priority based on current request rate usage and sets it in context.
type RequestColorMarkerEngine struct {
	redisClient *redis.Client
	key         string
	rates       []int
	nameSpace   string
	next        octollm.Engine
}

var _ octollm.Engine = (*RequestColorMarkerEngine)(nil)

// NewRequestColorMarkerEngine creates a request rate color marker engine
// redisClient: Redis client
// key: Redis key for storing request rate count
// rates: Request rate threshold array, must be strictly increasing
// nameSpace: Namespace for isolating priority across different namespaces, marker and limiter within the same nameSpace can communicate
// next: Next engine
func NewRequestColorMarkerEngine(redisClient *redis.Client, key string, rates []int, nameSpace string, next octollm.Engine) (*RequestColorMarkerEngine, error) {
	if next == nil {
		return nil, fmt.Errorf("next engine must not be nil")
	}

	if len(rates) == 0 {
		return &RequestColorMarkerEngine{
			redisClient: redisClient,
			key:         key,
			rates:       nil,
			nameSpace:   nameSpace,
			next:        next,
		}, nil
	}

	filteredRates, filtered := filterIncreasingRates(rates)
	if filtered {
		logrus.Warnf("request_color_marker_rates must be strictly increasing, filtered from %v to %v (removed %d non-increasing values)", rates, filteredRates, len(rates)-len(filteredRates))
	}

	return &RequestColorMarkerEngine{
		redisClient: redisClient,
		key:         key,
		rates:       filteredRates,
		nameSpace:   nameSpace,
		next:        next,
	}, nil
}

// allow attempts to calculate RPM priority and set it in context
func (e *RequestColorMarkerEngine) allow(ctx context.Context) (newCtx context.Context, done func(), err error) {
	// If rates is empty, directly pass through
	if len(e.rates) == 0 || e.redisClient == nil {
		return ctx, func() {}, nil
	}

	// Get current RPM usage
	currentRPM, err := e.getCurrentRPM(ctx)
	if err != nil {
		logrus.WithContext(ctx).Errorf("[RequestColorMarkerEngine] failed to get current request rate: %v", err)
		return ctx, func() {}, nil // On error, don't block, continue processing
	}

	// Rate limiting is mandatory: reject directly when reaching the highest tier
	if len(e.rates) > 0 && currentRPM >= e.rates[len(e.rates)-1] {
		logrus.WithContext(ctx).Warnf("[RequestColorMarkerEngine] request rate limit reached at max tier, key=%s, currentRate=%d, maxRate=%d", e.key, currentRPM, e.rates[len(e.rates)-1])
		return ctx, func() {}, errRateLimitReached
	}

	// Calculate priority and set it in context
	priority := e.calculatePriority(currentRPM)
	newCtx = e.setPriorityToContext(ctx, priority)

	logrus.WithContext(ctx).Debugf("[RequestColorMarkerEngine] key=%s, currentRate=%d, priority=%d", e.key, currentRPM, priority)

	return newCtx, func() {}, nil
}

func (e *RequestColorMarkerEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	ctx := req.Context()

	// Use allow method to perform coloring
	newCtx, done, err := e.allow(ctx)
	if err != nil {
		logrus.WithContext(ctx).Errorf("[RequestColorMarkerEngine] marker error: %v", err)
		return nil, err
	}

	// Use SetContext method to set new context
	req.SetContext(newCtx)

	// Process request
	resp, err := e.next.Process(req)

	// Cleanup
	done()

	return resp, err
}

func (e *RequestColorMarkerEngine) getCurrentRPM(ctx context.Context) (int, error) {
	val, err := e.redisClient.Get(ctx, e.key).Int()
	if err != nil && err != redis.Nil {
		return 0, err
	}

	// If key doesn't exist, return 0 (indicating no current usage)
	if err == redis.Nil {
		return 0, nil
	}

	// Redis stores remaining quota, here we need to calculate used amount
	// Assume burst is the maximum value in rates (first value, because rates are decreasing)
	maxRPM := e.rates[0]
	usedRPM := maxRPM - val
	if usedRPM < 0 {
		usedRPM = 0
	}

	return usedRPM, nil
}

func (e *RequestColorMarkerEngine) calculatePriority(currentRPM int) int {
	for i := 0; i < len(e.rates); i++ {
		if currentRPM <= e.rates[i] {
			return len(e.rates) - 1 - i
		}
	}
	return 0
}

func (e *RequestColorMarkerEngine) setPriorityToContext(ctx context.Context, priority int) context.Context {
	priorityStr := fmt.Sprintf("%s%d", ContextValuePrefixPriority, priority)
	contextKey := contextKey(fmt.Sprintf("%s:%s", e.nameSpace, ContextKeyPriority))
	return context.WithValue(ctx, contextKey, priorityStr)
}
