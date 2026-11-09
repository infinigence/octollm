package loadbalancer

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
)

// Tests with a non-nil affinityProvider exercise shard-key prioritized routing.
// nil affinityProvider skips that path and tests fallback selection only (weighted round-robin).

type stubEngine struct {
	resp         *octollm.Response
	err          error
	callCount    int
	lastReqCtx   context.Context
	lastCalledTs time.Time
}

func (s *stubEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	s.callCount++
	s.lastCalledTs = time.Now()
	if req != nil {
		s.lastReqCtx = req.Context()
	}
	if s.resp == nil && s.err == nil {
		return &octollm.Response{StatusCode: 200}, s.err
	}
	return s.resp, s.err
}

func (s *stubEngine) reset() {
	s.callCount = 0
	s.lastReqCtx = nil
	s.lastCalledTs = time.Time{}
}

func newTestRequest(t *testing.T) *octollm.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com", strings.NewReader("body"))
	assert.NoError(t, err)
	return octollm.NewRequest(req, octollm.APIFormatUnknown)
}

func TestNewShardKeyWeightedRoundRobin_Validation(t *testing.T) {
	backendEngine := &stubEngine{}

	// empty backends
	_, err := NewShardKeyWeightedRoundRobin(nil, time.Second, 1, nil, nil, nil)
	assert.Error(t, err)

	// concurrency key is unused by WRR selection
	_, err = NewShardKeyWeightedRoundRobin(
		[]BackendItem{
			{Name: "b1", Weight: 1, Engine: backendEngine},
		},
		time.Second, 1, nil,
		nil,
		newTestRedis(t),
	)
	assert.NoError(t, err)

	// negative weight
	_, err = NewShardKeyWeightedRoundRobin(
		withTestConcurrencyKeys([]BackendItem{
			{Name: "b1", Weight: -1, Engine: backendEngine},
		}),
		time.Second, 1, nil,
		nil,
		newTestRedis(t),
	)
	assert.Error(t, err)

	// negative layer
	_, err = NewShardKeyWeightedRoundRobin(
		withTestConcurrencyKeys([]BackendItem{
			{Name: "b1", Weight: 1, Layer: -1, Engine: backendEngine},
		}),
		time.Second, 1, nil,
		nil,
		newTestRedis(t),
	)
	assert.Error(t, err)

	// all zero weights are kept as configured; weight-0 backends are never selected
	lb, err := NewShardKeyWeightedRoundRobin(
		withTestConcurrencyKeys([]BackendItem{
			{Name: "b1", Weight: 0, Engine: backendEngine},
			{Name: "b2", Weight: 0, Engine: backendEngine},
		}),
		time.Second,
		1,
		nil,
		nil,
		newTestRedis(t),
	)
	require.NoError(t, err)
	if assert.Len(t, lb.backends, 2) {
		for i, b := range lb.backends {
			assert.Equalf(t, 0, b.weight, "backend %d weight", i)
			assert.Equalf(t, 0, b.currentWeight, "backend %d currentWeight", i)
		}
	}
}

func TestResolveAffinity_NilProvider(t *testing.T) {
	lb := &ShardKeyWeightedRoundRobin{}
	mapped, commit, err := lb.resolveAffinity(testhelper.CreateTestRequest())
	require.NoError(t, err)
	assert.Nil(t, mapped)
	assert.Nil(t, commit)
}

