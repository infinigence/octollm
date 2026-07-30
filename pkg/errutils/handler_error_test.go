package errutils

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewHandlerError_PreservesEmptyTypeForProtocolMiddleware(t *testing.T) {
	he := NewHandlerError(errors.New("x"), http.StatusBadRequest, "bad", "", "invalid_request")
	if he.Type != "" {
		t.Fatalf("Type = %q, want empty", he.Type)
	}
}

func TestNewHandlerError_PreservesConfiguredType(t *testing.T) {
	he := NewHandlerError(errors.New("x"), http.StatusBadRequest, "bad", "   ", "invalid_request")
	if he.Type != "   " {
		t.Fatalf("Type = %q, want original value", he.Type)
	}
}

func TestNewHandlerError_PreservesEmptyCodeForProtocolMiddleware(t *testing.T) {
	he := NewHandlerError(errors.New("x"), http.StatusBadRequest, "bad", "invalid_request_error", " ")
	if he.Code != " " {
		t.Fatalf("Code = %q, want original value", he.Code)
	}
}

func TestNewHandlerError_OmittedCodeRemainsEmpty(t *testing.T) {
	he := NewHandlerError(errors.New("x"), http.StatusBadRequest, "bad", "invalid_request_error", "")
	if he.Code != "" {
		t.Fatalf("Code = %q, want empty", he.Code)
	}
}

func TestNewHandlerError_SetsErrorFields(t *testing.T) {
	he := NewHandlerError(errors.New("x"), http.StatusTooManyRequests, "limited", "rate_limit_error", "rate_limit_exceeded")
	if he.Type != "rate_limit_error" {
		t.Fatalf("Type = %q, want rate_limit_error", he.Type)
	}
	if he.Code != "rate_limit_exceeded" {
		t.Fatalf("Code = %q, want rate_limit_exceeded", he.Code)
	}
	if he.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("StatusCode = %d, want %d", he.StatusCode, http.StatusTooManyRequests)
	}
}

func TestHandlerError_Error_NilReceiver(t *testing.T) {
	var he *HandlerError
	if got := he.Error(); got != "handler error" {
		t.Fatalf("Error() = %q, want %q", got, "handler error")
	}
}

func TestHandlerError_Error_NilErr(t *testing.T) {
	he := &HandlerError{Err: nil, Message: ""}
	if got := he.Error(); got != "handler error" {
		t.Fatalf("Error() = %q, want %q", got, "handler error")
	}
	he.Message = "fallback message"
	if got := he.Error(); got != "fallback message" {
		t.Fatalf("Error() = %q, want %q", got, "fallback message")
	}
}

func TestHandlerError_Unwrap_NilSafe(t *testing.T) {
	var he *HandlerError
	if he.Unwrap() != nil {
		t.Fatal("expected nil unwrap on nil receiver")
	}
}

func TestDefaultErrorMiddleware_KeepsRawBody(t *testing.T) {
	h := DefaultErrorMiddleware(func(w http.ResponseWriter, r *http.Request) {
		*r = *WithError(r, errors.New("boom"), http.StatusBadRequest, `{"code":1}`, "invalid_request_error", "invalid_request")
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := rec.Body.String(); got != `{"code":1}` {
		t.Fatalf("body = %q, want %q", got, `{"code":1}`)
	}
}

func TestOpenAIErrorMiddleware_WrapsHandlerError(t *testing.T) {
	h := OpenAIErrorMiddleware(func(w http.ResponseWriter, r *http.Request) {
		he := NewHandlerError(errors.New("boom"), http.StatusTooManyRequests, "limited", "rate_limit_error", "rate_limit_exceeded")
		param := "model"
		he.Param = &param
		he.AddHeader("Content-Length", "7")
		he.AddHeader("Retry-After", "1")
		*r = *WithHandlerError(r, he)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/custom-openai-route", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want empty", got)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
	want := `{"error":{"message":"limited","type":"rate_limit_error","param":"model","code":"rate_limit_exceeded"}}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestOpenAIErrorMiddleware_NilParamIsJSONNull(t *testing.T) {
	h := OpenAIErrorMiddleware(func(w http.ResponseWriter, r *http.Request) {
		*r = *WithError(r, errors.New("boom"), http.StatusBadRequest, "bad", "invalid_request_error", "invalid_request")
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))

	want := `{"error":{"message":"bad","type":"invalid_request_error","param":null,"code":"invalid_request"}}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestOpenAIErrorMiddleware_DerivesMissingMetadata(t *testing.T) {
	h := OpenAIErrorMiddleware(func(w http.ResponseWriter, r *http.Request) {
		*r = *WithError(r, errors.New("boom"), http.StatusTooManyRequests, "limited", "", "")
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))

	want := `{"error":{"message":"limited","type":"rate_limit_error","param":null,"code":"rate_limit_exceeded"}}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestOpenAIErrorMiddleware_NormalizesInvalidErrorStatus(t *testing.T) {
	h := OpenAIErrorMiddleware(func(w http.ResponseWriter, r *http.Request) {
		*r = *WithError(r, errors.New("boom"), http.StatusOK, "", "", "")
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	want := `{"error":{"message":"Internal Server Error","type":"server_error","param":null,"code":"gateway_internal_error"}}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestClaudeErrorMiddleware_WrapsHandlerError(t *testing.T) {
	h := ClaudeErrorMiddleware(func(w http.ResponseWriter, r *http.Request) {
		he := NewHandlerError(errors.New("boom"), http.StatusBadRequest, "bad request", "invalid_request_error", "ignored_by_claude")
		param := "model"
		he.Param = &param
		*r = *WithHandlerError(r, he)
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	want := `{"type":"error","error":{"type":"invalid_request_error","message":"bad request"}}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestClaudeErrorMiddleware_DerivesProtocolType(t *testing.T) {
	h := ClaudeErrorMiddleware(func(w http.ResponseWriter, r *http.Request) {
		*r = *WithError(r, errors.New("boom"), http.StatusForbidden, "forbidden", "", "")
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))

	want := `{"type":"error","error":{"type":"permission_error","message":"forbidden"}}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}
