package errutils

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

type ContextKey string

const errorKey ContextKey = "error"

type HandlerError struct {
	Err        error       // wrapped error returned to middleware
	StatusCode int         // HTTP status for the client response
	Message    string      // OpenAI and Claude error message
	Type       string      // optional protocol error type; middleware derives it when empty
	Param      *string     // OpenAI request parameter related to the error; nil for Claude
	Code       string      // optional machine-readable code; middleware derives it when needed
	Header     http.Header // optional headers to include in the response
}

type openAIErrorResponse struct {
	Error openAIError `json:"error"`
}

type openAIError struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    string  `json:"code"`
}

type claudeErrorResponse struct {
	Type  string      `json:"type"`
	Error claudeError `json:"error"`
}

type claudeError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e *HandlerError) Error() string {
	if e == nil {
		return "handler error"
	}
	if e.Err == nil {
		if e.Message != "" {
			return e.Message
		}
		return "handler error"
	}
	return e.Err.Error()
}

func (e *HandlerError) Unwrap() error {
	if e == nil {
		return nil
	}
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

func WithError(r *http.Request, err error, status int, msg, errorType, code string) *http.Request {
	return WithHandlerError(r, NewHandlerError(err, status, msg, errorType, code))
}

// NewHandlerError builds a protocol-neutral error for ErrorHandlingMiddleware.
// Protocol-specific defaults are applied by the selected middleware.
func NewHandlerError(err error, status int, msg, errorType, code string) *HandlerError {
	return &HandlerError{
		Err:        err,
		StatusCode: status,
		Message:    msg,
		Type:       errorType,
		Code:       code,
	}
}

func OpenAIErrorMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)

		err, ok := handlerErrorFromRequest(r)
		if !ok {
			return
		}
		status := normalizeErrorStatus(err.StatusCode)
		message := normalizeErrorMessage(err.Message, status)
		errorType := strings.TrimSpace(err.Type)
		if errorType == "" {
			errorType = openAIErrorType(status)
		}
		code := strings.TrimSpace(err.Code)
		if code == "" {
			code = openAIErrorCode(status)
		}

		body, marshalErr := json.Marshal(openAIErrorResponse{
			Error: openAIError{
				Message: message,
				Type:    errorType,
				Param:   err.Param,
				Code:    code,
			},
		})
		if marshalErr != nil {
			slog.ErrorContext(r.Context(), "errutils: marshal OpenAI handler error", "err", marshalErr)
			body = []byte(`{"error":{"message":"Internal Server Error","type":"server_error","param":null,"code":"gateway_internal_error"}}`)
		}
		writeHandlerError(w, r, err, status, body, "application/json")
	})
}

func ClaudeErrorMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)

		err, ok := handlerErrorFromRequest(r)
		if !ok {
			return
		}
		status := normalizeErrorStatus(err.StatusCode)
		message := normalizeErrorMessage(err.Message, status)
		errorType := strings.TrimSpace(err.Type)
		if errorType == "" {
			errorType = claudeErrorType(status)
		}

		body, marshalErr := json.Marshal(claudeErrorResponse{
			Type: "error",
			Error: claudeError{
				Type:    errorType,
				Message: message,
			},
		})
		if marshalErr != nil {
			slog.ErrorContext(r.Context(), "errutils: marshal Claude handler error", "err", marshalErr)
			body = []byte(`{"type":"error","error":{"type":"api_error","message":"Internal Server Error"}}`)
		}
		writeHandlerError(w, r, err, status, body, "application/json")
	})
}

func DefaultErrorMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)

		err, ok := handlerErrorFromRequest(r)
		if !ok {
			return
		}
		status := normalizeErrorStatus(err.StatusCode)
		message := normalizeErrorMessage(err.Message, status)

		contentType := "text/plain"
		body := []byte(message)
		if json.Valid(body) {
			contentType = "application/json"
		}
		writeHandlerError(w, r, err, status, body, contentType)
	})
}

func handlerErrorFromRequest(r *http.Request) (*HandlerError, bool) {
	err, ok := r.Context().Value(errorKey).(*HandlerError)
	return err, ok && err != nil
}

func normalizeErrorStatus(status int) int {
	if status < http.StatusBadRequest || status > 599 {
		return http.StatusInternalServerError
	}
	return status
}

func normalizeErrorMessage(message string, status int) string {
	if strings.TrimSpace(message) != "" {
		return message
	}
	if status == 499 {
		return "Client Closed Request"
	}
	if message = http.StatusText(status); message != "" {
		return message
	}
	return "Internal Server Error"
}

func openAIErrorType(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusPaymentRequired:
		return "insufficient_quota"
	case http.StatusForbidden:
		return "permission_denied_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusConflict:
		return "conflict_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case 499:
		return "client_closed_request"
	default:
		if status >= http.StatusInternalServerError {
			return "server_error"
		}
		return "invalid_request_error"
	}
}

func openAIErrorCode(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_required"
	case http.StatusPaymentRequired:
		return "insufficient_quota"
	case http.StatusForbidden:
		return "permission_denied"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusUnprocessableEntity:
		return "unprocessable_entity"
	case http.StatusTooManyRequests:
		return "rate_limit_exceeded"
	case 499:
		return "client_closed_request"
	case http.StatusServiceUnavailable:
		return "service_unavailable"
	default:
		if status >= http.StatusInternalServerError {
			return "gateway_internal_error"
		}
		return "invalid_request"
	}
}

func claudeErrorType(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusPaymentRequired:
		return "billing_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusGatewayTimeout:
		return "gateway_timeout"
	case 529:
		return "overloaded_error"
	default:
		if status >= http.StatusInternalServerError {
			return "api_error"
		}
		return "invalid_request_error"
	}
}

func writeHandlerError(w http.ResponseWriter, r *http.Request, err *HandlerError, status int, body []byte, contentType string) {
	slog.ErrorContext(r.Context(), fmt.Sprintf("Handler error: %v (returned as: %v)", err.Err, string(body)))

	header := err.Header.Clone()
	if header == nil {
		header = make(http.Header)
	}
	header.Set("Content-Type", contentType)
	header.Del("Content-Length")
	for key, values := range header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
