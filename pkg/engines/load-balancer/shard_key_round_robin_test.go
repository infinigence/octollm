package loadbalancer

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
)

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
	_, err := NewShardKeyWeightedRoundRobin(nil, time.Second, 1, time.Minute, nil, nil, "")
	assert.Error(t, err)

	// negative weight
	_, err = NewShardKeyWeightedRoundRobin(
		[]BackendItem{
			{Name: "b1", Weight: -1, Engine: backendEngine},
		},
		time.Second, 1, time.Minute, nil, nil, "",
	)
	assert.Error(t, err)

	// all zero weights -> normalized to 100
	lb, err := NewShardKeyWeightedRoundRobin(
		[]BackendItem{
			{Name: "b1", Weight: 0, Engine: backendEngine},
			{Name: "b2", Weight: 0, Engine: backendEngine},
		},
		time.Second, 1, time.Minute, nil, nil, "",
	)
	assert.NoError(t, err)
	if assert.Len(t, lb.backends, 2) {
		for i, b := range lb.backends {
			assert.Equalf(t, 100, b.weight, "backend %d weight", i)
			assert.GreaterOrEqualf(t, b.currentWeight, 0, "backend %d currentWeight lower bound", i)
			assert.LessOrEqualf(t, b.currentWeight, b.weight, "backend %d currentWeight upper bound", i)
		}
	}
}

func TestResolvePrioritizedBackends_NoRedisOrEmpty(t *testing.T) {
	lb := &ShardKeyWeightedRoundRobin{}
	assert.Nil(t, lb.resolvePrioritizedBackends(context.Background(), nil))

	lb.redisClient = nil
	assert.Nil(t, lb.resolvePrioritizedBackends(context.Background(), []string{"k1"}))
}

func TestResolvePrioritizedBackends_WithRedisAndTrim(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	backends := []BackendItem{
		{Name: "b1"},
		{Name: "b2"},
		{Name: "b3"},
		{Name: "b4"},
		{Name: "b5"},
	}
	lb, err := NewShardKeyWeightedRoundRobin(
		backends, 5*time.Second, 5, 5*time.Minute, nil, client, "",
	)
	require.NoError(t, err)

	ctx := context.Background()
	now := time.Now().Unix()

	// k1 has 2 members
	_, err = client.ZAdd(ctx, "k1",
		redis.Z{Score: float64(now - 10), Member: "b1"},
		redis.Z{Score: float64(now - 5), Member: "b2"},
	).Result()
	assert.NoError(t, err)

	// k2 has 5 members, should be trimmed to 3 most recent
	_, err = client.ZAdd(ctx, "k2",
		redis.Z{Score: float64(now - 50), Member: "b1"},
		redis.Z{Score: float64(now - 40), Member: "b2"},
		redis.Z{Score: float64(now - 30), Member: "b3"},
		redis.Z{Score: float64(now - 20), Member: "b4"},
		redis.Z{Score: float64(now - 10), Member: "b5"},
	).Result()
	assert.NoError(t, err)

	// Later shard key ("k2") should have higher priority than "k1"
	prioritized := lb.resolvePrioritizedBackends(ctx, []string{"k1", "k2"})
	if assert.NotEmpty(t, prioritized) {
		// First few should come from k2, most recent first: b5, b4, b3
		wantOrderPrefix := []string{"b5", "b4", "b3"}
		for i, name := range wantOrderPrefix {
			if assert.Less(t, i, len(prioritized)) {
				assert.Equalf(t, name, prioritized[i].name, "prioritized[%d]", i)
			}
		}
	}

	// ZSET for k2 should be trimmed to at most 3 members
	card, err := client.ZCard(ctx, "k2").Result()
	assert.NoError(t, err)
	assert.LessOrEqual(t, card, int64(3))
}

func TestGetNextEngine_PrioritizedHit(t *testing.T) {
	e1 := &stubEngine{}
	e2 := &stubEngine{}

	lb := &ShardKeyWeightedRoundRobin{
		backends: []*wrrBackend{
			{name: "a", weight: 1, engine: e1, currentWeight: 0},
			{name: "b", weight: 2, engine: e2, currentWeight: 0},
		},
	}

	name, eng, _ := lb.GetNextEngine(context.Background(), "b", nil)
	assert.Equal(t, "b", name)
	assert.Equal(t, e2, eng)

	// We don't assert exact weights here because the initial currentWeight is randomized in constructor.
	// Just ensure weights remain finite and have been updated.
	assert.NotZero(t, lb.backends[1].currentWeight)
	assert.NotZero(t, lb.backends[0].currentWeight)
}

