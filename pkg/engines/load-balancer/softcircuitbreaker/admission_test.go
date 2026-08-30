package softcircuitbreaker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/engines/client"
	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
)

func TestAdmission_NilRegistryAllows(t *testing.T) {
	hook := NewAdmission(nil, "m", nil)
	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	assert.True(t, allowed)
	assert.Nil(t, done)
}

func TestAdmission_NilReceiverAllows(t *testing.T) {
	var hook *Admission
	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	assert.True(t, allowed)
	assert.Nil(t, done)
}

func TestAdmission_DenyWhenDegradedQuotaExceeded(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	policy.Recovery.MinRequests = 100
	policy.DegradedTraffic.MaxRequests = 1
	reg := mustRegistry(t, policy)
	hook := NewAdmission(reg, "m", nil)
	req := testhelper.CreateTestRequest()

	done, allowed := hook.BeforeAttempt(req, "a")
	require.True(t, allowed)
	done(false)

	done, allowed = hook.BeforeAttempt(req, "a")
	require.True(t, allowed, "first degraded admit consumes the only quota slot")
	done(true)
	require.NoError(t, req.Body.Close())

	_, allowed = hook.BeforeAttempt(req, "a")
	assert.False(t, allowed, "quota is consumed on admit and is not returned on success")
}

func TestAdmission_SyncErrorClassifiesImmediately(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	reg := mustRegistry(t, policy)
	hook := NewAdmission(reg, "m", nil)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(false)

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, ModeDegraded, entry.Mode())
}

func TestAdmission_StreamProcessSuccessIsNotReclassifiedOnClose(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	reg := mustRegistry(t, policy)
	hook := NewAdmission(reg, "m", nil)

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
	done(err == nil)

	for range resp.Stream.Chan() {
	}
	resp.Stream.Close()

	streamErr, ok := client.GetClientProcessStreamError(req)
	require.True(t, ok)
	require.Error(t, streamErr)

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, ModeNormal, entry.Mode(), "stream close errors are not observed; Process err == nil is success")
	require.Len(t, entry.normalSamples, 1)
	assert.True(t, entry.normalSamples[0].success)
}

func TestAdmission_CleanStreamCloseIsSuccess(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	reg := mustRegistry(t, policy)
	hook := NewAdmission(reg, "m", nil)

	ch := make(chan *octollm.StreamChunk)
	close(ch)
	stream := octollm.NewStreamChan(ch, nil)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(true)
	stream.Close()

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, ModeNormal, entry.Mode())
	assert.Len(t, entry.normalSamples, 1)
	assert.True(t, entry.normalSamples[0].success)
}

func TestAdmission_CanceledStreamIsSuccess(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	reg := mustRegistry(t, policy)
	hook := NewAdmission(reg, "m", nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := testhelper.CreateTestRequest(testhelper.WithContext(ctx))

	ch := make(chan *octollm.StreamChunk)
	close(ch)
	stream := octollm.NewStreamChan(ch, nil)

	done, allowed := hook.BeforeAttempt(req, "a")
	require.True(t, allowed)
	done(true)
	stream.Close()

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, ModeNormal, entry.Mode())
	require.Len(t, entry.normalSamples, 1)
	assert.True(t, entry.normalSamples[0].success)
}

func TestAdmission_NilRespIsSuccess(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	reg := mustRegistry(t, policy)
	hook := NewAdmission(reg, "m", nil)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(true)

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, ModeNormal, entry.Mode())
	require.Len(t, entry.normalSamples, 1)
	assert.True(t, entry.normalSamples[0].success)
}

func TestAdmission_SyncErrorLogsTargetFailure(t *testing.T) {
	logs := installLogCapture(t)
	policy := testPolicy()
	policy.Failure.MinRequests = 20
	reg := mustRegistry(t, policy)
	hook := NewAdmission(reg, "m", nil)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(false)

	assert.Equal(t, 0, logs.count("soft_circuit_breaker_invalid_response"))
	assert.Equal(t, 1, logs.count("soft_circuit_breaker_target_failure"))
}

