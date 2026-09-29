package limiter

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/engines/usage"
	"github.com/infinigence/octollm/pkg/octollm"
)

// TokenLimiterEngine is a token-bucket-based limiter that supports negative balances
// and post-response token deduction.
//
// It is designed for token-style limits (e.g., TPM) where the exact deduction amount
// is only known after processing the request. When the response is closed, it
// deducts the total tokens recorded by a usage.CollectorEngine, which must sit
// below it in the chain; without one nothing is deducted.
type TokenLimiterEngine struct {
	redisClient *redis.Client
	key         string

	burst  int           // Maximum number of tokens (bucket capacity)
	rate   float64       // Number of tokens added per second
	window time.Duration // Logical time window for refilling
	ttl    time.Duration // Redis key TTL; chosen so that when expired, bucket is guaranteed full

	next octollm.Engine
}

var _ octollm.Engine = (*TokenLimiterEngine)(nil)

// NewTokenLimiterEngine creates a token limiter engine with support for negative
// balances and post-response deduction.
//
// redisClient: Redis client
// key: Redis key for storing token bucket state
// burst: Maximum number of tokens (bucket capacity), must be greater than 0
// limit: Maximum number of "nominal" tokens refilled within the time window, must be greater than 0
// window: Time window, must be greater than 0
// next: Next engine
func NewTokenLimiterEngine(
	redisClient *redis.Client,
	key string,
	burst int,
	limit int,
	window time.Duration,
	next octollm.Engine,
) (*TokenLimiterEngine, error) {
	if next == nil {
		return nil, fmt.Errorf("next engine must not be nil")
	}

	// If burst or limit is invalid, disable rate limiting (pass through)
	if burst <= 0 || limit <= 0 {
		return &TokenLimiterEngine{
			redisClient: redisClient,
			key:         key,
			burst:       0,
			rate:        0,
			window:      0,
			ttl:         0,
			next:        next,
		}, nil
	}

	if window <= 0 {
		return nil, fmt.Errorf("window must be positive")
	}

	rate := float64(limit) / window.Seconds()

	var ttl time.Duration
	if limit > 0 && burst > 0 {
		fullSeconds := (float64(burst) / float64(limit)) * window.Seconds()
		if fullSeconds <= 0 {
			ttl = window * 2
		} else {
			ttl = time.Duration(fullSeconds * 2 * float64(time.Second))
		}
	} else {
		ttl = window * 2
	}

	return &TokenLimiterEngine{
		redisClient: redisClient,
		key:         key,
		burst:       burst,
		rate:        rate,
		window:      window,
		ttl:         ttl,
		next:        next,
	}, nil
}

// allow attempts to allow the request to pass through by performing a token bucket check.
// It uses a Redis hash with fields:
//   - "tokens": current token balance (can be negative)
//   - "lastRefill": last refill timestamp (Unix seconds)
func (e *TokenLimiterEngine) allow(ctx context.Context) error {
	// If configuration is invalid or Redis is not set, directly pass through.
	if !e.enabled() {
		return nil
	}

	now := time.Now()
	nowUnix := now.Unix()

	// First, check TTL. If expired or key not exist, treat as full bucket and allow directly.
	ttl, err := e.redisClient.TTL(ctx, e.key).Result()
	if err != nil && err != redis.Nil {
		slog.ErrorContext(ctx, fmt.Sprintf("[TokenLimiterEngine] TTL error: %v, key: %s", err, e.key))
		return fmt.Errorf("failed to read token bucket ttl: %w", err)
	}
	if err == redis.Nil || ttl <= 0 {
		// Key expired or not exist, treat as not limited (bucket will be recreated on deduction).
		slog.DebugContext(ctx, fmt.Sprintf("[TokenLimiterEngine] key expired or not exist, allow request, key: %s", e.key))
		return nil
	}

	// Read current bucket state
	result, err := e.redisClient.HMGet(ctx, e.key, "tokens", "lastRefill").Result()
	if err != nil && err != redis.Nil {
		slog.ErrorContext(ctx, fmt.Sprintf("[TokenLimiterEngine] HMGet error: %v, key: %s", err, e.key))
		return fmt.Errorf("failed to read token bucket state: %w", err)
	}

	// Default values if not set
	tokens := int64(e.burst)
	lastRefill := nowUnix

	if len(result) == 2 {
		if v, ok := result[0].(string); ok && v != "" {
			if parsed, parseErr := parseInt64(v); parseErr == nil {
				tokens = parsed
			}
		}
		if v, ok := result[1].(string); ok && v != "" {
			if parsed, parseErr := parseInt64(v); parseErr == nil {
				lastRefill = parsed
			}
		}
	}

	if lastRefill > nowUnix {
		lastRefill = nowUnix
	}

	elapsed := nowUnix - lastRefill
	if elapsed < 0 {
		elapsed = 0
	}

	// Refill tokens based on elapsed time
	tokensToAdd := int64(float64(elapsed) * e.rate)
	if tokensToAdd > 0 {
		tokens += tokensToAdd
		if tokens > int64(e.burst) {
			tokens = int64(e.burst)
		}
	}

	// Check if we have at least 1 nominal token left to allow this request.
	if tokens <= 0 {
		slog.WarnContext(ctx, fmt.Sprintf("[TokenLimiterEngine] request limit reached, key: %s, tokens: %d, burst: %d", e.key, tokens, e.burst))
		return ErrRequestLimitReached
	}

	return nil
}

