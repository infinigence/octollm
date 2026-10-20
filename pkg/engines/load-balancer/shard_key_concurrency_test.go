package loadbalancer

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/engines/limiter"
	"github.com/infinigence/octollm/pkg/engines/load-balancer/common"
	ruleengine "github.com/infinigence/octollm/pkg/engines/rule-engine"
	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/exprenv"
	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
)

// Tests with a non-nil affinityProvider exercise shard-key prioritized routing.
// nil affinityProvider skips that path and tests fallback selection only
// (lowest concurrency ratio, limiter failover, etc.).

func TestMain(m *testing.M) {
	exprenv.RegisterDefaultExtractor("message5HashArray", &ruleengine.Message5HashArrayExtractor{})
	m.Run()
}

type backendConfig struct {
	svcName              string
	rate                 int
	concurrencyIndicator *atomic.Int32
}

func getBackendItemsFromConfigs(t *testing.T, rd *redis.Client, cfgs []backendConfig) []BackendItem {
	t.Helper()

	items := make([]BackendItem, len(cfgs))
	for i, cfg := range cfgs {
		name := cfg.svcName
		items[i] = BackendItem{
			Name:   name,
			Weight: cfg.rate, // weight is not relevant for this test
			Engine: buildBackendWithLimiter(t, rd, cfg),
			ConcurrencyKeyFn: func(*octollm.Request) string {
				return gpt4ConcurrencyKey(name)
			},
		}
	}
	return items
}

func buildTrackingEngine(cur *atomic.Int32) octollm.Engine {
	return octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		cur.Add(1)
		defer cur.Add(-1)
		time.Sleep(500 * time.Millisecond)
		return &octollm.Response{StatusCode: 200}, nil
	})
}

func buildBackendWithLimiter(t *testing.T, rd *redis.Client, cfg backendConfig) octollm.Engine {
	t.Helper()

	next := buildTrackingEngine(cfg.concurrencyIndicator)

	engine, err := limiter.NewConcurrencyColorLimiterEngine(
		rd,
		"concurrency_rate:service:gpt-4:"+cfg.svcName,
		limiter.QoSLimits{
			AvailableTotalMax: []int{cfg.rate},
			PriorityLevelMax:  []int{cfg.rate},
		},
		time.Minute,
		"",
		next,
	)
	require.NoError(t, err)

	return engine
}

func TestShardKeyConcurrency_No_ShardKey(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	var (
		cur1, cur2, cur3 atomic.Int32
	)

	cfgs := []backendConfig{
		{svcName: "svc1", rate: 100, concurrencyIndicator: &cur1},
		{svcName: "svc2", rate: 200, concurrencyIndicator: &cur2},
		{svcName: "svc3", rate: 300, concurrencyIndicator: &cur3},
	}
	items := getBackendItemsFromConfigs(t, rd, cfgs)

	t.Run("no shard keys results in round robin behavior", func(t *testing.T) {
		lb, err := NewShardKeyConcurrency(
			withGPT4ConcurrencyKeys(items),
			time.Second, 3, nil, rd,
			nil,
		)
		require.NoError(t, err)

		var wg sync.WaitGroup
		for range 150 {
			req := testhelper.CreateTestRequest()
			wg.Go(func() {
				_, err := lb.Process(req)
				assert.NoError(t, err)
			})
			time.Sleep(1 * time.Millisecond)
		}

		cur1Val := cur1.Load()
		cur2Val := cur2.Load()
		cur3Val := cur3.Load()

		require.True(t, cur1Val > 0, "expected some concurrency on svc1")
		assert.InDelta(t, float64(cur2Val)/float64(cur1Val), 2.0, 0.4, "expected concurrency ratio to reflect rate limits (2:1)")
		assert.InDelta(t, float64(cur3Val)/float64(cur1Val), 3.0, 0.4, "expected concurrency ratio to reflect rate limits (3:1)")

		wg.Wait()

		cur1Val = cur1.Load()
		cur2Val = cur2.Load()
		cur3Val = cur3.Load()
		assert.Equal(t, int32(0), cur1Val, "expected no concurrency on svc1 after all requests complete")
		assert.Equal(t, int32(0), cur2Val, "expected no concurrency on svc2 after all requests complete")
		assert.Equal(t, int32(0), cur3Val, "expected no concurrency on svc3 after all requests complete")
	})

	t.Run("two lbs interleaved", func(t *testing.T) {
		lb1, err := NewShardKeyConcurrency(
			withGPT4ConcurrencyKeys(items),
			time.Second, 3, nil, rd,
			nil,
		)
		require.NoError(t, err)

		lb2, err := NewShardKeyConcurrency(
			withGPT4ConcurrencyKeys(items[1:]),
			time.Second, 3, nil, rd,
			nil,
		)
		require.NoError(t, err)

		var wg sync.WaitGroup
		for i := range 300 {
			req := testhelper.CreateTestRequest()
			wg.Go(func() {
				if i%2 == 0 {
					_, err := lb2.Process(req)
					assert.NoError(t, err)
				} else {
					_, err := lb1.Process(req)
					assert.NoError(t, err)
				}
			})
			time.Sleep(1 * time.Millisecond)
		}

		cur1Val := cur1.Load()
		cur2Val := cur2.Load()
		cur3Val := cur3.Load()

		require.True(t, cur1Val > 0, "expected some concurrency on svc1")
		assert.InDelta(t, float64(cur2Val)/float64(cur1Val), 2.0, 0.2, "expected concurrency ratio to reflect rate limits (2:1)")
		assert.InDelta(t, float64(cur3Val)/float64(cur1Val), 3.0, 0.2, "expected concurrency ratio to reflect rate limits (3:1)")

		wg.Wait()

		cur1Val = cur1.Load()
		cur2Val = cur2.Load()
		cur3Val = cur3.Load()
		assert.Equal(t, int32(0), cur1Val, "expected no concurrency on svc1 after all requests complete")
		assert.Equal(t, int32(0), cur2Val, "expected no concurrency on svc2 after all requests complete")
		assert.Equal(t, int32(0), cur3Val, "expected no concurrency on svc3 after all requests complete")
	})
}

