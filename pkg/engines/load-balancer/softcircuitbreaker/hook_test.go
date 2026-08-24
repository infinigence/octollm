package softcircuitbreaker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/engines/client"
	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
)

func TestHook_NilRegistryAllows(t *testing.T) {
	hook := NewHook(nil, "m", nil)
	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	assert.True(t, allowed)
	assert.Nil(t, done)
}

func TestHook_NilReceiverAllows(t *testing.T) {
	var hook *Hook
	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	assert.True(t, allowed)
	assert.Nil(t, done)
}

func TestHook_DenyWhenDegradedQuotaExceeded(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	policy.Recovery.MinRequests = 100
	policy.DegradedTraffic.MaxRequests = 1
	reg := mustRegistry(t, policy)
	hook := NewHook(reg, "m", nil)
	req := testhelper.CreateTestRequest()

	done, allowed := hook.BeforeAttempt(req, "a")
	require.True(t, allowed)
	done(nil, io.EOF)

	done, allowed = hook.BeforeAttempt(req, "a")
	require.True(t, allowed, "first degraded admit consumes the only quota slot")
	done(&octollm.Response{StatusCode: 200, Body: req.Body}, nil)
	require.NoError(t, req.Body.Close())

	_, allowed = hook.BeforeAttempt(req, "a")
	assert.False(t, allowed, "quota is consumed on admit and is not returned on success")
}

func TestHook_SyncErrorClassifiesImmediately(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	reg := mustRegistry(t, policy)
	hook := NewHook(reg, "m", nil)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(nil, &errutils.UpstreamRespError{StatusCode: 502})

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, ModeDegraded, entry.Mode())
}

