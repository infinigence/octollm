# Expr Guide

This guide explains how to write expr expressions in OctoLLM's rule matching (`match`) and request rewriting (`set_keys_by_expr`).

## Expression Environment

All expressions run in the same environment. The top-level variable is `req`, which exposes the following methods:

| Method | Return Type | Description |
|--------|------------|-------------|
| `req.RawReq()` | `map[string]any` | Request body as JSON map, lazily parsed and cached |
| `req.Context("key")` | `any` | Read a value from the request context |
| `req.Feature("name")` | `any` | Invoke a registered feature extractor (see below) |
| `req.Header("key")` | `string` | Read a header from the received HTTP request (empty string if absent) |

---

## Available Features

Features are invoked via `req.Feature("name")`, computed on demand and cached per request.

| Feature Name | Return Type | Description |
|-------------|------------|-------------|
| `promptTextLen` | `int` | Total character count (rune) of all message texts |
| `prefix20` | `string` | FNV-32a hash (hex) of the first 20 runes of the first message + model name |
| `suffix20` | `string` | FNV-32a hash (hex) of the last 20 runes of the first message + model name |

> The number in `prefix20` / `suffix20` is the rune count to sample. Custom lengths (e.g. `prefix50`) can be registered by the application.

---

## Rule Matching (`match`)

The `match` field takes an expr expression that returns bool (or int/float64, non-zero = true). Rules are evaluated in order — **the first match wins**.

```yaml
models:
  my-model:
    default_rules:
      # 1. Block streaming requests
      - name: deny_stream
        match: "req.RawReq().stream == true"
        deny:
          reason_text: "streaming is not allowed"
          http_status_code: 403

      # 2. Route long prompts to a high-capacity backend
      - name: long_prompt
        match: "req.Feature('promptTextLen') > 8000"
        forward_weights:
          large-backend: 1

      # 3. Route by user group injected into context by middleware
      - name: vip_user
        match: "req.Context('user_group') == 'vip'"
        forward_weights:
          vip-backend: 1

      # 4. Route by incoming HTTP header (e.g. set by an API gateway)
      - name: org_route
        match: "req.Header('X-Org-Id') == 'my-org'"
        forward_weights:
          org-backend: 1

      # 5. Combine multiple conditions
      - name: heavy_request
        match: "req.RawReq().max_tokens > 4096 && req.RawReq().stream == false"
        forward_weights:
          batch-backend: 1

      # 6. Match a specific request by content hash (e.g. cache hit detection)
      - name: known_prefix
        match: "req.Feature('prefix20') == 'c6eec1e7'"
        forward_weights:
          cache-backend: 1

      # 7. Catch-all fallback
      - name: default
        match: "true"
        forward_weights:
          primary-backend: 1
```

---

## Request Rewriting (`set_keys_by_expr`)

Each key in `set_keys_by_expr` maps to an expr expression:

- Returns **non-`nil`**: the value is written to that JSON path in the request body
- Returns **`nil`**: the key is skipped (no-op)

```yaml
backends:
  my-upstream:
    base_url: https://api.example.com
    request_rewrites:
      # Static override (no expression needed)
      set_keys:
        model: "actual-upstream-model-name"

      # Dynamic computation
      set_keys_by_expr:
        # Cap max_tokens at 2048
        max_tokens: "req.RawReq().max_tokens > 2048 ? 2048 : req.RawReq().max_tokens"

        # Inject stream_options only when streaming; skip otherwise
        stream_options: "req.RawReq().stream == true ? {'include_usage': true} : nil"

        # Inject a dynamic value from context (written by upstream middleware)
        # user: "req.Context('user_id')"

      # Remove fields before forwarding
      remove_keys:
        - top_p
        - frequency_penalty
```

---

## Full Example

```yaml
backends:
  primary:
    base_url: https://api.primary.com
    request_rewrites:
      set_keys:
        model: gpt-4o
      set_keys_by_expr:
        max_tokens: "req.RawReq().max_tokens > 4096 ? 4096 : req.RawReq().max_tokens"
        stream_options: "req.RawReq().stream == true ? {'include_usage': true} : nil"

  fallback:
    base_url: https://api.fallback.com
    request_rewrites:
      set_keys:
        model: gpt-4o-mini

models:
  my-model:
    access: public
    default_rules:
      - name: block_huge_requests
        match: "req.Feature('promptTextLen') > 100000"
        deny:
          reason_text: "prompt too long"
          http_status_code: 400

      - name: long_prompt_fallback
        match: "req.Feature('promptTextLen') > 30000"
        forward_weights:
          fallback: 1

      - name: default
        match: "true"
        forward_weights:
          primary: 1
```

---

## Expr Syntax Quick Reference

Full documentation at [expr-lang.org](https://expr-lang.org). Common patterns:

```
# Comparison
req.RawReq().stream == true
req.RawReq().max_tokens > 1000

# Header check
req.Header("X-Org-Id") == "my-org"

# Logical
req.RawReq().stream && req.RawReq().temperature > 0.8
req.RawReq().stream || req.Context("allow_stream") == true

# Ternary
req.RawReq().max_tokens > 1000 ? 1000 : req.RawReq().max_tokens

# Array / field access
req.RawReq().messages[0].role == "user"
len(req.RawReq().messages) > 5

# Return nil to skip a set_keys_by_expr entry
req.RawReq().stream == true ? {'include_usage': true} : nil
```
