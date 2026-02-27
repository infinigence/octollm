package loadbalancer

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

type stubEngine struct {
	resp       *octollm.Response
	err        error
	callCount  int
	lastReqCtx context.Context
}

func (s *stubEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	s.callCount++
	if req != nil {
		s.lastReqCtx = req.Context()
	}
	if s.resp == nil {
		return &octollm.Response{StatusCode: 200}, s.err
	}
	return s.resp, s.err
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
	_, err := NewShardKeyWeightedRoundRobin(nil, time.Second, 1, time.Minute, 5, nil, nil)
	assert.Error(t, err)

	// negative weight
	_, err = NewShardKeyWeightedRoundRobin(
		[]BackendItem{
			{Name: "b1", Weight: -1, Engine: backendEngine},
		},
		time.Second, 1, time.Minute, 5, nil, nil,
	)
	assert.Error(t, err)

	// all zero weights -> normalized to 100
	lb, err := NewShardKeyWeightedRoundRobin(
		[]BackendItem{
			{Name: "b1", Weight: 0, Engine: backendEngine},
			{Name: "b2", Weight: 0, Engine: backendEngine},
		},
		time.Second, 1, time.Minute, 5, nil, nil,
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

	backends := []*wrrBackend{
		{name: "b1"},
		{name: "b2"},
		{name: "b3"},
		{name: "b4"},
		{name: "b5"},
	}
	lb := &ShardKeyWeightedRoundRobin{
		backends:          backends,
		redisClient:       client,
		minWeightMultiple: 5,
	}

	ctx := context.Background()
	now := time.Now().Unix()

	// k1 has 2 members
	_, err := client.ZAdd(ctx, "k1",
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
		minWeightMultiple: 5,
	}

	name, eng := lb.GetNextEngine("b")
	assert.Equal(t, "b", name)
	assert.Equal(t, e2, eng)

	// We don't assert exact weights here because the initial currentWeight is randomized in constructor.
	// Just ensure weights remain finite and have been updated.
	assert.NotZero(t, lb.backends[1].currentWeight)
	assert.NotZero(t, lb.backends[0].currentWeight)
}

func TestGetNextEngine_FallbackWhenPrioritizedBelowThreshold(t *testing.T) {
	ePrior := &stubEngine{}
	eOther := &stubEngine{}

	// totalWeight = 1 + 1 = 2, threshold = -10
	// After adding weight, currentWeight of prioritized backend = -100 + 1 = -99 < threshold
	lb := &ShardKeyWeightedRoundRobin{
		backends: []*wrrBackend{
			{name: "prior", weight: 1, engine: ePrior, currentWeight: -100},
			{name: "other", weight: 1, engine: eOther, currentWeight: 0},
		},
		minWeightMultiple: 5,
	}

	name, eng := lb.GetNextEngine("prior")
	assert.Equal(t, "other", name)
	assert.Equal(t, eOther, eng)
}

func TestGetNextEngine_NoEligibleBackend(t *testing.T) {
	lb := &ShardKeyWeightedRoundRobin{
		backends: []*wrrBackend{
			// weight <= 0 -> ignored
			{name: "a", weight: 0, engine: &stubEngine{}, currentWeight: 0},
		},
		minWeightMultiple: 5,
	}

	name, eng := lb.GetNextEngine("")
	assert.Equal(t, "", name)
	assert.Nil(t, eng)
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
		5,
		func(req *octollm.Request) []string {
			return []string{"shard-key-1"}
		},
		client,
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
	members, err := client.ZRange(ctx, "shard-key-1", 0, -1).Result()
	assert.NoError(t, err)
	if assert.Len(t, members, 1) {
		assert.Equal(t, "backend1", members[0])
	}

	ttl, err := client.TTL(ctx, "shard-key-1").Result()
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
		retryTimeout:      0,
		retryMaxCount:     10,
		minWeightMultiple: 5,
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
		retryTimeout:      time.Hour,
		retryMaxCount:     2,
		minWeightMultiple: 5,
	}

	req := newTestRequest(t)
	resp, err := lb.Process(req)
	assert.Error(t, err)
	if assert.NotNil(t, resp) {
		assert.Equal(t, 500, resp.StatusCode)
	}
	assert.Equal(t, 2, failEngine.callCount)
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
		minWeightMultiple: 5,
	}

	const totalPicks = 10 // sum of weights
	counts := map[string]int{}
	sequence := make([]string, 0, totalPicks)

	for i := 0; i < totalPicks; i++ {
		name, eng := lb.GetNextEngine("")
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

func TestGetNextEngine_ShardHitEventuallyFallsBackWhenBelowThreshold(t *testing.T) {
	// Three backends with weights 2,3,5. Prioritized shard always points to A.
	// With a relatively small minWeightMultiple, A will eventually fall below threshold
	// and the picker should start choosing other backends even when shard_key keeps hitting A.
	lb := &ShardKeyWeightedRoundRobin{
		backends: []*wrrBackend{
			{name: "A", weight: 2, engine: &stubEngine{}, currentWeight: 0},
			{name: "B", weight: 3, engine: &stubEngine{}, currentWeight: 0},
			{name: "C", weight: 5, engine: &stubEngine{}, currentWeight: 0},
		},
		minWeightMultiple: 1, // threshold = -1 * totalWeight
	}

	const iterations = 10
	sawA := false
	sawNonAAfterA := false

	for i := 0; i < iterations; i++ {
		name, eng := lb.GetNextEngine("A")
		assert.NotEmpty(t, name)
		assert.NotNil(t, eng)

		if name == "A" {
			sawA = true
		}
		if sawA && name != "A" {
			sawNonAAfterA = true
			break
		}
	}

	assert.True(t, sawA, "shard-hit backend A should be selected at least once")
	assert.True(t, sawNonAAfterA, "after enough hits, selection should eventually fall back to a non-A backend even when shard key keeps hitting A")
}
