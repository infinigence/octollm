package octollm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mailru/easyjson/jlexer"
)

type JSONParser[T any] struct{}

var _ Parser = (*JSONParser[string])(nil)

type JLexerParser interface {
	ParseJLexer(in *jlexer.Lexer)
}

var ErrStreamDone = fmt.Errorf("stream done")

var streamDoneMarker = []byte("[DONE]")

func (p *JSONParser[T]) Parse(data []byte) (any, error) {
	// Compare as bytes: string(data) would copy the whole body on every parse.
	if bytes.Equal(bytes.TrimSpace(data), streamDoneMarker) {
		return nil, fmt.Errorf("%w", ErrStreamDone)
	}

	v := new(T)
	switch jlparser := any(v).(type) {
	case JLexerParser:
		in := jlexer.Lexer{Data: data}
		jlparser.ParseJLexer(&in)
		if err := in.Error(); err != nil {
			// LexerError.Error() embeds a raw snippet of the input body; strip it
			// before the error propagates to logs.
			var lexErr *jlexer.LexerError
			if errors.As(err, &lexErr) {
				return nil, fmt.Errorf("json parse error: %s near offset %d", lexErr.Reason, lexErr.Offset)
			}
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