func TestResolveAffinity_WithRedisAndTrim(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	backends := []BackendItem{
		{Name: "b1", Weight: 1},
		{Name: "b2", Weight: 1},
		{Name: "b3", Weight: 1},
		{Name: "b4", Weight: 1},
		{Name: "b5", Weight: 1},
	}
	provider := newPrimaryShardKeyProvider(
		t,
		func(_ *octollm.Request) []string { return []string{"k1", "k2"} },
		client,
		"",
		5*time.Minute,
		backends,
	)
	lb, err := NewShardKeyWeightedRoundRobin(withTestConcurrencyKeys(backends), 5*time.Second, 5, provider, nil, client)
	require.NoError(t, err)

	ctx := context.Background()
	now := time.Now().Unix()

	var zerr error
	_, zerr = client.ZAdd(ctx, "k1",
		redis.Z{Score: float64(now - 10), Member: "b1"},
		redis.Z{Score: float64(now - 5), Member: "b2"},
	).Result()
	assert.NoError(t, zerr)

	_, zerr = client.ZAdd(ctx, "k2",
		redis.Z{Score: float64(now - 50), Member: "b1"},
		redis.Z{Score: float64(now - 40), Member: "b2"},
		redis.Z{Score: float64(now - 30), Member: "b3"},
		redis.Z{Score: float64(now - 20), Member: "b4"},
		redis.Z{Score: float64(now - 10), Member: "b5"},
	).Result()
	assert.NoError(t, zerr)

	req := testhelper.CreateTestRequest()
	prioritized, _, err := lb.resolveAffinity(req)
	require.NoError(t, err)
	if assert.NotEmpty(t, prioritized) {
		wantOrderPrefix := []string{"b5", "b4", "b3"}
		for i, name := range wantOrderPrefix {
			if assert.Less(t, i, len(prioritized)) {
				assert.Equalf(t, name, prioritized[i].backend.name, "prioritized[%d]", i)
			}
		}
	}

	card, err := client.ZCard(ctx, "k2").Result()
	assert.NoError(t, err)
	assert.LessOrEqual(t, card, int64(3))
}

func TestResolveAffinity_OnlyStrongCacheHits(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	backends := []BackendItem{
		{Name: "weak", Weight: 1},
		{Name: "strong", Weight: 1},
	}
	provider := newPrimaryShardKeyProvider(
		t,
		func(_ *octollm.Request) []string { return []string{"k1", "k2", "k3"} },
		client,
		"",
		5*time.Minute,
		backends,
	)
	lb, err := NewShardKeyWeightedRoundRobin(withTestConcurrencyKeys(backends), 5*time.Second, 5, provider, nil, client)
	require.NoError(t, err)

	ctx := context.Background()
	now := float64(time.Now().Unix())

	t.Run("weak hit on first key is not prioritized", func(t *testing.T) {
		_, zerr := client.ZAdd(ctx, "k1", redis.Z{Score: now, Member: "weak"}).Result()
		require.NoError(t, zerr)

		req := testhelper.CreateTestRequest()
		prioritized, _, err := lb.resolveAffinity(req)
		require.NoError(t, err)
		assert.Empty(t, prioritized)
	})

	t.Run("strong hits on last two keys are prioritized and weak hits are dropped", func(t *testing.T) {
		_, zerr := client.ZAdd(ctx, "k3", redis.Z{Score: now, Member: "strong"}).Result()
		require.NoError(t, zerr)

		req := testhelper.CreateTestRequest()
		prioritized, _, err := lb.resolveAffinity(req)
		require.NoError(t, err)
		require.Len(t, prioritized, 1)
		assert.Equal(t, "strong", prioritized[0].backend.name)
		assert.True(t, prioritized[0].strongCacheHit)
	})
}

func TestSelectNextEngine_PrioritizedHit(t *testing.T) {
	e1 := &stubEngine{}
	e2 := &stubEngine{}

	lb := &ShardKeyWeightedRoundRobin{
		backends: []*shardKeyRoundRobinBackend{
			{name: "a", weight: 1, engine: e1, currentWeight: 0},
			{name: "b", weight: 2, engine: e2, currentWeight: 0},
		},
	}

	sel := lb.selectNextEngine(context.Background(), "b", nil)
	require.NotNil(t, sel)
	assert.Equal(t, "b", sel.shardKeyRoundRobinBackend.name)
	assert.Equal(t, e2, sel.shardKeyRoundRobinBackend.engine)

	// We don't assert exact weights here because the initial currentWeight is randomized in constructor.
	// Just ensure weights remain finite and have been updated.
	assert.NotZero(t, lb.backends[1].currentWeight)
	assert.NotZero(t, lb.backends[0].currentWeight)
}

func TestSelectNextEngine_ZeroWeightBackendsNeverSelected(t *testing.T) {
	lb := &ShardKeyWeightedRoundRobin{
		backends: []*shardKeyRoundRobinBackend{
			{name: "a", weight: 0, engine: &stubEngine{}, currentWeight: 0},
			{name: "b", weight: 0, engine: &stubEngine{}, currentWeight: 0},
		},
	}

	sel := lb.selectNextEngine(context.Background(), "a", nil)
	require.NotNil(t, sel)
	assert.Nil(t, sel.shardKeyRoundRobinBackend)
}

