package limiter

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/redis/go-redis/v9"
)

// RequestLimiterEngine is a token bucket-based request rate limiter
type RequestLimiterEngine struct {
	redisClient       *redis.Client
	key               string
	burst             int           // Maximum number of tokens
	rate              float64       // Number of tokens added per second
	window            time.Duration // Time window
	ttlSec            int64         // Redis key TTL in seconds; when expired, bucket is guaranteed full
	tokenBucketScript *redis.Script
	next              octollm.Engine
}

var _ octollm.Engine = (*RequestLimiterEngine)(nil)

var errRequestLimitReached = fmt.Errorf("request limit reached")

// NewRequestLimiterEngine creates a token bucket-based request rate limiter engine
// redisClient: Redis client
// key: Redis key for storing token bucket state
// burst: Maximum number of tokens (bucket capacity), must be greater than 0
// limit: Maximum number of requests allowed within the time window, must be greater than 0
// window: Time window (supports seconds, minutes, etc.), must be greater than 0
// next: Next engine
//
// The rate (tokens per second) is automatically calculated as: rate = limit / window.Seconds()
// For example: limit=100, window=1*time.Minute means 100 requests per minute, rate=100/60≈1.67 tokens/second
func NewRequestLimiterEngine(redisClient *redis.Client, key string, burst int, limit int, window time.Duration, next octollm.Engine) (*RequestLimiterEngine, error) {
	if next == nil {
		return nil, fmt.Errorf("next engine must not be nil")
	}

	// If burst or limit is invalid, disable rate limiting (pass through)
	if burst <= 0 || limit <= 0 {
		return &RequestLimiterEngine{
			redisClient:       redisClient,
			key:               key,
			burst:             0,
			rate:              0,
			window:            0,
			ttlSec:            0,
			tokenBucketScript: nil,
			next:              next,
		}, nil
	}

	if window <= 0 {
		return nil, fmt.Errorf("window must be positive")
	}

	// Calculate rate: tokens per second = limit / window (in seconds)
	// This ensures that within the time window, at most 'limit' requests can pass through
	rate := float64(limit) / window.Seconds()

	// TTL = time to refill from 0 to burst + 1s buffer; when expired, bucket is guaranteed full
	ttlSec := int64(math.Ceil(float64(burst)/rate)) + 1
	if ttlSec < 1 {
		ttlSec = 1
	}

	return &RequestLimiterEngine{
		redisClient:       redisClient,
		key:               key,
		burst:             burst,
		rate:              rate,
		window:            window,
		ttlSec:            ttlSec,
		tokenBucketScript: redis.NewScript(tokenBucketLuaScript),
		next:              next,
	}, nil
}

// allow attempts to allow the request to pass through, performing token bucket rate limiting check
func (e *RequestLimiterEngine) allow(ctx context.Context) (done func(), err error) {
	// If configuration is invalid, directly pass through
	if e.burst <= 0 || e.rate <= 0 || e.redisClient == nil || e.tokenBucketScript == nil {
		return func() {}, nil
	}

	now := time.Now()
	nowUnix := now.Unix()

	// Use Lua script to implement token bucket algorithm
	result, err := e.tokenBucketScript.Run(ctx, e.redisClient, []string{e.key},
		e.burst, e.rate, nowUnix, e.ttlSec).Result()
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("[RequestLimiterEngine] token bucket script error: %v, key: %s", err, e.key))
		return func() {}, fmt.Errorf("token bucket script error: %w", err)
	}

	results, ok := result.([]interface{})
	if !ok || len(results) != 2 {
		slog.ErrorContext(ctx, fmt.Sprintf("[RequestLimiterEngine] unexpected script result format, key: %s", e.key))
		return func() {}, fmt.Errorf("unexpected script result format")
	}

	allowed, _ := results[0].(int64)
	tokens, _ := results[1].(int64)

	if allowed == 0 {
		slog.WarnContext(ctx, fmt.Sprintf("[RequestLimiterEngine] request limit reached, key: %s, tokens: %d, burst: %d", e.key, tokens, e.burst))
		return func() {}, errRequestLimitReached
	}

	slog.DebugContext(ctx, fmt.Sprintf("[RequestLimiterEngine] request allowed, key: %s, tokens: %d/%d", e.key, tokens, e.burst))

	// done function doesn't need to do anything, as tokens are already deducted in Lua script
	done = func() {}

	return done, nil
}

func (e *RequestLimiterEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	ctx := req.Context()

	// Use allow method to perform rate limiting
	done, err := e.allow(ctx)
	if err != nil {
		if err == errRequestLimitReached {
			slog.WarnContext(ctx, fmt.Sprintf("[RequestLimiterEngine] request limit reached, key: %s", e.key))
			return nil, &errutils.UpstreamRespError{
				StatusCode: 429,
				Body:       []byte("request limit reached"),
			}
		}
		slog.ErrorContext(ctx, fmt.Sprintf("[RequestLimiterEngine] request limiter error: %v, key: %s", err, e.key))
		return nil, &errutils.UpstreamRespError{
			StatusCode: 500,
			Body:       []byte("internal server error"),
		}
	}

	// Process request
	resp, err := e.next.Process(req)

	// Call done (although currently an empty function, maintains interface consistency)
	done()

	return resp, err
}

// tokenBucketLuaScript is the Lua script for token bucket algorithm
// KEYS[1]: token bucket key
// ARGV[1]: burst (maximum number of tokens)
// ARGV[2]: rate (number of tokens added per second)
// ARGV[3]: nowUnix (current timestamp)
// ARGV[4]: ttlSec (Redis key TTL in seconds; when expired, bucket is guaranteed full)
const tokenBucketLuaScript = `
local key = KEYS[1]
local burst = tonumber(ARGV[1])
local rate = tonumber(ARGV[2])
local nowUnix = tonumber(ARGV[3])
local ttlSec = tonumber(ARGV[4])

-- Get current token bucket state
local bucket = redis.call('HMGET', key, 'tokens', 'lastRefill')
local tokens = tonumber(bucket[1]) or burst
local lastRefill = tonumber(bucket[2]) or nowUnix

-- Calculate number of tokens to add
local elapsed = nowUnix - lastRefill
local tokensToAdd = math.floor(elapsed * rate)

-- Update token count (not exceeding burst)
tokens = math.min(burst, tokens + tokensToAdd)

-- Try to consume one token
local allowed = 0
if tokens >= 1 then
    tokens = tokens - 1
    allowed = 1
	redis.call('HMSET', key, 'tokens', tokens, 'lastRefill', nowUnix)
	redis.call('EXPIRE', key, ttlSec)
end


return {allowed, tokens}
`
