package softcircuitbreaker

import (
	"context"
	"log/slog"
	"time"

	loadbalancer "github.com/infinigence/octollm/pkg/engines/load-balancer"
	"github.com/infinigence/octollm/pkg/octollm"
)

// Admission is the default BackendAdmission. Registry outlives any one load balancer.
// A nil policy uses the Registry default. A non-nil policy is cloned at NewAdmission
// so later mutation of the caller's Policy does not race with GetOrCreate.
// YAML reloads should pass a new Policy to a new Admission.
type Admission struct {
	registry  *Registry
	modelName string
	policy    *Policy
}

var _ loadbalancer.BackendAdmission = (*Admission)(nil)

// NewAdmission builds an admission. A nil registry admits every backend and records nothing.
func NewAdmission(registry *Registry, modelName string, policy *Policy) *Admission {
	h := &Admission{
		registry:  registry,
		modelName: modelName,
	}
	if policy != nil {
		copied := *policy
		h.policy = &copied
	}
	return h
}

func (h *Admission) BeforeAttempt(req *octollm.Request, backendName string) (loadbalancer.AttemptDoneFunc, bool) {
	if h == nil || h.registry == nil {
		return nil, true
	}

	entry, err := h.registry.GetOrCreate(BreakerKey{
		ModelName:   h.modelName,
		BackendName: backendName,
	}, h.policy)
	// Missing key + invalid policy, or a nil registry lookup, fails open: admit
	// this attempt and do not record it. Validate Policy at NewRegistry / NewAdmission
	// construction. An existing entry keeps its last good policy.
	if err != nil || entry == nil {
		return nil, true
	}

	finish, allowed := entry.Acquire(req.Context(), time.Now())
	if !allowed {
		return nil, false
	}
	return func(ok bool) {
		recordAttempt(req.Context(), entry, finish, ok)
	}, true
}

func recordAttempt(ctx context.Context, entry *Entry, finish FinishFunc, ok bool) {
	if finish == nil {
		return
	}
	outcome := OutcomeTargetFailure
	if ok {
		outcome = OutcomeSuccess
	}
	logAttempt(ctx, entry, outcome)
	finish(outcome)
}

func logAttempt(ctx context.Context, entry *Entry, outcome Outcome) {
	if entry == nil || outcome != OutcomeTargetFailure {
		return
	}
	slog.DebugContext(ctx, "soft_circuit_breaker_target_failure",
		"model_name", entry.key.ModelName,
		"backend_name", entry.key.BackendName,
		"generation", entry.snapshotGeneration(),
		"outcome", outcome.String(),
	)
}
