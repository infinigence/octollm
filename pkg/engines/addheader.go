package engines

import "github.com/infinigence/octollm/pkg/octollm"

type AddHeaderEngine struct {
	Header map[string]string
	Next   octollm.Engine
}

var _ octollm.Engine = (*AddHeaderEngine)(nil)

func (e *AddHeaderEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	for k, v := range e.Header {
		req.Header.Set(k, v)
	}
	return e.Next.Process(req)
}
