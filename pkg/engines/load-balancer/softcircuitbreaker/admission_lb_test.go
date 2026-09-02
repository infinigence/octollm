package softcircuitbreaker

import (
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loadbalancer "github.com/infinigence/octollm/pkg/engines/load-balancer"
	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
)

type prioritizeBackends []string

func (p prioritizeBackends) Resolve(*octollm.Request) ([]*loadbalancer.PrioritizedBackend, loadbalancer.AffinityCommitFunc, error) {
	out := make([]*loadbalancer.PrioritizedBackend, 0, len(p))
	for _, name := range p {
		out = append(out, &loadbalancer.PrioritizedBackend{Name: name, StrongCacheHit: true})
	}
	return out, nil, nil
}

func seedHookLBConcurrency(t *testing.T, rd *redis.Client, backendName string, n int) {
	t.Helper()
	if n <= 0 {
		return
	}
	members := make([]redis.Z, n)
	for i := range members {
		members[i] = redis.Z{Score: float64(i), Member: fmt.Sprintf("m%d", i)}
	}
	require.NoError(t, rd.ZAdd(t.Context(), hookConcurrencyKey(nil, backendName), members...).Err())
}

type countingEngine struct {
	calls atomic.Int32
	resp  *octollm.Response
	err   error
}

func (e *countingEngine) Process(*octollm.Request) (*octollm.Response, error) {
	e.calls.Add(1)
	if e.resp == nil && e.err == nil {
		return &octollm.Response{StatusCode: 200}, nil
	}
	return e.resp, e.err
}

func denyAfterOneFailurePolicy(t *testing.T) Policy {
	t.Helper()
	policy, err := NewPolicy(
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		RateRule{Window: time.Minute, MinRequests: 100, Rate: 1},
		TrafficRule{Window: time.Minute, MaxRequests: 1},
	)
	require.NoError(t, err)
	return policy
}

func exhaustBackend(t *testing.T, hook *Admission, backendName string) {
	t.Helper()
	req := testhelper.CreateTestRequest()
	done, allowed := hook.BeforeAttempt(req, backendName)
	require.True(t, allowed)
	done(false)

	req = testhelper.CreateTestRequest()
	done, allowed = hook.BeforeAttempt(req, backendName)
	require.True(t, allowed)
	done(true)
}

func TestAdmission_WRR_DeniedBackendIsNotInvoked(t *testing.T) {
	reg := mustRegistry(t, denyAfterOneFailurePolicy(t))
	hook := NewAdmission(reg, "m", nil)
	exhaustBackend(t, hook, "A")

	engineA := &countingEngine{err: errors.New("should not be called")}
	engineB := &countingEngine{resp: &octollm.Response{StatusCode: 200}}
	lb, err := loadbalancer.NewShardKeyWeightedRoundRobin(
		[]loadbalancer.BackendItem{
			{Name: "A", Weight: 1, Engine: engineA},
			{Name: "B", Weight: 1, Engine: engineB},
		},
		time.Second, 3, prioritizeBackends{"A"}, hook,
	)
	require.NoError(t, err)

	resp, err := lb.Process(testhelper.CreateTestRequest())
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, int32(0), engineA.calls.Load())
	assert.Equal(t, int32(1), engineB.calls.Load())
}

func TestAdmission_WRR_AllDeniedReturnsNoBackendAvailable(t *testing.T) {
	reg := mustRegistry(t, denyAfterOneFailurePolicy(t))
	hook := NewAdmission(reg, "m", nil)
	exhaustBackend(t, hook, "A")
	exhaustBackend(t, hook, "B")

	engineA := &countingEngine{}
	engineB := &countingEngine{}
	lb, err := loadbalancer.NewShardKeyWeightedRoundRobin(
		[]loadbalancer.BackendItem{
			{Name: "A", Weight: 1, Engine: engineA},
			{Name: "B", Weight: 1, Engine: engineB},
		},
		time.Second, 3, nil, hook,
	)
	require.NoError(t, err)

	resp, err := lb.Process(testhelper.CreateTestRequest())
	require.EqualError(t, err, "no backend engine available")
	assert.Nil(t, resp)
	assert.Equal(t, int32(0), engineA.calls.Load())
	assert.Equal(t, int32(0), engineB.calls.Load())
}

