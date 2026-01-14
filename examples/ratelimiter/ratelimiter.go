package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/infinigence/octollm/pkg/ratelimiter"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

func main() {
	// 设置日志级别
	logrus.SetLevel(logrus.InfoLevel)

	// 连接 Redis (默认 localhost:6379)
	// 使用 Docker 启动 Redis: docker run -d -p 6379:6379 redis:latest
	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "", // 无密码
		DB:       0,  // 使用默认数据库
	})

	// 测试 Redis 连接
	ctx := context.Background()
	_, err := rdb.Ping(ctx).Result()
	if err != nil {
		log.Fatalf("无法连接到 Redis: %v\n请确保 Redis 已启动: docker run -d -p 6379:6379 redis:latest", err)
	}
	fmt.Println("✓ Redis 连接成功")

	// 示例1: 使用 ColorLimiter 进行染色
	fmt.Println("\n=== 示例1: ColorLimiter 染色示例 ===")
	ThreeColorAllPassExample(ctx, rdb)

	// 等待一下，清理数据
	time.Sleep(2 * time.Second)

	// 示例2: 使用 RateLimiter 进行限流
	fmt.Println("\n=== 示例2: RateLimiter 限流示例 ===")
	FourRateAllPassExample(ctx, rdb)

	// 示例3: 组合使用 ColorLimiter + RateLimiter
	fmt.Println("\n=== 示例3: ColorLimiter + RateLimiter 组合使用（3color, 4rate） ===")
	ThreeColorFourRateLimitedExample(ctx, rdb)

	// 等待一下，清理数据
	time.Sleep(2 * time.Second)

	fmt.Println("\n=== 示例4: ColorLimiter + RateLimiter 组合使用（4color, 3rate，小限制值） ===")
	FourColorThreeRateLimitedExample(ctx, rdb)

	fmt.Println("\n所有示例执行完成！")
}

// ThreeColorAllPassExample: ColorLimiter 染色示例
// 3个颜色优先级，所有请求都能通过
func ThreeColorAllPassExample(ctx context.Context, rdb *redis.Client) {
	// 创建 ColorLimiter
	// rates: [50, 100, 150] 表示：
	// - 并发数 <= 50 时，优先级为 2（最高）
	// - 并发数 <= 100 时，优先级为 1
	// - 并发数 <= 150 时，优先级为 0（最低）
	// - 并发数 > 150 时，被拒绝
	colorLimiter, err := ratelimiter.NewColorLimiter(
		rdb,
		"color_limiter:example",
		[]int{50, 100, 150}, // 递增的 rates
		30*time.Second,      // 30秒过期时间
	)
	if err != nil {
		log.Fatalf("创建 ColorLimiter 失败: %v", err)
	}

	fmt.Println("开始模拟请求...")
	var wg sync.WaitGroup

	// 模拟 10 个并发请求
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			reqCtx := context.Background()
			reqCtx, done, err := colorLimiter.Allow(reqCtx)
			if err != nil {
				fmt.Printf("请求 %d: 被拒绝 - %v\n", id, err)
				return
			}

			// 从 context 中获取优先级（现在 Allow 方法会返回带优先级的 context）
			if p, ok := ratelimiter.GetPriorityFromContext(reqCtx); ok {
				fmt.Printf("请求 %d: 允许通过，已染色，优先级=%d\n", id, p)
			} else {
				fmt.Printf("请求 %d: 允许通过，已染色\n", id)
			}

			// 模拟处理请求
			time.Sleep(100 * time.Millisecond)

			// 请求完成后调用 done 清理
			done()
		}(i)
	}

	wg.Wait()
	fmt.Println("ColorLimiter 示例完成")
}

// FourRateAllPassExample: RateLimiter 限流示例
// 4个限流优先级，所有请求都能通过
func FourRateAllPassExample(ctx context.Context, rdb *redis.Client) {
	// 创建 RateLimiter
	// concurrencyRates: [200, 150, 100, 50] 表示：
	// - 最大并发数为 200
	// - 优先级 0（最低）的并发限制为 50
	// - 优先级 1 的并发限制为 100
	// - 优先级 2 的并发限制为 150
	// - 优先级 3（最高）的并发限制为 200
	rateLimiter, err := ratelimiter.NewRateLimiter(
		rdb,
		"rate_limiter:example",
		[]int{200, 150, 100, 50}, // 递减的 concurrencyRates
		30*time.Second,           // 30秒过期时间
	)
	if err != nil {
		log.Fatalf("创建 RateLimiter 失败: %v", err)
	}

	fmt.Println("开始模拟不同优先级的请求...")
	var wg sync.WaitGroup
	successCount := 0
	failCount := 0
	var mu sync.Mutex

	// 模拟不同优先级的请求
	priorities := []int{0, 1, 2}
	for _, priority := range priorities {
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(p int, id int) {
				defer wg.Done()

				// 设置优先级到 context
				reqCtx := ratelimiter.SetPriorityToContext(ctx, p)

				done, err := rateLimiter.Allow(reqCtx)
				if err != nil {
					mu.Lock()
					failCount++
					mu.Unlock()
					fmt.Printf("请求 [优先级%d-%d]: 被限流 - %v\n", p, id, err)
					return
				}

				mu.Lock()
				successCount++
				mu.Unlock()
				fmt.Printf("请求 [优先级%d-%d]: 允许通过\n", p, id)

				// 模拟处理请求
				time.Sleep(50 * time.Millisecond)

				// 请求完成后调用 done 清理
				done()
			}(priority, i)
		}
	}

	wg.Wait()
	fmt.Printf("RateLimiter 示例完成: 成功 %d, 失败 %d\n", successCount, failCount)
}

