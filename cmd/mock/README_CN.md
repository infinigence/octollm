# Mock LLM 服务

一个兼容 OpenAI 协议的 Mock LLM 服务，可模拟聊天补全响应，支持配置延迟、输出长度和错误行为。

## 启动服务

```bash
go run ./cmd/mock
```

默认监听 `:8090`。

## 接口

- `POST /v1/chat/completions` — OpenAI Chat Completions
- `POST /v1/completions` — OpenAI Completions
- `POST /v1/messages` — Anthropic Messages

## Mock 参数

在**最后一条消息**内容的**第一行**写入 URL query string 格式的控制参数，第一行之后的内容作为正常的消息文本。

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `ttft` | Go duration | `100ms` | 首字生成时间 |
| `tpot` | Go duration | `10ms` | 每个 token 的生成时间 |
| `output_len` | int | 初始化文本长度 | 输出 rune 数量（不足时重复初始化文本填充） |
| `input_len` | int | 所有消息的 rune 总数 | usage 中的 prompt_tokens |
| `cached_len` | int | `0` | usage 中的 cached_tokens |
| `http_status` | int | `200` | 返回的 HTTP 状态码，非 200 时在等待 `ttft` 后返回错误 |

## 示例

### 基本请求（无参数）

> **注意：** 默认初始化文本较长，非流式请求建议设置 `output_len` 以避免长时间等待（总延迟 = ttft + tpot × output_len）。

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "output_len=10\n你好"}],
    "stream": false
  }'
```

### 流式请求

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "output_len=10\n你好"}],
    "stream": true
  }'
```

### 流式请求

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'
```

### 自定义延迟

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "ttft=2s&tpot=50ms\n你好"}],
    "stream": true
  }'
```

首 token 等待 2s，之后每个 token 间隔 50ms。

### 控制输出长度

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "output_len=50\n你好"}],
    "stream": false
  }'
```

初始化文本会重复填充到恰好 50 个 rune。如果设置了 `max_tokens` 且小于 `output_len`，输出会被截断，`finish_reason` 变为 `"length"`：

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "output_len=200\n你好"}],
    "max_tokens": 100,
    "stream": false
  }'
```

### Token 用量字段

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "input_len=1024&cached_len=512&output_len=100\n你好"}],
    "stream": false
  }'
```

响应 `usage` 字段：
- `prompt_tokens: 1024`
- `prompt_tokens_details.cached_tokens: 512`
- `completion_tokens: 100`

### 模拟上游错误

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "http_status=500&ttft=1s\n你好"}],
    "stream": false
  }'
```

等待 1s（`ttft` 时长）后返回 HTTP 500 错误。

### 组合使用所有参数

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "ttft=1.5s&tpot=20ms&output_len=30&input_len=2048&cached_len=1024\n这是实际的用户提问。"}],
    "stream": true
  }'
```

## 响应头

每个响应都包含 `X-Mock-Params` 头，内容为请求中的原始参数字符串。
