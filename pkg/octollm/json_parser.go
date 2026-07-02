package octollm

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mailru/easyjson/jlexer"
)

type JSONParser[T any] struct{}

var _ Parser = (*JSONParser[string])(nil)

type JLexerParser interface {
	ParseJLexer(in *jlexer.Lexer)
}

var ErrStreamDone = fmt.Errorf("stream done")

func (p *JSONParser[T]) Parse(data []byte) (any, error) {
	if strings.TrimSpace(string(data)) == "[DONE]" {
		return nil, fmt.Errorf("%w", ErrStreamDone)
	}

	v := new(T)
	switch jlparser := any(v).(type) {
	case JLexerParser:
		in := jlexer.Lexer{Data: data}
		jlparser.ParseJLexer(&in)
		if err := in.Error(); err != nil {
			return nil, err
		}
		return v, nil
	default:
		err := json.Unmarshal(data, v)
		if err != nil {
			return nil, err
		}
		return v, nil
	}
}

func (p *JSONParser[T]) Serialize(v any) ([]byte, error) {
	if vv, ok := v.(*T); ok {
		return json.Marshal(vv)
	}
	return nil, fmt.Errorf("value is not a pointer to T")
}
