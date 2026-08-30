package softcircuitbreaker

import (
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
	)
	if err != nil {
		panic(err)
	}
	return p
}

func TestNewPolicy_Valid(t *testing.T) {
	p, err := NewPolicy(
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		TrafficRule{Window: time.Second, MaxRequests: 1},
	)
	require.NoError(t, err)
	assert.Equal(t, time.Second, p.Failure.Window)
	assert.Equal(t, 1, p.DegradedTraffic.MaxRequests)
}

func TestPolicy_Equal(t *testing.T) {
	a := testPolicy()
	b := testPolicy()
	assert.True(t, a == b)

	b.DegradedTraffic.MaxRequests = 99
	assert.False(t, a == b)
}

func TestNewPolicy_RejectsInvalidRules(t *testing.T) {
	validRate := RateRule{Window: time.Second, MinRequests: 1, Rate: 1}
	validTraffic := TrafficRule{Window: time.Second, MaxRequests: 1}

	_, err := NewPolicy(RateRule{Window: 0, MinRequests: 1, Rate: 1}, validRate, validTraffic)
	require.Error(t, err)

	_, err = NewPolicy(RateRule{Window: time.Second, MinRequests: 0, Rate: 1}, validRate, validTraffic)
	require.Error(t, err)

	_, err = NewPolicy(RateRule{Window: time.Second, MinRequests: 1, Rate: 0}, validRate, validTraffic)
	require.Error(t, err)

	_, err = NewPolicy(RateRule{Window: time.Second, MinRequests: 1, Rate: 1.1}, validRate, validTraffic)
	require.Error(t, err)

	_, err = NewPolicy(validRate, validRate, TrafficRule{Window: time.Second, MaxRequests: 0})
	require.Error(t, err)
}
