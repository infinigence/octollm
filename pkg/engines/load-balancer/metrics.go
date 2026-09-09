package loadbalancer

import (
	"github.com/prometheus/client_golang/prometheus"
)

const (
	affinityKindStrongCacheAware = "strong_cache"
	affinityKindWeakCacheAware   = "weak_cache"
	affinityKindMiss             = "miss"
)

var (
	totalFailoverRequestsCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "octollm_total_failover_requests",
		Help: "total failover requests",
	}, []string{"model_name", "backend_name"})

	cacheAwareRouteCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "octollm_cache_aware_route_total",
		Help: "cache-aware LB outbound attempts by hit kind",
	}, []string{"expect", "actual", "model_name", "backend_name"})
)

func init() {
	prometheus.MustRegister(totalFailoverRequestsCounter)
	prometheus.MustRegister(cacheAwareRouteCounter)
}