// runCombinedExample 运行组合使用示例的通用函数
func runCombinedExample(colorLimiter *ratelimiter.ColorLimiter, rateLimiter *ratelimiter.RateLimiter, requestCount int) (successCount, failCount int) {
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i := 0; i < requestCount; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			reqCtx := context.Background()

			// 步骤1: 使用 ColorLimiter 进行染色（Allow 方法会返回带优先级的 context）
			reqCtx, colorDone, err := colorLimiter.Allow(reqCtx)
			if err != nil {
				mu.Lock()
				failCount++
				mu.Unlock()
				fmt.Printf("请求 %d: ColorLimiter 拒绝 - %v\n", id, err)
				return
			}
			time.Sleep(1 * time.Second)
			defer colorDone()

			// 验证优先级是否正确设置到 context 中
			p, _ := ratelimiter.GetPriorityFromContext(reqCtx)
			fmt.Printf("请求 %d: ColorLimiter 染色完成，优先级=%d\n", id, p)

			// 步骤2: 使用 RateLimiter 进行限流（从 context 中读取优先级）
			rateDone, err := rateLimiter.Allow(reqCtx)
			if err != nil {
				mu.Lock()
				failCount++
				mu.Unlock()
				fmt.Printf("请求 %d: RateLimiter 限流 - %v\n", id, err)
				return
			}
			defer rateDone()

			mu.Lock()
			successCount++
			mu.Unlock()
			fmt.Printf("请求 %d: 通过染色和限流检查\n", id)

			// 模拟处理请求
			time.Sleep(1000 * time.Millisecond)
		}(i)
	}

	wg.Wait()
	return successCount, failCount
}

// ThreeColorFourRateLimitedExample: ColorLimiter + RateLimiter 组合使用
// 3个颜色优先级，4个限流优先级，部分请求会被限制
func ThreeColorFourRateLimitedExample(ctx context.Context, rdb *redis.Client) {
	// 创建 ColorLimiter (3个优先级)
	colorRates := []int{10, 20, 30} // 递增的 rates，调小值以便看到限制
	colorLimiter, err := ratelimiter.NewColorLimiter(
		rdb,
		"combined3color4rate:color",
		colorRates,
		30*time.Second,
	)
	if err != nil {
		log.Fatalf("创建 ColorLimiter 失败: %v", err)
	}

	// 创建 RateLimiter (支持4个优先级，但只用到3个)
	rateLimiter, err := ratelimiter.NewRateLimiter(
		rdb,
		"combined3color4rate:rate",
		[]int{30, 25, 20, 15}, // 递减的 concurrencyRates（支持4个优先级，但实际只用前3个）
		30*time.Second,
	)
	if err != nil {
		log.Fatalf("创建 RateLimiter 失败: %v", err)
	}

	fmt.Printf("配置: ColorLimiter rates=%v (3个优先级), RateLimiter concurrencyRates=%v\n", colorRates, []int{30, 25, 20, 15})
	fmt.Println("开始模拟组合使用场景（发送30个请求，预期会有一些被限制）...")

	successCount, failCount := runCombinedExample(colorLimiter, rateLimiter, 30)
	fmt.Printf("组合使用示例完成: 成功 %d, 失败 %d\n", successCount, failCount)
}

// FourColorThreeRateLimitedExample: ColorLimiter + RateLimiter 组合使用
// 4个颜色优先级，3个限流优先级，部分请求会被限制
func FourColorThreeRateLimitedExample(ctx context.Context, rdb *redis.Client) {
	// 创建 ColorLimiter (4个优先级)
	colorRates := []int{5, 10, 15, 20} // 递增的 rates，更小的值以便看到限制
	colorLimiter, err := ratelimiter.NewColorLimiter(
		rdb,
		"combined4color3rate:color",
		colorRates,
		30*time.Second,
	)
	if err != nil {
		log.Fatalf("创建 ColorLimiter 失败: %v", err)
	}

	// 创建 RateLimiter (支持3个优先级)
	rateLimiter, err := ratelimiter.NewRateLimiter(
		rdb,
		"combined4color3rate:rate",
		[]int{15, 12, 8}, // 递减的 concurrencyRates（支持3个优先级）
		// 注意：4个颜色优先级会被映射到3个限流优先级
		30*time.Second,
	)
	if err != nil {
		log.Fatalf("创建 RateLimiter 失败: %v", err)
	}

	fmt.Printf("配置: ColorLimiter rates=%v (4个优先级), RateLimiter concurrencyRates=%v (3个优先级)\n", colorRates, []int{15, 12, 8})
	fmt.Println("开始模拟组合使用场景（发送25个请求，预期会有一些被限制）...")

	successCount, failCount := runCombinedExample(colorLimiter, rateLimiter, 25)
	fmt.Printf("组合使用示例完成: 成功 %d, 失败 %d\n", successCount, failCount)
}
