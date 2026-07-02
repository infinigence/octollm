# FastJSON Parser Guide (Deprecated)

> **Deprecated:** This guide documents the fastjson-based parser approach, which has been superseded by the jlexer-based approach. See `JLEXER_PARSER_GUIDE.md` for the current guide. This document is kept for historical reference.

This document explains how to write a fastjson-based parser for a struct in `pkg/types/` to satisfy the `octollm.Parser` interface, using `openai.ChatCompletionStreamChunk` as the reference example.

## Why fastjson

Standard `encoding/json` fills fields via reflection, which has performance overhead for high-frequency streaming parsing. fastjson uses a hand-written recursive-descent parser; after parsing, it accesses the value tree directly via pointers, bypassing reflection. Combined with hand-written field assignment code per struct, it's several times faster than `json.Unmarshal`.

## Prerequisite: octollm.Parser interface

```go
// pkg/octollm/request.go
type Parser interface {
    Parse(data []byte) (any, error)
    Serialize(v any) ([]byte, error)
}
```

`Parse` returns a pointer to the target struct (e.g. `*ChatCompletionStreamChunk`); `Serialize` accepts the same pointer type and serializes it.

## Circular dependency problem

The `pkg/octollm` package (via `handler.go`) imports `pkg/types/openai`, so `pkg/types/openai` **cannot** import `octollm` back.

**Solution:** Implement the Parser in the corresponding package under `pkg/types/`, without importing `octollm`. Go's structural typing means that as long as the method signatures match, no explicit interface declaration is needed. The calling code (in `pkg/engines/` etc.) imports both, and the compiler automatically confirms interface satisfaction.

For `ErrStreamDone`, define a local version in the Parser file rather than referencing `octollm.ErrStreamDone`.

## File organization

```
pkg/types/openai/
  chatcompletion_resp.go              <- struct definition
  stream_chunk_fastjson_parser.go     <- Parser implementation (with ParseFastJSON methods)
  stream_chunk_fastjson_parser_test.go <- tests
```

File naming: `<struct_snake_case>_fastjson_parser.go`.

## Core design: methods, not free functions

The Parser file is in the same package as the struct definition, so we extend each struct with a `ParseFastJSON` method rather than writing free functions:

```go
// Unified signature: receiver is pre-allocated; the method fills fields
func (x *Xxx) ParseFastJSON(v *fastjson.Value) error
```

Benefits:
- The method is associated with the type, making code navigation more intuitive
- The receiver is pre-allocated, so the caller just does `x := &Xxx{}; x.ParseFastJSON(v)`
- The signature is consistent; the `error` return value supports future validation logic

## Writing steps

### Step 1 — Analyze the target struct's field types

Categorize each field; different types require different handling:

| Go field type | JSON form | Handling |
|---|---|---|
| `string` | `"hello"` | `string(v.GetStringBytes("key"))` |
| `int` | `42` | `v.GetInt("key")` |
| `*string` | `"hello"` or absent | nil check + type check, then allocate pointer |
| `[]*Struct` | `[{...}]` | TypeArray check -> Array() -> loop, calling `elem.ParseFastJSON()` per element |
| `*Struct` | `{...}` or absent | TypeObject check -> `s := &Struct{}; s.ParseFastJSON(v)` |
| polymorphic interface (Kind 2) | string or array | `switch v.Type()` dispatch (see below) |
| polymorphic interface (Kind 3) | object with `type` field | peek `type` -> switch dispatch to concrete type (see below) |

### Step 2 — Write the Parser struct and two interface methods

