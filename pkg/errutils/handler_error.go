package errutils

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
)

type ContextKey string

const errorKey ContextKey = "error"

type HandlerError struct {
	Err        error       // wrapped error returned to middleware
	StatusCode int         // HTTP status for the client response
	Message    string      // body/message written to the client
	Header     http.Header // optional headers to include in the response
}

func (e *HandlerError) Error() string {
	return e.Err.Error()
}

func (e *HandlerError) Unwrap() error {
	return e.Err
}

func (e *HandlerError) AddHeader(key, value string) {
	if e.Header == nil {
		e.Header = make(http.Header)
	}
	e.Header.Add(key, value)
}

func WithHandlerError(r *http.Request, err *HandlerError) *http.Request {
	ctx := context.WithValue(r.Context(), errorKey, err)
	return r.WithContext(ctx)
}

func WithError(r *http.Request, err error, status int, msg string) *http.Request {
	return WithHandlerError(r, NewHandlerError(err, status, msg))
}

// NewHandlerError builds a HandlerError for ErrorHandlingMiddleware.
func NewHandlerError(err error, status int, msg string) *HandlerError {
	return &HandlerError{
		Err:        err,
		StatusCode: status,
		Message:    msg,
	}
}

func ErrorHandlingMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)

		if err, ok := r.Context().Value(errorKey).(*HandlerError); ok {
			slog.ErrorContext(r.Context(), fmt.Sprintf("Handler error: %v (returned as: %v)", err.Err, err.Message))

			errMsgBytes := []byte(err.Message)
			if json.Valid(errMsgBytes) {
				w.Header().Set("Content-Type", "application/json")
			} else {
				w.Header().Set("Content-Type", "text/plain")
			}
			for key, values := range err.Header {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
			w.WriteHeader(err.StatusCode)
			w.Write(errMsgBytes)
		}
	})
}
