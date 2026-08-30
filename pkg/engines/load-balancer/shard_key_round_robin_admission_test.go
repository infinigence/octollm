package loadbalancer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
)

type denyNamesAdmission struct {
	denied    map[string]struct{}
	mu        sync.Mutex
	attempted []string
	doneNames []string
}

func (h *denyNamesAdmission) BeforeAttempt(req *octollm.Request, backendName string) (AttemptDoneFunc, bool) {
	h.mu.Lock()
	h.attempted = append(h.attempted, backendName)
	h.mu.Unlock()
	if _, ok := h.denied[backendName]; ok {
		return nil, false
	}
	return func(ok bool) {
		h.mu.Lock()
		h.doneNames = append(h.doneNames, backendName)
		h.mu.Unlock()
	}, true
}

func snapshotCurrentWeights(lb *ShardKeyWeightedRoundRobin) []int {
	out := make([]int, len(lb.backends))
	for i, b := range lb.backends {
		out[i] = b.currentWeight
	}
	return out
}

func TestShardKeyWRR_Admission_SkipDoesNotConsumeRetry(t *testing.T) {
	engineA := &stubEngine{err: errors.New("should not be called")}
	engineB := &stubEngine{resp: &octollm.Response{StatusCode: 200}}
	hook := &denyNamesAdmission{denied: map[string]struct{}{"A": {}}}
	lb, err := NewShardKeyWeightedRoundRobin(
		[]BackendItem{
			{Name: "A", Weight: 10, Engine: engineA},
			{Name: "B", Weight: 1, Engine: engineB},
		},
		time.Second,
		1,
		nil,
		hook,
	)
	require.NoError(t, err)
	lb.backends[0].currentWeight = 100
	lb.backends[1].currentWeight = 0

	resp, err := lb.Process(testhelper.CreateTestRequest())
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 0, engineA.callCount)
	assert.Equal(t, 1, engineB.callCount)
	assert.Equal(t, []string{"B"}, hook.doneNames)
}

func TestShardKeyWRR_Admission_AllDeniedReturnsSentinel(t *testing.T) {
	engineA := &stubEngine{}
	engineB := &stubEngine{}
	hook := &denyNamesAdmission{denied: map[string]struct{}{"A": {}, "B": {}}}
	lb, err := NewShardKeyWeightedRoundRobin(
		[]BackendItem{
			{Name: "A", Weight: 5, Engine: engineA},
			{Name: "B", Weight: 5, Engine: engineB},
		},
		time.Second, 3, nil, hook,
	)
	require.NoError(t, err)
	before := snapshotCurrentWeights(lb)

	resp, err := lb.Process(testhelper.CreateTestRequest())
	require.ErrorIs(t, err, ErrNoBackendPermitted)
	assert.Nil(t, resp)
	assert.Equal(t, 0, engineA.callCount)
	assert.Equal(t, 0, engineB.callCount)
	assert.Empty(t, hook.doneNames)
	assert.Equal(t, before, snapshotCurrentWeights(lb))
}

func TestShardKeyWRR_Admission_PreservesRealErrorWhenRemainingDenied(t *testing.T) {
	upstream := errors.New("upstream boom")
	engineA := &stubEngine{resp: &octollm.Response{StatusCode: 502}, err: upstream}
	engineB := &stubEngine{}
	engineC := &stubEngine{}
	hook := &denyNamesAdmission{denied: map[string]struct{}{"B": {}, "C": {}}}
	lb, err := NewShardKeyWeightedRoundRobin(
		[]BackendItem{
			{Name: "A", Weight: 100, Engine: engineA},
			{Name: "B", Weight: 1, Engine: engineB},
			{Name: "C", Weight: 1, Engine: engineC},
		},
		time.Second, 10, nil, hook,
	)
	require.NoError(t, err)
	lb.backends[0].currentWeight = 1000
	lb.backends[1].currentWeight = 0
	lb.backends[2].currentWeight = 0

	resp, err := lb.Process(testhelper.CreateTestRequest())
	require.ErrorIs(t, err, upstream)
	require.NotNil(t, resp)
	assert.Equal(t, 502, resp.StatusCode)
	assert.Equal(t, 1, engineA.callCount)
	assert.Equal(t, 0, engineB.callCount)
	assert.Equal(t, 0, engineC.callCount)
}

func TestShardKeyWRR_Admission_RealFailureStillConsumesRetry(t *testing.T) {
	engineA := &stubEngine{err: errors.New("fail a")}
	engineB := &stubEngine{resp: &octollm.Response{StatusCode: 200}}
	lb, err := NewShardKeyWeightedRoundRobin(
		[]BackendItem{
			{Name: "A", Weight: 100, Engine: engineA},
			{Name: "B", Weight: 1, Engine: engineB},
		},
		time.Second, 2, nil, nil,
	)
	require.NoError(t, err)
	lb.backends[0].currentWeight = 1000
	lb.backends[1].currentWeight = 0

	resp, err := lb.Process(testhelper.CreateTestRequest())
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 1, engineA.callCount)
	assert.Equal(t, 1, engineB.callCount)
}