func TestGetNextEngine_AllWeightZeroBackends(t *testing.T) {
	lb := &ShardKeyWeightedRoundRobin{
		backends: []*wrrBackend{
			{name: "a", weight: 0, engine: &stubEngine{}, currentWeight: 0},
			{name: "b", weight: 0, engine: &stubEngine{}, currentWeight: 0},
		},
	}

	name, eng, _ := lb.GetNextEngine(context.Background(), "", nil)
	assert.NotEmpty(t, name)
	assert.Contains(t, []string{"a", "b"}, name)
	assert.NotNil(t, eng)
}

type failingReader struct{}

func (f *failingReader) Read(p []byte) (int, error) {
	return 0, errors.New("read error")
}

func (f *failingReader) Close() error { return nil }

func TestShardKeyWeightedRoundRobin_Process_BodyReadError(t *testing.T) {
	lb := &ShardKeyWeightedRoundRobin{
		backends: []*wrrBackend{
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

	lb, err := NewShardKeyWeightedRoundRobin(
		backends,
		time.Second,
		3,
		time.Minute,
		func(req *octollm.Request) []string {
			return []string{"shard-key-1"}
		},
		client,
		"model-prefix",
	)
	assert.NoError(t, err)

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
		backends: []*wrrBackend{
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
		backends: []*wrrBackend{
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

func TestGetNextEngine_SmoothWeightedRoundRobin_NoShard(t *testing.T) {
	// Weights: A=2, B=3, C=5, total=10.
	// For classic smooth WRR, in any window of length 10 we should see A,B,C chosen
	// exactly 2,3,5 times respectively (ignoring randomness in initial currentWeight).
	lb := &ShardKeyWeightedRoundRobin{
		backends: []*wrrBackend{
			{name: "A", weight: 2, engine: &stubEngine{}, currentWeight: 0},
			{name: "B", weight: 3, engine: &stubEngine{}, currentWeight: 0},
			{name: "C", weight: 5, engine: &stubEngine{}, currentWeight: 0},
		},
	}

	const totalPicks = 10 // sum of weights
	counts := map[string]int{}
	sequence := make([]string, 0, totalPicks)

	for i := 0; i < totalPicks; i++ {
		name, eng, _ := lb.GetNextEngine(context.Background(), "", nil)
		assert.NotEmpty(t, name)
		assert.NotNil(t, eng)
		counts[name]++
		sequence = append(sequence, name)
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
	lb, err := NewShardKeyWeightedRoundRobin(lbItems, time.Second, 5, time.Minute, shardKeyListFunc, rd, "shard-key:all-backend-exhausted")
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
		_, err = rd.ZAdd(ctx, "shard-key:all-backend-exhausted:shard-key-1",
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

func TestShardKeyWeightedRoundRobin_Process_FailoverToZeroWeightedBackend(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	stubA := &stubEngine{err: errors.New("failure")}
	stubB := &stubEngine{err: errors.New("failure")}
	stubC := &stubEngine{}

	lbItems := []BackendItem{
		{Name: "A", Weight: 10, Engine: stubA},
		{Name: "B", Weight: 5, Engine: stubB},
		{Name: "C", Weight: 0, Engine: stubC},
	}
	shardKeyListFunc := func(req *octollm.Request) []string {
		return []string{"shard-key-1"}
	}
	lb, err := NewShardKeyWeightedRoundRobin(lbItems, time.Second, 5, time.Minute, shardKeyListFunc, rd, "shard-key:failover-zero-weight")
	require.NoError(t, err)

	t.Run("weighted backends fail, zero-weight backend succeeds", func(t *testing.T) {
		// A B should be tried first (in order of prioritization), then failover to C which has zero weight but should still be tried and succeed
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

	t.Run("shard-key prioritizes weighted backends, zero-weight backend succeeds as last resort", func(t *testing.T) {
		ctx := context.Background()
		// b -> a -> c
		_, err = rd.ZAdd(ctx, "shard-key:failover-zero-weight:shard-key-1",
			redis.Z{Score: float64(time.Now().Unix() - 2), Member: "A"},
			redis.Z{Score: float64(time.Now().Unix() - 1), Member: "B"},
			redis.Z{Score: float64(time.Now().Unix()), Member: "C"},
		).Result()
		require.NoError(t, err)

		req := testhelper.CreateTestRequest()
		resp, err := lb.Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)

		assert.Equal(t, 1, stubA.callCount, "stubA should be called once")
		assert.Equal(t, 1, stubB.callCount, "stubB should be called once")
		assert.Equal(t, 1, stubC.callCount, "stubC should be called once")

		assert.Greater(t, stubA.lastCalledTs, stubB.lastCalledTs, "stubA and stubB should be called in order of prioritization")
		assert.Greater(t, stubC.lastCalledTs, stubB.lastCalledTs, "stubC should be called for failover after stubB")
	})
}

func TestShardKeyhWeightedRoundRobin_Process_ZeroWeightEngineNotFirstChoice(t *testing.T) {
	stubA := &stubEngine{}
	stubB := &stubEngine{}
	stubC := &stubEngine{}

	lbItems := []BackendItem{
		{Name: "A", Weight: 10, Engine: stubA},
		{Name: "B", Weight: 5, Engine: stubB},
		{Name: "C", Weight: 0, Engine: stubC},
	}

	lb, err := NewShardKeyWeightedRoundRobin(lbItems, time.Second, 5, time.Minute, nil, nil, "")
	require.NoError(t, err)

	t.Run("shard-key prioritizes weighted backends, zero-weight backend succeeds as last resort", func(t *testing.T) {
		for range 5 {
			req := testhelper.CreateTestRequest()
			resp, err := lb.Process(req)
			require.NoError(t, err)
			require.NotNil(t, resp)
		}
		assert.Equal(t, 0, stubC.callCount, "stubC with zero weight should not be called when other weighted backends are available and healthy")
		assert.Equal(t, 5, stubA.callCount+stubB.callCount, "stubA and stubB should be called for all requests since they are healthy and have non-zero weight")
	})
}

func TestShardKeyhWeightedRoundRobin_Process_ZeroWeightEngineCanBeLoadBalanced(t *testing.T) {
	stubA := &stubEngine{err: errors.New("failure")}
	stubB := &stubEngine{err: errors.New("failure")}
	stubC := &stubEngine{}
	stubD := &stubEngine{}

	lbItems := []BackendItem{
		{Name: "A", Weight: 10, Engine: stubA},
		{Name: "B", Weight: 5, Engine: stubB},
		{Name: "C", Weight: 0, Engine: stubC},
		{Name: "D", Weight: 0, Engine: stubD},
	}

	lb, err := NewShardKeyWeightedRoundRobin(lbItems, time.Second, 5, time.Minute, nil, nil, "")
	require.NoError(t, err)

	t.Run("shard-key prioritizes weighted backends, zero-weight backend succeeds as last resort", func(t *testing.T) {
		for range 100 {
			req := testhelper.CreateTestRequest()
			resp, err := lb.Process(req)
			require.NoError(t, err)
			require.NotNil(t, resp)
		}
		assert.Equal(t, 100, stubA.callCount, "stubA should be called once per request")
		assert.Equal(t, 100, stubB.callCount, "stubB should be called once per request")

		assert.Equal(t, 100, stubC.callCount+stubD.callCount, "zero-weight backends C and D should handle all 100 requests as fallback")

		assert.InDelta(t, 1.0, float64(stubC.callCount)/float64(stubD.callCount), 0.4, "C and D should be load balanced roughly equally")
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
		{Name: "A", Weight: 10, Engine: stubA},
		{Name: "B", Weight: 5, Engine: stubB},
		{Name: "C", Weight: 0, Engine: stubC},
	}

	shardKeyListFunc := func(req *octollm.Request) []string {
		return []string{req.Context().Value("selected-backend").(string)}
	}
	ctxA := context.WithValue(context.Background(), "selected-backend", "A")
	ctxC := context.WithValue(context.Background(), "selected-backend", "C")

	lb, err := NewShardKeyWeightedRoundRobin(lbItems, time.Second, 5, time.Minute, shardKeyListFunc, rd, "shard-key:select-by-ctx")
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

	t.Run("redis score is not updated after successful processing for zero-weight backend", func(t *testing.T) {
		initialScore := float64(time.Now().Unix() - 10)
		_, err = rd.ZAdd(ctx, "shard-key:select-by-ctx:C",
			redis.Z{Score: initialScore, Member: "C"},
		).Result()
		require.NoError(t, err)

		req := testhelper.CreateTestRequest(testhelper.WithContext(ctxC))
		_, err = lb.Process(req)
		require.NoError(t, err)

		assert.Equal(t, 1, stubA.callCount)
		assert.Equal(t, 1, stubB.callCount)
		assert.Equal(t, 1, stubC.callCount, "stubC should be called for failover")

		// Score should NOT be updated for zero-weight backends
		scoreAfter, err := rd.ZScore(ctx, "shard-key:select-by-ctx:C", "C").Result()
		require.NoError(t, err)
		assert.Equal(t, initialScore, scoreAfter, "Redis ZSET score for zero-weight backend C should not be updated after processing")
	})
}