func TestSelectNextEngine_PrioritizedOverdrawnBackendStillSelected(t *testing.T) {
	eA := &stubEngine{}
	eB := &stubEngine{}
	lb := &ShardKeyWeightedRoundRobin{
		backends: []*shardKeyRoundRobinBackend{
			{name: "A", weight: 10000, engine: eA, currentWeight: 0},
			{name: "B", weight: 1, engine: eB, currentWeight: 0},
		},
	}

	// There is no currentWeight floor: the hint wins however overdrawn its backend is.
	for i := 0; i < 100; i++ {
		sel := lb.selectNextEngine(context.Background(), "B", nil)
		require.NotNil(t, sel.shardKeyRoundRobinBackend)
		require.Equal(t, "B", sel.shardKeyRoundRobinBackend.name)
	}
	assert.Less(t, lb.backends[1].currentWeight, -100*10000+100)

	// Without the hint, smooth WRR sends traffic to the backend the hint did not drain.
	sel := lb.selectNextEngine(context.Background(), "", nil)
	require.NotNil(t, sel.shardKeyRoundRobinBackend)
	assert.Equal(t, "A", sel.shardKeyRoundRobinBackend.name)
}

func TestSelectNextEngine_ExcludedOrZeroWeightHintIsIgnored(t *testing.T) {
	lb := &ShardKeyWeightedRoundRobin{
		backends: []*shardKeyRoundRobinBackend{
			{name: "A", weight: 1, engine: &stubEngine{}},
			{name: "B", weight: 1, engine: &stubEngine{}},
			{name: "Z", weight: 0, engine: &stubEngine{}},
		},
	}

	sel := lb.selectNextEngine(context.Background(), "B", map[string]bool{"B": true})
	require.NotNil(t, sel.shardKeyRoundRobinBackend)
	assert.Equal(t, "A", sel.shardKeyRoundRobinBackend.name)

	sel = lb.selectNextEngine(context.Background(), "Z", nil)
	require.NotNil(t, sel.shardKeyRoundRobinBackend)
	assert.NotEqual(t, "Z", sel.shardKeyRoundRobinBackend.name)
}

type failingReader struct{}

func (f *failingReader) Read(p []byte) (int, error) {
	return 0, errors.New("read error")
}

func (f *failingReader) Close() error { return nil }

func TestShardKeyWeightedRoundRobin_Process_BodyReadError(t *testing.T) {
	lb := &ShardKeyWeightedRoundRobin{
		backends: []*shardKeyRoundRobinBackend{
			{name: "a", weight: 1, engine: &stubEngine{}},
		},
	}

	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com", &failingReader{})
	assert.NoError(t, err)

	req := octollm.NewRequest(httpReq, octollm.APIFormatUnknown)

	resp, err := lb.Process(req)
	assert.Error(t, err)
	assert.Nil(t, resp)
}

func TestShardKeyWeightedRoundRobin_Process_SuccessAndRedisUpdate(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	successEngine := &stubEngine{
		resp: &octollm.Response{StatusCode: 200},
		err:  nil,
	}

	backends := []BackendItem{
		{Name: "backend1", Weight: 1, Engine: successEngine},
	}

	provider := newPrimaryShardKeyProvider(
		t,
		func(req *octollm.Request) []string { return []string{"shard-key-1"} },
		client,
		"model-prefix",
		time.Minute,
		backends,
	)
	lb, err := NewShardKeyWeightedRoundRobin(withTestConcurrencyKeys(backends), time.Second, 3, provider, nil, client)
	require.NoError(t, err)

	req := newTestRequest(t)
	resp, err := lb.Process(req)
	assert.NoError(t, err)
	if assert.NotNil(t, resp) {
		assert.Equal(t, 200, resp.StatusCode)
	}

	// Backend name should be stored in metadata
	name, ok := GetSelectedBackendName(req)
	assert.True(t, ok)
	assert.Equal(t, "backend1", name)

	// Redis ZSET should be updated
	ctx := context.Background()
	members, err := client.ZRange(ctx, "model-prefix:shard-key-1", 0, -1).Result()
	assert.NoError(t, err)
	if assert.Len(t, members, 1) {
		assert.Equal(t, "backend1", members[0])
	}

	ttl, err := client.TTL(ctx, "model-prefix:shard-key-1").Result()
	assert.NoError(t, err)
	assert.Greater(t, ttl, time.Duration(0))
}