func TestShardKeyWRR_Admission_DoneCalledOncePerRealAttempt(t *testing.T) {
	engineA := &stubEngine{err: errors.New("fail a")}
	engineB := &stubEngine{resp: &octollm.Response{StatusCode: 200}}
	hook := &denyNamesAdmission{denied: map[string]struct{}{}}
	lb, err := NewShardKeyWeightedRoundRobin(
		[]BackendItem{
			{Name: "A", Weight: 100, Engine: engineA},
			{Name: "B", Weight: 1, Engine: engineB},
		},
		time.Second, 5, nil, hook,
	)
	require.NoError(t, err)
	lb.backends[0].currentWeight = 1000
	lb.backends[1].currentWeight = 0

	_, err = lb.Process(testhelper.CreateTestRequest())
	require.NoError(t, err)
	assert.Equal(t, []string{"A", "B"}, hook.doneNames)
}

func TestShardKeyWRR_Admission_NilAdmissionKeepsCurrentBehavior(t *testing.T) {
	engineA := &stubEngine{resp: &octollm.Response{StatusCode: 200}}
	lb, err := NewShardKeyWeightedRoundRobin(
		[]BackendItem{{Name: "A", Weight: 1, Engine: engineA}},
		time.Second, 1, nil, nil,
	)
	require.NoError(t, err)

	resp, err := lb.Process(testhelper.CreateTestRequest())
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 1, engineA.callCount)
}

func TestShardKeyWRR_Admission_RollbackRestoresAllCandidateWeights(t *testing.T) {
	engineA := &stubEngine{}
	engineB := &stubEngine{resp: &octollm.Response{StatusCode: 200}}
	engineC := &stubEngine{resp: &octollm.Response{StatusCode: 200}}
	hook := &denyNamesAdmission{denied: map[string]struct{}{"A": {}}}
	lb, err := NewShardKeyWeightedRoundRobin(
		[]BackendItem{
			{Name: "A", Weight: 5, Engine: engineA},
			{Name: "B", Weight: 5, Engine: engineB},
			{Name: "C", Weight: 5, Engine: engineC},
		},
		time.Second, 3, nil, hook,
	)
	require.NoError(t, err)
	lb.backends[0].currentWeight = 20
	lb.backends[1].currentWeight = 3
	lb.backends[2].currentWeight = 1
	beforeA := lb.backends[0].currentWeight
	beforeRemaining := []int{lb.backends[1].currentWeight, lb.backends[2].currentWeight}

	_, err = lb.Process(testhelper.CreateTestRequest())
	require.NoError(t, err)
	assert.Equal(t, 0, engineA.callCount)
	assert.Equal(t, 1, engineB.callCount+engineC.callCount)
	assert.Equal(t, beforeA, lb.backends[0].currentWeight, "denied backend must be fully restored")
	afterRemaining := []int{lb.backends[1].currentWeight, lb.backends[2].currentWeight}
	assert.NotEqual(t, beforeRemaining, afterRemaining, "remaining candidates keep the committed WRR update")
}

func TestShardKeyWRR_Admission_AffinityDenyAlsoRollsBack(t *testing.T) {
	engineA := &stubEngine{}
	engineB := &stubEngine{resp: &octollm.Response{StatusCode: 200}}
	engineC := &stubEngine{resp: &octollm.Response{StatusCode: 200}}
	hook := &denyNamesAdmission{denied: map[string]struct{}{"A": {}}}
	provider := affinityProviderFunc(func(*octollm.Request) ([]*PrioritizedBackend, AffinityCommitFunc, error) {
		return []*PrioritizedBackend{{Name: "A", StrongCacheHit: true}}, nil, nil
	})
	lb, err := NewShardKeyWeightedRoundRobin(
		[]BackendItem{
			{Name: "A", Weight: 1, Engine: engineA},
			{Name: "B", Weight: 1, Engine: engineB},
			{Name: "C", Weight: 1, Engine: engineC},
		},
		time.Second, 3, provider, hook,
	)
	require.NoError(t, err)
	before := snapshotCurrentWeights(lb)

	resp, err := lb.Process(testhelper.CreateTestRequest())
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 0, engineA.callCount)
	assert.Equal(t, 1, engineB.callCount+engineC.callCount)
	assert.Equal(t, before[0], lb.backends[0].currentWeight)
	assert.NotEqual(t, before[1:], snapshotCurrentWeights(lb)[1:])
}

func TestShardKeyWRR_Admission_AllowedSuccessDoesNotRollback(t *testing.T) {
	engineA := &stubEngine{resp: &octollm.Response{StatusCode: 200}}
	engineB := &stubEngine{resp: &octollm.Response{StatusCode: 200}}
	lb, err := NewShardKeyWeightedRoundRobin(
		[]BackendItem{
			{Name: "A", Weight: 7, Engine: engineA},
			{Name: "B", Weight: 3, Engine: engineB},
		},
		time.Second, 1, nil, &denyNamesAdmission{},
	)
	require.NoError(t, err)
	lb.backends[0].currentWeight = 4
	lb.backends[1].currentWeight = 0
	before := snapshotCurrentWeights(lb)

	_, err = lb.Process(testhelper.CreateTestRequest())
	require.NoError(t, err)
	assert.Equal(t, 1, engineA.callCount)
	assert.NotEqual(t, before, snapshotCurrentWeights(lb))
}

