# Mock LLM Server

A mock LLM server that simulates OpenAI Chat Completions and Anthropic Messages API responses, with configurable latency, output length, token usage, and error behavior.

## Quick Start

```bash
go run ./cmd/mock
```

Listens on `:8090`. Test it:

```bash
# OpenAI
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"mock","messages":[{"role":"user","content":"output_len=10\nHello"}]}'

# Anthropic
curl http://localhost:8090/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: test" \
  -H "anthropic-version: 2023-06-01" \
  -d '{"model":"mock","max_tokens":10,"messages":[{"role":"user","content":"output_len=10\nHello"}]}'
```

> **Tip:** The default init text is long. Always set `output_len` for non-stream requests to avoid long waits (total latency = ttft + tpot × output_len).

## Endpoints

| Endpoint | Protocol |
|---|---|
| `POST /v1/chat/completions` | OpenAI Chat Completions |
| `POST /v1/messages` | Anthropic Messages |

## Mock Parameters

Control mock behavior by adding a URL query-string line as the **first line** of the **last message** content. Everything after the first newline is treated as normal message content.

```
ttft=2.3s&tpot=30ms&output_len=200&input_len=100&cached_len=50
The rest of the message content goes here.
```

| Parameter | Type | Default | Description |
|---|---|---|---|
| `ttft` | Go duration | `100ms` | Time to first token |
| `tpot` | Go duration | `10ms` | Time per output token |
| `output_len` | int | length of init text | Number of output runes (init text is repeated to fill) |
| `input_len` | int | total rune count of all messages | Prompt/input token count in usage |
| `cached_len` | int | `0` | Cached token count in usage |
| `http_status` | int | `200` | HTTP status code. Non-200 fails after `ttft` delay |

### How output_len interacts with max_tokens

- If `max_tokens >= output_len`: output `output_len` runes, finish normally.
- If `max_tokens < output_len`: output `max_tokens` runes, finish with truncated reason.
  - OpenAI: `finish_reason` = `"length"`
  - Anthropic: `stop_reason` = `"max_tokens"`

### How parameters map to usage fields

| Parameter | OpenAI usage field | Anthropic usage field |
|---|---|---|
| `input_len` | `prompt_tokens` | `input_tokens` |
| actual output length | `completion_tokens` | `output_tokens` |
| `cached_len` | `prompt_tokens_details.cached_tokens` | `cache_read_input_tokens` |

## OpenAI Examples

### Non-stream

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "output_len=50\nHello"}],
    "stream": false
  }'
```

### Stream

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "ttft=2s&tpot=50ms&output_len=20\nHello"}],
    "stream": true
  }'
```

### max_tokens truncation

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "output_len=200\nHello"}],
    "max_tokens": 50
  }'
```

Response: `finish_reason: "length"`, `completion_tokens: 50`.

### Token usage

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "input_len=1024&cached_len=512&output_len=50\nHello"}]
  }'
```

### Simulate error

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mock",
    "messages": [{"role": "user", "content": "http_status=500&ttft=1s\nHello"}]
  }'
```

Waits 1s then returns HTTP 500.

## Anthropic Examples

All Anthropic examples require these headers:

```
-H "Content-Type: application/json" \
-H "x-api-key: test" \
-H "anthropic-version: 2023-06-01"
```

### Non-stream

```bash
curl http://localhost:8090/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: test" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "mock",
    "max_tokens": 50,
    "messages": [{"role": "user", "content": "output_len=50\nHello"}],
    "stream": false
  }'
```

### Stream

```bash
curl http://localhost:8090/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: test" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "mock",
    "max_tokens": 50,
    "messages": [{"role": "user", "content": "ttft=2s&tpot=50ms&output_len=20\nHello"}],
    "stream": true
  }'
```

### max_tokens truncation

```bash
curl http://localhost:8090/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: test" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "mock",
    "max_tokens": 20,
    "messages": [{"role": "user", "content": "output_len=200\nHello"}]
  }'
```

Response: `stop_reason: "max_tokens"`, `output_tokens: 20`.

### Token usage

```bash
curl http://localhost:8090/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: test" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "mock",
    "max_tokens": 50,
    "messages": [{"role": "user", "content": "input_len=1024&cached_len=512&output_len=50\nHello"}]
  }'
```

Response usage: `input_tokens: 1024`, `output_tokens: 50`, `cache_read_input_tokens: 512`.

### Simulate error

```bash
curl http://localhost:8090/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: test" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "mock",
    "max_tokens": 50,
    "messages": [{"role": "user", "content": "http_status=500&ttft=1s\nHello"}]
  }'
```

## Response Headers

Every response includes an `X-Mock-Params` header containing the raw parameter string from the request.
