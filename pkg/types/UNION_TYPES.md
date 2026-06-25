# JSON Polymorphic Fields

When a JSON field can legally hold values of different types (e.g. a string _or_ an array, a string _or_ an object), Go's `json.Unmarshal` cannot directly populate an interface variable—it doesn't know which concrete type to instantiate. This document describes the pattern used in this project to handle such fields.

## Two Kinds of Polymorphism

### Kind 1: Discriminated by a `type` field

The JSON object carries a `type` field that identifies the variant. No special handling is needed—define one struct with all variant-specific fields as optional pointers and let the standard `json` package do the work.

```json
{"type": "text",      "text": "hello"}
{"type": "image_url",  "image_url": {"url": "...", "detail": "high"}}
```

```go
type ContentItem struct {
    Type     string          `json:"type"`
    Text     string          `json:"text,omitempty"`
    ImageURL *ImageURLObject `json:"image_url,omitempty"`
}
```

> **Limitation:** This only works when all variants can share one struct without conflicts. If two variants define the same JSON key with incompatible Go types (e.g. `citations` as `[]*Citation` in one variant but `*DocumentCitations` in another), or a field needs `omitempty` in one variant but not another (e.g. `signature` is required for `thinking` blocks but absent for `text` blocks), a single struct is impossible. You must use Kind 2 even though a `type` field exists—see [Type-discriminated with field conflicts](#type-discriminated-with-field-conflicts) below.

### Kind 2: No `type` field—same field, fundamentally different JSON types

The same JSON key can be a string, an array, or an object with no common discriminator.

```json
"content": "hello"
"content": [{"type": "text", "text": "hello"}]

"stop": "end"
"stop": ["stop", "end"]

"tool_choice": "auto"
"tool_choice": {"type": "function", "function": {"name": "get_weather"}}
```

**This is the case that requires the Interface + Field pattern described below.**

A field can mix both kinds: the top-level split is Kind 2 (string vs. object), while the object's internal variants are distinguished by their `type` field (Kind 1). See the `ToolChoice` example below.

<a id="type-discriminated-with-field-conflicts"></a>

#### Type-discriminated with field conflicts

A subtler case: the JSON object has a `type` field (looking like Kind 1), but the variants have field conflicts that make a single struct impossible. The solution is still the Interface + Field pattern, but the Field helper dispatches by peeking at `type` rather than trying each concrete type sequentially.

Consider `MessageContentBlockParam` from the Anthropic Messages API. All variants carry a `type` field, but:

- **`citations`** is `[]*Citation` in `text` blocks vs `*DocumentCitations` in `document` blocks — same key, incompatible Go types.
- **`signature`** has no `omitempty` in `thinking` blocks — it must always marshal, even as `""`. Other variants don't have this field at all.
- **`content`** in `tool_result` blocks is itself polymorphic (string or array) — nested Kind 2.

```json
{"type": "text",        "text": "hello"}
{"type": "thinking",    "thinking": "...", "signature": "sig_123"}
{"type": "tool_use",    "id": "toolu_1", "name": "get_weather", "input": {}}
{"type": "tool_result", "tool_use_id": "toolu_1", "content": "Sunny, 25°C"}
```

## The Interface + Field Pattern

### Step 1 — Define an interface

Name the interface with a `Value` suffix. It serves as the declared field type in the host struct. Include business methods if there is shared behavior; otherwise use a private marker method.

```go
// With business methods
type MessageContent interface {
    ExtractText() string
}

// With only a marker method (no shared behavior)
type StopValue interface {
    isStopValue()
}
```

### Step 2 — Define concrete types

Each JSON form maps to one concrete type that satisfies the interface.

Simple case—string vs. array:

```go
type StopString string
func (StopString) isStopValue() {}

type StopArray []string
func (StopArray) isStopValue() {}
```

String vs. object, where the object itself uses Kind 1 internally:

```go
type ToolChoiceString string
func (ToolChoiceString) isToolChoiceValue() {}

type ToolChoiceObject struct {
    Type         string                  `json:"type"`
    Function     *ToolChoiceFunction     `json:"function,omitempty"`
    AllowedTools *ToolChoiceAllowedTools `json:"allowed_tools,omitempty"`
    Custom       *ToolChoiceCustom       `json:"custom,omitempty"`
}
func (ToolChoiceObject) isToolChoiceValue() {}
```

Type-discriminated concrete types (when `type` exists but field conflicts prevent Kind 1):

```go
type MessageContentBlockParam interface {
    ExtractText() string
}

type TextBlockParam struct {
    Type       string        `json:"type"`           // "text"
    Text       string        `json:"text"`
    Citations  []*Citation   `json:"citations,omitempty"`
}

type ThinkingBlockParam struct {
    Type      string `json:"type"`                  // "thinking"
    Thinking  string `json:"thinking"`
    Signature string `json:"signature"`             // no omitempty — always present, even ""
}

type ToolUseBlockParam struct {
    Type  string          `json:"type"`             // "tool_use"
    ID    string          `json:"id"`
    Name  string          `json:"name"`
    Input json.RawMessage `json:"input"`
}

// Fallback for unrecognized types — forward compatibility
type GeneralBlockParam struct {
    Type string `json:"type"`
}
```

### Step 3 — Define a private `xxxField` helper

Go cannot unmarshal directly into an interface. Define a private struct with an `UnmarshalJSON` method that attempts each possible concrete type in order:

```go
type stopField struct {
    Value StopValue
}

func (s *stopField) UnmarshalJSON(data []byte) error {
    var str string
    if err := json.Unmarshal(data, &str); err == nil {
        s.Value = StopString(str)
        return nil
    }
    var arr []string
    if err := json.Unmarshal(data, &arr); err != nil {
        return err
    }
    s.Value = StopArray(arr)
    return nil
}
```

```go
type toolChoiceField struct {
    Value ToolChoiceValue
}

func (t *toolChoiceField) UnmarshalJSON(data []byte) error {
    var str string
    if err := json.Unmarshal(data, &str); err == nil {
        t.Value = ToolChoiceString(str)
        return nil
    }
    var obj ToolChoiceObject
    if err := json.Unmarshal(data, &obj); err != nil {
        return err
    }
    t.Value = obj
    return nil
}
```

**Dispatch by `type` field (type-switch):**

When a `type` discriminator exists but field conflicts prevent a single struct, the Field helper peeks at `type` to choose the concrete type, then unmarshals the full data into it:

```go
type messageContentBlockParamField struct {
    Value MessageContentBlockParam
}

func (f *messageContentBlockParamField) UnmarshalJSON(data []byte) error {
    if string(data) == "null" || len(data) == 0 {
        return nil
    }
    var peek struct {
        Type string `json:"type"`
    }
    if err := json.Unmarshal(data, &peek); err != nil {
        return err
    }
    var v MessageContentBlockParam = &GeneralBlockParam{} // fallback
    switch peek.Type {
    case "text":
        v = &TextBlockParam{}
    case "thinking":
        v = &ThinkingBlockParam{}
    case "tool_use":
        v = &ToolUseBlockParam{}
    case "tool_result":
        v = &ToolResultBlockParam{}
    // ... other variants
    }
    if err := json.Unmarshal(data, v); err != nil {
        return err
    }
    f.Value = v
    return nil
}
```

The `GeneralBlockParam` fallback ensures forward compatibility: new `type` values from the API don't break unmarshaling.

**Two dispatch strategies:**

| | Try-each-type | Type-switch |
|---|---|---|
| When | No `type` field; variants are different JSON types | `type` exists but field conflicts |
| How | `json.Unmarshal` into each candidate; first success wins | Peek `type`, `switch` to pick type, then `json.Unmarshal` |
| Fallback | Last attempt's error | `GeneralBlockParam` catch-all |

### Step 4 — Use in the host struct's `UnmarshalJSON`

Override only the polymorphic fields in an auxiliary struct. Use `type Alias HostType` + embedding `*Alias` so all other fields are automatically unmarshaled without manual assignment.

```go
type Request struct {
    Model      string         `json:"model"`
    Stop       StopValue      `json:"stop,omitempty"`
    ToolChoice ToolChoiceValue `json:"tool_choice,omitempty"`
}

func (r *Request) UnmarshalJSON(data []byte) error {
    type Alias Request
    aux := struct {
        Stop       stopField       `json:"stop,omitempty"`
        ToolChoice toolChoiceField `json:"tool_choice,omitempty"`
        *Alias
    }{
        Alias: (*Alias)(r),
    }
    if err := json.Unmarshal(data, &aux); err != nil {
        return err
    }
    r.Stop = aux.Stop.Value
    r.ToolChoice = aux.ToolChoice.Value
    return nil
}
```

**Nested polymorphism:** A concrete type may itself contain a polymorphic field. In that case, the concrete type also needs its own `UnmarshalJSON` using the same alias pattern. For example, `ToolResultBlockParam.Content` is a `MessageContent` (Kind 2), so it overrides `content` with `messageContentField`:

```go
func (b *ToolResultBlockParam) UnmarshalJSON(data []byte) error {
    type Alias ToolResultBlockParam
    aux := struct {
        Content messageContentField `json:"content,omitempty"`
        *Alias
    }{
        Alias: (*Alias)(b),
    }
    if err := json.Unmarshal(data, &aux); err != nil {
        return err
    }
    b.Content = aux.Content.Value
    return nil
}
```

**Array of polymorphic objects:** When the field is a slice of interface values (`[]MessageContentBlockParam`), define a custom slice type with `UnmarshalJSON` that delegates each element to the Field helper:

```go
type MessageContentBlockArray []MessageContentBlockParam

func (m *MessageContentBlockArray) UnmarshalJSON(data []byte) error {
    if string(data) == "null" || len(data) == 0 {
        return nil
    }
    var fields []messageContentBlockParamField
    if err := json.Unmarshal(data, &fields); err != nil {
        return err
    }
    *m = make(MessageContentBlockArray, len(fields))
    for i, field := range fields {
        (*m)[i] = field.Value
    }
    return nil
}
```

**Why `type Alias` + `*Alias`?**
- `type Alias` strips the `UnmarshalJSON` method from the host type, preventing infinite recursion.
- Embedding `*Alias` lets the standard unmarshaler handle all non-polymorphic fields automatically.
- Only the polymorphic fields need overriding (replace the interface type with `xxxField`) and manual assignment (extract `.Value`).

## Marshaling

Interface values marshal correctly out of the box—the `json` package dispatches to the underlying concrete type:

```go
StopString("end")                                  → "end"
StopArray{"a", "b"}                                → ["a","b"]
ToolChoiceString("auto")                           → "auto"
ToolChoiceObject{Type:"function", Function:&{...}} → {"type":"function","function":{"name":"..."}}
```

No custom `MarshalJSON` is needed.

## String() methods

Implement `String()` on a concrete type only when the default formatting is insufficient—for example, when logging needs a concise summary rather than the full struct dump. Do **not** implement `String()` if it merely reproduces the default behavior.

```go
// Worth implementing: ToolChoiceObject's default fmt output is the entire struct,
// but logs only need a short label like "function(get_weather)".
func (t ToolChoiceObject) String() string {
    switch t.Type {
    case "function":
        if t.Function != nil {
            return fmt.Sprintf("function(%s)", t.Function.Name)
        }
    case "allowed_tools":
        return "allowed_tools"
    case "custom":
        if t.Custom != nil {
            return fmt.Sprintf("custom(%s)", t.Custom.Name)
        }
    }
    return t.Type
}

// NOT worth implementing: StopString is just a string, and StopArray is just a
// []string—fmt already produces the right output. Adding String() methods here
// would be redundant.
```

The `%s` / `%v` format verbs automatically call the underlying concrete type's `String()` method even when the value is stored in an interface variable:

```go
fmt.Fprintf(w, "  ToolChoice: %s\n", r.ToolChoice) // r.ToolChoice is ToolChoiceValue interface
```
