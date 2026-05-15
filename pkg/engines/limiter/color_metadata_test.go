package limiter

import (
	"testing"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/stretchr/testify/assert"
)

func assertMarkerAllow(t *testing.T, req *octollm.Request, wantPriority int) {
	t.Helper()
	p, ok := GetMarkerPriority(req)
	assert.True(t, ok)
	assert.Equal(t, wantPriority, p)
	a, ok := GetMarkerAction(req)
	assert.True(t, ok)
	assert.Equal(t, ColorActionAllow, a)
}

func assertMarkerDeny(t *testing.T, req *octollm.Request, wantPriority int) {
	t.Helper()
	p, ok := GetMarkerPriority(req)
	assert.True(t, ok)
	assert.Equal(t, wantPriority, p)
	a, ok := GetMarkerAction(req)
	assert.True(t, ok)
	assert.Equal(t, ColorActionDeny, a)
}

func assertLimiterAllow(t *testing.T, req *octollm.Request, wantPriority int) {
	t.Helper()
	p, ok := GetLimiterPriority(req)
	assert.True(t, ok)
	assert.Equal(t, wantPriority, p)
	a, ok := GetLimiterAction(req)
	assert.True(t, ok)
	assert.Equal(t, ColorActionAllow, a)
}

func assertLimiterDeny(t *testing.T, req *octollm.Request, wantPriority int) {
	t.Helper()
	p, ok := GetLimiterPriority(req)
	assert.True(t, ok)
	assert.Equal(t, wantPriority, p)
	a, ok := GetLimiterAction(req)
	assert.True(t, ok)
	assert.Equal(t, ColorActionDeny, a)
}
