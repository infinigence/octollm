package limiter

import (
	"github.com/infinigence/octollm/pkg/octollm"
)

// Color actions stored on request metadata.
const (
	// ColorActionAllow is 通过 (request allowed at the recorded priority).
	ColorActionAllow = "allow"
	// ColorActionDeny is 拦截 (rate-limited at the recorded priority).
	ColorActionDeny = "deny"
)

type markerPriorityMetadataKey struct{}
type markerActionMetadataKey struct{}
type limiterPriorityMetadataKey struct{}
type limiterActionMetadataKey struct{}

// GetMarkerPriority returns the marker-assigned priority band (0 = lowest).
// The second value is false when unset (nil request, never passed a marker, or non–rate-limit error).
func GetMarkerPriority(req *octollm.Request) (int, bool) {
	return getColorPriority(req, markerPriorityMetadataKey{})
}

// GetMarkerAction returns ColorActionAllow or ColorActionDeny.
// The second value is false when unset.
func GetMarkerAction(req *octollm.Request) (string, bool) {
	return getColorAction(req, markerActionMetadataKey{})
}

// GetLimiterPriority returns the limiter-evaluated priority band (from context / marker).
// The second value is false when unset.
func GetLimiterPriority(req *octollm.Request) (int, bool) {
	return getColorPriority(req, limiterPriorityMetadataKey{})
}

// GetLimiterAction returns ColorActionAllow or ColorActionDeny.
// The second value is false when unset.
func GetLimiterAction(req *octollm.Request) (string, bool) {
	return getColorAction(req, limiterActionMetadataKey{})
}

func getColorPriority(req *octollm.Request, key any) (int, bool) {
	if req == nil {
		return 0, false
	}
	raw, ok := req.GetMetadataValue(key)
	if !ok {
		return 0, false
	}
	p, ok := raw.(int)
	if !ok {
		return 0, false
	}
	return p, true
}

func getColorAction(req *octollm.Request, key any) (string, bool) {
	if req == nil {
		return "", false
	}
	raw, ok := req.GetMetadataValue(key)
	if !ok {
		return "", false
	}
	s, ok := raw.(string)
	if !ok || s == "" {
		return "", false
	}
	return s, true
}

func setMarkerColor(req *octollm.Request, priority int, action string) {
	if req == nil {
		return
	}
	req.SetMetadataValue(markerPriorityMetadataKey{}, priority)
	req.SetMetadataValue(markerActionMetadataKey{}, action)
}

func setLimiterColor(req *octollm.Request, priority int, action string) {
	if req == nil {
		return
	}
	req.SetMetadataValue(limiterPriorityMetadataKey{}, priority)
	req.SetMetadataValue(limiterActionMetadataKey{}, action)
}

func recordMarkerAllow(req *octollm.Request, priority int) {
	setMarkerColor(req, priority, ColorActionAllow)
}

func recordMarkerDeny(req *octollm.Request, priority int) {
	setMarkerColor(req, priority, ColorActionDeny)
}

func recordLimiterAllow(req *octollm.Request, priority int) {
	setLimiterColor(req, priority, ColorActionAllow)
}

func recordLimiterDeny(req *octollm.Request, priority int) {
	setLimiterColor(req, priority, ColorActionDeny)
}
