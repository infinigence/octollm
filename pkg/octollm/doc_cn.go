//go:build ignore

/*
包 octollm 定义了构建可组合 LLM 网关的核心接口与类型。

# Engine

Engine 是核心抽象。每个处理步骤——协议转换、规则匹配、重写、负载均衡、上游通信——
都实现为 Engine：

	type Engine interface {
	    Process(req *Request) (*Response, error)
	}

Engine 通过链式组合工作：外层 Engine 接收 Request，执行自身逻辑，然后通过持有的
内部引用（约定命名为 "next"）委托给下一个 Engine。链的末端是实际与上游服务通信的
客户端 Engine。

EngineFunc 可将普通函数适配为 Engine：

	var myEngine octollm.EngineFunc = func(req *octollm.Request) (*octollm.Response, error) { … }

# Request

Request 携带 Engine 所需的一切：HTTP 方法、API 格式、URL、查询参数、请求头以及
懒加载的请求体（UnifiedBody）。它还持有 context 和线程安全的元数据映射。

从 HTTP 请求创建 Request：

	req := octollm.NewRequestWithBodyParser(httpReq, octollm.APIFormatChatCompletions, parser)

创建空 Request 用于内部使用：

	req := octollm.NewEmptyRequest(ctx)

在 Engine.Process 中修改 Request：

可以直接修改原始 Request——修改会在后续 Engine 链中持续可见。如果希望修改仅对内部
Engine 生效、不暴露给外层调用者，则应通过浅拷贝隔离：

	req2 := req.WithContext(newCtx) // 浅拷贝，替换 context
	req3 := req.WithBody(newBody)   // 浅拷贝，替换 body

Metadata 用于在 Engine 链中传递引擎专用数据（重试次数、选中的后端、耗时信息等）：

	req.SetMetadataValue("retry", 2)
	val, ok := req.GetMetadataValue("retry")

Context 值可通过类型安全的辅助函数获取：

	model, ok := octollm.GetCtxValue[string](req, someKey)

不要假设 Request.Body 会被框架 Close。Request.Body.Close() 不保证被调用；
只有 Response 的 body 和 stream 有生命周期管理义务（见下文）。

# Response

Response 要么是非流式（设置 Body），要么是流式（设置 Stream），不会同时存在两者。

非流式：

	resp := octollm.NewNonStreamResponse(200, header, body)

流式：

	resp := octollm.NewStreamResponse(200, header, streamChan)

资源生命周期：

  - 非流式：消费 Response 的一方在使用完毕后必须调用 resp.Body.Close()。
  - 流式：消费 Response 的一方在读完 channel 后必须调用 resp.Stream.Close()。

重要：Close 通常不应在 Process 返回时调用。Process 返回后，可能仍有其他 goroutine
在读取 resp.Stream 或 resp.Body。Handler 层会在响应完全消费后通过 defer 管理 Close。

使用 OnClose 注册在 Close 时执行的清理回调：

	resp.Body.OnClose(func() { releaseResource() })
	resp.Stream.OnClose(func() { cancelContext() })

# UnifiedBody

UnifiedBody 是 Request、Response 和 StreamChunk 共用的懒加载、线程安全请求体抽象。
支持三种构造方式：

  - 从 io.ReadCloser（流式源）：NewBodyFromReader(r, parser)
  - 从原始字节：                NewBodyFromBytes(data, parser)
  - 从已解析数据：              NewBodyFromParsed(obj, parser)

懒加载访问：

  - Parsed() 从底层 reader（如有）读取数据，缓存字节，执行一次解析，返回解析结果。
    后续调用直接返回缓存值。
  - Bytes() 返回原始字节表示。若已解析数据被标记为脏（isDirty），会通过 Parser
    重新序列化。
  - Reader() 返回 io.ReadCloser。若原始 reader 仍可用则直接返回；否则从缓存字节
    创建新 reader。

线程安全：

所有公开方法（Parsed、Bytes、Reader、Close、OnClose）受内部 mutex 保护，
UnifiedBody 可安全地从多个 goroutine 并发使用。

不可变性：

UnifiedBody 在逻辑上是不可变的。如需修改 body 内容，应创建新的 UnifiedBody，
而非修改现有实例。内部私有状态转换（reader → bytes → parsed）会发生，但对外可见
的内容不会改变，除非构造新的 UnifiedBody。

# StreamChan 与 StreamChunk

StreamChan 封装了 StreamChunk 的只读 channel 以及 Close 函数。每个 StreamChunk
携带可选的 SSE 元数据（event 类型、id）和作为 chunk 载荷的 UnifiedBody。

通过 range 消费 chunk：

	for chunk := range resp.Stream.Chan() {
	    data, _ := chunk.Body.Bytes()
	    // 处理 data…
	}

消费完毕后务必调用 resp.Stream.Close() 以释放上游资源。

# SafeGo

在请求处理中启动 goroutine 时（如 fire-and-forget 流量复制、异步流排空），应使用
SafeGo 而非裸 go 语句：

	octollm.SafeGo(req, func() {
	    // 该 goroutine 在 panic 时会被 recover；gin.Recovery 仅覆盖 handler goroutine
	})

# Parser

Parser 是 body 内容编解码接口：

	type Parser interface {
	    Parse(data []byte) (any, error)
	    Serialize(data any) ([]byte, error)
	}

JSONParser[T] 提供了泛型 JSON 实现：

	parser := &octollm.JSONParser[openai.ChatCompletionRequest]{}
*/
package octollm