func TestShardKeyWeightedRoundRobin_Process_RetryTimeout(t *testing.T) {
	failEngine := &stubEngine{
		resp: &octollm.Response{StatusCode: 500},
		err:  errors.New("upstream error"),
	}

	lb := &ShardKeyWeightedRoundRobin{
		backends: []*shardKeyRoundRobinBackend{
			{name: "backend1", weight: 1, engine: failEngine},
		},
		retryTimeout:  0,
		retryMaxCount: 10,
	}

	req := newTestRequest(t)
	resp, err := lb.Process(req)
	assert.Error(t, err)
	if assert.NotNil(t, resp) {
		assert.Equal(t, 500, resp.StatusCode)
	}
	assert.Equal(t, 1, failEngine.callCount)
}

func TestShardKeyWeightedRoundRobin_Process_RetryMaxCount(t *testing.T) {
	failEngine := &stubEngine{
		resp: &octollm.Response{StatusCode: 500},
		err:  errors.New("upstream error"),
	}

	lb := &ShardKeyWeightedRoundRobin{
		backends: []*shardKeyRoundRobinBackend{
			{name: "backend1", weight: 1, engine: failEngine},
		},
		retryTimeout:  time.Hour,
		retryMaxCount: 2,
	}

	req := newTestRequest(t)
	resp, err := lb.Process(req)
	assert.Error(t, err)
	if assert.NotNil(t, resp) {
		assert.Equal(t, 500, resp.StatusCode)
	}
	// With excludeNames, once all backends are exhausted (1 backend tried), we stop.
	assert.Equal(t, 1, failEngine.callCount)
}

func TestSelectNextEngine_SmoothWeightedRoundRobin_NoShard(t *testing.T) {
	// Weights: A=2, B=3, C=5, total=10.
	// For classic smooth WRR, in any window of length 10 we should see A,B,C chosen
	// exactly 2,3,5 times respectively (ignoring randomness in initial currentWeight).
	lb := &ShardKeyWeightedRoundRobin{
		backends: []*shardKeyRoundRobinBackend{
			{name: "A", weight: 2, engine: &stubEngine{}, currentWeight: 0},
			{name: "B", weight: 3, engine: &stubEngine{}, currentWeight: 0},
			{name: "C", weight: 5, engine: &stubEngine{}, currentWeight: 0},
		},
	}

	const totalPicks = 10 // sum of weights
	counts := map[string]int{}
	sequence := make([]string, 0, totalPicks)

	for i := 0; i < totalPicks; i++ {
		sel := lb.selectNextEngine(context.Background(), "", nil)
		require.NotNil(t, sel)
		assert.NotEmpty(t, sel.shardKeyRoundRobinBackend.name)
		assert.NotNil(t, sel.shardKeyRoundRobinBackend.engine)
		counts[sel.shardKeyRoundRobinBackend.name]++
		sequence = append(sequence, sel.shardKeyRoundRobinBackend.name)
	}

	assert.Equal(t, 2, counts["A"])
	assert.Equal(t, 3, counts["B"])
	assert.Equal(t, 5, counts["C"])

	// Additionally, ensure that C does not appear 5 times consecutively at the start,
	// i.e., the sequence is not trivially "C C C C C ..." but interleaved.
	if len(sequence) >= 5 {
		allC := true
		for i := 0; i < 5; i++ {
			if sequence[i] != "C" {
				allC = false
				break
			}
		}
		assert.False(t, allC, "C should not appear 5 times consecutively at the beginning")
	}
}

