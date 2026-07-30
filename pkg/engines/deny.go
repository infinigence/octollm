package engines

import (
	"errors"
	"net/http"

	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/octollm"
)

type DenyEngine struct {
	ReasonText     string `json:"reason_text" yaml:"reason_text"`
	HTTPStatusCode int    `json:"http_status_code" yaml:"http_status_code"`
	Type           string `json:"type,omitempty" yaml:"type,omitempty"`
	Code           string `json:"code,omitempty" yaml:"code,omitempty"`
}

var ErrRequestDenied = errors.New("request denied")

var _ octollm.Engine = (*DenyEngine)(nil)

func (e *DenyEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	status := e.HTTPStatusCode
	if status == 0 {
		status = http.StatusBadRequest
	}
	return nil, errutils.NewHandlerError(
		ErrRequestDenied,
		status,
		e.ReasonText,
		e.Type,
		e.Code,
	)
}
