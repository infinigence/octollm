package octollm

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/mailru/easyjson/jlexer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock structs with ParseJLexer (jlexer fast path) ---

type mockJLexerType struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func (m *mockJLexerType) ParseJLexer(in *jlexer.Lexer) {
	isTopLevel := in.IsStart()
	if in.IsNull() {
		if isTopLevel {
			in.Consumed()
		}
		in.Skip()
		return
	}
	in.Delim('{')
	for !in.IsDelim('}') {
		key := in.UnsafeFieldName(false)
		in.WantColon()
		switch key {
		case "name":
			if in.IsNull() {
				in.Skip()
				m.Name = ""
			} else {
				m.Name = in.String()
			}
		case "count":
			m.Count = in.Int()
		default:
			in.SkipRecursive()
		}
		in.WantComma()
	}
	in.Delim('}')
	if isTopLevel {
		in.Consumed()
	}
}

// --- Mock struct WITHOUT ParseJLexer (encoding/json fallback) ---

type mockStdType struct {
	Label string `json:"label"`
	Value int    `json:"value"`
}

// --- Mock struct that produces a jlexer error ---

type mockErrorType struct{}

func (m *mockErrorType) ParseJLexer(in *jlexer.Lexer) {
	// Intentionally call in.String() on a non-string token to trigger error
	in.Delim('{')
	in.UnsafeFieldName(false)
	in.WantColon()
	_ = in.String() // will error if value is not a string
	in.Delim('}')
}

// --- Tests ---

func TestJSONParser_Parse_JLexerPath(t *testing.T) {
	parser := &JSONParser[mockJLexerType]{}
	data := []byte(`{"name":"test","count":42}`)

	result, err := parser.Parse(data)
	require.NoError(t, err)

	m, ok := result.(*mockJLexerType)
	require.True(t, ok)
	assert.Equal(t, "test", m.Name)
	assert.Equal(t, 42, m.Count)
}

func TestJSONParser_Parse_JLexerPath_NullFields(t *testing.T) {
	parser := &JSONParser[mockJLexerType]{}
	data := []byte(`{"name":null,"count":0}`)

	result, err := parser.Parse(data)
	require.NoError(t, err)

	m, ok := result.(*mockJLexerType)
	require.True(t, ok)
	assert.Equal(t, "", m.Name)
	assert.Equal(t, 0, m.Count)
}

func TestJSONParser_Parse_JLexerPath_UnknownFields(t *testing.T) {
	parser := &JSONParser[mockJLexerType]{}
	data := []byte(`{"name":"test","count":1,"extra":"ignored","nested":{"a":1}}`)

	result, err := parser.Parse(data)
	require.NoError(t, err)

	m, ok := result.(*mockJLexerType)
	require.True(t, ok)
	assert.Equal(t, "test", m.Name)
	assert.Equal(t, 1, m.Count)
}

func TestJSONParser_Parse_JLexerPath_TopLevelNull(t *testing.T) {
	parser := &JSONParser[mockJLexerType]{}
	data := []byte(`null`)

	result, err := parser.Parse(data)
	require.NoError(t, err)

	m, ok := result.(*mockJLexerType)
	require.True(t, ok)
	assert.Equal(t, "", m.Name)
	assert.Equal(t, 0, m.Count)
}

func TestJSONParser_Parse_JLexerPath_EmptyObject(t *testing.T) {
	parser := &JSONParser[mockJLexerType]{}
	data := []byte(`{}`)

	result, err := parser.Parse(data)
	require.NoError(t, err)

	m, ok := result.(*mockJLexerType)
	require.True(t, ok)
	assert.Equal(t, "", m.Name)
	assert.Equal(t, 0, m.Count)
}

func TestJSONParser_Parse_StdPath(t *testing.T) {
	parser := &JSONParser[mockStdType]{}
	data := []byte(`{"label":"hello","value":99}`)

	result, err := parser.Parse(data)
	require.NoError(t, err)

	m, ok := result.(*mockStdType)
	require.True(t, ok)
	assert.Equal(t, "hello", m.Label)
	assert.Equal(t, 99, m.Value)
}

func TestJSONParser_Parse_StdPath_UnknownFields(t *testing.T) {
	parser := &JSONParser[mockStdType]{}
	data := []byte(`{"label":"hello","value":1,"extra":"ignored"}`)

	result, err := parser.Parse(data)
	require.NoError(t, err)

	m, ok := result.(*mockStdType)
	require.True(t, ok)
	assert.Equal(t, "hello", m.Label)
	assert.Equal(t, 1, m.Value)
}

func TestJSONParser_Parse_StdPath_TopLevelNull(t *testing.T) {
	parser := &JSONParser[mockStdType]{}
	data := []byte(`null`)

	result, err := parser.Parse(data)
	require.NoError(t, err)

	m, ok := result.(*mockStdType)
	require.True(t, ok)
	assert.Equal(t, "", m.Label)
	assert.Equal(t, 0, m.Value)
}

func TestJSONParser_Parse_StdPath_InvalidJSON(t *testing.T) {
	parser := &JSONParser[mockStdType]{}
	data := []byte(`{invalid json`)

	_, err := parser.Parse(data)
	assert.Error(t, err)
}

func TestJSONParser_Parse_JLexerPath_InvalidJSON(t *testing.T) {
	parser := &JSONParser[mockErrorType]{}
	// The mock calls in.String() on a non-string value, producing a jlexer error
	data := []byte(`{"key":123}`)

	_, err := parser.Parse(data)
	assert.Error(t, err)
}