func TestShardKeyWeightedRoundRobin_Process_AllBackendExhausted(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	stubA := &stubEngine{err: errors.New("failure")}
	stubB := &stubEngine{err: errors.New("failure")}
	stubC := &stubEngine{err: errors.New("failure")}

	lbItems := []BackendItem{
		{Name: "A", Weight: 10, Engine: stubA},
		{Name: "B", Weight: 5, Engine: stubB},
		{Name: "C", Weight: 1, Engine: stubC},
	}
	shardKeyListFunc := func(req *octollm.Request) []string {
		return []string{"shard-key-1"}
	}
	provider := newPrimaryShardKeyProvider(t, shardKeyListFunc, rd, "shard-key:all-backend-exhausted", time.Minute, lbItems)
	lb, err := NewShardKeyWeightedRoundRobin(withTestConcurrencyKeys(lbItems), time.Second, 5, provider, nil, rd)
	require.NoError(t, err)

	t.Run("all backends exhausted without shard key prioritization", func(t *testing.T) {
		// 1. all backends should be tried exactly once before giving up
		req := testhelper.CreateTestRequest()
		resp, err := lb.Process(req)
		require.Error(t, err)
		require.Nil(t, resp)
		// All backends should have been tried once before giving up
		assert.Equal(t, 1, stubA.callCount, "stubA should be called once")
		assert.Equal(t, 1, stubB.callCount, "stubB should be called once")
		assert.Equal(t, 1, stubC.callCount, "stubC should be called once")
	})

	stubA.reset()
	stubB.reset()
	stubC.reset()

	t.Run("all backends exhausted with shard key prioritization", func(t *testing.T) {
		ctx := context.Background()
		_, err := rd.ZAdd(ctx, "shard-key:all-backend-exhausted:shard-key-1",
			redis.Z{Score: float64(time.Now().Unix() - 2), Member: "A"},
			redis.Z{Score: float64(time.Now().Unix() - 1), Member: "B"},
			redis.Z{Score: float64(time.Now().Unix()), Member: "C"},
		).Result()
		require.NoError(t, err)

		req := testhelper.CreateTestRequest()
		resp, err := lb.Process(req)
		require.Error(t, err)
		require.Nil(t, resp)
		// All backends should have been tried once before giving up, even with prioritization
		assert.Equal(t, 1, stubA.callCount, "stubA should be called once")
		assert.Equal(t, 1, stubB.callCount, "stubB should be called once")
		assert.Equal(t, 1, stubC.callCount, "stubC should be called once")

		assert.Greater(t, stubA.lastCalledTs, stubB.lastCalledTs, "stubA and stubB should be called in order of prioritization")
		assert.Greater(t, stubB.lastCalledTs, stubC.lastCalledTs, "stubB and stubC should be called in order of prioritization")
	})
}

func TestShardKeyWeightedRoundRobin_Process_FailoverToLowerLayerBackend(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	stubA := &stubEngine{err: errors.New("failure")}
	stubB := &stubEngine{err: errors.New("failure")}
	stubC := &stubEngine{}

	lbItems := []BackendItem{
		{Name: "A", Weight: 10, Layer: 1, Engine: stubA},
		{Name: "B", Weight: 5, Layer: 1, Engine: stubB},
		{Name: "C", Weight: 1, Layer: 0, Engine: stubC},
	}
	shardKeyListFunc := func(req *octollm.Request) []string {
		return []string{"shard-key-1"}
	}
	provider := newPrimaryShardKeyProvider(t, shardKeyListFunc, rd, "shard-key:failover-lower-layer", time.Minute, lbItems)
	lb, err := NewShardKeyWeightedRoundRobin(withTestConcurrencyKeys(lbItems), time.Second, 5, provider, nil, rd)
	require.NoError(t, err)

	t.Run("higher-layer backends fail, lower-layer backend succeeds", func(t *testing.T) {
		// A B in layer 1 should be tried first, then failover to C in layer 0 which should succeed
		req := testhelper.CreateTestRequest()
		resp, err := lb.Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		// All backends should have been tried once
		assert.Equal(t, 1, stubA.callCount, "stubA should be called once")
		assert.Equal(t, 1, stubB.callCount, "stubB should be called once")
		assert.Equal(t, 1, stubC.callCount, "stubC should be called once")
	})

	stubA.reset()
	stubB.reset()
	stubC.reset()

	t.Run("affinity order wins over layers", func(t *testing.T) {
		ctx := context.Background()
		// Affinity candidates are tried by recency whatever their layer: C (layer 0) first and it succeeds.
		_, err := rd.ZAdd(ctx, "shard-key:failover-lower-layer:shard-key-1",
			redis.Z{Score: float64(time.Now().Unix() - 2), Member: "A"},
			redis.Z{Score: float64(time.Now().Unix() - 1), Member: "B"},
			redis.Z{Score: float64(time.Now().Unix()), Member: "C"},
		).Result()
		require.NoError(t, err)

		req := testhelper.CreateTestRequest()
		resp, err := lb.Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)

		assert.Equal(t, 0, stubA.callCount, "stubA should not be called")
		assert.Equal(t, 0, stubB.callCount, "stubB should not be called")
		assert.Equal(t, 1, stubC.callCount, "stubC should be called first for affinity")
	})
}

