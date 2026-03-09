package limiter

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"

	"github.com/infinigence/octollm/pkg/octollm"
)

type nextStubEngine struct {
	resp      *octollm.Response
	err       error
	callCount int
}

func (n *nextStubEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	n.callCount++
	if n.resp == nil {
		return &octollm.Response{StatusCode: 200}, n.err
	}
	return n.resp, n.err
}

func newLimiterTestRequest(t *testing.T) *octollm.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com", strings.NewReader("body"))
	assert.NoError(t, err)
	return octollm.NewRequest(req, octollm.APIFormatUnknown)
}

func TestNewTokenLimiterEngine_Validation(t *testing.T) {
	// next must not be nil
	_, err := NewTokenLimiterEngine(nil, "k", 10, 10, time.Second, nil)
	assert.Error(t, err)

	next := &nextStubEngine{}

	// invalid burst or limit -> disabled limiter (pass-through)
	e, err := NewTokenLimiterEngine(nil, "k", 0, 10, time.Second, next)
	assert.NoError(t, err)
	assert.Equal(t, 0, e.burst)
	assert.Equal(t, 0.0, e.rate)

	e, err = NewTokenLimiterEngine(nil, "k", 10, 0, time.Second, next)
	assert.NoError(t, err)
	assert.Equal(t, 0, e.burst)
	assert.Equal(t, 0.0, e.rate)

	// window must be positive
	_, err = NewTokenLimiterEngine(nil, "k", 10, 10, 0, next)
	assert.Error(t, err)

	// ttl calculation
	burst := 100
	limit := 50
	window := 10 * time.Second
	e, err = NewTokenLimiterEngine(nil, "k", burst, limit, window, next)
	assert.NoError(t, err)
	fullSeconds := (float64(burst) / float64(limit)) * window.Seconds()
	wantTTL := time.Duration(fullSeconds * 2 * float64(time.Second))
	assert.Equal(t, wantTTL, e.ttl)
}

func TestTokenLimiter_allow_PassThroughWhenDisabledOrNoRedis(t *testing.T) {
	next := &nextStubEngine{}

	e, err := NewTokenLimiterEngine(nil, "k", 0, 0, time.Second, next)
	assert.NoError(t, err)

	// disabled due to burst/limit; allow should always pass
	assert.NoError(t, e.allow(context.Background()))

	// valid burst/limit but redisClient is nil -> pass through
	e, err = NewTokenLimiterEngine(nil, "k", 10, 10, time.Second, next)
	assert.NoError(t, err)
	assert.NoError(t, e.allow(context.Background()))
}

func TestTokenLimiter_allow_ExpiredOrMissingKey(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{}

	e, err := NewTokenLimiterEngine(client, "bucket", 10, 10, time.Second, next)
	assert.NoError(t, err)

	// No key exists yet; TTL will be -2, so limiter should allow
	assert.NoError(t, e.allow(context.Background()))
}

func TestTokenLimiter_allow_TTLReadError(t *testing.T) {
	mr := miniredis.RunT(t)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{}

	e, err := NewTokenLimiterEngine(client, "bucket", 10, 10, time.Second, next)
	assert.NoError(t, err)

	// Close miniredis to force a network error on TTL
	mr.Close()

	assert.Error(t, e.allow(context.Background()))
}

func TestTokenLimiter_allow_DeniedWhenTokensExhausted(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{}

	e, err := NewTokenLimiterEngine(client, "bucket", 10, 10, time.Second, next)
	assert.NoError(t, err)

	ctx := context.Background()
	now := time.Now().Unix()

	// Create the bucket first so that TTL is effective.
	err = client.HSet(ctx, "bucket",
		"tokens", fmt.Sprintf("%d", 0),
		"lastRefill", fmt.Sprintf("%d", now),
	).Err()
	assert.NoError(t, err)

	// Ensure TTL > 0 to take the normal path
	err = client.Expire(ctx, "bucket", time.Hour).Err()
	assert.NoError(t, err)

	err = e.allow(ctx)
	assert.ErrorIs(t, err, ErrRequestLimitReached)
}

