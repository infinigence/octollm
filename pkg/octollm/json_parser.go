package octollm

import (
	"encoding/json"
	"fmt"
	"strings"
)

type JSONParser[T any] struct{}

var _ Parser = (*JSONParser[string])(nil)

var ErrStreamDone = fmt.Errorf("stream done")

func (p *JSONParser[T]) Parse(data []byte) (any, error) {
	var v T
	err := json.Unmarshal(data, &v)
	if err != nil {
		if strings.TrimSpace(string(data)) == "[DONE]" {
			return nil, fmt.Errorf("%w", ErrStreamDone)
		}
		return nil, err
	}
	return &v, nil
}

func (p *JSONParser[T]) Serialize(v any) ([]byte, error) {
	if vv, ok := v.(*T); ok {
		return json.Marshal(vv)
	}
	return nil, fmt.Errorf("value is not a pointer to T")
}
