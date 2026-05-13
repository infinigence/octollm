package rerank

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustUnmarshalRerankReq(t *testing.T, jsonStr string) RerankRequest {
	t.Helper()
	var req RerankRequest
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &req))
	return req
}

func TestRerankRequest_String(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected string
	}{
		{
			name: "basic with top_n and return_documents",
			json: `{
				"query": "What is the capital of France?",
				"model": "bge-reranker-v2-m3",
				"documents": ["Paris is the capital of France.", "London is the capital of England."],
				"return_documents": true,
				"top_n": 5
			}`,
			// "What is the capital of France?" = 30 bytes
			expected: `(RerankRequest) {
  Model: "bge-reranker-v2-m3"
  Query: len(30)
  Documents: len(2)
  TopN: 5
  ReturnDocuments: true
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := mustUnmarshalRerankReq(t, tt.json)
			assert.Equal(t, tt.expected, req.String())
		})
	}
}

func mustUnmarshalRerankResp(t *testing.T, jsonStr string) RerankResponse {
	t.Helper()
	var resp RerankResponse
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &resp))
	return resp
}

func TestRerankResponse_String(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected string
	}{
		{
			name: "two results",
			json: `{
				"results": [
					{"index": 0, "relevance_score": 0.95, "document": {"text": "Paris is..."}},
					{"index": 1, "relevance_score": 0.5, "document": {"text": "London"}}
				],
				"model": "bge-reranker-v2-m3",
				"usage": {"prompt_tokens": 10, "total_tokens": 10}
			}`,
			// "Paris is..." = 11, "London" = 6
			expected: `(RerankResponse) {
  Model: "bge-reranker-v2-m3"
  Results: len(2)
    Result{index=0, relevance_score=0.950000, document_len=11}
    Result{index=1, relevance_score=0.500000, document_len=6}
  Usage: prompt=10, total=10
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := mustUnmarshalRerankResp(t, tt.json)
			assert.Equal(t, tt.expected, resp.String())
		})
	}
}
