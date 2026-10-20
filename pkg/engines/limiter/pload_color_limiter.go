package limiter

import (
	"fmt"
	"log/slog"
	"math"
	"slices"

	"github.com/infinigence/octollm/pkg/engines/load-balancer/common"
	"github.com/infinigence/octollm/pkg/octollm"
)

// PLoadScoreFunc returns the selected backend's prefill load in token equivalents.
// The returned score must use the same unit as PLoadLimits.AvailableTotalMax.
type PLoadScoreFunc func(req *octollm.Request) (float64, error)

// PLoadColorLimiterEngine admits a request when that score is below the priority band.
// A strong cache hit uses the ceiled headroom limit. Over limit returns ErrRateLimitReached.
type PLoadColorLimiterEngine struct {
	limits           PLoadLimits
	currentScoreFunc PLoadScoreFunc
	nameSpace        string
	next             octollm.Engine
}

var _ octollm.Engine = (*PLoadColorLimiterEngine)(nil)

// NewPLoadColorLimiterEngine copies and validates limits. currentScoreFunc and next are required.
func NewPLoadColorLimiterEngine(limits PLoadLimits, currentScoreFunc PLoadScoreFunc, nameSpace string, next octollm.Engine) (*PLoadColorLimiterEngine, error) {
	cloned, err := clonePLoadLimits(limits)
	if err != nil {
		return nil, err
	}
	if currentScoreFunc == nil {
		return nil, fmt.Errorf("p_load score func is required")
	}
	if next == nil {
		return nil, fmt.Errorf("next engine is required")
	}
	return &PLoadColorLimiterEngine{
		limits:           cloned,
		currentScoreFunc: currentScoreFunc,
		nameSpace:        nameSpace,
		next:             next,
	}, nil
}

// PLoadLimits is the static prefill-load admission config.
// AvailableTotalMax is ordered from high priority to low priority.
// HeadroomRatio expands the selected band only for a strong cache hit.
type PLoadLimits struct {
	AvailableTotalMax []float64
	HeadroomRatio     float64
}

func clonePLoadLimits(in PLoadLimits) (PLoadLimits, error) {
	if err := validatePLoadLimits(in); err != nil {
		return PLoadLimits{}, err
	}
	return PLoadLimits{
		AvailableTotalMax: slices.Clone(in.AvailableTotalMax),
		HeadroomRatio:     in.HeadroomRatio,
	}, nil
}

func validatePLoadLimits(in PLoadLimits) error {
	if len(in.AvailableTotalMax) == 0 {
		return fmt.Errorf("available_total_max must be non-empty")
	}
	if err := finiteNonNegative(in.HeadroomRatio, "headroom_ratio"); err != nil {
		return err
	}
	for i, v := range in.AvailableTotalMax {
		if err := finiteNonNegative(v, fmt.Sprintf("available_total_max[%d]", i)); err != nil {
			return err
		}
		if i > 0 && v > in.AvailableTotalMax[i-1] {
			return fmt.Errorf("available_total_max must be non-increasing")
		}
	}
	return nil
}

func finiteNonNegative(v float64, name string) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return fmt.Errorf("%s must be finite", name)
	}
	if v < 0 {
		return fmt.Errorf("%s must be >= 0", name)
	}
	return nil
}

// effectivePLoadLimit returns the admission cap for one band.
// CacheHitStrong with a non-zero headroom uses Ceil(base*(1+headroom)).
// Every other hit kind, and a zero headroom, returns base unchanged.
// base is already in the caller's unit; this function does not scale it.
func effectivePLoadLimit(base, headroom float64, kind common.CacheHitKind) float64 {
	if kind != common.CacheHitStrong || headroom == 0 {
		return base
	}
	return math.Ceil(base * (1 + headroom))
}

func (e *PLoadColorLimiterEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	ctx := req.Context()
	priority := 0
	if p, ok := priorityFromNamespace(ctx, e.nameSpace); ok {
		priority = p
	}
	i := qosIndex(priority, len(e.limits.AvailableTotalMax))
	kind := common.CacheHitKindFromRequest(req)
	base := e.limits.AvailableTotalMax[i]
	limit := effectivePLoadLimit(base, e.limits.HeadroomRatio, kind)

	score, err := e.currentScoreFunc(req)
	if err != nil {
		slog.WarnContext(ctx, fmt.Sprintf("p_load score failed: %v", err))
		return nil, err
	}
	if score > limit {
		slog.WarnContext(ctx, fmt.Sprintf("p_load limit reached, priority %d, hit %s, score %v, limit %v", priority, kind, score, limit))
		return nil, ErrRateLimitReached
	}
	slog.InfoContext(ctx, fmt.Sprintf("p_load allowed, priority %d, hit %s, score %v, limit %v", priority, kind, score, limit))
	return e.next.Process(req)
}