func TestAdmission_HTTPStatusWithoutErrorIsSuccess(t *testing.T) {
	policy, err := NewPolicy(
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		RateRule{Window: time.Minute, MinRequests: 100, Rate: 1},
		TrafficRule{Window: time.Minute, MaxRequests: 1},
	)
	require.NoError(t, err)
	reg := mustRegistry(t, policy)
	hook := NewAdmission(reg, "m", nil)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(true)
	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, ModeNormal, entry.Mode())
	require.Len(t, entry.normalSamples, 1)
	assert.True(t, entry.normalSamples[0].success)

	done, allowed = hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(true)
	assert.Equal(t, ModeNormal, entry.Mode(), "HTTP status on resp is ignored when Process err is nil")
	assert.Len(t, entry.normalSamples, 2)
}

func TestAdmission_DoneFalseIsFailureWithoutWhitelist(t *testing.T) {
	policy, err := NewPolicy(
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		RateRule{Window: time.Minute, MinRequests: 100, Rate: 1},
		TrafficRule{Window: time.Minute, MaxRequests: 1},
	)
	require.NoError(t, err)
	reg := mustRegistry(t, policy)
	hook := NewAdmission(reg, "m", nil)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(false)

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, ModeDegraded, entry.Mode(), "Admission records ok only; 413/429 skip is applied at the load balancer")
}

func TestAdmission_Empty200IsSuccess(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	reg := mustRegistry(t, policy)
	hook := NewAdmission(reg, "m", nil)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(true)

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, ModeNormal, entry.Mode())
	require.Len(t, entry.normalSamples, 1)
	assert.True(t, entry.normalSamples[0].success)
}

func TestAdmission_CanceledBodyIsSuccess(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	reg := mustRegistry(t, policy)
	hook := NewAdmission(reg, "m", nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := testhelper.CreateTestRequest(testhelper.WithContext(ctx))

	done, allowed := hook.BeforeAttempt(req, "a")
	require.True(t, allowed)
	done(true)
	require.NoError(t, req.Body.Close())

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, ModeNormal, entry.Mode())
	require.Len(t, entry.normalSamples, 1)
	assert.True(t, entry.normalSamples[0].success)
}

func TestAdmission_StreamCloseIsIdempotent(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 20
	reg := mustRegistry(t, policy)
	hook := NewAdmission(reg, "m", nil)

	ch := make(chan *octollm.StreamChunk)
	close(ch)
	stream := octollm.NewStreamChan(ch, nil)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(true)
	stream.Close()
	stream.Close()

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Len(t, entry.normalSamples, 1)
	assert.True(t, entry.normalSamples[0].success)
}

func TestAdmission_BodyCloseIsIdempotent(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 20
	reg := mustRegistry(t, policy)
	hook := NewAdmission(reg, "m", nil)
	req := testhelper.CreateTestRequest()

	done, allowed := hook.BeforeAttempt(req, "a")
	require.True(t, allowed)
	done(true)
	require.NoError(t, req.Body.Close())
	require.NoError(t, req.Body.Close())

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Len(t, entry.normalSamples, 1)
}

func TestAdmission_SuccessRecoversFromDegraded(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	policy.Recovery.MinRequests = 1
	policy.Recovery.Rate = 1
	reg := mustRegistry(t, policy)
	hook := NewAdmission(reg, "m", nil)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(false)
	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	require.Equal(t, ModeDegraded, entry.Mode())

	req := testhelper.CreateTestRequest()
	done, allowed = hook.BeforeAttempt(req, "a")
	require.True(t, allowed)
	done(true)
	require.NoError(t, req.Body.Close())
	assert.Equal(t, ModeNormal, entry.Mode())
}

