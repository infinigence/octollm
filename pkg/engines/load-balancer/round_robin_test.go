package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/infinigence/octollm/pkg/errutils"
)

func TestIsIgnoredAttemptError(t *testing.T) {
	assert.False(t, isIgnoredAttemptError(nil))
	assert.False(t, isIgnoredAttemptError(io.EOF))
	assert.True(t, isIgnoredAttemptError(context.Canceled))
	assert.True(t, isIgnoredAttemptError(fmt.Errorf("outer: %w", context.Canceled)))
	assert.True(t, isIgnoredAttemptError(&errutils.UpstreamHTTPError{Err: context.Canceled}))
	assert.False(t, isIgnoredAttemptError(&errutils.UpstreamRespError{StatusCode: 502}))
	assert.True(t, isIgnoredAttemptError(&errutils.UpstreamRespError{StatusCode: 429}))
	assert.True(t, isIgnoredAttemptError(&errutils.UpstreamRespError{StatusCode: 413}))
	assert.True(t, isIgnoredAttemptError(fmt.Errorf("outer: %w", &errutils.UpstreamRespError{StatusCode: 413})))
	assert.True(t, isIgnoredAttemptError(errutils.NewHandlerError(&errutils.UpstreamRespError{StatusCode: 413}, 429, "mapped", "", "")))
	assert.True(t, isIgnoredAttemptError(errutils.NewHandlerError(nil, 429, "rate", "rate_limit_error", "rate_limit_exceeded")))
	assert.True(t, isIgnoredAttemptError(errutils.NewHandlerError(nil, 413, "too large", "invalid_request_error", "request_too_large")))
	assert.True(t, isIgnoredAttemptError(fmt.Errorf("outer: %w", errutils.NewHandlerError(nil, 429, "rate", "rate_limit_error", "rate_limit_exceeded"))))
	assert.False(t, isIgnoredAttemptError(errutils.NewHandlerError(nil, 502, "bad gateway", "server_error", "gateway_internal_error")))
	assert.False(t, isIgnoredAttemptError(&errutils.UpstreamHTTPError{StatusCode: 504, Err: io.EOF}))
	assert.False(t, isIgnoredAttemptError(&net.OpError{Op: "dial", Err: errors.New("connection refused")}))
}
