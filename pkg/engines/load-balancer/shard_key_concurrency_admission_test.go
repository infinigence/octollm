package loadbalancer

import (
	"errors"
	"fmt"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
)

func newHookTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

func hookConcurrencyKeyFn(_ *octollm.Request, backendName string) string {
	return "concurrency_rate:service:gpt-4:" + backendName + ":tier_0"
}

func seedHookConcurrency(t *testing.T, rd *redis.Client, backendName string, n int) {
	t.Helper()
	if n <= 0 {
		return
	}
	members := make([]redis.Z, n)
	for i := range members {
		members[i] = redis.Z{Score: float64(i), Member: fmt.Sprintf("m%d", i)}
	}
	require.NoError(t, rd.ZAdd(t.Context(), hookConcurrencyKeyFn(nil, backendName), members...).Err())
}

func TestShardKeyConcurrency_Admission_SkipDoesNotConsumeRetry(t *testing.T) {
	engineA := &stubEngine{err: errors.New("should not be called")}
	engineB := &stubEngine{resp: &octollm.Response{StatusCode: 200}}
	hook := &denyNamesAdmission{denied: map[string]struct{}{"A": {}}}
	rd := newHookTestRedis(t)
	seedHookConcurrency(t, rd, "B", 10)
	lb, err := NewShardKeyConcurrency(
		[]BackendItem{
			{Name: "A", Weight: 1, Engine: engineA},
			{Name: "B", Weight: 1, Engine: engineB},
		},
		time.Second, 1, nil, rd, hookConcurrencyKeyFn, hook,
	)
	require.NoError(t, err)

	resp, err := lb.Process(testhelper.CreateTestRequest())
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 0, engineA.callCount)
	assert.Equal(t, 1, engineB.callCount)
	assert.Equal(t, []string{"B"}, hook.doneNames)
}

func TestShardKeyConcurrency_Admission_AllDeniedReturnsNoBackendAvailable(t *testing.T) {
	engineA := &stubEngine{}
	engineB := &stubEngine{}
	hook := &denyNamesAdmission{denied: map[string]struct{}{"A": {}, "B": {}}}
	lb, err := NewShardKeyConcurrency(
		[]BackendItem{
			{Name: "A", Weight: 1, Engine: engineA},
			{Name: "B", Weight: 1, Engine: engineB},
		},
		time.Second, 3, nil, newHookTestRedis(t), hookConcurrencyKeyFn, hook,
	)
	require.NoError(t, err)

	resp, err := lb.Process(testhelper.CreateTestRequest())
	require.EqualError(t, err, "no backend engine available")
	assert.Nil(t, resp)
	assert.Equal(t, 0, engineA.callCount)
	assert.Equal(t, 0, engineB.callCount)
	assert.Empty(t, hook.doneNames)
}

func TestShardKeyConcurrency_Admission_PreservesRealErrorWhenRemainingDenied(t *testing.T) {
	upstream := errors.New("upstream boom")
	engineA := &stubEngine{resp: &octollm.Response{StatusCode: 502}, err: upstream}
	engineB := &stubEngine{}
	engineC := &stubEngine{}
	hook := &denyNamesAdmission{denied: map[string]struct{}{"B": {}, "C": {}}}
	rd := newHookTestRedis(t)
	seedHookConcurrency(t, rd, "B", 10)
	seedHookConcurrency(t, rd, "C", 10)
	lb, err := NewShardKeyConcurrency(
		[]BackendItem{
			{Name: "A", Weight: 1, Engine: engineA},
			{Name: "B", Weight: 1, Engine: engineB},
			{Name: "C", Weight: 1, Engine: engineC},
		},
		time.Second, 10, nil, rd, hookConcurrencyKeyFn, hook,
	)
	require.NoError(t, err)

	resp, err := lb.Process(testhelper.CreateTestRequest())
	require.ErrorIs(t, err, upstream)
	require.NotNil(t, resp)
	assert.Equal(t, 502, resp.StatusCode)
	assert.Equal(t, 1, engineA.callCount)
	assert.Equal(t, 0, engineB.callCount)
	assert.Equal(t, 0, engineC.callCount)
}