// deduction subtracts used tokens from the bucket, refilling it first.
func (e *TokenLimiterEngine) deduction(ctx context.Context, used int64) error {
	if used <= 0 {
		slog.WarnContext(ctx, fmt.Sprintf("[TokenLimiterEngine] token deduction value is less than or equal to 0, key: %s, used: %d", e.key, used))
		return nil
	}

	now := time.Now()
	nowUnix := now.Unix()

	ttl, err := e.redisClient.TTL(ctx, e.key).Result()
	if err != nil && err != redis.Nil {
		slog.ErrorContext(ctx, fmt.Sprintf("[TokenLimiterEngine] TTL error in deduction: %v, key: %s", err, e.key))
		return fmt.Errorf("failed to read token bucket ttl in deduction: %w", err)
	}

	var tokens int64
	var lastRefill int64

	if err == redis.Nil || ttl <= 0 {
		tokens = int64(e.burst) - used
		lastRefill = nowUnix
	} else {
		result, err := e.redisClient.HMGet(ctx, e.key, "tokens", "lastRefill").Result()
		if err != nil && err != redis.Nil {
			slog.ErrorContext(ctx, fmt.Sprintf("[TokenLimiterEngine] HMGet error in deduction: %v, key: %s", err, e.key))
			return fmt.Errorf("failed to read token bucket state in deduction: %w", err)
		}

		tokens = int64(e.burst)
		lastRefill = nowUnix

		if len(result) == 2 {
			if v, ok := result[0].(string); ok && v != "" {
				if parsed, parseErr := parseInt64(v); parseErr == nil {
					tokens = parsed
				}
			}
			if v, ok := result[1].(string); ok && v != "" {
				if parsed, parseErr := parseInt64(v); parseErr == nil {
					lastRefill = parsed
				}
			}
		}

		if lastRefill > nowUnix {
			lastRefill = nowUnix
		}

		elapsed := nowUnix - lastRefill
		if elapsed < 0 {
			elapsed = 0
		}

		tokensToAdd := int64(float64(elapsed) * e.rate)
		if tokensToAdd > 0 {
			tokens += tokensToAdd
			if tokens > int64(e.burst) {
				tokens = int64(e.burst)
			}
			lastRefill = nowUnix
		}

		tokens -= used
	}

	pipe := e.redisClient.Pipeline()
	pipe.HSet(ctx, e.key, "tokens", tokens, "lastRefill", lastRefill)
	if e.ttl > 0 {
		pipe.Expire(ctx, e.key, e.ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		slog.ErrorContext(ctx, fmt.Sprintf("[TokenLimiterEngine] persist state error in deduction: %v, key: %s", err, e.key))
		return fmt.Errorf("failed to persist token bucket state in deduction: %w", err)
	}

	return nil
}

func (e *TokenLimiterEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	ctx := req.Context()

	// Pre-check using token bucket
	if err := e.allow(ctx); err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("[TokenLimiterEngine] token limiter error: %v, key: %s", err, e.key))
		return nil, err
	}

	resp, err := e.next.Process(req)
	if err != nil || resp == nil || !e.enabled() {
		return resp, err
	}

	// Deduct once the response is fully consumed, when the usage is final.
	deduct := func() { e.deductUsage(req) }
	if resp.Stream != nil {
		resp.Stream.OnClose(deduct)
	} else if resp.Body != nil {
		resp.Body.OnClose(deduct)
	} else {
		deduct()
	}
	return resp, nil
}

func (e *TokenLimiterEngine) enabled() bool {
	return e.burst > 0 && e.rate > 0 && e.redisClient != nil
}

// deductUsage deducts the usage recorded on req. It runs on a context detached
// from req's, so the usage is still persisted when the client has disconnected.
func (e *TokenLimiterEngine) deductUsage(req *octollm.Request) {
	ctx := context.WithoutCancel(req.Context())
	u := usage.Get(req)
	if u == nil {
		slog.WarnContext(ctx, fmt.Sprintf("[TokenLimiterEngine] no usage recorded, skipping deduction, key: %s", e.key))
		return
	}
	if err := e.deduction(ctx, u.GetTotalTokens()); err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("[TokenLimiterEngine] deduction error: %v, key: %s", err, e.key))
	}
}

// parseInt64 is a small helper to parse string to int64.
func parseInt64(s string) (int64, error) {
	var v int64
	_, err := fmt.Sscan(s, &v)
	return v, err
}
