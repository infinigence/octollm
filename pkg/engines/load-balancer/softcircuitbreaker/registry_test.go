package softcircuitbreaker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustRegistry(t *testing.T, policy Policy) *Registry {
	t.Helper()
	reg, err := NewRegistry(policy)
	require.NoError(t, err)
	return reg
}

func mustGetOrCreate(t *testing.T, reg *Registry, key BreakerKey, policy *Policy) *Entry {
	t.Helper()
	entry, err := reg.GetOrCreate(key, policy)
	require.NoError(t, err)
	require.NotNil(t, entry)
	return entry
}

func TestRegistry_GetOrCreateSameKey(t *testing.T) {
	reg := mustRegistry(t, testPolicy())
	key := BreakerKey{ModelName: "m", BackendName: "a"}
	p := testPolicy()
	first := mustGetOrCreate(t, reg, key, &p)
	second := mustGetOrCreate(t, reg, key, &p)
	other := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "b"}, &p)
	assert.Same(t, first, second)
	assert.NotSame(t, first, other)
}

func TestRegistry_NilPolicyUsesRegistryPolicy(t *testing.T) {
	p := testPolicy()
	p.DegradedTraffic.MaxRequests = 7
	reg := mustRegistry(t, p)
	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil)
	assert.Equal(t, 7, entry.Policy().DegradedTraffic.MaxRequests)
	assert.Same(t, entry, mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, nil))
}

func TestRegistry_NilPolicyRebuildsCustomEntryWithRegistryPolicy(t *testing.T) {
	defaultPolicy := testPolicy()
	defaultPolicy.DegradedTraffic.MaxRequests = 7
	customPolicy := testPolicy()
	customPolicy.Failure.MinRequests = 1
	customPolicy.Failure.Rate = 1
	customPolicy.DegradedTraffic.MaxRequests = 3
	reg := mustRegistry(t, defaultPolicy)
	key := BreakerKey{ModelName: "m", BackendName: "a"}

	entry := mustGetOrCreate(t, reg, key, &customPolicy)
	entry.complete(context.Background(), 0, OutcomeTargetFailure, time.Unix(0, 0))
	require.Equal(t, ModeDegraded, entry.Mode())

	rebuilt := mustGetOrCreate(t, reg, key, nil)
	assert.NotSame(t, entry, rebuilt)
	assert.Equal(t, defaultPolicy, rebuilt.Policy())
	assert.Equal(t, ModeNormal, rebuilt.Mode())
}

func TestRegistry_NilPolicyReusesEntryWithRegistryPolicy(t *testing.T) {
	defaultPolicy := testPolicy()
	reg := mustRegistry(t, defaultPolicy)
	key := BreakerKey{ModelName: "m", BackendName: "a"}

	entry := mustGetOrCreate(t, reg, key, &defaultPolicy)
	assert.Same(t, entry, mustGetOrCreate(t, reg, key, nil))
}

func TestRegistry_NilIsDisabled(t *testing.T) {
	var reg *Registry
	p := testPolicy()
	entry, err := reg.GetOrCreate(BreakerKey{ModelName: "m", BackendName: "a"}, &p)
	require.NoError(t, err)
	assert.Nil(t, entry)
}

func TestRegistry_ConcurrentGetOrCreate(t *testing.T) {
	reg := mustRegistry(t, testPolicy())
	key := BreakerKey{ModelName: "m", BackendName: "a"}
	policy := testPolicy()

	var wg sync.WaitGroup
	got := make([]*Entry, 32)
	errs := make([]error, 32)
	for i := range got {
		wg.Go(func() {
			got[i], errs[i] = reg.GetOrCreate(key, &policy)
		})
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err)
		assert.Same(t, got[0], got[i])
	}
}

