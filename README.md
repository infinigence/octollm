# OctoLLM

**OctoLLM** is a high-performance LLM (Large Language Model) gateway and development framework designed for high-traffic production environments. It stands out for its flexibility, extensibility, and modular design.

OctoLLM serves two main purposes:
1.  **Standalone Gateway**: A ready-to-use LLM gateway configured via a YAML file.
2.  **Development Framework**: A Go-based framework for building custom LLM gateways and plugins with ease.

## ✨ Features & Roadmap

### Implemented Features
- [x] **Protocol Support**: Supports OpenAI-compatible `chat/completions` interface forwarding.
- [x] **Load Balancing**: Configurable weighted round-robin load balancing across multiple backends.
- [x] **Rule Engine**: Powerful routing and logic based on expressions (e.g., checking request parameters).
- [x] **Security**: API Key authentication and authorization, integratable with the rule engine for granular control.
- [x] **Traffic Body Rewrite**: Request and response rewriting and transformation capabilities.
- [x] **Extensible Design**: Modular `Engine` interface allowing arbitrary nesting and composition of features.

### Planned Features
- [ ] **Content Moderation**: Integration with external services for content safety.
- [ ] **Advanced Rate Limiting**: Distributed rate limiting capabilities (e.g., Redis-based).
- [ ] **Multi-protocol Support**: Support for additional LLM protocols and protocol translation.
- [ ] **Comprehensive Unit Tests**: Expanding test coverage for stability.
- [ ] **Dynamic Configuration**: Loading configuration from relational databases.

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

## 🚀 Getting Started

### Prerequisites

*   Go 1.25 or higher

### Configuration

OctoLLM uses a YAML configuration file (`config.yaml`) to define backends, models, and user access policies.

*   **Example Configuration**: See [examples/config-rule.yaml](examples/config-rule.yaml) for a starter template.
*   **Detailed Documentation**: Read the full [Configuration Guide](docs/config.md) for in-depth explanation of all options.

### Building and Running the Standalone Gateway

To build the standalone gateway:

```bash
go build -o . ./cmd/...
```

Copy an example configuration file from the `examples` directory:

```bash
cp examples/config.yaml .
# Edit config.yaml as needed
```

To run the gateway:

```bash
./octollm-server
```

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