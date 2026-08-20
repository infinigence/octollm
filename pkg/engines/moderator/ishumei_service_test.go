package moderator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestIshumeiService 构造一个指向 httptest.Server 的 IshumeiModeratorService。
func newTestIshumeiService(t *testing.T, handler http.HandlerFunc) *IshumeiModeratorService {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewIshumeiModeratorService(&IshumeiModeratorConfig{
		AccessKey: "test-ak",
		AppID:     "test-app",
		EventID:   "output",
		Type:      "TEXTRISK",
		APIURL:    srv.URL,
	})
}

func TestIshumeiModeratorService_Allow(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		body        string
		blockReview bool
		wantBlocked bool
		wantAPIFail bool
	}{
		{
			name:       "PASS allowed",
			statusCode: http.StatusOK,
			body:       `{"code":1100,"message":"成功","riskLevel":"PASS"}`,
		},
		{
			name:       "REVIEW allowed by default",
			statusCode: http.StatusOK,
			body:       `{"code":1100,"riskLevel":"REVIEW"}`,
		},
		{
			name:        "REVIEW blocked when BlockReview",
			statusCode:  http.StatusOK,
			body:        `{"code":1100,"riskLevel":"REVIEW","riskLabel1":"political"}`,
			blockReview: true,
			wantBlocked: true,
		},
		{
			name:        "REJECT blocked",
			statusCode:  http.StatusOK,
			body:        `{"code":1100,"riskLevel":"REJECT","riskLabel1":"porn"}`,
			wantBlocked: true,
		},
		{
			name:        "non-1100 code API failed",
			statusCode:  http.StatusOK,
			body:        `{"code":1902,"message":"参数不合法"}`,
			wantAPIFail: true,
		},
		{
			name:        "non-200 status API failed",
			statusCode:  http.StatusInternalServerError,
			body:        `{}`,
			wantAPIFail: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestIshumeiService(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			})
			svc.blockReview = tt.blockReview

			err := svc.Allow(t.Context(), []rune("测试文本"))

			switch {
			case tt.wantBlocked:
				require.Error(t, err)
				assert.True(t, IsModerationBlocked(err))
			case tt.wantAPIFail:
				require.Error(t, err)
				assert.True(t, IsModerationAPIFailed(err))
			default:
				assert.NoError(t, err)
			}
		})
	}
}

func TestIshumeiModeratorService_RequestPayload(t *testing.T) {
	var got ishumeiRequest
	var gotMethod, gotContentType string
	var tokenIDs []string

	svc := newTestIshumeiService(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":1100,"riskLevel":"PASS"}`))
	})

	// 连续两次调用，tokenId 应每次不同
	for range 2 {
		require.NoError(t, svc.Allow(t.Context(), []rune("hello 世界")))
		tokenIDs = append(tokenIDs, got.Data.TokenID)
	}

	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "application/json", gotContentType)
	assert.Equal(t, "test-ak", got.AccessKey)
	assert.Equal(t, "test-app", got.AppID)
	assert.Equal(t, "output", got.EventID)
	assert.Equal(t, "TEXTRISK", got.Type)
	assert.Equal(t, "hello 世界", got.Data.Text)
	assert.NotEmpty(t, tokenIDs[0])
	assert.NotEmpty(t, tokenIDs[1])
	assert.NotEqual(t, tokenIDs[0], tokenIDs[1])
}

func TestIshumeiModeratorService_MaxRuneLen(t *testing.T) {
	assert.Equal(t, 10000, NewIshumeiModeratorService(nil).MaxRuneLen())
	assert.Equal(t, 600, NewIshumeiModeratorService(&IshumeiModeratorConfig{MaxRuneLen: 600}).MaxRuneLen())
}

func TestIshumeiModeratorService_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // 关闭后连接被拒绝

	svc := NewIshumeiModeratorService(&IshumeiModeratorConfig{
		AccessKey: "ak",
		AppID:     "app",
		EventID:   "output",
		APIURL:    url,
		Timeout:   2 * time.Second,
	})
	err := svc.Allow(t.Context(), []rune("hello"))
	require.Error(t, err)
	assert.True(t, IsModerationAPIFailed(err))
}