func TestShardKeyConcurrency_Failover(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	retryCount := 0
	nextEngine := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		retryCount++
		return nil, fmt.Errorf("engine failure")
	})

	items := []BackendItem{
		{Name: "svc1", Weight: 100, Engine: nextEngine},
		{Name: "svc2", Weight: 100, Engine: nextEngine},
		{Name: "svc3", Weight: 100, Engine: nextEngine},
	}

	lb, err := NewShardKeyConcurrency(
		withGPT4ConcurrencyKeys(items),
		time.Second, 3, nil, rd,
		nil,
	)
	require.NoError(t, err)

	req := testhelper.CreateTestRequest()
	_, err = lb.Process(req)
	assert.Error(t, err)
	assert.Equal(t, 3, retryCount, "expected to retry on all 3 backends before failing")
}

func TestShardKeyConcurrency_NotRetriableError(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	callCount := 0
	// A 413 upstream error is not retriable: Process must return after a single
	// engine call instead of failing over to the other backends.
	nextEngine := octollm.EngineFunc(func(req *octollm.Request) (*octollm.Response, error) {
		callCount++
		return nil, &errutils.UpstreamRespError{StatusCode: http.StatusRequestEntityTooLarge}
	})

	items := []BackendItem{
		{Name: "svc1", Weight: 100, Engine: nextEngine},
		{Name: "svc2", Weight: 100, Engine: nextEngine},
		{Name: "svc3", Weight: 100, Engine: nextEngine},
	}

	lb, err := NewShardKeyConcurrency(
		withGPT4ConcurrencyKeys(items),
		time.Second, 3, nil, rd,
		nil,
	)
	require.NoError(t, err)

	_, err = lb.Process(testhelper.CreateTestRequest())
	assert.Error(t, err)
	assert.Equal(t, 1, callCount, "expected no retry on a non-retriable error")
}

