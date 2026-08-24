package softcircuitbreaker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/octollm"
)

// BreakerMode is the admission mode of one backend breaker entry.
type BreakerMode uint8

const (
	ModeNormal BreakerMode = iota
	ModeDegraded
)

// Outcome is the classified result of one real backend attempt.
type Outcome uint8

const (
	// OutcomeNeutral is ignored by failure and recovery windows.
	OutcomeNeutral Outcome = iota
	// OutcomeSuccess is a confirmed healthy completion.
	OutcomeSuccess
	// OutcomeTargetFailure is a real attempt error not excluded by the HTTP status whitelist.
	OutcomeTargetFailure
)

func (o Outcome) String() string {
	switch o {
	case OutcomeNeutral:
		return "neutral"
	case OutcomeSuccess:
		return "success"
	case OutcomeTargetFailure:
		return "target_failure"
	default:
		return "unknown"
	}
}

// FinishFunc records one attempt outcome. Implementations are idempotent.
type FinishFunc func(outcome Outcome)

type resultSample struct {
	at      time.Time
	success bool
}

// Entry holds breaker state for one BreakerKey.
type Entry struct {
	key    BreakerKey
	policy Policy

	mu sync.Mutex

	mode       BreakerMode
	generation uint64

	normalSamples        []resultSample
	recoverySamples      []resultSample
	degradedRequestTimes []time.Time
}

