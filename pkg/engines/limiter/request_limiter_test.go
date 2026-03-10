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

func newRequestLimiterTestRequest(t *testing.T) *octollm.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com", strings.NewReader("body"))
	assert.NoError(t, err)
	return octollm.NewRequest(req, octollm.APIFormatUnknown)
}

// TestRequestLimiter_2PerMinute_1PerSecond_35Seconds tests: 2 requests per minute, 1 request per second, for 35 seconds.
// Expected: first 2 pass, ~28 rejected in between, 3rd passes around second 31 (token refills in ~30s).
func TestRequestLimiter_2PerMinute_1PerSecond_35Seconds(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	next := &nextStubEngine{
		resp: &octollm.Response{StatusCode: 200},
	}

	// 1分钟2个请求: burst=2, limit=2, window=1min, rate=2/60≈0.0333 tokens/sec
	e, err := NewRequestLimiterEngine(client, "req-bucket", 2, 2, time.Minute, next)
	assert.NoError(t, err)

	allowedCount := 0
	deniedCount := 0

	for i := 0; i < 35; i++ {
		req := newRequestLimiterTestRequest(t)
		resp, err := e.Process(req)

		allowed := err == nil && resp != nil
		if allowed {
			allowedCount++
			t.Logf("[sec %2d] request #%2d: allowed (next.callCount=%d)", i, i+1, next.callCount)
		} else {
			deniedCount++
			t.Logf("[sec %2d] request #%2d: rate limited (err=%v)", i, i+1, err)
		}

		// 1 request per second
		if i < 34 {
			time.Sleep(time.Second)
		}
	}

	t.Logf("--- summary: allowed=%d, denied=%d, next.callCount=%d ---", allowedCount, deniedCount, next.callCount)

	assert.Equal(t, 3, allowedCount, "expected 3 requests to pass")
	assert.Equal(t, 3, next.callCount, "next engine should be called 3 times")
}