func TestShardKeyConcurrency_ShardKey(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	var (
		cur1, cur2, cur3 atomic.Int32
	)

	cfgs := []backendConfig{
		{svcName: "svc1", rate: 100, concurrencyIndicator: &cur1},
		{svcName: "svc2", rate: 200, concurrencyIndicator: &cur2},
		{svcName: "svc3", rate: 300, concurrencyIndicator: &cur3},
	}
	items := getBackendItemsFromConfigs(t, rd, cfgs)

	shardKeyListGetter := func(req *octollm.Request) []string {
		env := exprenv.Get(req)
		return env.ReqEnv.Feature("message5HashArray").([]string)
	}

	provider := newPrimaryShardKeyProvider(t, shardKeyListGetter, rd, "", time.Minute, items)
	lb, err := NewShardKeyConcurrency(
		withGPT4ConcurrencyKeys(items),
		time.Second, 3, provider, rd,
		nil,
	)
	require.NoError(t, err)

	var wg sync.WaitGroup

	body2 := &openai.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []*openai.Message{
			{
				Role:    "system",
				Content: openai.MessageContentString("You are a helpful assistant."),
			},
			{
				Role:    "user",
				Content: openai.MessageContentString("Hello, world!"),
			},
		},
	}

	wg.Go(func() {
		req := testhelper.CreateTestRequest()
		_, err := lb.Process(req)
		assert.NoError(t, err)
	})

	time.Sleep(100 * time.Millisecond)

	wg.Go(func() {
		req := testhelper.CreateTestRequest(testhelper.WithBody(body2))
		_, err := lb.Process(req)
		assert.NoError(t, err)
	})

	time.Sleep(100 * time.Millisecond)

	cur1Val := cur1.Load()
	cur2Val := cur2.Load()
	cur3Val := cur3.Load()

	assert.ElementsMatch(t, []int32{1, 1, 0}, []int32{cur1Val, cur2Val, cur3Val},
		"expected 1 in-flight for shard key 1, 1 for shard key 2, 0 for svc3")

	wg.Wait()

	for range 20 {
		req := testhelper.CreateTestRequest()
		go func() {
			_, err := lb.Process(req)
			assert.NoError(t, err)
		}()
		time.Sleep(1 * time.Millisecond)
	}

	for range 10 {
		req := testhelper.CreateTestRequest(testhelper.WithBody(body2))
		go func() {
			_, err := lb.Process(req)
			assert.NoError(t, err)
		}()
		time.Sleep(1 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	cur1Val = cur1.Load()
	cur2Val = cur2.Load()
	cur3Val = cur3.Load()

	// The initial routing is non-deterministic (first requests use concurrency-based LB with
	// empty Redis), so we don't assert which svc holds which count — only that the distribution
	// across all three backends matches the expected {20, 10, 0} multiset.
	assert.ElementsMatch(t, []int32{20, 10, 0}, []int32{cur1Val, cur2Val, cur3Val},
		"expected 20 in-flight for shard key 1, 10 for shard key 2, 0 for svc3")
}

func TestShardKeyConcurrency_maxConcurrencyFn(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	next := octollm.EngineFunc(func(_ *octollm.Request) (*octollm.Response, error) {
		return &octollm.Response{StatusCode: 200}, nil
	})

	t.Run("positive override via maxConcurrencyFn", func(t *testing.T) {
		var seenBackend atomic.Value
		items := []BackendItem{{
			Name:   "only",
			Weight: 10,
			Engine: next,
			MaxConcurrencyFn: func(_ *octollm.Request) int {
				seenBackend.Store("only")
				return 10
			},
		}}
		lb, err := NewShardKeyConcurrency(
			withTestConcurrencyKeys(items),
			time.Second, 3, nil, rd,
			nil,
		)
		require.NoError(t, err)
		req := testhelper.CreateTestRequest()
		resp, err := lb.Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "only", seenBackend.Load().(string))
	})

	t.Run("nil fn ignores dynamic denominator", func(t *testing.T) {
		items := []BackendItem{{
			Name:   "only",
			Weight: 10,
			Engine: next,
		}}
		lb, err := NewShardKeyConcurrency(
			withTestConcurrencyKeys(items),
			time.Second, 3, nil, rd,
			nil,
		)
		require.NoError(t, err)
		req := testhelper.CreateTestRequest()
		resp, err := lb.Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
	})

	t.Run("fn return zero excludes single backend", func(t *testing.T) {
		var calls atomic.Int32
		items := []BackendItem{{
			Name:   "only",
			Weight: 10,
			Engine: next,
			MaxConcurrencyFn: func(_ *octollm.Request) int {
				calls.Add(1)
				return 0
			},
		}}
		lb, err := NewShardKeyConcurrency(
			withTestConcurrencyKeys(items),
			time.Second, 3, nil, rd,
			nil,
		)
		require.NoError(t, err)
		_, err = lb.Process(testhelper.CreateTestRequest())
		require.Error(t, err)
		assert.Positive(t, calls.Load())
	})

	t.Run("fn return zero skips backend two candidates", func(t *testing.T) {
		items := []BackendItem{
			{
				Name: "blocked", Weight: 100, Engine: next,
				MaxConcurrencyFn: func(_ *octollm.Request) int { return 0 },
			},
			{
				Name: "open", Weight: 50, Engine: next,
				MaxConcurrencyFn: func(_ *octollm.Request) int { return 50 },
			},
		}
		lb, err := NewShardKeyConcurrency(
			withTestConcurrencyKeys(items),
			time.Second, 3, nil, rd,
			nil,
		)
		require.NoError(t, err)
		req := testhelper.CreateTestRequest()
		resp, err := lb.Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		mv, mdOK := req.GetMetadataValue(backendName)
		require.True(t, mdOK)
		nameVal, strOK := mv.(string)
		require.True(t, strOK)
		assert.Equal(t, "open", nameVal)
	})

	t.Run("fn return zero still tries prioritized shard-key backend", func(t *testing.T) {
		items := []BackendItem{
			{
				Name: "blocked", Weight: 100, Engine: next,
				MaxConcurrencyFn: func(_ *octollm.Request) int { return 0 },
			},
			{
				Name: "open", Weight: 50, Engine: next,
				MaxConcurrencyFn: func(_ *octollm.Request) int { return 50 },
			},
		}
		const keyPrefix = "pfx-prioritized-zero"
		const shardKey = "sk"
		err := rd.ZAdd(t.Context(), keyPrefix+":"+shardKey, redis.Z{
			Score:  1,
			Member: "blocked",
		}).Err()
		require.NoError(t, err)

		provider := newPrimaryShardKeyProvider(
			t,
			func(_ *octollm.Request) []string { return []string{shardKey} },
			rd,
			keyPrefix,
			time.Minute,
			items,
		)
		lb, err := NewShardKeyConcurrency(
			withTestConcurrencyKeys(items),
			time.Second, 3, provider, rd,
			nil,
		)
		require.NoError(t, err)
		req := testhelper.CreateTestRequest()
		resp, err := lb.Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		mv, mdOK := req.GetMetadataValue(backendName)
		require.True(t, mdOK)
		nameVal, strOK := mv.(string)
		require.True(t, strOK)
		assert.Equal(t, "blocked", nameVal)
	})

	t.Run("weight zero allowed when maxConcurrencyFn set", func(t *testing.T) {
		items := []BackendItem{{
			Name:   "dyn",
			Weight: 0,
			Engine: next,
			MaxConcurrencyFn: func(_ *octollm.Request) int {
				return 10
			},
		}}
		lb, err := NewShardKeyConcurrency(
			withTestConcurrencyKeys(items),
			time.Second, 3, nil, rd,
			nil,
		)
		require.NoError(t, err)
		req := testhelper.CreateTestRequest()
		resp, err := lb.Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
	})

	t.Run("weight zero fn returns zero cannot select", func(t *testing.T) {
		items := []BackendItem{{
			Name:             "bad",
			Weight:           0,
			Engine:           next,
			MaxConcurrencyFn: func(_ *octollm.Request) int { return 0 },
		}}
		lb, err := NewShardKeyConcurrency(
			withTestConcurrencyKeys(items),
			time.Second, 3, nil, rd,
			nil,
		)
		require.NoError(t, err)
		_, err = lb.Process(testhelper.CreateTestRequest())
		require.Error(t, err)
	})

	t.Run("weight zero without fn skipped empty", func(t *testing.T) {
		items := []BackendItem{{
			Name:   "skip",
			Weight: 0,
			Engine: next,
		}}
		_, err := NewShardKeyConcurrency(
			items,
			time.Second, 3, nil, rd,
			nil,
		)
		require.Error(t, err)
	})

	t.Run("missing concurrency key function", func(t *testing.T) {
		items := []BackendItem{{
			Name:   "only",
			Weight: 10,
			Engine: next,
		}}
		_, err := NewShardKeyConcurrency(items, time.Second, 3, nil, rd, nil)
		require.Error(t, err)
	})
}

