package loadbalancer

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	totalFailoverRequestsCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "octollm_total_failover_requests",
		Help: "total failover requests",
	}, []string{"model_name", "backend_name"})
)

func init() {
	prometheus.MustRegister(totalFailoverRequestsCounter)
}
