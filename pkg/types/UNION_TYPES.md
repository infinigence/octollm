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
