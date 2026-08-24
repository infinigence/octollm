package loadbalancer

import (
	"errors"

	"github.com/infinigence/octollm/pkg/octollm"
)

// AttemptDoneFunc is invoked once after a real backend Engine.Process returns.
// A nil hook, or a nil done from BeforeAttempt, means no completion callback.
type AttemptDoneFunc func(resp *octollm.Response, err error)

// BackendAttemptHook is a static admission extension for shard-key load balancers.
// LB implementations call it after selecting a backend and before Engine.Process.
// A nil hook skips admission and preserves current balancer behavior.
type BackendAttemptHook interface {
	BeforeAttempt(req *octollm.Request, backendName string) (done AttemptDoneFunc, allowed bool)
}

// ErrNoBackendPermitted is returned when every candidate backend was rejected by
// the attempt hook and no real backend Engine was invoked in this request.
var ErrNoBackendPermitted = errors.New("no backend permitted by attempt hook")
