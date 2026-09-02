package loadbalancer

import (
	"github.com/infinigence/octollm/pkg/octollm"
)

// AttemptDoneFunc is invoked after a real backend Engine.Process returns.
// Callers skip it on 413/429. ok is true when err == nil.
// A nil admission, or a nil done from BeforeAttempt, means no completion callback.
type AttemptDoneFunc func(ok bool)

// BackendAdmission is a static admission extension for shard-key load balancers.
// LB implementations call it after selecting a backend and before Engine.Process.
// A nil admission skips checks and preserves current balancer behavior.
type BackendAdmission interface {
	BeforeAttempt(req *octollm.Request, backendName string) (done AttemptDoneFunc, allowed bool)
}
