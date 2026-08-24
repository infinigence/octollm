package softcircuitbreaker

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPolicy() Policy {
	p, err := NewPolicy(
		RateRule{Window: time.Second, MinRequests: 2, Rate: 0.5},
		RateRule{Window: time.Second, MinRequests: 2, Rate: 0.5},
		TrafficRule{Window: time.Second, MaxRequests: 2},
		nil,
	)
	if err != nil {
		panic(err)
	}
	return p
}

func TestNewPolicy_EmptyWhitelistIsValid(t *testing.T) {
	p, err := NewPolicy(
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		TrafficRule{Window: time.Second, MaxRequests: 1},
		nil,
	)
	require.NoError(t, err)
	assert.Empty(t, p.ExcludedHTTPStatusCodes)
	assert.NotContains(t, p.ExcludedHTTPStatusCodes, http.StatusRequestEntityTooLarge)
	assert.NotContains(t, p.ExcludedHTTPStatusCodes, http.StatusTooManyRequests)
}

func TestNewPolicy_MergesDuplicateStatuses(t *testing.T) {
	p, err := NewPolicy(
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		TrafficRule{Window: time.Second, MaxRequests: 1},
		[]int{http.StatusTooManyRequests, http.StatusTooManyRequests, http.StatusBadGateway},
	)
	require.NoError(t, err)
	assert.Len(t, p.ExcludedHTTPStatusCodes, 2)
	assert.Contains(t, p.ExcludedHTTPStatusCodes, http.StatusTooManyRequests)
	assert.Contains(t, p.ExcludedHTTPStatusCodes, http.StatusBadGateway)
}

func TestNewPolicy_UsesCallerWhitelistOnly(t *testing.T) {
	p, err := NewPolicy(
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		TrafficRule{Window: time.Second, MaxRequests: 1},
		[]int{http.StatusInternalServerError, http.StatusTooManyRequests},
	)
	require.NoError(t, err)
	assert.Contains(t, p.ExcludedHTTPStatusCodes, http.StatusInternalServerError)
	assert.Contains(t, p.ExcludedHTTPStatusCodes, http.StatusTooManyRequests)
	assert.NotContains(t, p.ExcludedHTTPStatusCodes, http.StatusRequestEntityTooLarge)
}

func TestPolicy_Same(t *testing.T) {
	a := testPolicy()
	b := testPolicy()
	assert.True(t, a.Same(b))

	b.DegradedTraffic.MaxRequests = 99
	assert.False(t, a.Same(b))

	c := testPolicy()
	c.ExcludedHTTPStatusCodes = map[int]struct{}{429: {}}
	assert.False(t, a.Same(c))

	d := testPolicy()
	d.ExcludedHTTPStatusCodes = map[int]struct{}{}
	assert.True(t, a.Same(d))
}

func TestNewPolicy_RejectsInvalidRules(t *testing.T) {
	validRate := RateRule{Window: time.Second, MinRequests: 1, Rate: 1}
	validTraffic := TrafficRule{Window: time.Second, MaxRequests: 1}

	_, err := NewPolicy(RateRule{Window: 0, MinRequests: 1, Rate: 1}, validRate, validTraffic, nil)
	require.Error(t, err)

	_, err = NewPolicy(RateRule{Window: time.Second, MinRequests: 0, Rate: 1}, validRate, validTraffic, nil)
	require.Error(t, err)

	_, err = NewPolicy(RateRule{Window: time.Second, MinRequests: 1, Rate: 0}, validRate, validTraffic, nil)
	require.Error(t, err)

	_, err = NewPolicy(RateRule{Window: time.Second, MinRequests: 1, Rate: 1.1}, validRate, validTraffic, nil)
	require.Error(t, err)

	_, err = NewPolicy(validRate, validRate, TrafficRule{Window: time.Second, MaxRequests: 0}, nil)
	require.Error(t, err)

	_, err = NewPolicy(validRate, validRate, validTraffic, []int{399})
	require.Error(t, err)

	_, err = NewPolicy(validRate, validRate, validTraffic, []int{600})
	require.Error(t, err)
}