// Mode returns the current admission mode. A nil Entry is treated as NORMAL.
func (e *Entry) Mode() BreakerMode {
	if e == nil {
		return ModeNormal
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.mode
}

// IsExcludedHTTPStatus reports whether status is in this entry's frozen whitelist.
func (e *Entry) IsExcludedHTTPStatus(status int) bool {
	if e == nil {
		return false
	}
	_, ok := e.policy.ExcludedHTTPStatusCodes[status]
	return ok
}

// Policy returns a copy of this entry's frozen policy. The whitelist map is cloned.
func (e *Entry) Policy() Policy {
	if e == nil {
		return Policy{}
	}
	return clonePolicy(e.policy)
}

// snapshotGeneration returns the current generation for logging.
// A nil Entry is generation 0.
func (e *Entry) snapshotGeneration() uint64 {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.generation
}

// Acquire admits one attempt. NORMAL always admits. DEGRADED consumes rolling
// request quota and may deny. A nil Entry admits with a no-op FinishFunc.
// Quota is consumed on admit and is not released when the attempt finishes.
// ctx is attached to deny/complete logs so request trace IDs can be correlated.
func (e *Entry) Acquire(ctx context.Context, now time.Time) (FinishFunc, bool) {
	if e == nil {
		return func(Outcome) {}, true
	}

	e.mu.Lock()
	if e.mode == ModeDegraded && !e.tryAcquireDegradedRequestLocked(now) {
		modelName, backendName := e.key.ModelName, e.key.BackendName
		generation := e.generation
		windowSeconds := e.policy.DegradedTraffic.Window.Seconds()
		maxRequests := e.policy.DegradedTraffic.MaxRequests
		admitted := len(e.degradedRequestTimes)
		e.mu.Unlock()
		slog.DebugContext(ctx, "soft_circuit_breaker_backend_denied",
			"model_name", modelName,
			"backend_name", backendName,
			"generation", generation,
			"window_seconds", windowSeconds,
			"max_requests", maxRequests,
			"admitted_requests", admitted,
		)
		return nil, false
	}
	generation := e.generation
	e.mu.Unlock()

	var once sync.Once
	return func(outcome Outcome) {
		once.Do(func() {
			e.complete(ctx, generation, outcome, time.Now())
		})
	}, true
}

// tryAcquireDegradedRequestLocked consumes one degraded-mode quota slot.
// It drops timestamps older than DegradedTraffic.Window, then admits if the
// remaining count is below MaxRequests. Caller must hold e.mu.
func (e *Entry) tryAcquireDegradedRequestLocked(now time.Time) bool {
	cutoff := now.Add(-e.policy.DegradedTraffic.Window)
	n := 0
	for _, at := range e.degradedRequestTimes {
		if !at.Before(cutoff) {
			e.degradedRequestTimes[n] = at
			n++
		}
	}
	e.degradedRequestTimes = e.degradedRequestTimes[:n]
	if len(e.degradedRequestTimes) >= e.policy.DegradedTraffic.MaxRequests {
		return false
	}
	e.degradedRequestTimes = append(e.degradedRequestTimes, now)
	return true
}

// complete records one attempt outcome. A mismatched generation is ignored
// (the breaker already tripped or recovered after Acquire). Neutral does not
// enter the sliding windows. ctx is only used for started/ended/stale logs.
func (e *Entry) complete(ctx context.Context, generation uint64, outcome Outcome, now time.Time) {
	e.mu.Lock()
	if generation != e.generation {
		modelName, backendName := e.key.ModelName, e.key.BackendName
		current := e.generation
		e.mu.Unlock()
		slog.DebugContext(ctx, "soft_circuit_breaker_stale_result_ignored",
			"model_name", modelName,
			"backend_name", backendName,
			"request_generation", generation,
			"current_generation", current,
			"outcome", outcome.String(),
		)
		return
	}
	if outcome == OutcomeNeutral {
		e.mu.Unlock()
		return
	}

	var started, ended bool
	var total, success, failure int
	if e.mode == ModeNormal {
		started, total, success, failure = e.recordNormalLocked(outcome, now)
	} else {
		ended, total, success, failure = e.recordDegradedLocked(outcome, now)
	}
	modelName, backendName := e.key.ModelName, e.key.BackendName
	gen := e.generation
	failureWindow := e.policy.Failure.Window.Seconds()
	failureThreshold := e.policy.Failure.Rate
	recoveryWindow := e.policy.Recovery.Window.Seconds()
	recoveryThreshold := e.policy.Recovery.Rate
	e.mu.Unlock()

	if started {
		slog.InfoContext(ctx, "soft_circuit_breaker_started",
			"model_name", modelName,
			"backend_name", backendName,
			"generation", gen,
			"window_seconds", failureWindow,
			"total_count", total,
			"success_count", success,
			"failure_count", failure,
			"failure_rate", float64(failure)/float64(total),
			"failure_threshold", failureThreshold,
		)
	}
	if ended {
		slog.InfoContext(ctx, "soft_circuit_breaker_ended",
			"model_name", modelName,
			"backend_name", backendName,
			"generation", gen,
			"window_seconds", recoveryWindow,
			"total_count", total,
			"success_count", success,
			"failure_count", failure,
			"success_rate", float64(success)/float64(total),
			"success_threshold", recoveryThreshold,
		)
	}
}

// recordNormalLocked appends a NORMAL-mode sample and trips to DEGRADED when
// the failure window has at least Failure.MinRequests and failure/total >=
// Failure.Rate. Caller must hold e.mu.
func (e *Entry) recordNormalLocked(outcome Outcome, now time.Time) (tripped bool, total, success, failure int) {
	e.normalSamples = pruneSamples(e.normalSamples, now.Add(-e.policy.Failure.Window))
	e.normalSamples = append(e.normalSamples, resultSample{
		at:      now,
		success: outcome == OutcomeSuccess,
	})
	total, success, failure = countSamples(e.normalSamples)
	if len(e.normalSamples) < e.policy.Failure.MinRequests {
		return false, total, success, failure
	}
	if float64(failure)/float64(len(e.normalSamples)) >= e.policy.Failure.Rate {
		e.enterDegradedLocked()
		return true, total, success, failure
	}
	return false, total, success, failure
}

// recordDegradedLocked appends a DEGRADED-mode sample and recovers to NORMAL
// when the recovery window has at least Recovery.MinRequests and
// success/total >= Recovery.Rate. Caller must hold e.mu.
func (e *Entry) recordDegradedLocked(outcome Outcome, now time.Time) (recovered bool, total, success, failure int) {
	e.recoverySamples = pruneSamples(e.recoverySamples, now.Add(-e.policy.Recovery.Window))
	e.recoverySamples = append(e.recoverySamples, resultSample{
		at:      now,
		success: outcome == OutcomeSuccess,
	})
	total, success, failure = countSamples(e.recoverySamples)
	if len(e.recoverySamples) < e.policy.Recovery.MinRequests {
		return false, total, success, failure
	}
	if float64(success)/float64(len(e.recoverySamples)) >= e.policy.Recovery.Rate {
		e.recoverNormalLocked()
		return true, total, success, failure
	}
	return false, total, success, failure
}

// enterDegradedLocked switches to DEGRADED, bumps generation, and clears
// all windows so in-flight FinishFuncs from NORMAL become stale. Caller must hold e.mu.
func (e *Entry) enterDegradedLocked() {
	e.mode = ModeDegraded
	e.generation++
	e.normalSamples = nil
	e.recoverySamples = nil
	e.degradedRequestTimes = nil
}

// recoverNormalLocked switches to NORMAL, bumps generation, and clears all
// windows. Caller must hold e.mu.
func (e *Entry) recoverNormalLocked() {
	e.mode = ModeNormal
	e.generation++
	e.normalSamples = nil
	e.recoverySamples = nil
	e.degradedRequestTimes = nil
}

// ClassifyAttemptError maps a real backend attempt error to an Outcome.
// Client-canceled request context is Neutral. An extractable HTTP status in
// this entry's exclusion set is Neutral. Everything else, including
// deadline/timeout and errors with no HTTP status, is TargetFailure.
// error.code is not inspected. A nil Entry has no whitelist.
func (e *Entry) ClassifyAttemptError(req *octollm.Request, err error) Outcome {
	if isClientCanceled(req) {
		return OutcomeNeutral
	}
	if status, ok := ExtractHTTPStatus(err); ok && e.IsExcludedHTTPStatus(status) {
		return OutcomeNeutral
	}
	return OutcomeTargetFailure
}

// ClassifyHTTPStatus maps a response HTTP status to an Outcome.
// Client-canceled request context is Neutral. Excluded statuses are Neutral.
// Other 4xx/5xx statuses are TargetFailure. Statuses below 400 are Success.
// A nil Entry has no whitelist.
func (e *Entry) ClassifyHTTPStatus(req *octollm.Request, status int) Outcome {
	if isClientCanceled(req) {
		return OutcomeNeutral
	}
	if e.IsExcludedHTTPStatus(status) {
		return OutcomeNeutral
	}
	if status >= 400 {
		return OutcomeTargetFailure
	}
	return OutcomeSuccess
}

// isClientCanceled reports whether the request context was canceled by the
// client. DeadlineExceeded is not treated as cancel.
func isClientCanceled(req *octollm.Request) bool {
	if req == nil {
		return false
	}
	ctx := req.Context()
	if ctx == nil {
		return false
	}
	return errors.Is(ctx.Err(), context.Canceled)
}

// ExtractHTTPStatus walks the error chain for a positive HTTP status.
// Order: UpstreamRespError, UpstreamHTTPError, HandlerError.
func ExtractHTTPStatus(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	var respErr *errutils.UpstreamRespError
	if errors.As(err, &respErr) && respErr.StatusCode > 0 {
		return respErr.StatusCode, true
	}
	var httpErr *errutils.UpstreamHTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode > 0 {
		return httpErr.StatusCode, true
	}
	var handlerErr *errutils.HandlerError
	if errors.As(err, &handlerErr) && handlerErr.StatusCode > 0 {
		return handlerErr.StatusCode, true
	}
	return 0, false
}

// pruneSamples drops samples strictly before cutoff and keeps the rest in
// original order. The returned slice reuses samples' backing array.
func pruneSamples(samples []resultSample, cutoff time.Time) []resultSample {
	n := 0
	for _, sample := range samples {
		if !sample.at.Before(cutoff) {
			samples[n] = sample
			n++
		}
	}
	return samples[:n]
}

// countSamples returns window size and success/failure counts. A sample with
// success=false is a target failure.
func countSamples(samples []resultSample) (total, success, failure int) {
	total = len(samples)
	for _, sample := range samples {
		if sample.success {
			success++
		} else {
			failure++
		}
	}
	return total, success, failure
}
