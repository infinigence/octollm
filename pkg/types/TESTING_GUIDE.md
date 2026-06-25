# JSON Polymorphic Types — Test Writing Guide

## Test Structure

### Top-down ordering

Place the top-level struct test first, then sub-component tests below. Readers see the most important end-to-end test immediately; sub-component tests serve as documentation for individual pieces.

```
TestResponsesRequest_Marshal_UnmarshalJSON       ← top-level struct
TestResponsesInputValue_Marshal_UnmarshalJSON    ← interface + field helper
TestResponsesInputMessageContent_Marshal_UnmarshalJSON  ← nested interface
TestResponsesInputMessageContent_ExtractText      ← business method
TestResponsesInputMessage_Marshal_UnmarshalJSON   ← mid-level struct
TestResponsesResponse_UnmarshalJSON               ← response struct
```

### testCases table-driven

Always use a `testCases` slice of named structs, even with a single case. This makes adding cases trivial and keeps the pattern consistent.

```go
func TestFoo_Marshal_UnmarshalJSON(t *testing.T) {
    testCases := []struct {
        Name   string
        JSON   string
        Object Foo
    }{
        {
            Name:   "Bar",
            JSON:   `{"field":"value"}`,
            Object: Foo{Field: "value"},
        },
    }

    for _, tc := range testCases {
        t.Run("Unmarshal_"+tc.Name, func(t *testing.T) { ... })
        t.Run("Marshal_"+tc.Name, func(t *testing.T) { ... })
    }
}
```

## Marshal / Unmarshal Pairs

By default, every test case should include both Marshal and Unmarshal sub-tests. When certain cases cannot round-trip (e.g. minimal-view structs that ignore JSON keys), add `MarshalOnly` / `UnmarshalOnly` bool fields to the testCases struct to skip the relevant sub-test:

```go
testCases := []struct {
    Name          string
    JSON          string
    Object        TargetType
    MarshalOnly   bool
    UnmarshalOnly bool
}{
    {
        Name:   "RoundTrip",
        JSON:   `{"field":"value"}`,
        Object: TargetType{Field: "value"},
    },
    {
        Name:          "MinimalView",
        JSON:          `{"field":"value","extra":"ignored"}`,
        Object:        TargetType{Field: "value"},
        UnmarshalOnly: true, // Marshal output won't include "extra"
    },
}

for _, tc := range testCases {
    if !tc.UnmarshalOnly {
        t.Run("Marshal_"+tc.Name, func(t *testing.T) { ... })
    }
    if !tc.MarshalOnly {
        t.Run("Unmarshal_"+tc.Name, func(t *testing.T) { ... })
    }
}
```

This keeps all cases in a single table while cleanly handling asymmetry.

## Testing Interface-Level Types

Interface values (`XxxValue`) and their private field helpers (`xxxField`) are tested together. Unmarshal goes through the field helper; Marshal goes through the concrete type directly.

```go
func TestStopValue_Marshal_UnmarshalJSON(t *testing.T) {
    testCases := []struct {
        Name   string
        JSON   string
        Object StopValue
    }{
        {Name: "String", JSON: `"end"`, Object: StopString("end")},
        {Name: "Array", JSON: `["a","b"]`, Object: StopArray{"a", "b"}},
    }

    for _, tc := range testCases {
        t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
            var sf stopField
            err := json.Unmarshal([]byte(tc.JSON), &sf)
            require.NoError(t, err)
            assert.Equal(t, tc.Object, sf.Value)
        })
        t.Run("Marshal_"+tc.Name, func(t *testing.T) {
            data, err := json.Marshal(tc.Object)
            require.NoError(t, err)
            assert.JSONEq(t, tc.JSON, string(data))
        })
    }
}
```

## Testing Business Methods Separately

Interface business methods (e.g. `ExtractText()`) should have their own test function, independent of Marshal/Unmarshal. Unmarshal the value through the field helper, then assert on the method result.

```go
func TestResponsesInputMessageContent_ExtractText(t *testing.T) {
    testCases := []struct {
        Name          string
        JSON          string
        ExtractedText string
    }{
        {Name: "String", JSON: `"hello"`, ExtractedText: "hello"},
        {Name: "Array", JSON: `[{"type":"input_text","text":"hi"}]`, ExtractedText: "hi"},
    }

    for _, tc := range testCases {
        t.Run("ExtractText_"+tc.Name, func(t *testing.T) {
            var sf responsesInputMessageContentField
            err := json.Unmarshal([]byte(tc.JSON), &sf)
            require.NoError(t, err)
            assert.Equal(t, tc.ExtractedText, sf.Value.ExtractText())
        })
    }
}
```

## Test Data Guidelines

### Use non-zero values

Prefer non-zero test values (e.g. `CachedTokens: 3` not `0`, `ReasoningTokens: 2` not `0`). Zero values can mask bugs where a field is simply not set — a nil pointer and a pointer-to-zero both produce `0` on access, but are semantically different.

### Don't test language guarantees

Skip cases that are guaranteed by Go's `json` package behavior, such as:
- Reusing a variable for sequential unmarshal (Go overwrites all fields)
- Invalid JSON types producing errors (the `json` package handles this)

Focus test effort on the **polymorphic dispatch logic** — the core value of the Interface + Field pattern.
