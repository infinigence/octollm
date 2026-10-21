package limiter

import (
	"errors"
	"math"
	"testing"

	"github.com/infinigence/octollm/pkg/engines/load-balancer/common"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClonePLoadLimits(t *testing.T) {
	in := PLoadLimits{AvailableTotalMax: []float64{200, 100, 90}, HeadroomRatio: 1.2}
	got, err := clonePLoadLimits(in)
	require.NoError(t, err)

	in.AvailableTotalMax[0] = 1
	in.HeadroomRatio = 9
	assert.Equal(t, []float64{200, 100, 90}, got.AvailableTotalMax)
	assert.Equal(t, 1.2, got.HeadroomRatio)
}

func TestValidatePLoadLimits(t *testing.T) {
	ok := PLoadLimits{AvailableTotalMax: []float64{200, 200, 90}, HeadroomRatio: 1.2}
	_, err := clonePLoadLimits(ok)
	require.NoError(t, err)

	cases := []PLoadLimits{
		{},
		{AvailableTotalMax: []float64{}},
		{AvailableTotalMax: []float64{200, 100}, HeadroomRatio: math.NaN()},
		{AvailableTotalMax: []float64{200, math.Inf(1)}},
		{AvailableTotalMax: []float64{200, -1}},
		{AvailableTotalMax: []float64{90, 100}},
		{HeadroomRatio: -0.1, AvailableTotalMax: []float64{200}},
	}
	for _, in := range cases {
		_, err := clonePLoadLimits(in)
		require.Error(t, err)
	}
}

func TestEffectivePLoadLimitStrongBoundaries(t *testing.T) {
	bands := []struct {
		base    float64
		atLimit float64
		below   float64
	}{
		{base: 200, atLimit: 441, below: 440},
		{base: 100, atLimit: 221, below: 220},
		{base: 90, atLimit: 199, below: 198},
	}
	for _, band := range bands {
		limit := effectivePLoadLimit(band.base, 1.2, common.CacheHitStrong)
		assert.Equal(t, band.atLimit, limit)
		assert.False(t, band.atLimit > limit)
		assert.False(t, band.below > limit)
		assert.True(t, (band.atLimit+1) > limit)
	}
}

func TestEffectivePLoadLimitHeadroomOnlyForStrong(t *testing.T) {
	const score = 250.0
	miss := effectivePLoadLimit(200, 1.2, common.CacheMiss)
	assert.True(t, score > miss)

	weak := effectivePLoadLimit(200, 1.2, common.CacheHitWeak)
	assert.True(t, score > weak)

	strong := effectivePLoadLimit(200, 1.2, common.CacheHitStrong)
	assert.False(t, score > strong)

	noHeadroom := effectivePLoadLimit(200, 0, common.CacheHitStrong)
	assert.Equal(t, 200.0, noHeadroom)
}

func TestEffectivePLoadLimitKeepsFraction(t *testing.T) {
	limit := effectivePLoadLimit(200.5, 1.2, common.CacheHitStrong)
	assert.Equal(t, 442.0, limit)
	assert.False(t, limit > limit)
	assert.True(t, 442.1 > limit)
}

func TestNewPLoadColorLimiterEngineValidation(t *testing.T) {
	next := &nextStubEngine{}
	currentScoreFunc := func(*octollm.Request) (float64, error) { return 0, nil }
	_, err := NewPLoadColorLimiterEngine(PLoadLimits{}, currentScoreFunc, "ns", next)
	require.Error(t, err)
	_, err = NewPLoadColorLimiterEngine(PLoadLimits{AvailableTotalMax: []float64{200, 100, 90}}, nil, "ns", next)
	require.Error(t, err)
	_, err = NewPLoadColorLimiterEngine(PLoadLimits{AvailableTotalMax: []float64{200, 100, 90}}, currentScoreFunc, "ns", nil)
	require.Error(t, err)
}

func TestPLoadColorLimiterEngineBands(t *testing.T) {
	ns := "ns"
	cases := []struct {
		name     string
		priority *int
		kind     common.CacheHitKind
		score    float64
		allow    bool
	}{
		{name: "p2 miss at base", priority: intPtr(2), kind: common.CacheMiss, score: 200, allow: true},
		{name: "p2 miss above", priority: intPtr(2), kind: common.CacheMiss, score: 201, allow: false},
		{name: "p2 miss below", priority: intPtr(2), kind: common.CacheMiss, score: 199, allow: true},
		{name: "p1 miss at base", priority: intPtr(1), kind: common.CacheMiss, score: 100, allow: true},
		{name: "p1 miss above", priority: intPtr(1), kind: common.CacheMiss, score: 101, allow: false},
		{name: "p0 miss at base", priority: intPtr(0), kind: common.CacheMiss, score: 90, allow: true},
		{name: "p0 miss above", priority: intPtr(0), kind: common.CacheMiss, score: 91, allow: false},
		{name: "p0 miss below", priority: intPtr(0), kind: common.CacheMiss, score: 89, allow: true},
		{name: "p2 strong at ceil", priority: intPtr(2), kind: common.CacheHitStrong, score: 441, allow: true},
		{name: "p2 strong above ceil", priority: intPtr(2), kind: common.CacheHitStrong, score: 442, allow: false},
		{name: "p2 strong below ceil", priority: intPtr(2), kind: common.CacheHitStrong, score: 440, allow: true},
		{name: "p1 strong at ceil", priority: intPtr(1), kind: common.CacheHitStrong, score: 221, allow: true},
		{name: "p1 strong above ceil", priority: intPtr(1), kind: common.CacheHitStrong, score: 222, allow: false},
		{name: "p0 strong at ceil", priority: intPtr(0), kind: common.CacheHitStrong, score: 199, allow: true},
		{name: "p0 strong above ceil", priority: intPtr(0), kind: common.CacheHitStrong, score: 200, allow: false},
		{name: "missing priority uses p0", kind: common.CacheMiss, score: 91, allow: false},
		{name: "negative priority uses p0", priority: intPtr(-1), kind: common.CacheMiss, score: 91, allow: false},
		{name: "priority above range uses p2", priority: intPtr(9), kind: common.CacheMiss, score: 200, allow: true},
		{name: "weak uses base", priority: intPtr(2), kind: common.CacheHitWeak, score: 200, allow: true},
		{name: "weak above base", priority: intPtr(2), kind: common.CacheHitWeak, score: 201, allow: false},
		{name: "invalid kind uses miss", priority: intPtr(2), kind: common.CacheHitKind("bogus"), score: 200, allow: true},
		{name: "strong allows score above base", priority: intPtr(2), kind: common.CacheHitStrong, score: 250, allow: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next := &nextStubEngine{resp: &octollm.Response{StatusCode: 200}}
			e, err := NewPLoadColorLimiterEngine(samplePLoadLimits(), fixedScore(tc.score), ns, next)
			require.NoError(t, err)
			req := withHitKind(newConcurrencyColorLimiterTestRequest(t, ns, tc.priority), tc.kind)
			_, err = e.Process(req)
			if tc.allow {
				require.NoError(t, err)
				assert.Equal(t, 1, next.callCount)
				return
			}
			want := ErrPLoadRateLimitReached
			if tc.kind == common.CacheHitStrong {
				want = ErrPLoadStrongCacheRateLimitReached
			}
			require.ErrorIs(t, err, want)
			assert.ErrorIs(t, err, ErrRateLimitReached)
			assert.Equal(t, 0, next.callCount)
		})
	}
}

