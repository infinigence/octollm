package client

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
)

// capturedRequest holds what the test server received.
type capturedRequest struct {
	header     http.Header
	bodyBytes  []byte
	bodyGunzip []byte // set when Content-Encoding is gzip
}

func newEchoServer(t *testing.T, captured *capturedRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.header = r.Header.Clone()
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		captured.bodyBytes = body

		if r.Header.Get("Content-Encoding") == "gzip" {
			gr, err := gzip.NewReader(bytes.NewReader(body))
			require.NoError(t, err)
			uncompressed, err := io.ReadAll(gr)
			require.NoError(t, err)
			captured.bodyGunzip = uncompressed
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[]}`))
	}))
}

func TestGeneralEndpoint_RequestCompression_Gzip(t *testing.T) {
	var captured capturedRequest
	srv := newEchoServer(t, &captured)
	defer srv.Close()

	endpoint := NewGeneralEndpoint(GeneralEndpointConfig{
		BaseURL: srv.URL,
		Endpoints: map[octollm.APIFormat]string{
			octollm.APIFormatChatCompletions: "/v1/chat/completions",
		},
		RequestCompression: "gzip",
	})

	req := testhelper.CreateTestRequest()
	originalBody, err := req.Body.Bytes()
	require.NoError(t, err)

	_, err = endpoint.Process(req)
	require.NoError(t, err)

	assert.Equal(t, "gzip", captured.header.Get("Content-Encoding"))
	assert.JSONEq(t, string(originalBody), string(captured.bodyGunzip),
		"decompressed body should match original")
}