func TestShardKeyWRR_Admission_FailoverAfterAllowedDoesNotRollback(t *testing.T) {
	engineA := &stubEngine{err: errors.New("fail")}
	engineB := &stubEngine{resp: &octollm.Response{StatusCode: 200}}
	hook := &denyNamesAdmission{}
	lb, err := NewShardKeyWeightedRoundRobin(
		[]BackendItem{
			{Name: "A", Weight: 5, Engine: engineA},
			{Name: "B", Weight: 5, Engine: engineB},
		},
		time.Second, 5, nil, hook,
	)
	require.NoError(t, err)
	lb.backends[0].currentWeight = 40
	lb.backends[1].currentWeight = 0
	before := snapshotCurrentWeights(lb)

	_, err = lb.Process(testhelper.CreateTestRequest())
	require.NoError(t, err)
	assert.Equal(t, 1, engineA.callCount)
	assert.Equal(t, 1, engineB.callCount)
	assert.NotEqual(t, before[0], lb.backends[0].currentWeight)
	assert.NotEqual(t, before[1], lb.backends[1].currentWeight)
}

func TestShardKeyWRR_Admission_RollbackRestoresWeights(t *testing.T) {
	lb := &ShardKeyWeightedRoundRobin{
		backends: []*wrrBackend{
			{name: "A", weight: 5, engine: &stubEngine{}, currentWeight: 10},
			{name: "B", weight: 5, engine: &stubEngine{}, currentWeight: 2},
		},
	}
	before := snapshotCurrentWeights(lb)
	selection := lb.selectNextEngine(context.Background(), "", nil)
	require.NotNil(t, selection)
	require.NotNil(t, selection.rollback)
	afterSelect := snapshotCurrentWeights(lb)
	assert.NotEqual(t, before, afterSelect)

	selection.Rollback()
	assert.Equal(t, before, snapshotCurrentWeights(lb))
}

func TestShardKeyWRR_Admission_AllZeroRollbackIsNoop(t *testing.T) {
	engineA := &stubEngine{}
	engineB := &stubEngine{}
	hook := &denyNamesAdmission{denied: map[string]struct{}{"A": {}, "B": {}}}
	lb := &ShardKeyWeightedRoundRobin{
		backends: []*wrrBackend{
			{name: "A", weight: 0, engine: engineA, currentWeight: 0},
			{name: "B", weight: 0, engine: engineB, currentWeight: 0},
		},
		retryTimeout:     time.Second,
		retryMaxCount:    3,
		backendAdmission: hook,
	}
	before := snapshotCurrentWeights(lb)

	resp, err := lb.Process(testhelper.CreateTestRequest())
	require.ErrorIs(t, err, ErrNoBackendPermitted)
	assert.Nil(t, resp)
	assert.Equal(t, 0, engineA.callCount)
	assert.Equal(t, 0, engineB.callCount)
	assert.Equal(t, before, snapshotCurrentWeights(lb))
}

func TestShardKeyWRR_Admission_IgnoredHTTPStatusDoesNotCallDone(t *testing.T) {
	engineA := &stubEngine{
		resp: &octollm.Response{StatusCode: 413},
		err:  &errutils.UpstreamRespError{StatusCode: 413},
	}
	hook := &denyNamesAdmission{}
	lb, err := NewShardKeyWeightedRoundRobin(
		[]BackendItem{
			{Name: "A", Weight: 1, Engine: engineA},
			{Name: "B", Weight: 1, Engine: &stubEngine{}},
		},
		time.Second, 5, nil, hook,
	)
	require.NoError(t, err)
	lb.backends[0].currentWeight = 100
	lb.backends[1].currentWeight = 0

	resp, err := lb.Process(testhelper.CreateTestRequest())
	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 1, engineA.callCount)
	assert.Empty(t, hook.doneNames)
}

func TestShardKeyWRR_Admission_ConcurrentSelectRollback(t *testing.T) {
	engine := &stubEngine{resp: &octollm.Response{StatusCode: 200}}
	hook := &denyNamesAdmission{denied: map[string]struct{}{"A": {}}}
	lb, err := NewShardKeyWeightedRoundRobin(
		[]BackendItem{
			{Name: "A", Weight: 5, Engine: &stubEngine{}},
			{Name: "B", Weight: 5, Engine: engine},
		},
		time.Second, 3, nil, hook,
	)
	require.NoError(t, err)

	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			resp, err := lb.Process(testhelper.CreateTestRequest())
			assert.NoError(t, err)
			assert.NotNil(t, resp)
		})
	}
	wg.Wait()
}
