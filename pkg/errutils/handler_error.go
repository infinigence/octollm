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
	Err        error  // 原始错误
	StatusCode int    // HTTP 状态码
	Message    string // 对外显示的消息
}

func (e *HandlerError) Error() string {
	return e.Err.Error()
}

func (e *HandlerError) Unwrap() error {
	return e.Err
}

func WithHandlerError(r *http.Request, err *HandlerError) *http.Request {
	ctx := context.WithValue(r.Context(), errorKey, err)
	return r.WithContext(ctx)
}

func WithError(r *http.Request, err error, status int, msg string) *http.Request {
	return WithHandlerError(r, NewHandlerError(err, status, msg))
}

// 创建错误实例的辅助函数
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
			w.WriteHeader(err.StatusCode)
			w.Write(errMsgBytes)
		}
	})
}