```go
package openai

import (
    "encoding/json"
    "fmt"
    "strings"

    "github.com/valyala/fastjson"
)

var ErrStreamDone = fmt.Errorf("stream done")

type StreamChunkFastJSONParser struct{}

func (p *StreamChunkFastJSONParser) Parse(data []byte) (any, error) {
    if strings.TrimSpace(string(data)) == "[DONE]" {
        return nil, fmt.Errorf("%w", ErrStreamDone)
    }

    v, err := fastjson.ParseBytes(data)
    if err != nil {
        return nil, err
    }

    chunk := &ChatCompletionStreamChunk{}
    if err := chunk.ParseFastJSON(v); err != nil {
        return nil, err
    }
    return chunk, nil
}

func (p *StreamChunkFastJSONParser) Serialize(v any) ([]byte, error) {
    chunk, ok := v.(*ChatCompletionStreamChunk)
    if !ok {
        return nil, fmt.Errorf("value is not a *ChatCompletionStreamChunk")
    }
    return json.Marshal(chunk)
}
```

Key points:
- `Parse` first checks the `[DONE]` sentinel (only needed for streaming parsers), then parses with `fastjson.ParseBytes`, and finally calls `chunk.ParseFastJSON(v)` to fill fields
- `Serialize` directly uses standard `json.Marshal`, no special handling

### Step 3 — Write the ParseFastJSON method for the top-level struct

Assign fields one by one by type:

```go
func (c *ChatCompletionStreamChunk) ParseFastJSON(v *fastjson.Value) error {
    // string field — GetStringBytes returns nil for absent keys, string(nil) = "" (zero value)
    c.ID = string(v.GetStringBytes("id"))
    // int field — GetInt returns 0 for absent keys (zero value)
    c.Created = v.GetInt("created")

    // *string field — must check nil first, then Type
    if sf := v.Get("system_fingerprint"); sf != nil && sf.Type() == fastjson.TypeString {
        s := string(sf.GetStringBytes())
        c.SystemFingerprint = &s
    }

    // []*Struct field — check TypeArray, Array() to get elements, loop calling sub-method
    if cv := v.Get("choices"); cv != nil && cv.Type() == fastjson.TypeArray {
        arr, _ := cv.Array()
        c.Choices = make([]*ChatCompletionStreamChoice, len(arr))
        for i, choiceVal := range arr {
            choice := &ChatCompletionStreamChoice{}
            if err := choice.ParseFastJSON(choiceVal); err != nil {
                return err
            }
            c.Choices[i] = choice
        }
    }

    // *Struct field — check TypeObject, allocate then call sub-method
    if uv := v.Get("usage"); uv != nil && uv.Type() == fastjson.TypeObject {
        u := &Usage{}
        if err := u.ParseFastJSON(uv); err != nil {
            return err
        }
        c.Usage = u
    }

    return nil
}
```

### Step 4 — Write ParseFastJSON methods for nested structs

Every nested struct gets its own `ParseFastJSON` method for recursive processing. The signature is always `func (x *Xxx) ParseFastJSON(v *fastjson.Value) error`:

```go
func (c *ChatCompletionStreamChoice) ParseFastJSON(v *fastjson.Value) error {
    c.FinishReason = string(v.GetStringBytes("finish_reason"))
    c.Index = v.GetInt("index")

    if dv := v.Get("delta"); dv != nil && dv.Type() == fastjson.TypeObject {
        m := &Message{}
        if err := m.ParseFastJSON(dv); err != nil {
            return err
        }
        c.Delta = m
    }
    return nil
}
```

Named slice types (e.g. `type MessageContentArray []*MessageContentItem`) can also define methods:

```go
func (a *MessageContentArray) ParseFastJSON(v *fastjson.Value) error {
    arr, _ := v.Array()
    *a = make(MessageContentArray, len(arr))
    for i, itemVal := range arr {
        item := &MessageContentItem{}
        if err := item.ParseFastJSON(itemVal); err != nil {
            return err
        }
        (*a)[i] = item
    }
    return nil
}
```

## Handling polymorphic fields

The project uses polymorphic field patterns described in `UNION_TYPES.md`. In the fastjson parser, different strategies apply depending on the kind of polymorphism:

### Kind 1: Has a `type` field, single struct holds all variants

The JSON object has a `type` field distinguishing variants, and all variant fields can coexist in one struct (no type conflicts). **No dispatch needed** — each field is read independently, and absent fields are automatically zero-valued:

```go
func (c *ContentItem) ParseFastJSON(v *fastjson.Value) error {
    c.Type = string(v.GetStringBytes("type"))
    c.Text = string(v.GetStringBytes("text"))           // "" if absent
    if img := v.Get("image_url"); img != nil && img.Type() == fastjson.TypeObject {
        c.ImageURL = &ImageURLObject{...}
    }
    return nil
}
```

### Kind 2: No `type` field, same key can be different JSON types

The same JSON key can be a string, array, or object with no common discriminator. Use `switch v.Type()` to dispatch to different concrete types:

```go
// content can be string or array
if cv := v.Get("content"); cv != nil {
    switch cv.Type() {
    case fastjson.TypeString:
        m.Content = MessageContentString(string(cv.GetStringBytes()))
    case fastjson.TypeArray:
        var arr MessageContentArray
        if err := arr.ParseFastJSON(cv); err != nil {
            return err
        }
        m.Content = arr
    }
    // TypeNull and other types -> no assignment, field stays nil (matches json.Unmarshal)
}

// image_url can be string or object
if imgv := v.Get("image_url"); imgv != nil {
    switch imgv.Type() {
    case fastjson.TypeString:
        item.ImageURL = MessageContentItemImageURLString(string(imgv.GetStringBytes()))
    case fastjson.TypeObject:
        item.ImageURL = &MessageContentItemImageURL{
            URL:    string(imgv.GetStringBytes("url")),
            Detail: string(imgv.GetStringBytes("detail")),
        }
    }
}
```

### Kind 3: Has a `type` field, but variants have field type conflicts

The JSON object has a `type` field, but different variants use incompatible Go types for the same JSON key (e.g. `citations` is `[]*Citation` in the `text` variant but `*DocumentCitations` in the `document` variant), making a single struct impossible. Peek at the `type` value, then switch to different concrete types:

```go
// anthropic.MessageContentBlockParam example
if cb := v.Get("content_block"); cb != nil {
    blockType := string(cb.GetStringBytes("type"))
    switch blockType {
    case "text":
        block := &TextBlockParam{}
        block.ParseFastJSON(cb)
        e.ContentBlock = block
    case "thinking":
        block := &ThinkingBlockParam{}
        block.ParseFastJSON(cb)
        e.ContentBlock = block
    case "tool_use":
        block := &ToolUseBlockParam{}
        block.ParseFastJSON(cb)
        e.ContentBlock = block
    default:
        block := &GeneralBlockParam{}  // fallback — forward compatibility
        block.ParseFastJSON(cb)
        e.ContentBlock = block
    }
}
```

The `default` branch uses a fallback type (e.g. `GeneralBlockParam`) to ensure new `type` values from the API don't break.

### Three kinds of polymorphism compared

| | Kind 1 (single struct) | Kind 2 (no type field) | Kind 3 (type + field conflicts) |
|---|---|---|---|
| JSON characteristic | Has `type`, no field conflicts | No `type`, same key with different JSON types | Has `type`, but field types conflict |
| Dispatch method | No dispatch, read fields independently | `switch v.Type()` | `switch string(v.GetStringBytes("type"))` |
| Fallback needed | No | Unmatched type is skipped | Yes, e.g. `GeneralBlockParam` fallback |
| Reference implementation | — | `Message.Content` | `anthropic.parseContentBlockParamJLexer` |

## fastjson API quick reference

| Method | Return value | Behavior for absent key | Notes |
|---|---|---|---|
| `fastjson.ParseBytes(data)` | `(*Value, error)` | — | Package-level function, creates a new Parser each time |
| `v.GetStringBytes("key")` | `[]byte` | `nil` | `string(nil)` = `""` |
| `v.GetInt("key")` | `int` | `0` | |
| `v.Get("key")` | `*Value` | `nil` | **Subsequent `.Type()` will panic** |
| `v.Type()` | `fastjson.Type` | — | **Nil receiver panics; must check `!= nil` first** |
| `v.Array()` | `([]*Value, error)` | — | Only call when `Type() == TypeArray` |

