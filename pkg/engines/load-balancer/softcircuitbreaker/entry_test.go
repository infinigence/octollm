package softcircuitbreaker

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustEntry(t *testing.T, policy Policy) *Entry {
	t.Helper()
	reg, err := NewRegistry(policy)
	require.NoError(t, err)
	entry, err := reg.GetOrCreate(BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	require.NoError(t, err)
	require.NotNil(t, entry)
	return entry
}

func TestEntry_NilAcquireAllows(t *testing.T) {
	var entry *Entry
	finish, allowed := entry.Acquire(context.Background(), time.Now())
	require.True(t, allowed)
	require.NotNil(t, finish)
	finish(OutcomeTargetFailure)
	assert.Equal(t, ModeNormal, entry.Mode())
}

func TestEntry_NormalTripsToDegradedOnFailureRate(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 2
	policy.Failure.Rate = 1
	entry := mustEntry(t, policy)
	now := time.Unix(0, 0)

	_, allowed := entry.Acquire(context.Background(), now)
	require.True(t, allowed)
	entry.complete(context.Background(), 0, OutcomeTargetFailure, now)
	assert.Equal(t, ModeNormal, entry.Mode())

	entry.complete(context.Background(), 0, OutcomeTargetFailure, now)
	assert.Equal(t, ModeDegraded, entry.Mode())
	assert.Empty(t, entry.normalSamples)
}

func TestEntry_MixedFailureRateTripsAtThreshold(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 2
	policy.Failure.Rate = 0.5
	entry := mustEntry(t, policy)
	now := time.Unix(0, 0)

	entry.complete(context.Background(), 0, OutcomeSuccess, now)
	assert.Equal(t, ModeNormal, entry.Mode())
	entry.complete(context.Background(), 0, OutcomeTargetFailure, now)
	assert.Equal(t, ModeDegraded, entry.Mode())
}

func TestEntry_FailureRateBelowThresholdStaysNormal(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 3
	policy.Failure.Rate = 0.5
	entry := mustEntry(t, policy)
	now := time.Unix(0, 0)

	entry.complete(context.Background(), 0, OutcomeTargetFailure, now)
	entry.complete(context.Background(), 0, OutcomeSuccess, now)
	entry.complete(context.Background(), 0, OutcomeSuccess, now)
	assert.Equal(t, ModeNormal, entry.Mode(), "1/3 failure rate is below 0.5")
	assert.Len(t, entry.normalSamples, 3)
}

func TestEntry_BelowMinRequestsStaysNormal(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 20
	policy.Failure.Rate = 0.01
	entry := mustEntry(t, policy)
	now := time.Unix(0, 0)
	entry.complete(context.Background(), 0, OutcomeTargetFailure, now)
	assert.Equal(t, ModeNormal, entry.Mode())
}

func TestEntry_NeutralDoesNotCount(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 2
	policy.Failure.Rate = 1
	entry := mustEntry(t, policy)
	now := time.Unix(0, 0)
	entry.complete(context.Background(), 0, OutcomeNeutral, now)
	entry.complete(context.Background(), 0, OutcomeTargetFailure, now)
	assert.Equal(t, ModeNormal, entry.Mode())
	assert.Len(t, entry.normalSamples, 1)
}

func TestEntry_DegradedNeutralDoesNotCount(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 2
	policy.Failure.Rate = 1
	policy.Recovery.MinRequests = 2
	policy.Recovery.Rate = 1
	entry := mustEntry(t, policy)
	now := time.Unix(0, 0)
	entry.complete(context.Background(), 0, OutcomeTargetFailure, now)
	entry.complete(context.Background(), 0, OutcomeTargetFailure, now)
	require.Equal(t, ModeDegraded, entry.Mode())
	gen := entry.generation

	entry.complete(context.Background(), gen, OutcomeNeutral, now)
	entry.complete(context.Background(), gen, OutcomeNeutral, now)
	assert.Equal(t, ModeDegraded, entry.Mode())
	assert.Empty(t, entry.recoverySamples)

	entry.complete(context.Background(), gen, OutcomeSuccess, now)
	assert.Equal(t, ModeDegraded, entry.Mode())
	entry.complete(context.Background(), gen, OutcomeSuccess, now)
	assert.Equal(t, ModeNormal, entry.Mode())
}

func TestEntry_DegradedRecoversOnSuccessRate(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 2
	policy.Failure.Rate = 1
	policy.Recovery.MinRequests = 2
	policy.Recovery.Rate = 0.5
	entry := mustEntry(t, policy)
	now := time.Unix(0, 0)
	entry.complete(context.Background(), 0, OutcomeTargetFailure, now)
	entry.complete(context.Background(), 0, OutcomeTargetFailure, now)
	require.Equal(t, ModeDegraded, entry.Mode())
	gen := entry.generation

	entry.complete(context.Background(), gen, OutcomeSuccess, now)
	assert.Equal(t, ModeDegraded, entry.Mode())
	entry.complete(context.Background(), gen, OutcomeSuccess, now)
	assert.Equal(t, ModeNormal, entry.Mode())
	assert.Empty(t, entry.recoverySamples)
}

func TestEntry_RecoveryRateBelowThresholdStaysDegraded(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 2
	policy.Failure.Rate = 1
	policy.Recovery.MinRequests = 2
	policy.Recovery.Rate = 0.5
	entry := mustEntry(t, policy)
	now := time.Unix(0, 0)
	entry.complete(context.Background(), 0, OutcomeTargetFailure, now)
	entry.complete(context.Background(), 0, OutcomeTargetFailure, now)
	require.Equal(t, ModeDegraded, entry.Mode())
	gen := entry.generation

	entry.complete(context.Background(), gen, OutcomeTargetFailure, now)
	entry.complete(context.Background(), gen, OutcomeTargetFailure, now)
	assert.Equal(t, ModeDegraded, entry.Mode(), "0/2 success rate is below 0.5")
	assert.Len(t, entry.recoverySamples, 2)
}

func TestEntry_DegradedQuotaConsumedOnAcquireNotReleasedOnFinish(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	policy.Recovery.MinRequests = 100
	policy.DegradedTraffic.MaxRequests = 2
	policy.DegradedTraffic.Window = time.Minute
	entry := mustEntry(t, policy)
	now := time.Unix(0, 0)
	entry.complete(context.Background(), 0, OutcomeTargetFailure, now)
	require.Equal(t, ModeDegraded, entry.Mode())

	finish1, allowed := entry.Acquire(context.Background(), now)
	require.True(t, allowed)
	finish2, allowed := entry.Acquire(context.Background(), now)
	require.True(t, allowed)
	_, allowed = entry.Acquire(context.Background(), now)
	assert.False(t, allowed)

	finish1(OutcomeSuccess)
	finish2(OutcomeSuccess)
	_, allowed = entry.Acquire(context.Background(), now)
	assert.False(t, allowed, "quota must not be released when attempts finish")
}

func TestEntry_DegradedQuotaExpiresWithWindow(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	policy.DegradedTraffic.MaxRequests = 1
	policy.DegradedTraffic.Window = 10 * time.Second
	entry := mustEntry(t, policy)
	now := time.Unix(0, 0)
	entry.complete(context.Background(), 0, OutcomeTargetFailure, now)
	require.Equal(t, ModeDegraded, entry.Mode())

	_, allowed := entry.Acquire(context.Background(), now)
	require.True(t, allowed)
	_, allowed = entry.Acquire(context.Background(), now.Add(time.Second))
	assert.False(t, allowed)

	_, allowed = entry.Acquire(context.Background(), now.Add(11*time.Second))
	assert.True(t, allowed)
}

func TestEntry_StaleGenerationIsDropped(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1
	policy.Failure.Rate = 1
	policy.Recovery.MinRequests = 1
	policy.Recovery.Rate = 1
	entry := mustEntry(t, policy)
	now := time.Unix(0, 0)

	finish, allowed := entry.Acquire(context.Background(), now)
	require.True(t, allowed)
	entry.complete(context.Background(), 0, OutcomeTargetFailure, now)
	require.Equal(t, ModeDegraded, entry.Mode())

	finish(OutcomeSuccess)
	assert.Equal(t, ModeDegraded, entry.Mode())
	assert.Empty(t, entry.recoverySamples)
}

func TestEntry_FinishIsIdempotent(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 20
	entry := mustEntry(t, policy)
	finish, allowed := entry.Acquire(context.Background(), time.Unix(0, 0))
	require.True(t, allowed)
	finish(OutcomeTargetFailure)
	finish(OutcomeTargetFailure)
	assert.Len(t, entry.normalSamples, 1)
}

func TestEntry_OldSamplesFallOutOfWindow(t *testing.T) {
	policy := testPolicy()
	policy.Failure.Window = 10 * time.Second
	policy.Failure.MinRequests = 2
	policy.Failure.Rate = 1
	entry := mustEntry(t, policy)
	now := time.Unix(0, 0)
	entry.complete(context.Background(), 0, OutcomeTargetFailure, now)
	entry.complete(context.Background(), 0, OutcomeTargetFailure, now.Add(11*time.Second))
	assert.Equal(t, ModeNormal, entry.Mode())
	assert.Len(t, entry.normalSamples, 1)
}

func TestEntry_ConcurrentAcquireFinish(t *testing.T) {
	policy := testPolicy()
	policy.Failure.MinRequests = 1_000_000
	policy.Failure.Rate = 1
	entry := mustEntry(t, policy)

	var wg sync.WaitGroup
	var allowed atomic.Int64
	for range 64 {
		wg.Go(func() {
			finish, ok := entry.Acquire(context.Background(), time.Now())
			if !ok {
				return
			}
			allowed.Add(1)
			finish(OutcomeSuccess)
		})
	}
	wg.Wait()
	assert.Equal(t, int64(64), allowed.Load())
	assert.Equal(t, ModeNormal, entry.Mode())
	assert.Equal(t, 64, len(entry.normalSamples))
}

type captureHandler struct {
	mu       sync.Mutex
	messages []string
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.messages = append(h.messages, r.Message)
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) count(msg string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.messages {
		if m == msg {
			n++
		}
	}
	return n
}

func installLogCapture(t *testing.T) *captureHandler {
	t.Helper()
	h := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

func TestEntry_StartedLogOnceOnTrip(t *testing.T) {
	logs := installLogCapture(t)
	policy := testPolicy()
	policy.Failure.MinRequests = 2
	policy.Failure.Rate = 1
	entry := mustEntry(t, policy)
	now := time.Unix(0, 0)

	entry.complete(context.Background(), 0, OutcomeTargetFailure, now)
	assert.Equal(t, 0, logs.count("soft_circuit_breaker_started"))

	entry.complete(context.Background(), 0, OutcomeTargetFailure, now)
	assert.Equal(t, 1, logs.count("soft_circuit_breaker_started"))

	entry.complete(context.Background(), entry.generation, OutcomeTargetFailure, now)
	assert.Equal(t, 1, logs.count("soft_circuit_breaker_started"))
	assert.Equal(t, 0, logs.count("soft_circuit_breaker_ended"))
}

func TestEntry_EndedLogOnceOnRecover(t *testing.T) {
	logs := installLogCapture(t)
	policy := testPolicy()
	policy.Failure.MinRequests = 2
	policy.Failure.Rate = 1
	policy.Recovery.MinRequests = 2
	policy.Recovery.Rate = 0.5
	entry := mustEntry(t, policy)
	now := time.Unix(0, 0)
	entry.complete(context.Background(), 0, OutcomeTargetFailure, now)
	entry.complete(context.Background(), 0, OutcomeTargetFailure, now)
	require.Equal(t, ModeDegraded, entry.Mode())
	assert.Equal(t, 1, logs.count("soft_circuit_breaker_started"))

	gen := entry.generation
	entry.complete(context.Background(), gen, OutcomeSuccess, now)
	assert.Equal(t, 0, logs.count("soft_circuit_breaker_ended"))
	entry.complete(context.Background(), gen, OutcomeSuccess, now)
	assert.Equal(t, 1, logs.count("soft_circuit_breaker_ended"))

	entry.complete(context.Background(), entry.generation, OutcomeSuccess, now)
	assert.Equal(t, 1, logs.count("soft_circuit_breaker_ended"))
	assert.Equal(t, 1, logs.count("soft_circuit_breaker_started"))
}
