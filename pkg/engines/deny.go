package engines

import (
	"errors"

	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/octollm"
)

type DenyEngine struct {
	ReasonText     string `json:"reason_text" yaml:"reason_text"`
	HTTPStatusCode int    `json:"http_status_code" yaml:"http_status_code"`
}

var ErrRequestDenied = errors.New("request denied")

var _ octollm.Engine = (*DenyEngine)(nil)

func (e *DenyEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	return nil, &errutils.HandlerError{
		Err:        ErrRequestDenied,
		StatusCode: e.HTTPStatusCode,
		Message:    e.ReasonText,
	}
}