// seedConcurrency seeds the concurrency ZSET for a backend with n distinct members, so the load
// balancer's ZCard reads back exactly n in-flight requests.
func seedConcurrency(t *testing.T, rd *redis.Client, backendName string, n int) {
	t.Helper()
	if n <= 0 {
		return
	}
	members := make([]redis.Z, n)
	for i := range members {
		members[i] = redis.Z{Score: float64(i), Member: fmt.Sprintf("m%d", i)}
	}
	require.NoError(t, rd.ZAdd(t.Context(), testConcurrencyKey(backendName), members...).Err())
}

func TestShardKeyConcurrency_HitKindAndLimiterFailover(t *testing.T) {
	selectedBackend := func(t *testing.T, req *octollm.Request) string {
		t.Helper()
		mv, ok := req.GetMetadataValue(backendName)
		require.True(t, ok)
		name, ok := mv.(string)
		require.True(t, ok)
		return name
	}

	t.Run("strong affinity is tried even when busy and writes strong", func(t *testing.T) {
		mr := miniredis.RunT(t)
		defer mr.Close()
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		hot := &captureHitKindEngine{}
		cool := &captureHitKindEngine{}
		items := []BackendItem{
			{Name: "hot", Weight: 10, Engine: hot},
			{Name: "cool", Weight: 10, Engine: cool},
		}
		seedConcurrency(t, rd, "hot", 10)

		const keyPrefix = "hk-strong"
		require.NoError(t, rd.ZAdd(t.Context(), keyPrefix+":sk2", redis.Z{Score: 1, Member: "hot"}).Err())
		provider := newPrimaryShardKeyProvider(t, func(_ *octollm.Request) []string { return []string{"sk0", "sk1", "sk2"} }, rd, keyPrefix, time.Minute, items)
		lb, err := NewShardKeyConcurrency(withTestConcurrencyKeys(items), time.Second, 3, provider, rd, nil)
		require.NoError(t, err)

		req := testhelper.CreateTestRequest()
		resp, err := lb.Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "hot", selectedBackend(t, req))
		assert.Equal(t, []common.CacheHitKind{common.CacheHitStrong}, hot.kindsCopy())
		assert.Empty(t, cool.kindsCopy())
	})

	t.Run("weak affinity limiter reject failovers and second attempt is miss", func(t *testing.T) {
		mr := miniredis.RunT(t)
		defer mr.Close()
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		hotCap := &captureHitKindEngine{}
		cool := &captureHitKindEngine{}
		items := []BackendItem{
			{Name: "hot", Weight: 10, Engine: rejectUnlessStrong(hotCap)},
			{Name: "cool", Weight: 10, Engine: cool},
		}

		const keyPrefix = "hk-weak"
		require.NoError(t, rd.ZAdd(t.Context(), keyPrefix+":sk2", redis.Z{Score: 1, Member: "hot"}).Err())
		provider := newPrimaryShardKeyProviderWithPolicy(t, func(_ *octollm.Request) []string {
			return []string{"sk0", "sk1", "sk2"}
		}, rd, keyPrefix, time.Minute, items, StrongHitPolicyLeaf)
		lb, err := NewShardKeyConcurrency(withTestConcurrencyKeys(items), time.Second, 3, provider, rd, nil)
		require.NoError(t, err)

		req := testhelper.CreateTestRequest()
		resp, err := lb.Process(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "cool", selectedBackend(t, req))
		assert.Equal(t, []common.CacheHitKind{common.CacheHitWeak}, hotCap.kindsCopy())
		assert.Equal(t, []common.CacheHitKind{common.CacheMiss}, cool.kindsCopy())
	})

	t.Run("all limiter rejects return last limiter error", func(t *testing.T) {
		mr := miniredis.RunT(t)
		defer mr.Close()
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		reject := octollm.EngineFunc(func(_ *octollm.Request) (*octollm.Response, error) {
			return nil, limiter.ErrRateLimitReached
		})
		items := []BackendItem{
			{Name: "a", Weight: 10, Engine: reject},
			{Name: "b", Weight: 10, Engine: reject},
		}
		lb, err := NewShardKeyConcurrency(withTestConcurrencyKeys(items), time.Second, 3, nil, rd, nil)
		require.NoError(t, err)
		resp, err := lb.Process(testhelper.CreateTestRequest())
		require.ErrorIs(t, err, limiter.ErrRateLimitReached)
		assert.Nil(t, resp)
	})

	t.Run("limiter reject after retry timeout does not call the next backend", func(t *testing.T) {
		mr := miniredis.RunT(t)
		defer mr.Close()
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		var calls atomic.Int32
		eng := octollm.EngineFunc(func(_ *octollm.Request) (*octollm.Response, error) {
			if calls.Add(1) == 1 {
				time.Sleep(30 * time.Millisecond)
				return nil, limiter.ErrRateLimitReached
			}
			return &octollm.Response{StatusCode: 200}, nil
		})
		items := []BackendItem{
			{Name: "a", Weight: 10, Engine: eng},
			{Name: "b", Weight: 10, Engine: eng},
		}
		lb, err := NewShardKeyConcurrency(withTestConcurrencyKeys(items), 5*time.Millisecond, 10, nil, rd, nil)
		require.NoError(t, err)

		resp, err := lb.Process(testhelper.CreateTestRequest())
		require.ErrorIs(t, err, limiter.ErrRateLimitReached)
		assert.Nil(t, resp)
		assert.Equal(t, int32(1), calls.Load())
	})

	t.Run("limiter timeout keeps an earlier upstream error", func(t *testing.T) {
		mr := miniredis.RunT(t)
		defer mr.Close()
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		upstreamErr := fmt.Errorf("upstream temporarily unavailable")
		var cCalls atomic.Int32
		items := []BackendItem{
			{Name: "a", Weight: 10, Engine: octollm.EngineFunc(func(*octollm.Request) (*octollm.Response, error) {
				return nil, upstreamErr
			})},
			{Name: "b", Weight: 10, Engine: octollm.EngineFunc(func(*octollm.Request) (*octollm.Response, error) {
				time.Sleep(30 * time.Millisecond)
				return nil, limiter.ErrRateLimitReached
			})},
			{Name: "c", Weight: 10, Engine: octollm.EngineFunc(func(*octollm.Request) (*octollm.Response, error) {
				cCalls.Add(1)
				return &octollm.Response{StatusCode: 200}, nil
			})},
		}
		seedConcurrency(t, rd, "b", 1)
		seedConcurrency(t, rd, "c", 100)
		lb, err := NewShardKeyConcurrency(withTestConcurrencyKeys(items), 5*time.Millisecond, 10, nil, rd, nil)
		require.NoError(t, err)

		resp, err := lb.Process(testhelper.CreateTestRequest())
		require.ErrorIs(t, err, upstreamErr)
		require.NotErrorIs(t, err, limiter.ErrRateLimitReached)
		assert.Nil(t, resp)
		assert.Equal(t, int32(0), cCalls.Load())
	})

	t.Run("real upstream error wins over later limiter rejects", func(t *testing.T) {
		mr := miniredis.RunT(t)
		defer mr.Close()
		rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		upstreamErr := fmt.Errorf("upstream temporarily unavailable")
		failEngine := octollm.EngineFunc(func(_ *octollm.Request) (*octollm.Response, error) {
			return nil, upstreamErr
		})
		reject := octollm.EngineFunc(func(_ *octollm.Request) (*octollm.Response, error) {
			return nil, limiter.ErrRateLimitReached
		})
		items := []BackendItem{
			{Name: "strong", Weight: 10, Engine: failEngine},
			{Name: "weak1", Weight: 10, Engine: reject},
			{Name: "weak2", Weight: 10, Engine: reject},
		}

		const keyPrefix = "hk-real"
		require.NoError(t, rd.ZAdd(t.Context(), keyPrefix+":sk2", redis.Z{Score: 1, Member: "strong"}).Err())
		require.NoError(t, rd.ZAdd(t.Context(), keyPrefix+":sk0",
			redis.Z{Score: 1, Member: "weak1"}, redis.Z{Score: 2, Member: "weak2"}).Err())
		provider := newPrimaryShardKeyProvider(t, func(_ *octollm.Request) []string { return []string{"sk0", "sk1", "sk2"} }, rd, keyPrefix, time.Minute, items)
		lb, err := NewShardKeyConcurrency(withTestConcurrencyKeys(items), time.Second, 3, provider, rd, nil)
		require.NoError(t, err)

		resp, err := lb.Process(testhelper.CreateTestRequest())
		require.ErrorIs(t, err, upstreamErr)
		require.NotErrorIs(t, err, limiter.ErrRateLimitReached)
		assert.Nil(t, resp)
	})
}