func TestRegistry_PolicyIsPerKey(t *testing.T) {
	reg := mustRegistry(t, testPolicy())
	strict, err := NewPolicy(
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		TrafficRule{Window: time.Second, MaxRequests: 1},
	)
	require.NoError(t, err)
	lenient, err := NewPolicy(
		RateRule{Window: time.Minute, MinRequests: 20, Rate: 0.9},
		RateRule{Window: time.Minute, MinRequests: 10, Rate: 0.5},
		TrafficRule{Window: time.Second, MaxRequests: 5},
	)
	require.NoError(t, err)

	a := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "a"}, &strict)
	b := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "b"}, &lenient)

	assert.Equal(t, 1, a.Policy().DegradedTraffic.MaxRequests)
	assert.Equal(t, 5, b.Policy().DegradedTraffic.MaxRequests)
}

func TestRegistry_GetOrCreateRebuildsWhenPolicyDiffers(t *testing.T) {
	reg := mustRegistry(t, testPolicy())
	key := BreakerKey{ModelName: "m", BackendName: "a"}
	first, err := NewPolicy(
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		RateRule{Window: time.Second, MinRequests: 100, Rate: 1},
		TrafficRule{Window: time.Minute, MaxRequests: 1},
	)
	require.NoError(t, err)
	second, err := NewPolicy(
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		RateRule{Window: time.Second, MinRequests: 100, Rate: 1},
		TrafficRule{Window: time.Minute, MaxRequests: 100},
	)
	require.NoError(t, err)

	entry := mustGetOrCreate(t, reg, key, &first)
	now := time.Unix(0, 0)
	entry.complete(context.Background(), 0, OutcomeTargetFailure, now)
	require.Equal(t, ModeDegraded, entry.Mode())

	rebuilt := mustGetOrCreate(t, reg, key, &second)
	assert.NotSame(t, entry, rebuilt)
	assert.Equal(t, ModeNormal, rebuilt.Mode())
	assert.Equal(t, 100, rebuilt.Policy().DegradedTraffic.MaxRequests)
}

func TestRegistry_GetOrCreateSamePolicyReturnsCache(t *testing.T) {
	reg := mustRegistry(t, testPolicy())
	key := BreakerKey{ModelName: "m", BackendName: "a"}
	p := testPolicy()
	first := mustGetOrCreate(t, reg, key, &p)
	copy := p
	again := mustGetOrCreate(t, reg, key, &copy)
	assert.Same(t, first, again)
}

func TestNewRegistry_RejectsInvalidPolicy(t *testing.T) {
	_, err := NewRegistry(Policy{})
	require.Error(t, err)
}

func TestRegistry_GetOrCreateRejectsInvalidPolicyOnCreate(t *testing.T) {
	reg := mustRegistry(t, testPolicy())
	_, err := reg.GetOrCreate(BreakerKey{ModelName: "m", BackendName: "a"}, &Policy{})
	require.Error(t, err)
}

func TestRegistry_InvalidRebuildKeepsOldEntry(t *testing.T) {
	p, err := NewPolicy(
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		RateRule{Window: time.Second, MinRequests: 100, Rate: 1},
		TrafficRule{Window: time.Minute, MaxRequests: 1},
	)
	require.NoError(t, err)
	reg := mustRegistry(t, p)
	key := BreakerKey{ModelName: "m", BackendName: "a"}
	entry := mustGetOrCreate(t, reg, key, &p)
	entry.complete(context.Background(), 0, OutcomeTargetFailure, time.Unix(0, 0))
	require.Equal(t, ModeDegraded, entry.Mode())

	again, err := reg.GetOrCreate(key, &Policy{})
	require.NoError(t, err)
	assert.Same(t, entry, again)
	assert.Equal(t, ModeDegraded, again.Mode())
}

func TestRegistry_CallerMutationDoesNotAffectEntry(t *testing.T) {
	p := testPolicy()
	p.DegradedTraffic.MaxRequests = 7
	entry := mustGetOrCreate(t, mustRegistry(t, p), BreakerKey{ModelName: "m", BackendName: "a"}, &p)
	assert.Equal(t, 7, entry.Policy().DegradedTraffic.MaxRequests)

	p.DegradedTraffic.MaxRequests = 99
	assert.Equal(t, 7, entry.Policy().DegradedTraffic.MaxRequests)

	copied := entry.Policy()
	copied.DegradedTraffic.MaxRequests = 1
	assert.Equal(t, 7, entry.Policy().DegradedTraffic.MaxRequests)
}