func TestTokenLimiter_allow_AllowedWhenTokensAvailable(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{}

	e, err := NewTokenLimiterEngine(client, "bucket", 10, 10, time.Second, next)
	assert.NoError(t, err)

	ctx := context.Background()
	now := time.Now().Unix()

	err = client.HSet(ctx, "bucket",
		"tokens", fmt.Sprintf("%d", 5),
		"lastRefill", fmt.Sprintf("%d", now),
	).Err()
	assert.NoError(t, err)

	err = client.Expire(ctx, "bucket", time.Hour).Err()
	assert.NoError(t, err)

	assert.NoError(t, e.allow(ctx))
}

func TestTokenLimiter_deduction_NoOpCases(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{}

	e, err := NewTokenLimiterEngine(client, "bucket", 10, 10, time.Second, next)
	assert.NoError(t, err)

	ctx := context.Background()

	// used <= 0: deduction is no-op, returns nil without persisting
	assert.NoError(t, e.deduction(ctx, 0))
	assert.NoError(t, e.deduction(ctx, -1))
}

func TestTokenLimiter_deduction_TTLReadError(t *testing.T) {
	mr := miniredis.RunT(t)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{}

	e, err := NewTokenLimiterEngine(client, "bucket", 10, 10, time.Second, next)
	assert.NoError(t, err)

	// Close miniredis to force a network error on TTL
	mr.Close()

	assert.Error(t, e.deduction(context.Background(), 5))
}

func TestTokenLimiter_deduction_CreateNewBucketOnMissingKey(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{}

	e, err := NewTokenLimiterEngine(client, "bucket", 10, 10, time.Second, next)
	assert.NoError(t, err)

	ctx := context.Background()
	assert.NoError(t, e.deduction(ctx, 3))

	tokensStr, err := client.HGet(ctx, "bucket", "tokens").Result()
	assert.NoError(t, err)

	var tokens int64
	_, err = fmt.Sscan(tokensStr, &tokens)
	assert.NoError(t, err)
	assert.Equal(t, int64(e.burst-3), tokens)

	ttl, err := client.TTL(ctx, "bucket").Result()
	assert.NoError(t, err)
	assert.Greater(t, ttl, time.Duration(0))
}

// Max deduction ratio logic was removed; corresponding tests are no longer needed.

func TestTokenLimiter_Process_RequestLimitReachedAndInternalError(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{}

	e, err := NewTokenLimiterEngine(client, "bucket", 10, 10, time.Second, next)
	assert.NoError(t, err)

	// Case 1: request limit reached
	ctx := context.Background()
	now := time.Now().Unix()
	err = client.HSet(ctx, "bucket",
		"tokens", fmt.Sprintf("%d", 0),
		"lastRefill", fmt.Sprintf("%d", now),
	).Err()
	assert.NoError(t, err)

	err = client.Expire(ctx, "bucket", time.Hour).Err()
	assert.NoError(t, err)

	req := newLimiterTestRequest(t)
	resp, err := e.Process(req)
	assert.Nil(t, resp)

	assert.ErrorIs(t, err, ErrRequestLimitReached)

	// Case 2: internal error due to TTL read failure
	mr2 := miniredis.RunT(t)
	client2 := redis.NewClient(&redis.Options{Addr: mr2.Addr()})
	e2, err := NewTokenLimiterEngine(client2, "bucket2", 10, 10, time.Second, next)
	assert.NoError(t, err)

	mr2.Close()

	req2 := newLimiterTestRequest(t)
	resp2, err := e2.Process(req2)
	assert.Nil(t, resp2)

	assert.Error(t, err)
}

