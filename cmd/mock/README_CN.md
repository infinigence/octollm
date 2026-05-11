# Mock LLM 服务

模拟 OpenAI Chat Completions 和 Anthropic Messages API 的 Mock 服务，支持配置延迟、输出长度、Token 用量和错误行为。

## 快速开始

```bash
go run ./cmd/mock
```

默认监听 `:8090`。测试：

```bash
# OpenAI
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"mock","messages":[{"role":"user","content":"output_len=10\n你好"}]}'

# Anthropic
curl http://localhost:8090/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: test" \
  -H "anthropic-version: 2023-06-01" \
  -d '{"model":"mock","max_tokens":10,"messages":[{"role":"user","content":"output_len=10\n你好"}]}'
```

> **提示：** 默认初始化文本较长，非流式请求请务必设置 `output_len` 以避免长时间等待（总延迟 = ttft + tpot × output_len）。

## 接口

| 接口 | 协议 |
|---|---|
| `POST /v1/chat/completions` | OpenAI Chat Completions |
| `POST /v1/messages` | Anthropic Messages |

## Mock 参数

在**最后一条消息**内容的**第一行**写入 URL query string 格式的控制参数，第一行之后的内容作为正常的消息文本。

```
ttft=2.3s&tpot=30ms&output_len=200&input_len=100&cached_len=50
这里是正常的消息内容。
```

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `ttft` | Go duration | `100ms` | 首字生成时间 |
| `tpot` | Go duration | `10ms` | 每个 token 的生成时间 |
| `output_len` | int | 初始化文本长度 | 输出 rune 数量（不足时重复初始化文本填充） |
| `input_len` | int | 所有消息的 rune 总数 | usage 中的输入 token 数 |
| `cached_len` | int | `0` | usage 中的缓存 token 数 |
| `http_status` | int | `200` | 返回的 HTTP 状态码，非 200 时在等待 `ttft` 后返回错误 |

### output_len 与 max_tokens 的关系

- `max_tokens >= output_len`：正常输出 `output_len` 个 rune。
- `max_tokens < output_len`：输出被截断为 `max_tokens` 个 rune。
  - OpenAI：`finish_reason` = `"length"`
  - Anthropic：`stop_reason` = `"max_tokens"`

### 参数到 usage 字段的映射

| 参数 | OpenAI 字段 | Anthropic 字段 |
|---|---|---|
| `input_len` | `prompt_tokens` | `input_tokens` |
| 实际输出长度 | `completion_tokens` | `output_tokens` |
| `cached_len` | `prompt_tokens_details.cached_tokens` | `cache_read_input_tokens` |

## OpenAI 示例

### 非流式

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "output_len=50\n你好"}],
    "stream": false
  }'
```

### 流式

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "ttft=2s&tpot=50ms&output_len=20\n你好"}],
    "stream": true
  }'
```

### max_tokens 截断

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "output_len=200\n你好"}],
    "max_tokens": 50
  }'
```

响应：`finish_reason: "length"`，`completion_tokens: 50`。

### Token 用量

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "input_len=1024&cached_len=512&output_len=50\n你好"}]
  }'
```

### 模拟错误

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "http_status=500&ttft=1s\n你好"}]
  }'
```

等待 1s 后返回 HTTP 500。

## Anthropic 示例

所有 Anthropic 请求需携带以下 Headers：

```
-H "Content-Type: application/json" \
-H "x-api-key: test" \
-H "anthropic-version: 2023-06-01"
```

### 非流式

```bash
curl http://localhost:8090/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: test" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "mock",
    "max_tokens": 50,
    "messages": [{"role": "user", "content": "output_len=50\n你好"}],
    "stream": false
  }'
```

### 流式

```bash
curl http://localhost:8090/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: test" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "mock",
    "max_tokens": 50,
    "messages": [{"role": "user", "content": "ttft=2s&tpot=50ms&output_len=20\n你好"}],
    "stream": true
  }'
```

### max_tokens 截断

```bash
curl http://localhost:8090/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: test" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "mock",
    "max_tokens": 20,
    "messages": [{"role": "user", "content": "output_len=200\n你好"}]
  }'
```

响应：`stop_reason: "max_tokens"`，`output_tokens: 20`。

### Token 用量

```bash
curl http://localhost:8090/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: test" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "mock",
    "max_tokens": 50,
    "messages": [{"role": "user", "content": "input_len=1024&cached_len=512&output_len=50\n你好"}]
  }'
```

响应 usage：`input_tokens: 1024`，`output_tokens: 50`，`cache_read_input_tokens: 512`。

### 模拟错误

```bash
curl http://localhost:8090/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: test" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "mock",
    "max_tokens": 50,
    "messages": [{"role": "user", "content": "http_status=500&ttft=1s\n你好"}]
  }'
```

## 响应头

每个响应都包含 `X-Mock-Params` 头，内容为请求中的原始参数字符串。
