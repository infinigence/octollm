package loadbalancer

import (
	"github.com/prometheus/client_golang/prometheus"
)

const (
	affinityKindStrongHitCacheAware = "strong_hit_cache_aware"
	affinityKindWeakHitCacheAware   = "weak_hit_cache_aware"
	affinityKindWeakHitFallback     = "weak_hit_fallback"
	affinityKindMiss                = "miss"
)

var (
	totalFailoverRequestsCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "octollm_total_failover_requests",
		Help: "total failover requests",
	}, []string{"model_name", "backend_name"})

	cacheAwareRouteCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "octollm_cache_aware_route_total",
		Help: "cache-aware LB outbound attempts by hit kind",
	}, []string{"kind", "model_name", "backend_name"})
)

func init() {
	prometheus.MustRegister(totalFailoverRequestsCounter)
	prometheus.MustRegister(cacheAwareRouteCounter)
}
