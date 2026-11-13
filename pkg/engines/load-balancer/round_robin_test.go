package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/engines/limiter"
	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
)

func TestIsBackendFailureError(t *testing.T) {
	assert.True(t, isBackendFailureError(nil))
	assert.True(t, isBackendFailureError(io.EOF))
	assert.False(t, isBackendFailureError(context.Canceled))
	assert.False(t, isBackendFailureError(fmt.Errorf("outer: %w", context.Canceled)))
	assert.False(t, isBackendFailureError(&errutils.UpstreamHTTPError{Err: context.Canceled}))
	assert.True(t, isBackendFailureError(&errutils.UpstreamRespError{StatusCode: 502}))
	assert.True(t, isBackendFailureError(&errutils.UpstreamRespError{StatusCode: 429}))
	assert.False(t, isBackendFailureError(&errutils.UpstreamRespError{StatusCode: 413}))
	assert.False(t, isBackendFailureError(fmt.Errorf("outer: %w", &errutils.UpstreamRespError{StatusCode: 413})))
	assert.False(t, isBackendFailureError(errutils.NewHandlerError(&errutils.UpstreamRespError{StatusCode: 413}, 429, "mapped", "", "")))
	assert.True(t, isBackendFailureError(errutils.NewHandlerError(nil, 429, "rate", "rate_limit_error", "rate_limit_exceeded")))
	assert.False(t, isBackendFailureError(errutils.NewHandlerError(limiter.ErrRateLimitReached, 429, "rate", "rate_limit_error", "rate_limit_exceeded")))
	assert.True(t, isBackendFailureError(fmt.Errorf("outer: %w", errutils.NewHandlerError(nil, 429, "rate", "rate_limit_error", "rate_limit_exceeded"))))
	assert.True(t, isBackendFailureError(errutils.NewHandlerError(nil, 502, "bad gateway", "server_error", "gateway_internal_error")))
	assert.True(t, isBackendFailureError(&errutils.UpstreamHTTPError{StatusCode: 504, Err: io.EOF}))
	assert.True(t, isBackendFailureError(&net.OpError{Op: "dial", Err: errors.New("connection refused")}))
	assert.False(t, isBackendFailureError(limiter.ErrRateLimitReached))
	assert.False(t, isBackendFailureError(fmt.Errorf("outer: %w", limiter.ErrRateLimitReached)))
	assert.False(t, isBackendFailureError(limiter.ErrStrongCacheRateLimitReached))
	assert.False(t, isBackendFailureError(fmt.Errorf("outer: %w", limiter.ErrStrongCacheRateLimitReached)))
	assert.False(t, isBackendFailureError(limiter.ErrPLoadRateLimitReached))
	assert.False(t, isBackendFailureError(fmt.Errorf("outer: %w", limiter.ErrPLoadRateLimitReached)))
	assert.False(t, isBackendFailureError(limiter.ErrPLoadStrongCacheRateLimitReached))
	assert.False(t, isBackendFailureError(fmt.Errorf("outer: %w", limiter.ErrPLoadStrongCacheRateLimitReached)))
	assert.False(t, isBackendFailureError(limiter.ErrTPMRateLimitReached))
	assert.False(t, isBackendFailureError(fmt.Errorf("outer: %w", limiter.ErrTPMRateLimitReached)))
	assert.False(t, isBackendFailureError(limiter.ErrTPMStrongCacheRateLimitReached))
	assert.False(t, isBackendFailureError(fmt.Errorf("outer: %w", limiter.ErrTPMStrongCacheRateLimitReached)))
}

func TestIsLimiterRejectError(t *testing.T) {
	assert.False(t, isLimiterRejectError(nil))
	assert.False(t, isLimiterRejectError(io.EOF))
	assert.False(t, isLimiterRejectError(context.Canceled))
	assert.False(t, isLimiterRejectError(&errutils.UpstreamRespError{StatusCode: 429}))
	assert.False(t, isLimiterRejectError(fmt.Errorf("outer: %w", &errutils.UpstreamRespError{StatusCode: 429})))
	assert.False(t, isLimiterRejectError(errutils.NewHandlerError(nil, 429, "rate", "rate_limit_error", "rate_limit_exceeded")))
	assert.False(t, isLimiterRejectError(errutils.NewHandlerError(&errutils.UpstreamRespError{StatusCode: 429}, 429, "rate", "rate_limit_error", "rate_limit_exceeded")))
	assert.True(t, isLimiterRejectError(errutils.NewHandlerError(limiter.ErrRateLimitReached, 429, "rate", "rate_limit_error", "rate_limit_exceeded")))
	for _, err := range []error{
		limiter.ErrRateLimitReached,
		limiter.ErrStrongCacheRateLimitReached,
		limiter.ErrPLoadRateLimitReached,
		limiter.ErrPLoadStrongCacheRateLimitReached,
		limiter.ErrTPMRateLimitReached,
		limiter.ErrTPMStrongCacheRateLimitReached,
	} {
		assert.True(t, isLimiterRejectError(err), err.Error())
		assert.True(t, isLimiterRejectError(fmt.Errorf("outer: %w", err)), err.Error())
	}
}

func TestWeightedRoundRobin_FailoverCounterSkipsLimiterReject(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want float64
	}{
		{name: "upstream 429 counted", err: &errutils.UpstreamRespError{StatusCode: 429}, want: 1},
		{name: "upstream 502 counted", err: &errutils.UpstreamRespError{StatusCode: 502}, want: 1},
		{name: "limiter reject not counted", err: limiter.ErrRateLimitReached, want: 0},
		{name: "tpm limiter reject not counted", err: limiter.ErrTPMRateLimitReached, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modelName := "failover-counter-" + t.Name()
			failing := octollm.EngineFunc(func(*octollm.Request) (*octollm.Response, error) {
				return nil, tt.err
			})
			// One backend and retryMaxCount 2: the first failure fails over once, the second returns.
			lb, err := NewWeightedRoundRobin([]BackendItem{{Name: "a", Weight: 1, Engine: failing}}, time.Minute, 2)
			require.NoError(t, err)

			ctx := context.WithValue(context.Background(), octollm.ContextKeyModelName, modelName)
			_, err = lb.Process(testhelper.CreateTestRequest(testhelper.WithContext(ctx)))
			require.ErrorIs(t, err, tt.err)

			var m dto.Metric
			require.NoError(t, totalFailoverRequestsCounter.WithLabelValues(modelName, "a").Write(&m))
			assert.Equal(t, tt.want, m.GetCounter().GetValue())
		})
	}
}
