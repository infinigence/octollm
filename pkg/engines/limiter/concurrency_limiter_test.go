package limiter

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

func newConcurrencyLimiterTestRequest(t *testing.T) *octollm.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com", strings.NewReader("body"))
	assert.NoError(t, err)
	return octollm.NewRequest(req, octollm.APIFormatUnknown)
}

type nilRespEngine struct{}

func (nilRespEngine) Process(*octollm.Request) (*octollm.Response, error) { return nil, nil }

func TestNewConcurrencyLimiterEngine_Validation(t *testing.T) {
	next := &nextStubEngine{}

	// next must not be nil
	_, err := NewConcurrencyLimiterEngine(nil, "k", 1, time.Second, nil)
	assert.Error(t, err)

	// invalid concurrency -> disabled (pass-through)
	e, err := NewConcurrencyLimiterEngine(nil, "k", 0, time.Second, next)
	assert.NoError(t, err)
	assert.Equal(t, 0, e.concurrency)
	assert.Equal(t, time.Duration(0), e.timeout)
	assert.Nil(t, e.acquireScript)
	assert.Nil(t, e.releaseScript)
	assert.Nil(t, e.renewScript)

	// timeout must be positive when enabled
	_, err = NewConcurrencyLimiterEngine(nil, "k", 1, 0, next)
	assert.Error(t, err)

	// valid config
	e, err = NewConcurrencyLimiterEngine(nil, "k", 1, time.Second, next)
	assert.NoError(t, err)
	assert.NotNil(t, e.acquireScript)
	assert.NotNil(t, e.releaseScript)
	assert.NotNil(t, e.renewScript)
}

func TestConcurrencyLimiter_allow_PassThroughWhenDisabled(t *testing.T) {
	next := &nextStubEngine{}
	e, err := NewConcurrencyLimiterEngine(nil, "k", 0, time.Second, next)
	assert.NoError(t, err)

	done, err := e.allow(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, done)
	done()
}

func TestConcurrencyLimiter_allow_DeniedAndReleasedOnDone(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{resp: &octollm.Response{StatusCode: 200}}

	e, err := NewConcurrencyLimiterEngine(client, "concurrency-key", 1, 10*time.Second, next)
	assert.NoError(t, err)

	// First acquire ok
	done1, err := e.allow(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, done1)

	// Second acquire denied
	done2, err := e.allow(context.Background())
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrConcurrencyLimitReached)
	assert.NotNil(t, done2) // should be a no-op func

	// Release first, then should allow again
	done1()
	done3, err := e.allow(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, done3)
	done3()
}

func TestConcurrencyLimiter_allow_AcquireScriptError(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{}

	e, err := NewConcurrencyLimiterEngine(client, "k", 1, time.Second, next)
	assert.NoError(t, err)

	// Close redis to force network error on script run
	mr.Close()

	_, err = e.allow(context.Background())
	assert.Error(t, err)
}

func TestConcurrencyLimiter_allow_UnexpectedScriptResultFormat(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{}

	e, err := NewConcurrencyLimiterEngine(client, "k", 1, time.Second, next)
	assert.NoError(t, err)

	// Override with a script returning non-table result
	e.acquireScript = redis.NewScript(`return 1`)

	_, err = e.allow(context.Background())
	assert.Error(t, err)
}

func TestConcurrencyLimiter_Process_ReleasesOnBodyClose(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	key := "body-close-key"

	next := &nextStubEngine{
		resp: &octollm.Response{
			StatusCode: 200,
			Body:       octollm.NewBodyFromBytes([]byte("ok"), nil),
		},
	}
	e, err := NewConcurrencyLimiterEngine(client, key, 1, 10*time.Second, next)
	assert.NoError(t, err)

	req := newConcurrencyLimiterTestRequest(t)
	resp, err := e.Process(req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.NotNil(t, resp.Body)

	// Slot should still be held until Close
	n, err := client.ZCard(context.Background(), key).Result()
	assert.NoError(t, err)
	assert.Equal(t, int64(1), n)

	assert.NoError(t, resp.Body.Close())

	// After close, slot should be released
	n, err = client.ZCard(context.Background(), key).Result()
	assert.NoError(t, err)
	assert.Equal(t, int64(0), n)
}

func TestConcurrencyLimiter_Process_ReleasesOnStreamClose(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	key := "stream-close-key"

	ch := make(chan *octollm.StreamChunk)
	stream := octollm.NewStreamChan(ch, func() { close(ch) })

	next := &nextStubEngine{
		resp: &octollm.Response{
			StatusCode: 200,
			Stream:     stream,
		},
	}
	e, err := NewConcurrencyLimiterEngine(client, key, 1, 10*time.Second, next)
	assert.NoError(t, err)

	req := newConcurrencyLimiterTestRequest(t)
	resp, err := e.Process(req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.NotNil(t, resp.Stream)

	// Slot should still be held until Close
	n, err := client.ZCard(context.Background(), key).Result()
	assert.NoError(t, err)
	assert.Equal(t, int64(1), n)

	resp.Stream.Close()

	// After close, slot should be released
	n, err = client.ZCard(context.Background(), key).Result()
	assert.NoError(t, err)
	assert.Equal(t, int64(0), n)
}

func TestConcurrencyLimiter_Process_ReleasesImmediatelyOnNextErrorOrNilResp(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	keyErr := "next-error-key"
	keyNil := "next-nil-key"

	// next returns error (with resp too); limiter should release immediately
	nextErr := &nextStubEngine{
		resp: &octollm.Response{StatusCode: 200, Body: octollm.NewBodyFromBytes([]byte("x"), nil)},
		err:  assert.AnError,
	}
	eErr, err := NewConcurrencyLimiterEngine(client, keyErr, 1, 10*time.Second, nextErr)
	assert.NoError(t, err)
	_, err = eErr.Process(newConcurrencyLimiterTestRequest(t))
	assert.Error(t, err)
	n, err := client.ZCard(context.Background(), keyErr).Result()
	assert.NoError(t, err)
	assert.Equal(t, int64(0), n)

	// next returns nil resp; limiter should release immediately
	eNil, err := NewConcurrencyLimiterEngine(client, keyNil, 1, 10*time.Second, nilRespEngine{})
	assert.NoError(t, err)
	resp, err := eNil.Process(newConcurrencyLimiterTestRequest(t))
	assert.NoError(t, err)
	assert.Nil(t, resp)
	n, err = client.ZCard(context.Background(), keyNil).Result()
	assert.NoError(t, err)
	assert.Equal(t, int64(0), n)
}