func TestShardKeyWeightedRoundRobin_Process_LowerLayerEngineNotFirstChoice(t *testing.T) {
	stubA := &stubEngine{}
	stubB := &stubEngine{}
	stubC := &stubEngine{}

	lbItems := []BackendItem{
		{Name: "A", Weight: 10, Layer: 1, Engine: stubA},
		{Name: "B", Weight: 5, Layer: 1, Engine: stubB},
		{Name: "C", Weight: 100, Layer: 0, Engine: stubC},
	}

	lb, err := NewShardKeyWeightedRoundRobin(withTestConcurrencyKeys(lbItems), time.Second, 5, nil, nil, newTestRedis(t)) // skip shard-key prioritized routing
	require.NoError(t, err)

	t.Run("WRR prefers higher-layer backends; lower layer not called when healthy alternatives exist", func(t *testing.T) {
		for range 5 {
			req := testhelper.CreateTestRequest()
			resp, err := lb.Process(req)
			require.NoError(t, err)
			require.NotNil(t, resp)
		}
		assert.Equal(t, 0, stubC.callCount, "stubC in a lower layer should not be called when higher-layer backends are available and healthy")
		assert.Equal(t, 5, stubA.callCount+stubB.callCount, "stubA and stubB should be called for all requests since they are healthy and in the higher layer")
	})
}

func TestShardKeyWeightedRoundRobin_Process_LowerLayerEnginesCanBeLoadBalanced(t *testing.T) {
	stubA := &stubEngine{err: errors.New("failure")}
	stubB := &stubEngine{err: errors.New("failure")}
	stubC := &stubEngine{}
	stubD := &stubEngine{}

	lbItems := []BackendItem{
		{Name: "A", Weight: 10, Layer: 1, Engine: stubA},
		{Name: "B", Weight: 5, Layer: 1, Engine: stubB},
		{Name: "C", Weight: 1, Layer: 0, Engine: stubC},
		{Name: "D", Weight: 1, Layer: 0, Engine: stubD},
	}

	lb, err := NewShardKeyWeightedRoundRobin(withTestConcurrencyKeys(lbItems), time.Second, 5, nil, nil, newTestRedis(t)) // skip shard-key prioritized routing
	require.NoError(t, err)

	t.Run("WRR failover to lower-layer backends load-balanced when all higher-layer backends fail", func(t *testing.T) {
		for range 1000 {
			req := testhelper.CreateTestRequest()
			resp, err := lb.Process(req)
			require.NoError(t, err)
			require.NotNil(t, resp)
		}
		assert.Equal(t, 1000, stubA.callCount, "stubA should be called once per request")
		assert.Equal(t, 1000, stubB.callCount, "stubB should be called once per request")

		assert.Equal(t, 1000, stubC.callCount+stubD.callCount, "lower-layer backends C and D should handle all requests as fallback")

		assert.InDelta(t, 1.0, float64(stubC.callCount)/float64(stubD.callCount), 0.3, "C and D should be load balanced roughly equally")
	})
}