func TestPLoadColorLimiterEngineScoreErrorSkipsNext(t *testing.T) {
	ns := "ns"
	scoreErr := errors.New("snapshot missing")
	next := &nextStubEngine{}
	e, err := NewPLoadColorLimiterEngine(samplePLoadLimits(), func(*octollm.Request) (float64, error) {
		return 0, scoreErr
	}, ns, next)
	require.NoError(t, err)

	_, err = e.Process(newConcurrencyColorLimiterTestRequest(t, ns, intPtr(2)))
	require.ErrorIs(t, err, scoreErr)
	assert.NotErrorIs(t, err, ErrRateLimitReached)
	assert.Equal(t, 0, next.callCount)
}

func TestPLoadColorLimiterEngineNegativeScoreAllowed(t *testing.T) {
	ns := "ns"
	next := &nextStubEngine{resp: &octollm.Response{StatusCode: 200}}
	e, err := NewPLoadColorLimiterEngine(samplePLoadLimits(), fixedScore(-1), ns, next)
	require.NoError(t, err)
	_, err = e.Process(newConcurrencyColorLimiterTestRequest(t, ns, intPtr(2)))
	require.NoError(t, err)
	assert.Equal(t, 1, next.callCount)
}

func samplePLoadLimits() PLoadLimits {
	return PLoadLimits{AvailableTotalMax: []float64{200, 100, 90}, HeadroomRatio: 1.2}
}

func fixedScore(score float64) PLoadScoreFunc {
	return func(*octollm.Request) (float64, error) { return score, nil }
}

func intPtr(v int) *int { return &v }