func TestAdmission_WRR_PreservesRealErrorWhenRemainingDenied(t *testing.T) {
	reg := mustRegistry(t, denyAfterOneFailurePolicy(t))
	hook := NewAdmission(reg, "m", nil)
	exhaustBackend(t, hook, "B")

	upstream := errors.New("upstream boom")
	engineA := &countingEngine{resp: &octollm.Response{StatusCode: 502}, err: upstream}
	engineB := &countingEngine{}
	lb, err := loadbalancer.NewShardKeyWeightedRoundRobin(
		[]loadbalancer.BackendItem{
			{Name: "A", Weight: 1, Engine: engineA},
			{Name: "B", Weight: 1, Engine: engineB},
		},
		time.Second, 5, nil, hook,
	)
	require.NoError(t, err)

	resp, err := lb.Process(testhelper.CreateTestRequest())
	require.ErrorIs(t, err, upstream)
	require.NotNil(t, resp)
	assert.Equal(t, 502, resp.StatusCode)
	assert.Equal(t, int32(1), engineA.calls.Load())
	assert.Equal(t, int32(0), engineB.calls.Load())
}

func TestAdmission_Concurrency_DeniedBackendIsNotInvoked(t *testing.T) {
	reg := mustRegistry(t, denyAfterOneFailurePolicy(t))
	hook := NewAdmission(reg, "m", nil)
	exhaustBackend(t, hook, "A")

	engineA := &countingEngine{err: errors.New("should not be called")}
	engineB := &countingEngine{resp: &octollm.Response{StatusCode: 200}}
	rd := newHookLBRedis(t)
	seedHookLBConcurrency(t, rd, "B", 10)
	lb, err := loadbalancer.NewShardKeyConcurrency(
		[]loadbalancer.BackendItem{
			{Name: "A", Weight: 1, Engine: engineA},
			{Name: "B", Weight: 1, Engine: engineB},
		},
		time.Second, 3, nil, rd, hookConcurrencyKey, hook,
	)
	require.NoError(t, err)

	resp, err := lb.Process(testhelper.CreateTestRequest())
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, int32(0), engineA.calls.Load())
	assert.Equal(t, int32(1), engineB.calls.Load())
}

func TestAdmission_Concurrency_AllDeniedReturnsNoBackendAvailable(t *testing.T) {
	reg := mustRegistry(t, denyAfterOneFailurePolicy(t))
	hook := NewAdmission(reg, "m", nil)
	exhaustBackend(t, hook, "A")
	exhaustBackend(t, hook, "B")

	engineA := &countingEngine{}
	engineB := &countingEngine{}
	lb, err := loadbalancer.NewShardKeyConcurrency(
		[]loadbalancer.BackendItem{
			{Name: "A", Weight: 1, Engine: engineA},
			{Name: "B", Weight: 1, Engine: engineB},
		},
		time.Second, 3, nil, newHookLBRedis(t), hookConcurrencyKey, hook,
	)
	require.NoError(t, err)

	resp, err := lb.Process(testhelper.CreateTestRequest())
	require.EqualError(t, err, "no backend engine available")
	assert.Nil(t, resp)
	assert.Equal(t, int32(0), engineA.calls.Load())
	assert.Equal(t, int32(0), engineB.calls.Load())
}

func TestAdmission_WRR_ExcludedHTTPStatusIsNotRecorded(t *testing.T) {
	policy, err := NewPolicy(
		RateRule{Window: time.Second, MinRequests: 1, Rate: 1},
		RateRule{Window: time.Minute, MinRequests: 100, Rate: 1},
		TrafficRule{Window: time.Minute, MaxRequests: 1},
	)
	require.NoError(t, err)
	reg := mustRegistry(t, policy)
	hook := NewAdmission(reg, "m", &policy)

	engine := &countingEngine{err: &errutils.UpstreamRespError{StatusCode: http.StatusTooManyRequests}}
	lb, err := loadbalancer.NewShardKeyWeightedRoundRobin(
		[]loadbalancer.BackendItem{{Name: "A", Weight: 1, Engine: engine}},
		time.Second, 3, nil, hook,
	)
	require.NoError(t, err)

	_, err = lb.Process(testhelper.CreateTestRequest())
	require.Error(t, err)

	entry := mustGetOrCreate(t, reg, BreakerKey{ModelName: "m", BackendName: "A"}, nil)
	assert.Equal(t, ModeNormal, entry.Mode())
	assert.Empty(t, entry.normalSamples)
}

func newHookLBRedis(t *testing.T) *redis.Client {
	t.Helper()
	return redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
}

func hookConcurrencyKey(_ *octollm.Request, backendName string) string {
	return "softcb:concurrency:" + backendName
}