func TestJSONParser_Parse_DoneSentinel(t *testing.T) {
	parser := &JSONParser[mockJLexerType]{}

	_, err := parser.Parse([]byte("[DONE]"))
	assert.ErrorIs(t, err, ErrStreamDone)
}

func TestJSONParser_Parse_DoneSentinelWithWhitespace(t *testing.T) {
	parser := &JSONParser[mockJLexerType]{}

	_, err := parser.Parse([]byte("  [DONE]  "))
	assert.ErrorIs(t, err, ErrStreamDone)
}

func TestJSONParser_Parse_DoneSentinelStdPath(t *testing.T) {
	parser := &JSONParser[mockStdType]{}

	_, err := parser.Parse([]byte("[DONE]"))
	assert.ErrorIs(t, err, ErrStreamDone)
}

func TestJSONParser_Parse_DoneNotFalsePositive(t *testing.T) {
	parser := &JSONParser[mockJLexerType]{}
	// "[DONE]" as a JSON string value, not the raw sentinel
	data := []byte(`{"name":"[DONE]","count":0}`)

	result, err := parser.Parse(data)
	require.NoError(t, err)

	m, ok := result.(*mockJLexerType)
	require.True(t, ok)
	assert.Equal(t, "[DONE]", m.Name)
}

func TestJSONParser_Serialize_JLexerPath(t *testing.T) {
	parser := &JSONParser[mockJLexerType]{}
	v := &mockJLexerType{Name: "test", Count: 42}

	data, err := parser.Serialize(v)
	require.NoError(t, err)

	var m mockJLexerType
	require.NoError(t, json.Unmarshal(data, &m))
	assert.Equal(t, "test", m.Name)
	assert.Equal(t, 42, m.Count)
}

func TestJSONParser_Serialize_StdPath(t *testing.T) {
	parser := &JSONParser[mockStdType]{}
	v := &mockStdType{Label: "hello", Value: 99}

	data, err := parser.Serialize(v)
	require.NoError(t, err)

	var m mockStdType
	require.NoError(t, json.Unmarshal(data, &m))
	assert.Equal(t, "hello", m.Label)
	assert.Equal(t, 99, m.Value)
}

func TestJSONParser_Serialize_TypeMismatch(t *testing.T) {
	parser := &JSONParser[mockJLexerType]{}

	// Pass a *mockStdType to a JSONParser[mockJLexerType]
	_, err := parser.Serialize(&mockStdType{Label: "wrong"})
	assert.Error(t, err)
}

func TestJSONParser_Serialize_NonPointerValue(t *testing.T) {
	parser := &JSONParser[mockJLexerType]{}

	// Pass a non-pointer value
	_, err := parser.Serialize(mockJLexerType{Name: "test"})
	assert.Error(t, err)
}

func TestJSONParser_Serialize_NilValue(t *testing.T) {
	parser := &JSONParser[mockJLexerType]{}

	_, err := parser.Serialize(nil)
	assert.Error(t, err)
}

func TestJSONParser_SatisfiesParserInterface(t *testing.T) {
	var _ Parser = (*JSONParser[mockJLexerType])(nil)
	var _ Parser = (*JSONParser[mockStdType])(nil)
	var _ Parser = (*JSONParser[string])(nil)
	var _ Parser = (*JSONParser[int])(nil)
}

func TestJSONParser_Parse_RoundTrip(t *testing.T) {
	parser := &JSONParser[mockJLexerType]{}
	original := &mockJLexerType{Name: "roundtrip", Count: 7}

	data, err := parser.Serialize(original)
	require.NoError(t, err)

	result, err := parser.Parse(data)
	require.NoError(t, err)

	m, ok := result.(*mockJLexerType)
	require.True(t, ok)
	assert.Equal(t, original, m)
}

func TestJSONParser_Parse_StdRoundTrip(t *testing.T) {
	parser := &JSONParser[mockStdType]{}
	original := &mockStdType{Label: "roundtrip", Value: 3}

	data, err := parser.Serialize(original)
	require.NoError(t, err)

	result, err := parser.Parse(data)
	require.NoError(t, err)

	m, ok := result.(*mockStdType)
	require.True(t, ok)
	assert.Equal(t, original, m)
}

func TestJSONParser_Parse_EmptyData(t *testing.T) {
	// Empty data should not match [DONE] sentinel
	t.Run("JLexerPath", func(t *testing.T) {
		parser := &JSONParser[mockJLexerType]{}
		_, err := parser.Parse([]byte(""))
		assert.Error(t, err)
		assert.False(t, errors.Is(err, ErrStreamDone))
	})

	t.Run("StdPath", func(t *testing.T) {
		parser := &JSONParser[mockStdType]{}
		_, err := parser.Parse([]byte(""))
		assert.Error(t, err)
		assert.False(t, errors.Is(err, ErrStreamDone))
	})
}

func TestJSONParser_Parse_PrimitiveType(t *testing.T) {
	parser := &JSONParser[string]{}

	result, err := parser.Parse([]byte(`"hello"`))
	require.NoError(t, err)

	s, ok := result.(*string)
	require.True(t, ok)
	assert.Equal(t, "hello", *s)
}

func TestJSONParser_Serialize_PrimitiveType(t *testing.T) {
	parser := &JSONParser[string]{}
	v := "hello"

	data, err := parser.Serialize(&v)
	require.NoError(t, err)
	assert.Equal(t, `"hello"`, string(data))
}