### fastjson.Type constants

`TypeObject` · `TypeArray` · `TypeString` · `TypeNumber` · `TypeNull` · `TypeBoolean` · `TypeTrue` · `TypeFalse`

## Key pitfall: nil checking

**`v.Get("missing_key")` returns nil; calling `.Type()` on nil panics.**

This is the most common runtime error during development. All return values of `v.Get("key")` must be checked for `!= nil` before calling `.Type()`:

```go
// Bad: panics if "system_fingerprint" is absent
if sf := v.Get("system_fingerprint"); sf.Type() == fastjson.TypeString { ... }

// Good: safe
if sf := v.Get("system_fingerprint"); sf != nil && sf.Type() == fastjson.TypeString { ... }
```

`GetStringBytes` and `GetInt` handle nil internally, so no extra check is needed.

## Aligning with standard json.Unmarshal behavior

The parser must produce results identical to `json.Unmarshal`, including edge cases:

| JSON situation | json.Unmarshal behavior | fastjson parser should do |
|---|---|---|
| Key absent | Field keeps zero value | `GetStringBytes` returns nil -> `""`; `Get` returns nil -> skip |
| `"key": null` | Pointer field is nil; string field is zero value | `Get` returns non-nil but `Type()` = `TypeNull`, matches no case -> skip |
| `"key": ""` | String field is `""` | `Type()` = `TypeString`, assign `""` normally |
| `"choices": []` | Slice is non-nil empty slice | `Array()` returns empty slice, `make(T, 0)` -> non-nil empty slice |
| `"choices": null` | Slice is nil | `Type()` = `TypeNull`, doesn't match `TypeArray` -> skip, field stays nil |

## Testing strategy

Compare the fastjson parser's output with standard `json.Unmarshal` output field-by-field (`assert.Equal`). Test files cannot import `octollm` (same circular dependency issue), so use `encoding/json` directly as the reference implementation:

```go
func TestStreamChunkFastJSONParser_Parse(t *testing.T) {
    fastParser := &StreamChunkFastJSONParser{}

    testCases := []struct {
        Name string
        JSON string
    }{
        {Name: "TextDelta", JSON: `{...}`},
        // ... cover all field types and edge cases
    }

    for _, tc := range testCases {
        t.Run(tc.Name, func(t *testing.T) {
            var stdChunk ChatCompletionStreamChunk
            err := json.Unmarshal([]byte(tc.JSON), &stdChunk)
            require.NoError(t, err)

            fastResult, fastErr := fastParser.Parse([]byte(tc.JSON))
            require.NoError(t, fastErr)

            fastChunk := fastResult.(*ChatCompletionStreamChunk)
            assert.Equal(t, &stdChunk, fastChunk)
        })
    }
}
```

### Test case coverage checklist

- **Each field type** at least one case: string, int, `*string`, `[]*Struct`, `*Struct`, polymorphic interface
- **Missing fields**: empty JSON `{}`, verify all fields are zero-valued
- **null values**: `"finish_reason": null`, verify pointer/slice is nil
- **Empty array**: `"choices": []`, verify slice is non-nil empty slice
- **Polymorphic dispatch**: Kind 2 string and array each one case; Kind 3 each `type` value one case
- **`[DONE]` sentinel**: verify it returns `ErrStreamDone`
- **Serialize**: verify JSON output contains key fields; verify type mismatch returns error

## Full reference

- Implementation: `pkg/types/openai/stream_chunk_fastjson_parser.go`
- Tests: `pkg/types/openai/stream_chunk_fastjson_parser_test.go`
- Target struct: `pkg/types/openai/chatcompletion_resp.go` (`ChatCompletionStreamChunk`)
- Parser interface: `pkg/octollm/request.go` (`Parser` interface)
- Standard parser (cross-reference): `pkg/octollm/json_parser.go` (`JSONParser[T]`)
- Polymorphic field patterns: `pkg/types/UNION_TYPES.md`
