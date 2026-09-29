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

	"github.com/infinigence/octollm/pkg/engines/usage"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
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

// usageEngine returns a chat completion reporting the given total tokens, with
// a usage collector in front of it as a real chain would have.
func usageEngine(totalTokens int, stream bool) octollm.Engine {
	return usage.NewCollectorEngine(octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		data := fmt.Sprintf(`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":%d}}`, totalTokens)
		if !stream {
			return octollm.NewNonStreamResponse(http.StatusOK, http.Header{},
				octollm.NewBodyFromBytes([]byte(data), &octollm.JSONParser[openai.ChatCompletionResponse]{})), nil
		}
		ch := make(chan *octollm.StreamChunk, 1)
		ch <- &octollm.StreamChunk{Body: octollm.NewBodyFromBytes([]byte(data), &octollm.JSONParser[openai.ChatCompletionStreamChunk]{})}
		close(ch)
		return octollm.NewStreamResponse(http.StatusOK, http.Header{}, octollm.NewStreamChan(ch, nil)), nil
	}))
}

func bucketTokens(t *testing.T, client *redis.Client, key string) int64 {
	t.Helper()
	tokensStr, err := client.HGet(context.Background(), key, "tokens").Result()
	assert.NoError(t, err)
	var tokens int64
	_, err = fmt.Sscan(tokensStr, &tokens)
	assert.NoError(t, err)
	return tokens
}

func TestTokenLimiter_Process_DeductsUsageOnClose(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			mr := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: mr.Addr()})

			e, err := NewTokenLimiterEngine(client, "bucket", 10, 10, time.Second, usageEngine(4, stream))
			assert.NoError(t, err)

			resp, err := e.Process(newLimiterTestRequest(t))
			assert.NoError(t, err)
			if !assert.NotNil(t, resp) {
				return
			}

			// Nothing is deducted until the response is closed.
			assert.False(t, mr.Exists("bucket"))

			if stream {
				for range resp.Stream.Chan() {
				}
				resp.Stream.Close()
			} else {
				assert.NoError(t, resp.Body.Close())
			}
			assert.Equal(t, int64(10-4), bucketTokens(t, client, "bucket"))
		})
	}
}

func TestTokenLimiter_Process_WithoutCollectorSkipsDeduction(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{resp: &octollm.Response{StatusCode: 201}}

	e, err := NewTokenLimiterEngine(client, "bucket", 10, 10, time.Second, next)
	assert.NoError(t, err)

	resp, err := e.Process(newLimiterTestRequest(t))
	assert.NoError(t, err)
	if assert.NotNil(t, resp) {
		assert.Equal(t, 201, resp.StatusCode)
	}
	assert.False(t, mr.Exists("bucket"))
}

func TestTokenLimiter_Process_DisabledLimiterSkipsDeduction(t *testing.T) {
	// A disabled limiter has no Redis client; closing the response must not touch it.
	e, err := NewTokenLimiterEngine(nil, "bucket", 0, 0, time.Second, usageEngine(4, false))
	assert.NoError(t, err)

	resp, err := e.Process(newLimiterTestRequest(t))
	assert.NoError(t, err)
	assert.NoError(t, resp.Body.Close())
}

func TestTokenLimiter_Process_StackedEnginesBothKeysDeducted(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	const used = 4
	inner, err := NewTokenLimiterEngine(client, "bucket-inner", 10, 10, time.Second, usageEngine(used, false))
	assert.NoError(t, err)
	outer, err := NewTokenLimiterEngine(client, "bucket-outer", 10, 10, time.Second, inner)
	assert.NoError(t, err)

	resp, err := outer.Process(newLimiterTestRequest(t))
	assert.NoError(t, err)
	assert.NoError(t, resp.Body.Close())

	for _, key := range []string{"bucket-outer", "bucket-inner"} {
		assert.Equal(t, int64(10-used), bucketTokens(t, client, key), key)
		ttl, err := client.TTL(context.Background(), key).Result()
		assert.NoError(t, err)
		assert.Greater(t, ttl, time.Duration(0), key)
	}
}
