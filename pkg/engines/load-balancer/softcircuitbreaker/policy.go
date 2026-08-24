package softcircuitbreaker

import (
	"fmt"
	"maps"
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
// ExcludedHTTPStatusCodes is caller-supplied; empty means no HTTP status is excluded.
// Callers must not mutate the map after NewPolicy / GetOrCreate.
type Policy struct {
	Failure                 RateRule
	Recovery                RateRule
	DegradedTraffic         TrafficRule
	ExcludedHTTPStatusCodes map[int]struct{}
}

// NewPolicy validates rules and freezes excludedHTTPStatusCodes.
// Codes must be in [400, 599]. Duplicate values are merged. An empty list is valid.
func NewPolicy(failure RateRule, recovery RateRule, degraded TrafficRule, excludedHTTPStatusCodes []int) (Policy, error) {
	excluded := make(map[int]struct{}, len(excludedHTTPStatusCodes))
	for _, status := range excludedHTTPStatusCodes {
		excluded[status] = struct{}{}
	}
	return normalizePolicy(Policy{
		Failure:                 failure,
		Recovery:                recovery,
		DegradedTraffic:         degraded,
		ExcludedHTTPStatusCodes: excluded,
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

	excluded := make(map[int]struct{}, len(policy.ExcludedHTTPStatusCodes))
	for status := range policy.ExcludedHTTPStatusCodes {
		if err := validateExcludedHTTPStatus(status); err != nil {
			return Policy{}, err
		}
		excluded[status] = struct{}{}
	}
	policy.ExcludedHTTPStatusCodes = excluded
	return policy, nil
}

func clonePolicy(policy Policy) Policy {
	policy.ExcludedHTTPStatusCodes = maps.Clone(policy.ExcludedHTTPStatusCodes)
	if policy.ExcludedHTTPStatusCodes == nil {
		policy.ExcludedHTTPStatusCodes = make(map[int]struct{})
	}
	return policy
}

// Same reports whether two policies have identical rules and excluded HTTP status sets.
func (p Policy) Same(other Policy) bool {
	return p.Failure == other.Failure &&
		p.Recovery == other.Recovery &&
		p.DegradedTraffic == other.DegradedTraffic &&
		maps.Equal(p.ExcludedHTTPStatusCodes, other.ExcludedHTTPStatusCodes)
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

func validateExcludedHTTPStatus(status int) error {
	if status < 400 || status > 599 {
		return fmt.Errorf("softcircuitbreaker: excluded HTTP status %d must be in [400, 599]", status)
	}
	return nil
}