func TestShardKeyWeightedRoundRobin_Process_Priority(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	stubA := &stubEngine{}
	stubB := &stubEngine{}
	stubC := &stubEngine{}

	lbItems := []BackendItem{
		{Name: "A", Weight: 10, Layer: 1, Engine: stubA},
		{Name: "B", Weight: 5, Layer: 1, Engine: stubB},
		{Name: "C", Weight: 1, Layer: 0, Engine: stubC},
	}

	shardKeyListFunc := func(req *octollm.Request) []string {
		return []string{req.Context().Value("selected-backend").(string)}
	}
	ctxA := context.WithValue(context.Background(), "selected-backend", "A")
	ctxC := context.WithValue(context.Background(), "selected-backend", "C")

	provider := newPrimaryShardKeyProvider(t, shardKeyListFunc, rd, "shard-key:select-by-ctx", time.Minute, lbItems)
	lb, err := NewShardKeyWeightedRoundRobin(withTestConcurrencyKeys(lbItems), time.Second, 5, provider, nil, rd)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("redis score is updated after successful processing with shard key prioritization", func(t *testing.T) {
		initialScore := float64(time.Now().Unix() - 10)
		_, err = rd.ZAdd(ctx, "shard-key:select-by-ctx:A",
			redis.Z{Score: initialScore, Member: "A"},
		).Result()
		require.NoError(t, err)

		req := testhelper.CreateTestRequest(testhelper.WithContext(ctxA))
		_, err = lb.Process(req)
		require.NoError(t, err)

		assert.Equal(t, 1, stubA.callCount, "stubA should be called for shard key prioritization")

		// Score should be updated to a more recent timestamp after successful processing
		updatedScore, err := rd.ZScore(ctx, "shard-key:select-by-ctx:A", "A").Result()
		require.NoError(t, err)
		assert.Greater(t, updatedScore, initialScore, "Redis ZSET score for A should be updated to a more recent timestamp after processing")
	})

	stubA.reset()
	stubB.reset()
	stubA.err = errors.New("failure")
	stubB.err = errors.New("failure")

	t.Run("redis score is updated after lower-layer affinity backend succeeds", func(t *testing.T) {
		initialScore := float64(time.Now().Unix() - 10)
		_, err = rd.ZAdd(ctx, "shard-key:select-by-ctx:C",
			redis.Z{Score: initialScore, Member: "C"},
		).Result()
		require.NoError(t, err)

		req := testhelper.CreateTestRequest(testhelper.WithContext(ctxC))
		_, err = lb.Process(req)
		require.NoError(t, err)

		// C is tried first for affinity although A and B sit in a higher layer
		assert.Equal(t, 0, stubA.callCount)
		assert.Equal(t, 0, stubB.callCount)
		assert.Equal(t, 1, stubC.callCount, "stubC should be called for affinity")

		scoreAfter, err := rd.ZScore(ctx, "shard-key:select-by-ctx:C", "C").Result()
		require.NoError(t, err)
		assert.Greater(t, scoreAfter, initialScore, "Redis ZSET score for lower-layer backend C should be updated after processing")
	})
}

func TestShardKeyWeightedRoundRobin_Process_RouteMetricRecordsLastAttemptOnce(t *testing.T) {
	stubA := &stubEngine{err: errors.New("failure")}
	stubB := &stubEngine{}
	lb, err := NewShardKeyWeightedRoundRobin(
		withTestConcurrencyKeys([]BackendItem{
			{Name: "A", Weight: 1, Layer: 0, Engine: stubA},
			{Name: "B", Weight: 1, Layer: 1, Engine: stubB},
		}),
		time.Second, 5,
		affinityProviderFunc(func(*octollm.Request) ([]*PrioritizedBackend, AffinityCommitFunc, error) {
			return []*PrioritizedBackend{{Name: "A", StrongCacheHit: true}}, nil, nil
		}),
		nil, newTestRedis(t),
	)
	require.NoError(t, err)

	const model = "route-metric-test-model"
	ctx := context.WithValue(context.Background(), octollm.ContextKeyModelName, model)
	_, err = lb.Process(testhelper.CreateTestRequest(testhelper.WithContext(ctx)))
	require.NoError(t, err)
	// A (affinity, lower layer) fails first, B serves the request: only B's attempt is recorded.
	assert.Equal(t, 1, stubA.callCount)
	assert.Equal(t, 1, stubB.callCount)
	assert.Equal(t, 0.0, routeCount(t, "strong", "strong", model, "A"))
	assert.Equal(t, 1.0, routeCount(t, "strong", "miss", model, "B"))
}

func routeCount(t *testing.T, expect, actual, model, backend string) float64 {
	t.Helper()
	want := map[string]string{"expect": expect, "actual": actual, "model_name": model, "backend_name": backend}
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "octollm_cache_aware_route_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			matched := true
			for _, label := range metric.GetLabel() {
				if want[label.GetName()] != label.GetValue() {
					matched = false
				}
			}
			if matched {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}
