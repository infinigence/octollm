package softcircuitbreaker

import (
	"fmt"
	"time"
)

// RateRule is a sliding-window rate threshold.
type RateRule struct {
	Window      time.Duration
	MinRequests int
	Rate        float64
}

// TrafficRule is a sliding-window request-count limit.
type TrafficRule struct {
	Window      time.Duration
	MaxRequests int
}

// Policy is the immutable runtime configuration of one breaker entry.
type Policy struct {
	Failure         RateRule
	Recovery        RateRule
	DegradedTraffic TrafficRule
}

// NewPolicy validates rules.
func NewPolicy(failure RateRule, recovery RateRule, degraded TrafficRule) (Policy, error) {
	return normalizePolicy(Policy{
		Failure:         failure,
		Recovery:        recovery,
		DegradedTraffic: degraded,
	})
}

func normalizePolicy(policy Policy) (Policy, error) {
	if err := validateRateRule("failure", policy.Failure); err != nil {
		return Policy{}, err
	}
	if err := validateRateRule("recovery", policy.Recovery); err != nil {
		return Policy{}, err
	}
	if err := validateTrafficRule("degraded_request_limit", policy.DegradedTraffic); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

func validateRateRule(name string, rule RateRule) error {
	if rule.Window <= 0 {
		return fmt.Errorf("softcircuitbreaker: %s.window must be > 0", name)
	}
	if rule.MinRequests <= 0 {
		return fmt.Errorf("softcircuitbreaker: %s.min_requests must be > 0", name)
	}
	if rule.Rate <= 0 || rule.Rate > 1 {
		return fmt.Errorf("softcircuitbreaker: %s.rate must be in (0, 1]", name)
	}
	return nil
}

func validateTrafficRule(name string, rule TrafficRule) error {
	if rule.Window <= 0 {
		return fmt.Errorf("softcircuitbreaker: %s.window must be > 0", name)
	}
	if rule.MaxRequests <= 0 {
		return fmt.Errorf("softcircuitbreaker: %s.max_requests must be > 0", name)
	}
	return nil
}
