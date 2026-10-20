package common

import "github.com/infinigence/octollm/pkg/octollm"

// CacheHitKind is the cache-affinity outcome of one backend attempt.
type CacheHitKind string

const (
	CacheHitStrong CacheHitKind = "strong"
	CacheHitWeak   CacheHitKind = "weak"
	CacheMiss      CacheHitKind = "miss"
)

// CacheHitKindFromRequest reads the cache-affinity outcome written on one attempt.
// A missing or illegal value is a miss.
func CacheHitKindFromRequest(req *octollm.Request) CacheHitKind {
	kind, ok := octollm.GetCtxValue[CacheHitKind](req, octollm.ContextKeyCacheHitKind)
	if !ok {
		return CacheMiss
	}
	switch kind {
	case CacheHitStrong, CacheHitWeak, CacheMiss:
		return kind
	default:
		return CacheMiss
	}
}
