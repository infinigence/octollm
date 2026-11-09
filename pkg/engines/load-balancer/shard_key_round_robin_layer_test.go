package loadbalancer

import (
	"context"
	"errors"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
)

func TestNewShardKeyWeightedRoundRobin_LayerIsStored(t *testing.T) {
	e := &stubEngine{}
	lb, err := NewShardKeyWeightedRoundRobin(
		withTestConcurrencyKeys([]BackendItem{
			{Name: "a", Weight: 7, Layer: 1, Engine: e},
			{Name: "b", Weight: 0, Layer: 0, Engine: e},
			{Name: "d", Weight: 5, Layer: 2, Engine: e},
		}),
		time.Second, 1, nil, nil, newTestRedis(t),
	)
	require.NoError(t, err)
	weights := map[string]int{}
	layers := map[string]int{}
	for _, b := range lb.backends {
		weights[b.name] = b.weight
		layers[b.name] = b.layer
	}
	assert.Equal(t, map[string]int{"a": 7, "b": 0, "d": 5}, weights)
	assert.Equal(t, map[string]int{"a": 1, "b": 0, "d": 2}, layers)
}

func TestSelectNextEngine_Layer_HigherLayerFirst(t *testing.T) {
	lb := &ShardKeyWeightedRoundRobin{
		backends: []*shardKeyRoundRobinBackend{
			{name: "low", weight: 100, layer: 0, engine: &stubEngine{}},
			{name: "high", weight: 1, layer: 2, engine: &stubEngine{}},
			{name: "mid", weight: 100, layer: 1, engine: &stubEngine{}},
		},
	}

	for i := 0; i < 10; i++ {
		sel := lb.selectNextEngine(context.Background(), "", nil)
		require.NotNil(t, sel.shardKeyRoundRobinBackend)
		assert.Equal(t, "high", sel.shardKeyRoundRobinBackend.name)
	}

	sel := lb.selectNextEngine(context.Background(), "", map[string]bool{"high": true})
	require.NotNil(t, sel.shardKeyRoundRobinBackend)
	assert.Equal(t, "mid", sel.shardKeyRoundRobinBackend.name)

	sel = lb.selectNextEngine(context.Background(), "", map[string]bool{"high": true, "mid": true})
	require.NotNil(t, sel.shardKeyRoundRobinBackend)
	assert.Equal(t, "low", sel.shardKeyRoundRobinBackend.name)

	sel = lb.selectNextEngine(context.Background(), "", map[string]bool{"high": true, "mid": true, "low": true})
	assert.Nil(t, sel.shardKeyRoundRobinBackend)
}

func TestSelectNextEngine_Layer_TotalWeightPerLayer(t *testing.T) {
	// Layer 1: A=2, B=3 (total 5). Layer 0: C=100 must not contribute to layer 1's total weight,
	// otherwise the smooth-WRR penalty is skewed and the 2:3 ratio is lost.
	lb := &ShardKeyWeightedRoundRobin{
		backends: []*shardKeyRoundRobinBackend{
			{name: "A", weight: 2, layer: 1, engine: &stubEngine{}},
			{name: "B", weight: 3, layer: 1, engine: &stubEngine{}},
			{name: "C", weight: 100, layer: 0, engine: &stubEngine{}},
		},
	}

	counts := map[string]int{}
	for i := 0; i < 5; i++ {
		sel := lb.selectNextEngine(context.Background(), "", nil)
		require.NotNil(t, sel.shardKeyRoundRobinBackend)
		counts[sel.shardKeyRoundRobinBackend.name]++
	}
	assert.Equal(t, map[string]int{"A": 2, "B": 3}, counts)
	// Smooth WRR returns to the initial state after one full cycle of the active layer.
	assert.Equal(t, 0, lb.backends[0].currentWeight)
	assert.Equal(t, 0, lb.backends[1].currentWeight)
	// The inactive layer's WRR state is untouched.
	assert.Equal(t, 0, lb.backends[2].currentWeight)
}

func TestSelectNextEngine_Layer_ZeroWeightLayerIsSkipped(t *testing.T) {
	// A layer whose backends all have weight 0 has nothing selectable, so it must not block the
	// layers below it.
	lb := &ShardKeyWeightedRoundRobin{
		backends: []*shardKeyRoundRobinBackend{
			{name: "A", weight: 10, layer: 2, engine: &stubEngine{}},
			{name: "Z", weight: 0, layer: 1, engine: &stubEngine{}},
			{name: "C", weight: 10, layer: 0, engine: &stubEngine{}},
		},
	}

	sel := lb.selectNextEngine(context.Background(), "", nil)
	require.NotNil(t, sel.shardKeyRoundRobinBackend)
	assert.Equal(t, "A", sel.shardKeyRoundRobinBackend.name)

	sel = lb.selectNextEngine(context.Background(), "Z", map[string]bool{"A": true})
	require.NotNil(t, sel.shardKeyRoundRobinBackend)
	assert.Equal(t, "C", sel.shardKeyRoundRobinBackend.name)
}

