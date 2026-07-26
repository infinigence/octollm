package engines

import (
	"errors"
	"net/http"
	"testing"

	"github.com/infinigence/octollm/pkg/errutils"
)

func TestDenyEngineProcess_UsesConfiguredErrorMetadata(t *testing.T) {
	param := "n"
	engine := &DenyEngine{
		ReasonText:     "only n=1 is allowed for this model",
		HTTPStatusCode: http.StatusBadRequest,
		Type:           "invalid_request_error",
		Param:          &param,
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
	if handlerErr.Param == nil || *handlerErr.Param != param {
		t.Fatalf("Param = %v, want %q", handlerErr.Param, param)
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
