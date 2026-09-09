package loadbalancer

import (
	"github.com/infinigence/octollm/pkg/octollm"
)

// backendHeadroom is the per-backend cache-miss utilization policy: given an
// already-fetched concurrency count, decide whether a cache-miss (or weak hit)
// may occupy this backend. Redis I/O, affinity strength, and exclude/retry
// stay in the load balancer Process loops.
type backendHeadroom struct {
	maxConcurrencyFn        ShardMaxConcurrencyFn
	cacheMissMaxUtilization float64
}

func newBackendHeadroom(item BackendItem) backendHeadroom {
	fn := item.MaxConcurrencyFn
	if fn == nil {
		w := item.Weight
		fn = func(*octollm.Request) int { return w }
	}
	return backendHeadroom{
		maxConcurrencyFn:        fn,
		cacheMissMaxUtilization: item.CacheMissMaxUtilization,
	}
}

func (h backendHeadroom) enabled() bool {
	return h.cacheMissMaxUtilization > 0 && h.cacheMissMaxUtilization < 1.0
}

func (h backendHeadroom) denom(req *octollm.Request) int {
	if h.maxConcurrencyFn == nil {
		return 0
	}
	return h.maxConcurrencyFn(req)
}

// exceeded reports whether a cache-miss should be denied to preserve headroom.
// Callers must not invoke it when Redis failed to read concurrency (fail-open).
// A disabled ceiling never exceeds. A missing/non-positive denominator exceeds.
func (h backendHeadroom) exceeded(req *octollm.Request, concurrency int64) bool {
	if !h.enabled() {
		return false
	}
	d := h.denom(req)
	if d <= 0 {
		return true
	}
	return float64(concurrency)/float64(d) > h.cacheMissMaxUtilization
}