func TestSelectNextEngine_Layer_AffinityBeatsLayer(t *testing.T) {
	lb := &ShardKeyWeightedRoundRobin{
		backends: []*shardKeyRoundRobinBackend{
			{name: "A", weight: 1, layer: 1, engine: &stubEngine{}},
			{name: "C", weight: 1, layer: 0, engine: &stubEngine{}, currentWeight: -5},
			{name: "D", weight: 1, layer: 0, engine: &stubEngine{}, currentWeight: 100},
		},
	}

	// C sits in a lower layer and has a lower currentWeight than D, yet the hint wins while A is available.
	sel := lb.selectNextEngine(context.Background(), "C", nil)
	require.NotNil(t, sel.shardKeyRoundRobinBackend)
	assert.Equal(t, "C", sel.shardKeyRoundRobinBackend.name)

	// The WRR update runs in C's layer only: C pays layer 0's total weight, A is untouched.
	assert.Equal(t, 0, lb.backends[0].currentWeight)
	assert.Equal(t, -5-2+1, lb.backends[1].currentWeight)
	assert.Equal(t, 101, lb.backends[2].currentWeight)
}

func TestShardKeyWeightedRoundRobin_Process_LayerFailover(t *testing.T) {
	stubA := &stubEngine{err: errors.New("failure")}
	stubB := &stubEngine{err: errors.New("failure")}
	stubC := &stubEngine{}

	lb, err := NewShardKeyWeightedRoundRobin(
		withTestConcurrencyKeys([]BackendItem{
			{Name: "C", Weight: 100, Layer: 0, Engine: stubC},
			{Name: "A", Weight: 1, Layer: 1, Engine: stubA},
			{Name: "B", Weight: 1, Layer: 1, Engine: stubB},
		}),
		time.Second, 5, nil, nil, newTestRedis(t),
	)
	require.NoError(t, err)

	req := testhelper.CreateTestRequest()
	_, err = lb.Process(req)
	require.NoError(t, err)
	assert.Equal(t, 1, stubA.callCount)
	assert.Equal(t, 1, stubB.callCount)
	assert.Equal(t, 1, stubC.callCount)
	name, _ := GetSelectedBackendName(req)
	assert.Equal(t, "C", name)
}

func TestShardKeyWeightedRoundRobin_Process_LayerAdmissionDenyFallsToNextLayer(t *testing.T) {
	stubA := &stubEngine{}
	stubC := &stubEngine{}
	hook := &denyNamesAdmission{denied: map[string]struct{}{"A": {}}}

	lb, err := NewShardKeyWeightedRoundRobin(
		withTestConcurrencyKeys([]BackendItem{
			{Name: "A", Weight: 1, Layer: 1, Engine: stubA},
			{Name: "C", Weight: 1, Layer: 0, Engine: stubC},
		}),
		time.Second, 1, nil, hook, newTestRedis(t),
	)
	require.NoError(t, err)

	_, err = lb.Process(testhelper.CreateTestRequest())
	require.NoError(t, err)
	assert.Equal(t, 0, stubA.callCount)
	assert.Equal(t, 1, stubC.callCount)
	assert.Equal(t, []string{"A", "C"}, hook.attempted)
}

func TestShardKeyWeightedRoundRobin_Process_LayerWithAffinity(t *testing.T) {
	mr := miniredis.RunT(t)
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	stubA := &stubEngine{}
	stubC := &stubEngine{}
	stubD := &stubEngine{}
	lbItems := withTestConcurrencyKeys([]BackendItem{
		{Name: "A", Weight: 1, Layer: 1, Engine: stubA},
		{Name: "C", Weight: 1, Layer: 0, Engine: stubC},
		{Name: "D", Weight: 1, Layer: 0, Engine: stubD},
	})
	getter := func(req *octollm.Request) []string { return []string{"k1", "k2"} }
	provider := newPrimaryShardKeyProvider(t, getter, rd, "layer-test", time.Minute, lbItems)
	lb, err := NewShardKeyWeightedRoundRobin(lbItems, time.Second, 5, provider, nil, rd)
	require.NoError(t, err)

	ctx := context.Background()
	_, err = rd.ZAdd(ctx, "layer-test:k2", redis.Z{Score: 1, Member: "C"}).Result()
	require.NoError(t, err)

	t.Run("lower-layer affinity wins over higher layer", func(t *testing.T) {
		_, err := lb.Process(testhelper.CreateTestRequest())
		require.NoError(t, err)
		assert.Equal(t, 0, stubA.callCount)
		assert.Equal(t, 1, stubC.callCount)
		assert.Equal(t, 0, stubD.callCount)
	})

	t.Run("failed affinity backend falls back to the highest layer", func(t *testing.T) {
		stubC.reset()
		stubC.err = errors.New("failure")
		// Bias WRR toward D so an A pick can only come from layer priority.
		for _, b := range lb.backends {
			if b.name == "D" {
				b.currentWeight = 1000
			}
		}

		_, err := lb.Process(testhelper.CreateTestRequest())
		require.NoError(t, err)
		assert.Equal(t, 1, stubC.callCount)
		assert.Equal(t, 1, stubA.callCount)
		assert.Equal(t, 0, stubD.callCount)
		assert.Greater(t, stubA.lastCalledTs, stubC.lastCalledTs)
	})
}
