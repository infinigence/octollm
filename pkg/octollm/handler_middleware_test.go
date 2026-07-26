package octollm

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/infinigence/octollm/pkg/errutils"
)

func TestErrorHandlingMiddleware_SelectsByAPIFormat(t *testing.T) {
	tests := []struct {
		name       string
		format     APIFormat
		wantBody   string
		wantHeader string
	}{
		{
			name:       "OpenAI",
			format:     APIFormatChatCompletions,
			wantBody:   `{"error":{"message":"bad","type":"invalid_request_error","param":null,"code":"invalid_request"}}`,
			wantHeader: "application/json",
		},
		{
			name:       "Claude",
			format:     APIFormatClaudeMessages,
			wantBody:   `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`,
			wantHeader: "application/json",
		},
		{
			name:       "default",
			format:     APIFormatRerank,
			wantBody:   "bad",
			wantHeader: "text/plain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				*r = *errutils.WithError(
					r,
					errors.New("boom"),
					http.StatusBadRequest,
					"bad",
					"invalid_request_error",
					"invalid_request",
				)
			})
			handler := ErrorHandlingMiddleware(tt.format, next)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/same-path-for-all-formats", nil))

			if got := rec.Header().Get("Content-Type"); got != tt.wantHeader {
				t.Fatalf("Content-Type = %q, want %q", got, tt.wantHeader)
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Fatalf("body = %q, want %q", got, tt.wantBody)
			}
		})
	}
}
