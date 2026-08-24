package softcircuitbreaker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/infinigence/octollm/pkg/engines/client"
	loadbalancer "github.com/infinigence/octollm/pkg/engines/load-balancer"
	"github.com/infinigence/octollm/pkg/octollm"
)

// Hook is the default BackendAttemptHook. Registry outlives any one load balancer.
// A nil policy uses the Registry default. A non-nil policy is cloned at NewHook
// so later mutation of the caller's Policy does not race with GetOrCreate.
// YAML reloads should pass a new Policy to a new Hook.
type Hook struct {
	registry  *Registry
	modelName string
	policy    *Policy
}

var _ loadbalancer.BackendAttemptHook = (*Hook)(nil)

// NewHook builds a hook. A nil registry admits every backend and records nothing.
func NewHook(registry *Registry, modelName string, policy *Policy) *Hook {
	h := &Hook{
		registry:  registry,
		modelName: modelName,
	}
	if policy != nil {
		cloned := clonePolicy(*policy)
		h.policy = &cloned
	}
	return h
}

func (h *Hook) BeforeAttempt(req *octollm.Request, backendName string) (loadbalancer.AttemptDoneFunc, bool) {
	if h == nil || h.registry == nil {
		return nil, true
	}

	entry, err := h.registry.GetOrCreate(BreakerKey{
		ModelName:   h.modelName,
		BackendName: backendName,
	}, h.policy)
	// Missing key + invalid policy, or a nil registry lookup, fails open: admit
	// this attempt and do not record it. Validate Policy at NewRegistry / NewHook
	// construction. An existing entry keeps its last good policy.
	if err != nil || entry == nil {
		return nil, true
	}

	finish, allowed := entry.Acquire(req.Context(), time.Now())
	if !allowed {
		return nil, false
	}
	return func(resp *octollm.Response, err error) {
		observeBackendAttempt(req, resp, err, entry, finish)
	}, true
}

func observeBackendAttempt(
	req *octollm.Request,
	resp *octollm.Response,
	err error,
	entry *Entry,
	finish FinishFunc,
) {
	if finish == nil {
		return
	}
	var once sync.Once
	complete := func(outcome Outcome, path string, status int, hasStatus, invalid bool) {
		once.Do(func() {
			logAttempt(req.Context(), entry, outcome, path, status, hasStatus, invalid)
			finish(outcome)
		})
	}

	if err != nil {
		status, hasStatus := ExtractHTTPStatus(err)
		complete(entry.ClassifyAttemptError(req, err), "sync", status, hasStatus, false)
		return
	}
	if resp == nil {
		complete(OutcomeTargetFailure, "sync", 0, false, true)
		return
	}
	if resp.StatusCode >= 400 {
		complete(entry.ClassifyHTTPStatus(req, resp.StatusCode), "sync", resp.StatusCode, true, false)
		return
	}
	if resp.Stream != nil {
		resp.Stream.OnClose(func() {
			outcome, status, hasStatus := classifyStreamCompletion(req, entry)
			complete(outcome, "stream", status, hasStatus, false)
		})
		return
	}
	if resp.Body != nil {
		resp.Body.OnClose(func() {
			complete(classifyBodyCompletion(req), "body", 0, false, false)
		})
		return
	}
	complete(OutcomeNeutral, "sync", 0, false, false)
}

func logAttempt(ctx context.Context, entry *Entry, outcome Outcome, path string, status int, hasStatus, invalid bool) {
	if entry == nil || (!invalid && outcome != OutcomeTargetFailure) {
		return
	}
	generation := entry.snapshotGeneration()
	if invalid {
		slog.WarnContext(ctx, "soft_circuit_breaker_invalid_response",
			"model_name", entry.key.ModelName,
			"backend_name", entry.key.BackendName,
			"generation", generation,
			"completion_path", path,
			"outcome", outcome.String(),
		)
	}
	if outcome != OutcomeTargetFailure {
		return
	}
	args := []any{
		"model_name", entry.key.ModelName,
		"backend_name", entry.key.BackendName,
		"generation", generation,
		"completion_path", path,
		"outcome", outcome.String(),
	}
	if hasStatus {
		args = append(args, "http_status", status)
	}
	slog.DebugContext(ctx, "soft_circuit_breaker_target_failure", args...)
}

func classifyStreamCompletion(req *octollm.Request, entry *Entry) (Outcome, int, bool) {
	if isClientCanceled(req) {
		return OutcomeNeutral, 0, false
	}
	if streamErr, ok := client.GetClientProcessStreamError(req); ok && streamErr != nil {
		status, hasStatus := ExtractHTTPStatus(streamErr)
		return entry.ClassifyAttemptError(req, streamErr), status, hasStatus
	}
	return OutcomeSuccess, 0, false
}

func classifyBodyCompletion(req *octollm.Request) Outcome {
	if isClientCanceled(req) {
		return OutcomeNeutral
	}
	return OutcomeSuccess
}
