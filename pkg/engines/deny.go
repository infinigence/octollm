package engines

import (
	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/octollm"
)

type DenyEngine struct {
	ReasonText     string `json:"reason_text" yaml:"reason_text"`
	HTTPStatusCode int    `json:"http_status_code" yaml:"http_status_code"`
}

var _ octollm.Engine = (*DenyEngine)(nil)

func (e *DenyEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	return nil, &errutils.UpstreamRespError{
		StatusCode: e.HTTPStatusCode,
		Body:       []byte(e.ReasonText),
	}
}