func TestHook_StreamCloseUsesClientProcessStreamError(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	reg := mustRegistry(t, policy)
	hook := NewHook(reg, "m", nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{}`))
	}))
	t.Cleanup(srv.Close)

	endpoint := client.NewHTTPEndpoint().
		WithURLGetter(func(*octollm.Request) (string, error) { return srv.URL, nil }).
		WithParser(
			func(*octollm.Request) octollm.Parser { return &octollm.JSONParser[json.RawMessage]{} },
			func(*octollm.Request) (octollm.Parser, client.StreamingType) {
				return &octollm.JSONParser[json.RawMessage]{}, client.StreamingTypeJSON
			},
		)

	ctx := context.WithValue(context.Background(), octollm.ContextKeyAction, "streamGenerateContent")
	req := testhelper.CreateTestRequest(testhelper.WithContext(ctx))

	done, allowed := hook.BeforeAttempt(req, "a")
	require.True(t, allowed)

	resp, err := endpoint.Process(req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.Stream)
	done(resp, err)

	for range resp.Stream.Chan() {
	}
	resp.Stream.Close()

	streamErr, ok := client.GetClientProcessStreamError(req)
	require.True(t, ok)
	require.Error(t, streamErr)

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, ModeDegraded, entry.Mode())
}

func TestHook_CleanStreamCloseIsSuccess(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	reg := mustRegistry(t, policy)
	hook := NewHook(reg, "m", nil)

	ch := make(chan *octollm.StreamChunk)
	close(ch)
	stream := octollm.NewStreamChan(ch, nil)
	resp := &octollm.Response{StatusCode: 200, Stream: stream}

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(resp, nil)
	stream.Close()

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, ModeNormal, entry.Mode())
	assert.Len(t, entry.normalSamples, 1)
	assert.True(t, entry.normalSamples[0].success)
}

func TestHook_CanceledStreamIsNeutral(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	reg := mustRegistry(t, policy)
	hook := NewHook(reg, "m", nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := testhelper.CreateTestRequest(testhelper.WithContext(ctx))

	ch := make(chan *octollm.StreamChunk)
	close(ch)
	stream := octollm.NewStreamChan(ch, nil)
	resp := &octollm.Response{StatusCode: 200, Stream: stream}

	done, allowed := hook.BeforeAttempt(req, "a")
	require.True(t, allowed)
	done(resp, nil)
	stream.Close()

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, ModeNormal, entry.Mode())
	assert.Empty(t, entry.normalSamples)
}

func TestHook_NilRespIsTargetFailure(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	reg := mustRegistry(t, policy)
	hook := NewHook(reg, "m", nil)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(nil, nil)

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, ModeDegraded, entry.Mode())
}

func TestHook_NilRespLogsInvalidResponseAndTargetFailure(t *testing.T) {
	logs := installLogCapture(t)
	policy := testPolicy()
	policy.Failure.MinRequests = 20
	reg := mustRegistry(t, policy)
	hook := NewHook(reg, "m", nil)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(nil, nil)

	assert.Equal(t, 1, logs.count("soft_circuit_breaker_invalid_response"))
	assert.Equal(t, 1, logs.count("soft_circuit_breaker_target_failure"))
}

func TestHook_SyncErrorLogsTargetFailure(t *testing.T) {
	logs := installLogCapture(t)
	policy := testPolicy()
	policy.Failure.MinRequests = 20
	reg := mustRegistry(t, policy)
	hook := NewHook(reg, "m", nil)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(nil, &errutils.UpstreamRespError{StatusCode: 502})

	assert.Equal(t, 0, logs.count("soft_circuit_breaker_invalid_response"))
	assert.Equal(t, 1, logs.count("soft_circuit_breaker_target_failure"))
}

func TestHook_HTTPStatusWithoutErrorUsesWhitelist(t *testing.T) {
	policy, err := NewPolicy(
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		RateRule{Window: time.Minute, MinRequests: 100, Rate: 1},
		TrafficRule{Window: time.Minute, MaxRequests: 1},
		[]int{http.StatusTooManyRequests},
	)
	require.NoError(t, err)
	reg := mustRegistry(t, policy)
	hook := NewHook(reg, "m", nil)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(&octollm.Response{StatusCode: http.StatusTooManyRequests}, nil)
	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, ModeNormal, entry.Mode())
	assert.Empty(t, entry.normalSamples)

	done, allowed = hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(&octollm.Response{StatusCode: http.StatusBadGateway}, nil)
	assert.Equal(t, ModeDegraded, entry.Mode())
}

func TestHook_ExcludedUpstreamErrorIsNeutral(t *testing.T) {
	policy, err := NewPolicy(
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		RateRule{Window: time.Minute, MinRequests: 100, Rate: 1},
		TrafficRule{Window: time.Minute, MaxRequests: 1},
		[]int{http.StatusTooManyRequests},
	)
	require.NoError(t, err)
	reg := mustRegistry(t, policy)
	hook := NewHook(reg, "m", nil)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(nil, &errutils.UpstreamRespError{StatusCode: http.StatusTooManyRequests})

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, ModeNormal, entry.Mode())
	assert.Empty(t, entry.normalSamples)
}

func TestHook_Empty200IsNeutral(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	reg := mustRegistry(t, policy)
	hook := NewHook(reg, "m", nil)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(&octollm.Response{StatusCode: 200}, nil)

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, ModeNormal, entry.Mode())
	assert.Empty(t, entry.normalSamples)
}

func TestHook_CanceledBodyIsNeutral(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	reg := mustRegistry(t, policy)
	hook := NewHook(reg, "m", nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := testhelper.CreateTestRequest(testhelper.WithContext(ctx))

	done, allowed := hook.BeforeAttempt(req, "a")
	require.True(t, allowed)
	done(&octollm.Response{StatusCode: 200, Body: req.Body}, nil)
	require.NoError(t, req.Body.Close())

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, ModeNormal, entry.Mode())
	assert.Empty(t, entry.normalSamples)
}

func TestHook_StreamCloseIsIdempotent(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 20
	reg := mustRegistry(t, policy)
	hook := NewHook(reg, "m", nil)

	ch := make(chan *octollm.StreamChunk)
	close(ch)
	stream := octollm.NewStreamChan(ch, nil)
	resp := &octollm.Response{StatusCode: 200, Stream: stream}

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(resp, nil)
	stream.Close()
	stream.Close()

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Len(t, entry.normalSamples, 1)
	assert.True(t, entry.normalSamples[0].success)
}

func TestHook_BodyCloseIsIdempotent(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 20
	reg := mustRegistry(t, policy)
	hook := NewHook(reg, "m", nil)
	req := testhelper.CreateTestRequest()

	done, allowed := hook.BeforeAttempt(req, "a")
	require.True(t, allowed)
	resp := &octollm.Response{StatusCode: 200, Body: req.Body}
	done(resp, nil)
	require.NoError(t, req.Body.Close())
	require.NoError(t, req.Body.Close())

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Len(t, entry.normalSamples, 1)
}

func TestHook_SuccessRecoversFromDegraded(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	policy.Recovery.MinRequests = 1
	policy.Recovery.Rate = 1
	reg := mustRegistry(t, policy)
	hook := NewHook(reg, "m", nil)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(nil, io.EOF)
	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	require.Equal(t, ModeDegraded, entry.Mode())

	req := testhelper.CreateTestRequest()
	done, allowed = hook.BeforeAttempt(req, "a")
	require.True(t, allowed)
	done(&octollm.Response{StatusCode: 200, Body: req.Body}, nil)
	require.NoError(t, req.Body.Close())
	assert.Equal(t, ModeNormal, entry.Mode())
}

func TestHook_BodyCloseSuccess(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 20
	reg := mustRegistry(t, policy)
	hook := NewHook(reg, "m", nil)
	req := testhelper.CreateTestRequest()

	done, allowed := hook.BeforeAttempt(req, "a")
	require.True(t, allowed)
	resp := &octollm.Response{StatusCode: 200, Body: req.Body}
	done(resp, nil)
	require.NoError(t, req.Body.Close())

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Len(t, entry.normalSamples, 1)
	assert.True(t, entry.normalSamples[0].success)
}

func TestHook_InjectedPolicyIsUsedInsteadOfRegistryDefault(t *testing.T) {
	defaultPolicy := testPolicy()
	defaultPolicy.Failure.MinRequests = 100
	defaultPolicy.Failure.Rate = 1
	reg := mustRegistry(t, defaultPolicy)

	strict, err := NewPolicy(
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		RateRule{Window: time.Minute, MinRequests: 100, Rate: 1},
		TrafficRule{Window: time.Minute, MaxRequests: 1},
		nil,
	)
	require.NoError(t, err)
	hook := NewHook(reg, "m", &strict)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(nil, io.EOF)

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, ModeDegraded, entry.Mode())
	assert.Equal(t, 1, entry.Policy().Failure.MinRequests)
}

func TestHook_PolicyChangeRebuildsEntry(t *testing.T) {
	first, err := NewPolicy(
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		RateRule{Window: time.Minute, MinRequests: 100, Rate: 1},
		TrafficRule{Window: time.Minute, MaxRequests: 1},
		nil,
	)
	require.NoError(t, err)
	second, err := NewPolicy(
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		RateRule{Window: time.Minute, MinRequests: 100, Rate: 1},
		TrafficRule{Window: time.Minute, MaxRequests: 100},
		nil,
	)
	require.NoError(t, err)
	reg := mustRegistry(t, first)

	hook := NewHook(reg, "m", &first)
	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(nil, io.EOF)
	done, allowed = hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed, "first degraded admit consumes the only quota slot")
	done(nil, io.EOF)
	_, allowed = hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.False(t, allowed)

	reloaded := NewHook(reg, "m", &second)
	done, allowed = reloaded.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed, "rebuilt entry starts in NORMAL and admits")
	done(nil, io.EOF)
	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, 100, entry.Policy().DegradedTraffic.MaxRequests)
}

func TestHook_InvalidPolicyKeepsOldEntryAndRecords(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	policy.Recovery.MinRequests = 100
	policy.DegradedTraffic.MaxRequests = 1
	reg := mustRegistry(t, policy)
	hook := NewHook(reg, "m", &policy)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(nil, io.EOF)
	done, allowed = hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed, "first degraded admit consumes the only quota slot")
	done(nil, io.EOF)
	_, allowed = hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.False(t, allowed)

	broken := NewHook(reg, "m", &Policy{})
	_, allowed = broken.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	assert.False(t, allowed, "invalid policy must keep the degraded entry and still deny")
	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, ModeDegraded, entry.Mode())
}

func TestHook_InvalidPolicyWithoutEntryAllows(t *testing.T) {
	reg := mustRegistry(t, testPolicy())
	hook := NewHook(reg, "m", &Policy{})
	key := BreakerKey{ModelName: "m", BackendName: "a"}

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	assert.True(t, allowed, "missing key + invalid policy fails open")
	assert.Nil(t, done)
	_, exists := reg.entries[key]
	assert.False(t, exists, "fail-open must not create an entry")
}

func TestHook_CallerPolicyMutationDoesNotAffectHook(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	policy.Recovery.MinRequests = 100
	policy.DegradedTraffic.MaxRequests = 1
	reg := mustRegistry(t, policy)
	hook := NewHook(reg, "m", &policy)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(nil, io.EOF)
	done, allowed = hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(nil, io.EOF)
	_, allowed = hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.False(t, allowed)

	policy.DegradedTraffic.MaxRequests = 100
	policy.ExcludedHTTPStatusCodes[http.StatusBadGateway] = struct{}{}

	_, allowed = hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	assert.False(t, allowed, "cloned hook policy must keep the degraded quota")
	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, 1, entry.Policy().DegradedTraffic.MaxRequests)
	assert.False(t, entry.IsExcludedHTTPStatus(http.StatusBadGateway))
}
