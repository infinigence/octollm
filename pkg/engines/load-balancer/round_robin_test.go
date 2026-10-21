package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/infinigence/octollm/pkg/engines/limiter"
	"github.com/infinigence/octollm/pkg/errutils"
)

func TestIsBackendFailureError(t *testing.T) {
	assert.True(t, isBackendFailureError(nil))
	assert.True(t, isBackendFailureError(io.EOF))
	assert.False(t, isBackendFailureError(context.Canceled))
	assert.False(t, isBackendFailureError(fmt.Errorf("outer: %w", context.Canceled)))
	assert.False(t, isBackendFailureError(&errutils.UpstreamHTTPError{Err: context.Canceled}))
	assert.True(t, isBackendFailureError(&errutils.UpstreamRespError{StatusCode: 502}))
	assert.False(t, isBackendFailureError(&errutils.UpstreamRespError{StatusCode: 429}))
	assert.False(t, isBackendFailureError(&errutils.UpstreamRespError{StatusCode: 413}))
	assert.False(t, isBackendFailureError(fmt.Errorf("outer: %w", &errutils.UpstreamRespError{StatusCode: 413})))
	assert.False(t, isBackendFailureError(errutils.NewHandlerError(&errutils.UpstreamRespError{StatusCode: 413}, 429, "mapped", "", "")))
	assert.False(t, isBackendFailureError(errutils.NewHandlerError(nil, 429, "rate", "rate_limit_error", "rate_limit_exceeded")))
	assert.False(t, isBackendFailureError(errutils.NewHandlerError(nil, 413, "too large", "invalid_request_error", "request_too_large")))
	assert.False(t, isBackendFailureError(fmt.Errorf("outer: %w", errutils.NewHandlerError(nil, 429, "rate", "rate_limit_error", "rate_limit_exceeded"))))
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
}