func TestShardKeyConcurrency_Admission_DoneCalledOncePerRealAttempt(t *testing.T) {
	engineA := &stubEngine{err: errors.New("fail a")}
	engineB := &stubEngine{resp: &octollm.Response{StatusCode: 200}}
	hook := &denyNamesAdmission{}
	rd := newHookTestRedis(t)
	seedHookConcurrency(t, rd, "B", 10)
	lb, err := NewShardKeyConcurrency(
		[]BackendItem{
			{Name: "A", Weight: 1, Engine: engineA},
			{Name: "B", Weight: 1, Engine: engineB},
		},
		time.Second, 5, nil, rd, hookConcurrencyKeyFn, hook,
	)
	require.NoError(t, err)

	_, err = lb.Process(testhelper.CreateTestRequest())
	require.NoError(t, err)
	assert.Equal(t, 1, engineA.callCount)
	assert.Equal(t, 1, engineB.callCount)
	assert.ElementsMatch(t, []string{"A", "B"}, hook.doneNames)
}

func TestShardKeyConcurrency_Admission_PrioritizedDenyFallsBack(t *testing.T) {
	engineA := &stubEngine{}
	engineB := &stubEngine{resp: &octollm.Response{StatusCode: 200}}
	hook := &denyNamesAdmission{denied: map[string]struct{}{"A": {}}}
	provider := affinityProviderFunc(func(*octollm.Request) ([]*PrioritizedBackend, AffinityCommitFunc, error) {
		return []*PrioritizedBackend{{Name: "A", StrongCacheHit: true}}, nil, nil
	})
	lb, err := NewShardKeyConcurrency(
		[]BackendItem{
			{Name: "A", Weight: 1, Engine: engineA},
			{Name: "B", Weight: 1, Engine: engineB},
		},
		time.Second, 3, provider, newHookTestRedis(t), hookConcurrencyKeyFn, hook,
	)
	require.NoError(t, err)

	resp, err := lb.Process(testhelper.CreateTestRequest())
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 0, engineA.callCount)
	assert.Equal(t, 1, engineB.callCount)
	assert.Equal(t, []string{"B"}, hook.doneNames)
}

func TestShardKeyConcurrency_Admission_RealFailureStillConsumesRetry(t *testing.T) {
	engineA := &stubEngine{err: errors.New("fail a")}
	engineB := &stubEngine{resp: &octollm.Response{StatusCode: 200}}
	rd := newHookTestRedis(t)
	seedHookConcurrency(t, rd, "A", 0)
	seedHookConcurrency(t, rd, "B", 10)
	lb, err := NewShardKeyConcurrency(
		[]BackendItem{
			{Name: "A", Weight: 1, Engine: engineA},
			{Name: "B", Weight: 1, Engine: engineB},
		},
		time.Second, 2, nil, rd, hookConcurrencyKeyFn, nil,
	)
	require.NoError(t, err)

	resp, err := lb.Process(testhelper.CreateTestRequest())
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 1, engineA.callCount)
	assert.Equal(t, 1, engineB.callCount)
}

func TestShardKeyConcurrency_Admission_NilAdmissionKeepsCurrentBehavior(t *testing.T) {
	engineA := &stubEngine{resp: &octollm.Response{StatusCode: 200}}
	lb, err := NewShardKeyConcurrency(
		[]BackendItem{{Name: "A", Weight: 1, Engine: engineA}},
		time.Second, 1, nil, newHookTestRedis(t), hookConcurrencyKeyFn, nil,
	)
	require.NoError(t, err)

	resp, err := lb.Process(testhelper.CreateTestRequest())
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 1, engineA.callCount)
}