func TestTokenLimiter_Process_SuccessAndDeduction(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{
		resp: &octollm.Response{StatusCode: 201},
	}

	e, err := NewTokenLimiterEngine(client, "bucket", 10, 10, time.Second, next)
	assert.NoError(t, err)

	// Initialize bucket so that allow() passes easily
	ctx := context.Background()
	now := time.Now().Unix()
	err = client.HSet(ctx, "bucket",
		"tokens", fmt.Sprintf("%d", e.burst),
		"lastRefill", fmt.Sprintf("%d", now),
	).Err()
	assert.NoError(t, err)

	err = client.Expire(ctx, "bucket", time.Hour).Err()
	assert.NoError(t, err)

	req := newLimiterTestRequest(t)

	resp, err := e.Process(req)
	assert.NoError(t, err)
	if assert.NotNil(t, resp) {
		assert.Equal(t, 201, resp.StatusCode)
	}
	assert.Equal(t, 1, next.callCount)

	// Process sets DeDuctionCallback in metadata; simulate downstream calling DoDeduction with used=4
	assert.NoError(t, DoDeduction(ctx, req, 4))

	// Deduction callback should have persisted updated tokens (burst - 4)
	tokensStr, err := client.HGet(ctx, "bucket", "tokens").Result()
	assert.NoError(t, err)

	var tokens int64
	_, err = fmt.Sscan(tokensStr, &tokens)
	assert.NoError(t, err)
	assert.Equal(t, int64(e.burst)-4, tokens)
}

func TestDoDeduction_WithoutCallbackReturnsError(t *testing.T) {
	req := newLimiterTestRequest(t)
	err := DoDeduction(context.Background(), req, 10)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "deDuctionCallbacks not found")
}

func TestTokenLimiter_Process_StackedEnginesBothKeysDeducted(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{
		resp: &octollm.Response{StatusCode: 201},
	}

	inner, err := NewTokenLimiterEngine(client, "bucket-inner", 10, 10, time.Second, next)
	assert.NoError(t, err)
	outer, err := NewTokenLimiterEngine(client, "bucket-outer", 10, 10, time.Second, inner)
	assert.NoError(t, err)

	req := newLimiterTestRequest(t)
	resp, err := outer.Process(req)
	assert.NoError(t, err)
	if assert.NotNil(t, resp) {
		assert.Equal(t, 201, resp.StatusCode)
	}
	assert.Equal(t, 1, next.callCount)

	// Ensure both limiters appended their callbacks to the same request metadata.
	raw, ok := req.GetMetadataValue(deDuctionCallbackKey{})
	assert.True(t, ok)
	callbacks, ok := raw.(DeDuctionCallbacks)
	assert.True(t, ok)
	assert.Len(t, callbacks.callbacks, 2)

	// Trigger post-response deduction. Both keys should be created/updated independently.
	const used int64 = 4
	assert.NoError(t, DoDeduction(context.Background(), req, used))

	ctx := context.Background()

	tokensOuterStr, err := client.HGet(ctx, "bucket-outer", "tokens").Result()
	assert.NoError(t, err)
	var tokensOuter int64
	_, err = fmt.Sscan(tokensOuterStr, &tokensOuter)
	assert.NoError(t, err)
	assert.Equal(t, int64(10)-used, tokensOuter)

	tokensInnerStr, err := client.HGet(ctx, "bucket-inner", "tokens").Result()
	assert.NoError(t, err)
	var tokensInner int64
	_, err = fmt.Sscan(tokensInnerStr, &tokensInner)
	assert.NoError(t, err)
	assert.Equal(t, int64(10)-used, tokensInner)

	ttlOuter, err := client.TTL(ctx, "bucket-outer").Result()
	assert.NoError(t, err)
	assert.Greater(t, ttlOuter, time.Duration(0))

	ttlInner, err := client.TTL(ctx, "bucket-inner").Result()
	assert.NoError(t, err)
	assert.Greater(t, ttlInner, time.Duration(0))
}
