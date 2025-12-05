# Configuration Guide

OctoLLM uses a YAML configuration file to define backends, models, and user access policies.

## Structure Overview

The configuration is divided into three main sections:

1.  **`backends`**: (Optional) Configurations for upstream LLM providers that can be reused.
2.  **`models`**: (Required) Defines the logical models exposed by OctoLLM.
3.  **`users`**: (Optional) Defines organizations and users with their specific permissions and rules.

## 1. Backends (Optional)

This section defines the connection details for upstream LLM providers. It is optional; you can define complete provider information directly in the `models` section, or reference a global backend and override its fields.

```yaml
backends:
  provider_name:
    base_url: "https://api.provider.com/v1"
    url_path_chat: "/chat/completions"
    api_key: "sk-..." # Optional: Upstream API Key
    http_proxy: "http://proxy:8080" # Optional
```

*   `base_url`: The base URL of the upstream provider.
*   `url_path_chat`: The specific path for chat completions.

## 2. Models

This section defines the models that OctoLLM exposes to its clients.

```yaml
models:
  exposed-model-name:
    access: public # Options: public, internal, private
    backends:
      default:1: 
        # Option A: Reference a global backend
        use: provider_name
        # You can also override or add fields to the referenced backend
        api_key: "override-api-key"

        # Option B: Define backend inline without 'use'
        # base_url: ...

        # Rewrites
        request_rewrites:
          set_keys:
            model: "actual-upstream-model-name"
          set_keys_by_expr:
            max_tokens: "RawReq.max_tokens > 1000 ? 1000 : RawReq.max_tokens"
          remove_keys: ["top_p"]
        
        response_rewrites:
           # Same structure as request_rewrites
        
        stream_chunk_rewrites:
           # Same structure as request_rewrites

    # Default rules apply to all requests for this model, 
    # executed after user-specific rules (if any).
    default_rules:
      - name: deny_streaming_by_default
        match: "RawReq.stream == true"
        deny:
          reason_text: "Streaming denied by default"
          http_status_code: 403
```

### Model Properties

*   `access`: 
    *   `public`: (Default) Accessible without authentication.
    *   `internal`: Requires authentication.
    *   `private`: Requires authentication and explicit permission in the `users` section.
*   `backends`: A list of backends to route to. The key `default:1` implies a weighted round-robin strategy (weight 1).
*   `default_rules`: A list of rules applied to all requests. See the **Rules** section below for details. These are evaluated last, after any user-specific rules.

### Rewrites

OctoLLM supports powerful modification of requests and responses.

*   **`request_rewrites`**: Modify the request body before sending to upstream.
*   **`response_rewrites`**: Modify the response body before returning to the client (for non-streaming).
*   **`stream_chunk_rewrites`**: Modify individual chunks in a streaming response.

Supported operations:
*   `set_keys`: Set static values for fields.
*   `set_keys_by_expr`: Set values dynamically using expressions.
*   `remove_keys`: Remove fields from the JSON body.

## 3. Users

This section manages access control and fine-grained rules for users/organizations.

```yaml
users:
  org_name:
    api_keys:
      user_id: "octo-key-..."
    models:
      exposed-model-name:
        rules:
          - name: rule_name
            match: "RawReq.stream == true"
            deny:
              reason_text: "Streaming denied"
              http_status_code: 403
```

*   `api_keys`: Map user identifiers to their API keys.
*   `rules`: Define logic to allow/deny requests or route them differently based on the request content.

### Rules Engine

Rules are defined as an ordered list. They are executed sequentially. **Once a rule matches, execution stops** (unless configured otherwise in future versions), and the defined action is taken.

*   **`match`**: An expression to evaluate against the request.
    *   The syntax follows [expr-lang](https://expr-lang.org/).
    *   You can access the raw request body fields via the `RawReq` variable (e.g., `RawReq.messages[0].role == 'system'`).
*   **`deny`**: Configuration to reject the request if matched.
    *   `reason_text`: The error message returned to the client.
    *   `http_status_code`: The HTTP status code to return.
*   **`forward_weights`**: Redefine the load balancing weights for the model's backends.
    *   Map of `backend_name: weight`.
    *   Example:
        ```yaml
        forward_weights:
          special_backend: 10
          default:1: 1
        ```

**Default Behavior**: If a rule matches but neither `deny` nor `forward_weights` is specified, the request is distributed equally among backends named `default:*` (e.g., `default:1`, `default:2`).
