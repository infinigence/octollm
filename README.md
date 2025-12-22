# OctoLLM

**OctoLLM** is a high-performance LLM (Large Language Model) gateway and development framework designed for high-traffic production environments. It stands out for its flexibility, extensibility, and modular design.

OctoLLM serves two main purposes:
1.  **Standalone Gateway**: A ready-to-use LLM gateway configured via a YAML file.
2.  **Development Framework**: A Go-based framework for building custom LLM gateways and plugins with ease.

## ✨ Features & Roadmap

### Implemented Features
- [x] **Multi-Protocol Support**: Supports OpenAI-compatible `chat/completions` and Claude `messages` interface forwarding.
- [x] **Load Balancing**: Configurable weighted round-robin load balancing across multiple backends.
- [x] **Rule Engine**: Powerful routing and logic based on expressions (e.g., checking request parameters).
- [x] **Security**: API Key authentication and authorization, integratable with the rule engine for granular control.
- [x] **Traffic Body Rewrite**: Request and response rewriting and transformation capabilities.
- [x] **Extensible Design**: Modular `Engine` interface allowing arbitrary nesting and composition of features.
- [x] **Protocol Conversion**: Support serving Claude `messages` protocol from OpenAI `chat/completions` backend.

### Planned Features
- [ ] **Content Moderation**: Integration with external services for content safety.
- [ ] **Advanced Rate Limiting**: Distributed rate limiting capabilities (e.g., Redis-based).
- [ ] **Comprehensive Unit Tests**: Expanding test coverage for stability.
- [ ] **Dynamic Configuration**: Loading configuration from relational databases.

## 🔧 Getting Started

Here is an example of how to use OctoLLM Engines as the building blocks of a custom LLM gateway. If you are looking for a ready-to-use gateway, please refer to the [Standalone Gateway](#🚀-using-the-standalone-gateway) section.


```golang
package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/infinigence/octollm/pkg/engines"
	"github.com/infinigence/octollm/pkg/engines/client"
	"github.com/infinigence/octollm/pkg/engines/converter"
	"github.com/infinigence/octollm/pkg/octollm"
)

func main() {
	mux := http.NewServeMux()

	// Create a general endpoint to access an OpenAI-compatible API
	ep := client.NewGeneralEndpoint(client.GeneralEndpointConfig{
		BaseURL: "https://cloud.infini-ai.com/maas",
		Endpoints: map[octollm.APIFormat]string{
			octollm.APIFormatChatCompletions: "/v1/chat/completions",
		},
		APIKey: os.Getenv("OCTOLLM_API_KEY"),
	})
	mux.Handle("/v1/chat/completions", octollm.ChatCompletionsHandler(ep))

	// Create a converter to convert OpenAI-compatible API to Claude messages API
	conv := converter.NewChatCompletionsToClaudeMessages(ep)
	mux.Handle("/v1/messages", octollm.MessagesHandler(conv))

	// Create a rewrite engine to force the model to use kimi-k2-instruct
	rewrite := engines.NewRewriteEngine(conv, &engines.RewritePolicy{
		SetKeys: map[string]any{"stream": true},
	}, nil, nil)
	mux.Handle("/force-stream/v1/messages", octollm.MessagesHandler(rewrite))

	// Start the server
	if err := http.ListenAndServe(":8080", mux); err != nil {
		fmt.Printf("failed to start server: %v", err)
	}
}
```

The complete example code is available in the [examples](examples) directory.

## 🚀 Using the Standalone Gateway

### Building the Standalone Gateway

To build the standalone gateway:

```bash
go build -o . ./cmd/...
```

### Configuration

The standalone gateway uses a YAML configuration file (`config.yaml`) to define backends, models, and user access policies.

*   **Example Configuration**: See [examples/config-rule.yaml](examples/config-rule.yaml) for a starter template.
*   **Detailed Documentation**: Read the full [Configuration Guide](docs/config.md) for in-depth explanation of all options.

Copy an example configuration file from the `examples` directory:

```bash
cp examples/config-minimal.yaml ./config.yaml
# Edit config.yaml and set an API key for the infini backend
```

### Running the Standalone Gateway

```bash
./octollm-server
```

### Using Claude Code with OpenAI-compatible Services

Here is an example of how to use the standalone gateway to serve Claude `messages` protocol from OpenAI `chat/completions` backend, so that you can use Claude CLI.

Copy the protocol conversion example config:

```bash
cp examples/config-protocol-conversion.yaml ./config.yaml
# Edit config.yaml and set an API key for the infini backend
```

To run the gateway:

```bash
./octollm-server
```

Config and run Claude CLI to use the OctoLLM gateway:

```bash
export ANTHROPIC_BASE_URL=http://localhost:8080
export ANTHROPIC_AUTH_TOKEN=xxx # any non-empty value works, since octollm auth is disabled in the config
claude --model kimi-k2-instruct # or other models defined in your config.yaml
```

For persistent configuration of Claude CLI, edit `~/.claude/settings.json`.

## 🏗 Architecture

### Core Design Philosophy

*   **Unified Engine Interface**: The core is built around a simple `Engine` interface with a single `Process` method. This handles both standard and streaming responses (via Go channels).
    ```go
    type Engine interface {
        Process(req *Request) (*Response, error)
    }
    ```
*   **Lazy Parsing**: Requests and responses use lazy parsing. Content is parsed only when accessed, minimizing memory usage and CPU cycles. Unused content remains as an `io.Reader`, avoiding unnecessary copying.
*   **Modularity**: Engines can be nested arbitrarily. Each Engine implements a specific function (e.g., authentication, logging, routing) without needing to know the details of others.
*   **Lightweight Core**: The `octollm` package is minimal, containing only essential interfaces and structs. Implementations reside in the `engines` directory.

## 🔌 Development & Extensions

OctoLLM is designed to be easily extended. You can implement your own `Engine` to add custom logic.

1.  Import `github.com/infinigence/octollm/pkg/octollm`
2.  Implement the `Engine` interface:
    ```go
    type MyCustomEngine struct {
        Next octollm.Engine
    }

    func (e *MyCustomEngine) Process(req *octollm.Request) (*octollm.Response, error) {
        // Custom logic before request
        resp, err := e.Next.Process(req)
        // Custom logic after response
        return resp, err
    }
    ```
3.  Build a top-level `Engine` that chains your custom engine with the existing ones. And use this top-level engine to process HTTP requests.
    ```go
    func main() {
        // Initialize the top engine
        // topEngine := &MyCustomEngine{...}

        // Start HTTP server
        http.HandleFunc("/chat/completions", octollm.ChatCompletionsHandler(topEngine))
        http.ListenAndServe(":8080", nil)
    }
    ```