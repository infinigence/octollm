package errutils

import (
	"fmt"
	"net/http"
)

// UpstreamRespError indicates an error that is returned by the upstream server.
type UpstreamRespError struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func (e *UpstreamRespError) Error() string {
	return fmt.Sprintf("upstream response error: status code %d, body %s", e.StatusCode, string(e.Body))
}

// UpstreamHTTPError indicates an error during HTTP request to the upstream server.
type UpstreamHTTPError struct {
	Err        error
	StatusCode int
}

func (e *UpstreamHTTPError) Error() string {
	if e.StatusCode == 0 {
		return fmt.Sprintf("upstream http error: err %s", e.Err.Error())
	}
	return fmt.Sprintf("upstream http error: status code %d, err %s", e.StatusCode, e.Err.Error())
}

// type UpstreamGeneralError struct {
// 	Err error
// }

// func (e *UpstreamGeneralError) Error() string {
// 	return fmt.Sprintf("upstream other error: err %s", e.Err.Error())
// }

// func UpstreamGeneralErrorf(str string, args ...any) *UpstreamGeneralError {
// 	return &UpstreamGeneralError{
// 		Err: fmt.Errorf(str, args...),
// 	}
// }
