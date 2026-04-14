package loadbalancer

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/engines/limiter"
	ruleengine "github.com/infinigence/octollm/pkg/engines/rule-engine"
	"github.com/infinigence/octollm/pkg/exprenv"
	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
)

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
		items[i] = BackendItem{
			Name:   cfg.svcName,
			Weight: cfg.rate, // weight is not relevant for this test
			Engine: buildBackendWithLimiter(t, rd, cfg),
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
		[]int{cfg.rate},
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
			items,
			time.Second, 3, time.Minute, nil, rd, "",
			func(_ *octollm.Request, backendName string) string {
				return "concurrency_rate:service:gpt-4:" + backendName + ":tier_0"
			},
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

	t.Run("two lbs interleaved", func(t *testing.T) {
		lb1, err := NewShardKeyConcurrency(
			items,
			time.Second, 3, time.Minute, nil, rd, "",
			func(_ *octollm.Request, backendName string) string {
				return "concurrency_rate:service:gpt-4:" + backendName + ":tier_0"
			},
		)
		require.NoError(t, err)

		lb2, err := NewShardKeyConcurrency(
			items[1:],
			time.Second, 3, time.Minute, nil, rd, "",
			func(_ *octollm.Request, backendName string) string {
				return "concurrency_rate:service:gpt-4:" + backendName + ":tier_0"
			},
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
		items,
		time.Second, 3, time.Minute, nil, rd, "",
		func(req *octollm.Request, backendName string) string {
			return "concurrency_rate:service:gpt-4:" + backendName + ":tier_0"
		},
	)
	require.NoError(t, err)

	req := testhelper.CreateTestRequest()
	_, err = lb.Process(req)
	assert.Error(t, err)
	assert.Equal(t, 3, retryCount, "expected to retry on all 3 backends before failing")
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

	lb, err := NewShardKeyConcurrency(
		items,
		time.Second, 3, time.Minute, shardKeyListGetter, rd, "",
		func(req *octollm.Request, backendName string) string {
			return "concurrency_rate:service:gpt-4:" + backendName + ":tier_0"
		},
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
		_, err = lb.Process(req)
		assert.NoError(t, err)
	})

	time.Sleep(100 * time.Millisecond)

	wg.Go(func() {
		req := testhelper.CreateTestRequest(testhelper.WithBody(body2))
		_, err = lb.Process(req)
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
