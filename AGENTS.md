# AGENTS.md

This file provides guidance to AI coding agents (Claude Code, Codex, etc.) when working with code in this repository. `CLAUDE.md` is a symlink to this file.

## Project Overview

OctoLLM is a Go-based LLM gateway and development framework. It serves as both a standalone YAML-configured gateway and a framework for building custom LLM gateways with plugins.

## Build & Development Commands

```bash
# Build the standalone gateway
go build -o . ./cmd/...

# Run the server (reads config from ./config.yaml by default)
./octollm-server
./octollm-server -c /path/to/config.yaml  # custom config path

# Run all tests
go test ./...

# Dependencies
go mod tidy
```

## Architecture

### Core Design

The system uses a composable **Engine interface** pattern:

```go
type Engine interface {
    Process(req *Request) (*Response, error)
}
```

Engines can be arbitrarily nested, each handling specific concerns (client communication, protocol conversion, rewriting, load balancing, rule matching).

**Key principles:**
- Lazy parsing: Content parsed only when accessed; unused content stays as `io.Reader`
- Unified interface: Single `Process` method handles both standard and streaming responses
- Minimal core: `/pkg/octollm` contains only essential interfaces; implementations live in `/pkg/engines`

### Package Structure

- **`/pkg/octollm`** - Core interfaces: Engine, Request, Response, UnifiedBody, Stream, HTTP handlers
- **`/pkg/engines/client`** - Upstream provider clients (HTTPEndpoint, ClaudeMessagesEndpoint, OpenAI)
- **`/pkg/engines/converter`** - Protocol converters (OpenAI ↔ Claude messages)
- **`/pkg/engines/load-balancer`** - Weighted round-robin load balancing with retry logic
- **`/pkg/engines/limiter`** - Concurrency limiting (Lua/Redis-backed); "color" variants reserve cache-hit headroom via per-priority tier bands
- **`/pkg/engines/rule-engine`** - Matcher-based rule chains using expr-lang expressions
- **`/pkg/engines/moderator`** - Text content moderation around the next engine (Ali/Netease services, OpenAI/Claude adapters); probabilistic gating on input/output paths
- **`/pkg/engines/image-url-fetch`** - Downloads remote images and inlines them as base64 in the raw JSON body (OpenAI `image_url` and Anthropic Messages URL sources)
- **`/pkg/engines/traffic-replication`** - Mirrors a sampled fraction of requests to additional engine targets (uses `SafeGo`)
- **`/pkg/engines/rewrite.go`** - JSON path-based request/response/stream-chunk rewriting
- **`/pkg/engines/addheader.go`, `add_header_by_expr.go`, `deny.go`** - Small standalone engines (static/expr-based header injection, request denial)
- **`/pkg/exprenv`** - expr-lang environment for rule/rewrite expressions; exposes `req.Header()`, `req.Feature()`, `req.Context()`, `req.RawReq()`; `exprenv.Get(req)` retrieves or creates the env from request context, `exprenv.Sentinel` is a zero-value env used for compile-time type checking
- **`/pkg/composer`** - YAML config parsing, model-to-engine mapping, orchestration
- **`/pkg/errutils`** - Custom error types (`UpstreamHTTPError` vs handler errors)
- **`/pkg/types/anthropic`** - Claude message request/response types; `docs/api-create-snap*.md` is a dated snapshot of Anthropic's official Create Message API spec — the source-of-truth to model these types against (consult the latest snapshot when adding/editing fields)
- **`/pkg/types/openai`** - OpenAI ChatCompletion types
- **`/pkg/types/UNION_TYPES.md`** - READ before adding/editing any `/pkg/types` type: the polymorphic-JSON convention (Interface + `xxxField` pattern, dispatch, marshaling)
- **`/pkg/types/TESTING_GUIDE.md`** - READ before testing those types: table-driven Marshal/Unmarshal conventions
- **`/cmd/octollm-server`** - Standalone server entry point with Gin routes and auth middleware

### Request Flow

1. HTTP request → `octollm.Request` (lazy body parsing)
2. RuleComposer selects engine based on model/rules
3. Engine chain processes request (conversion, rewriting, load balancing)
4. Response returned as JSON or SSE stream

### Configuration

YAML config structure (`config.yaml`):
```yaml
backends:  # Reusable upstream provider configs
models:    # Exposed LLM models with routing
users:     # User/org access control and rate limits
```

Example configs in `/examples/` demonstrate minimal setup, rules, protocol conversion, and authentication.

See `docs/config.md` for the full config reference and `docs/expr-guide.md` for the expr-lang rule/rewrite expression guide.

## Key Dependencies

- **gin-gonic/gin** - Web framework
- **expr-lang/expr** - Expression language for rule matching
- **anthropic-sdk-go**, **openai-go** - Provider SDKs
- **tidwall/sjson** - JSON manipulation for rewriting
- **goccy/go-yaml** - YAML parsing

## Code Patterns

- All functions accept `ctx context.Context` for cancellation
- Thread-safe caching with RWMutex in ModelRepo and RuleComposer
- Builder pattern for HTTPEndpoint configuration
- Custom error types: `UpstreamHTTPError` vs handler errors
- Use `octollm.SafeGo(req, fn)` instead of bare `go` for any goroutine spawned inside request or stream processing (e.g. fire-and-forget replication, async stream draining). gin.Recovery and similar middleware only recover panics on the handler goroutine; a panic on a separately spawned goroutine will crash the process.
