package client

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
	"github.com/infinigence/octollm/pkg/types/systemone"
)

// capturedRequest holds what the test server received.
type capturedRequest struct {
	header     http.Header
	bodyBytes  []byte
	bodyGunzip []byte // set when Content-Encoding is gzip
	bodyUnzstd []byte // set when Content-Encoding is zstd
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

		if r.Header.Get("Content-Encoding") == "zstd" {
			dec, err := zstd.NewReader(bytes.NewReader(body))
			require.NoError(t, err)
			uncompressed, err := io.ReadAll(dec)
			require.NoError(t, err)
			captured.bodyUnzstd = uncompressed
			dec.Close()
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[]}`))
	}))
}

// A 307 redirect makes net/http re-send the body through GetBody. With compression,
// GetBody must return the compressed bytes, or the re-sent body goes out uncompressed
// under the compressed Content-Length and Content-Encoding.
func TestGeneralEndpoint_RequestCompression_ResendUsesCompressedBody(t *testing.T) {
	for _, compression := range []string{"gzip", "zstd"} {
		t.Run(compression, func(t *testing.T) {
			var captured capturedRequest
			echo := newEchoServer(t, &captured)
			defer echo.Close()

			redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				http.Redirect(w, r, echo.URL+r.URL.Path, http.StatusTemporaryRedirect)
			}))
			defer redirect.Close()

			endpoint := NewGeneralEndpoint(GeneralEndpointConfig{
				BaseURL: redirect.URL,
				Endpoints: map[octollm.APIFormat]string{
					octollm.APIFormatChatCompletions: "/v1/chat/completions",
				},
				RequestCompression: compression,
			})

			req := testhelper.CreateTestRequest()
			originalBody, err := req.Body.Bytes()
			require.NoError(t, err)

			_, err = endpoint.Process(req)
			require.NoError(t, err)

			assert.Equal(t, compression, captured.header.Get("Content-Encoding"))
			decoded := captured.bodyGunzip
			if compression == "zstd" {
				decoded = captured.bodyUnzstd
			}
			assert.JSONEq(t, string(originalBody), string(decoded),
				"re-sent body should decompress to the original")
		})
	}
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

func TestGeneralEndpoint_RequestCompression_Zstd(t *testing.T) {
	var captured capturedRequest
	srv := newEchoServer(t, &captured)
	defer srv.Close()

	endpoint := NewGeneralEndpoint(GeneralEndpointConfig{
		BaseURL: srv.URL,
		Endpoints: map[octollm.APIFormat]string{
			octollm.APIFormatChatCompletions: "/v1/chat/completions",
		},
		RequestCompression: "zstd",
	})

	req := testhelper.CreateTestRequest()
	originalBody, err := req.Body.Bytes()
	require.NoError(t, err)

	_, err = endpoint.Process(req)
	require.NoError(t, err)

	assert.Equal(t, "zstd", captured.header.Get("Content-Encoding"))
	assert.JSONEq(t, string(originalBody), string(captured.bodyUnzstd),
		"decompressed body should match original")
}

func TestGeneralEndpoint_DecisionFormats_DefaultPathAndParser(t *testing.T) {
	testCases := []struct {
		Name     string
		Format   octollm.APIFormat
		ReqBody  string
		RespBody string
		WantPath string
		WantType any
	}{
		{
			Name:     "SystemOne",
			Format:   octollm.APIFormatSystemOne,
			ReqBody:  `{"model":"jev-latest","state":"hi","questions":{"a":{"type":"noul","instructions":"Is it a greeting?"}}}`,
			RespBody: `{"model":"jev-1.13.0","answers":{"a":{"type":"noul","noul":0.95}},"usage":{"input_tokens":12,"output_tokens":3}}`,
			WantPath: "/v1/systemone",
			WantType: &systemone.SystemOneResponse{},
		},
		{
			Name:     "Decisions",
			Format:   octollm.APIFormatDecisions,
			ReqBody:  `{"model":"gpt-6-luna","input":"hi","questions":[{"type":"predicate","name":"a","instructions":"Is it a greeting?"}]}`,
			RespBody: `{"model":"gpt-6-luna","answers":[{"type":"predicate","name":"a","probability":0.95}],"usage":{"input_tokens":12,"output_tokens":0,"total_tokens":12}}`,
			WantPath: "/v1/decisions",
			WantType: &openai.DecisionsResponse{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(tc.RespBody))
			}))
			defer srv.Close()

			endpoint := NewGeneralEndpoint(GeneralEndpointConfig{
				BaseURL:   srv.URL,
				Endpoints: map[octollm.APIFormat]string{tc.Format: ""}, // empty means the default path
			})

			req := testhelper.CreateTestRequest(testhelper.WithFormat(tc.Format), testhelper.WithBody(tc.ReqBody))
			resp, err := endpoint.Process(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, tc.WantPath, gotPath)
			parsed, err := resp.Body.Parsed()
			require.NoError(t, err)
			assert.IsType(t, tc.WantType, parsed)
		})
	}
}