func TestAdmission_BodyCloseSuccess(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 20
	reg := mustRegistry(t, policy)
	hook := NewAdmission(reg, "m", nil)
	req := testhelper.CreateTestRequest()

	done, allowed := hook.BeforeAttempt(req, "a")
	require.True(t, allowed)
	done(true)
	require.NoError(t, req.Body.Close())

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Len(t, entry.normalSamples, 1)
	assert.True(t, entry.normalSamples[0].success)
}

func TestAdmission_InjectedPolicyIsUsedInsteadOfRegistryDefault(t *testing.T) {
	defaultPolicy := testPolicy()
	defaultPolicy.Failure.MinRequests = 100
	defaultPolicy.Failure.Rate = 1
	reg := mustRegistry(t, defaultPolicy)

	strict, err := NewPolicy(
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		RateRule{Window: time.Minute, MinRequests: 100, Rate: 1},
		TrafficRule{Window: time.Minute, MaxRequests: 1},
	)
	require.NoError(t, err)
	hook := NewAdmission(reg, "m", &strict)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(false)

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, &strict)
	assert.Equal(t, ModeDegraded, entry.Mode())
	assert.Equal(t, 1, entry.Policy().Failure.MinRequests)
}

func TestAdmission_PolicyChangeRebuildsEntry(t *testing.T) {
	first, err := NewPolicy(
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		RateRule{Window: time.Minute, MinRequests: 100, Rate: 1},
		TrafficRule{Window: time.Minute, MaxRequests: 1},
	)
	require.NoError(t, err)
	second, err := NewPolicy(
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		RateRule{Window: time.Minute, MinRequests: 100, Rate: 1},
		TrafficRule{Window: time.Minute, MaxRequests: 100},
	)
	require.NoError(t, err)
	reg := mustRegistry(t, first)

	hook := NewAdmission(reg, "m", &first)
	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(false)
	done, allowed = hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed, "first degraded admit consumes the only quota slot")
	done(false)
	_, allowed = hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.False(t, allowed)

	reloaded := NewAdmission(reg, "m", &second)
	done, allowed = reloaded.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed, "rebuilt entry starts in NORMAL and admits")
	done(false)
	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, &second)
	assert.Equal(t, 100, entry.Policy().DegradedTraffic.MaxRequests)
}

func TestAdmission_InvalidPolicyKeepsOldEntryAndRecords(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	policy.Recovery.MinRequests = 100
	policy.DegradedTraffic.MaxRequests = 1
	reg := mustRegistry(t, policy)
	hook := NewAdmission(reg, "m", &policy)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(false)
	done, allowed = hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed, "first degraded admit consumes the only quota slot")
	done(false)
	_, allowed = hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.False(t, allowed)

	broken := NewAdmission(reg, "m", &Policy{})
	_, allowed = broken.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	assert.False(t, allowed, "invalid policy must keep the degraded entry and still deny")
	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, ModeDegraded, entry.Mode())
}

func TestAdmission_InvalidPolicyWithoutEntryAllows(t *testing.T) {
	reg := mustRegistry(t, testPolicy())
	hook := NewAdmission(reg, "m", &Policy{})
	key := BreakerKey{ModelName: "m", BackendName: "a"}

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	assert.True(t, allowed, "missing key + invalid policy fails open")
	assert.Nil(t, done)
	_, exists := reg.entries[key]
	assert.False(t, exists, "fail-open must not create an entry")
}

func TestAdmission_CallerPolicyMutationDoesNotAffectAdmission(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	policy.Recovery.MinRequests = 100
	policy.DegradedTraffic.MaxRequests = 1
	reg := mustRegistry(t, policy)
	hook := NewAdmission(reg, "m", &policy)

	done, allowed := hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(false)
	done, allowed = hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.True(t, allowed)
	done(false)
	_, allowed = hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	require.False(t, allowed)

	policy.DegradedTraffic.MaxRequests = 100

	_, allowed = hook.BeforeAttempt(testhelper.CreateTestRequest(), "a")
	assert.False(t, allowed, "cloned hook policy must keep the degraded quota")
	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, 1, entry.Policy().DegradedTraffic.MaxRequests)
}
