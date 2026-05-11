# Mock LLM Server

A mock OpenAI-compatible LLM server that simulates chat completion responses with configurable latency, output length, and error behavior.

## Start the Server

```bash
go run ./cmd/mock
```

Listens on `:8090` by default.

## Endpoints

- `POST /v1/chat/completions` — OpenAI Chat Completions
- `POST /v1/completions` — OpenAI Completions
- `POST /v1/messages` — Anthropic Messages

## Mock Parameters

Control mock behavior by adding a URL query-string line as the **first line** of the **last message** content. Everything after the first newline is treated as normal message content.

| Parameter | Type | Default | Description |
|---|---|---|---|
| `ttft` | Go duration | `100ms` | Time to first token |
| `tpot` | Go duration | `10ms` | Time per output token |
| `output_len` | int | length of init text | Number of output runes (text is repeated to fill) |
| `input_len` | int | total rune count of messages | Prompt tokens in usage |
| `cached_len` | int | `0` | Cached prompt tokens in usage |
| `http_status` | int | `200` | HTTP status code. Non-200 fails after `ttft` delay |

## Examples

### Basic request (no params)

> **Note:** The default init text is long. For non-stream requests, set `output_len` to avoid long waits (total latency = ttft + tpot × output_len).

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "output_len=10\nHello"}],
    "stream": false
  }'
```

### Streaming request

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "output_len=10\nHello"}],
    "stream": true
  }'
```

### Custom latency

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "ttft=2s&tpot=50ms\nHello"}],
    "stream": true
  }'
```

This waits 2s before the first token, then 50ms between each subsequent token.

### Control output length

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "output_len=50\nHello"}],
    "stream": false
  }'
```

The init text is repeated to produce exactly 50 runes. If `max_tokens` is set and is less than `output_len`, output is truncated and `finish_reason` becomes `"length"`:

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "output_len=200\nHello"}],
    "max_tokens": 100,
    "stream": false
  }'
```

### Token usage fields

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "input_len=1024&cached_len=512&output_len=100\nHello"}],
    "stream": false
  }'
```

Response `usage` will show:
- `prompt_tokens: 1024`
- `prompt_tokens_details.cached_tokens: 512`
- `completion_tokens: 100`

### Simulate upstream error

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "http_status=500&ttft=1s\nHello"}],
    "stream": false
  }'
```

Waits 1s (the `ttft` duration), then returns HTTP 500 with a mock error body.

### All parameters combined

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "ttft=1.5s&tpot=20ms&output_len=30&input_len=2048&cached_len=1024\nThis is the actual user prompt."}],
    "stream": true
  }'
```

## Response Headers

Every response includes an `X-Mock-Params` header containing the raw parameter string from the request.
