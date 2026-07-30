package engines

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/octollm"
)

func TestDenyEngineProcess_UsesConfiguredErrorMetadata(t *testing.T) {
	engine := &DenyEngine{
		ReasonText:     "only n=1 is allowed for this model",
		HTTPStatusCode: http.StatusBadRequest,
		Type:           "invalid_request_error",
		Code:           "invalid_request",
	}

	_, err := engine.Process(nil)
	var handlerErr *errutils.HandlerError
	if !errors.As(err, &handlerErr) {
		t.Fatalf("error = %T, want *errutils.HandlerError", err)
	}
	if handlerErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("StatusCode = %d, want %d", handlerErr.StatusCode, http.StatusBadRequest)
	}
	if handlerErr.Message != engine.ReasonText {
		t.Fatalf("Message = %q, want %q", handlerErr.Message, engine.ReasonText)
	}
	if handlerErr.Type != engine.Type {
		t.Fatalf("Type = %q, want %q", handlerErr.Type, engine.Type)
	}
	if handlerErr.Code != engine.Code {
		t.Fatalf("Code = %q, want %q", handlerErr.Code, engine.Code)
	}
}

func TestDenyEngineProcess_LeavesProtocolMetadataForMiddleware(t *testing.T) {
	engine := &DenyEngine{ReasonText: "denied"}

	_, err := engine.Process(nil)
	var handlerErr *errutils.HandlerError
	if !errors.As(err, &handlerErr) {
		t.Fatalf("error = %T, want *errutils.HandlerError", err)
	}
	if handlerErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("StatusCode = %d, want %d", handlerErr.StatusCode, http.StatusBadRequest)
	}
	if handlerErr.Type != "" {
		t.Fatalf("Type = %q, want empty", handlerErr.Type)
	}
	if handlerErr.Code != "" {
		t.Fatalf("Code = %q, want empty", handlerErr.Code)
	}
}

func TestDenyEngineProcess_RendersConfiguredErrorByAPIFormat(t *testing.T) {
	tests := []struct {
		name       string
		engine     *DenyEngine
		handler    func(octollm.Engine) http.HandlerFunc
		wantStatus int
		wantType   string
		wantBody   string
	}{
		{
			name: "OpenAI derives missing metadata",
			engine: &DenyEngine{
				ReasonText:     "limited",
				HTTPStatusCode: http.StatusTooManyRequests,
			},
			handler:    octollm.ChatCompletionsHandler,
			wantStatus: http.StatusTooManyRequests,
			wantType:   "application/json",
			wantBody:   `{"error":{"message":"limited","type":"rate_limit_error","param":null,"code":"rate_limit_exceeded"}}`,
		},
		{
			name: "OpenAI preserves custom metadata",
			engine: &DenyEngine{
				ReasonText:     "only n=1 is allowed",
				HTTPStatusCode: http.StatusUnprocessableEntity,
				Type:           "custom_request_error",
				Code:           "only_n_1",
			},
			handler:    octollm.ChatCompletionsHandler,
			wantStatus: http.StatusUnprocessableEntity,
			wantType:   "application/json",
			wantBody:   `{"error":{"message":"only n=1 is allowed","type":"custom_request_error","param":null,"code":"only_n_1"}}`,
		},
		{
			name: "Claude preserves type and omits code",
			engine: &DenyEngine{
				ReasonText:     "denied",
				HTTPStatusCode: http.StatusForbidden,
				Type:           "permission_error",
				Code:           "ignored_by_claude",
			},
			handler:    octollm.MessagesHandler,
			wantStatus: http.StatusForbidden,
			wantType:   "application/json",
			wantBody:   `{"type":"error","error":{"type":"permission_error","message":"denied"}}`,
		},
		{
			name: "default format returns message only",
			engine: &DenyEngine{
				ReasonText:     "denied",
				HTTPStatusCode: http.StatusTeapot,
				Type:           "ignored_by_default",
				Code:           "ignored_by_default",
			},
			handler:    octollm.RerankHandler,
			wantStatus: http.StatusTeapot,
			wantType:   "text/plain",
			wantBody:   "denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", nil)

			tt.handler(tt.engine).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Header().Get("Content-Type"); got != tt.wantType {
				t.Fatalf("Content-Type = %q, want %q", got, tt.wantType)
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Fatalf("body = %q, want %q", got, tt.wantBody)
			}
		})
	}
}
