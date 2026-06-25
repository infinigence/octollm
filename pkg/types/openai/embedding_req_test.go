package openai

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmbeddingRequest_Marshal_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		Name   string
		JSON   string
		Object EmbeddingRequest
	}{
		{
			Name: "StringInput",
			JSON: `{"input":"The quick brown fox jumps over the lazy dog","model":"text-embedding-ada-002"}`,
			Object: EmbeddingRequest{
				Input: EmbeddingRequestInputString("The quick brown fox jumps over the lazy dog"),
				Model: "text-embedding-ada-002",
			},
		},
		{
			Name: "ArrayInput",
			JSON: `{"input":["Hello world","How are you?","I am fine"],"model":"text-embedding-ada-002"}`,
			Object: EmbeddingRequest{
				Input: EmbeddingRequestInputStringArray{"Hello world", "How are you?", "I am fine"},
				Model: "text-embedding-ada-002",
			},
		},
		{
			Name: "WithNormalizeEmbeddings",
			JSON: `{"input":"test","model":"text-embedding-ada-002","normalize_embeddings":true}`,
			Object: EmbeddingRequest{
				Input:               EmbeddingRequestInputString("test"),
				Model:               "text-embedding-ada-002",
				NormalizeEmbeddings: boolPtr(true),
			},
		},
	}

	for _, tc := range testCases {
		t.Run("Unmarshal_"+tc.Name, func(t *testing.T) {
			var req EmbeddingRequest
			err := json.Unmarshal([]byte(tc.JSON), &req)
			require.NoError(t, err)
			assert.Equal(t, tc.Object, req)
		})
		t.Run("Marshal_"+tc.Name, func(t *testing.T) {
			data, err := json.Marshal(tc.Object)
			require.NoError(t, err)
			assert.JSONEq(t, tc.JSON, string(data))
		})
	}
}

func TestRequestContentStringOrStringArray_GetDataLength(t *testing.T) {
	testCases := []struct {
		Name       string
		Input      EmbeddingRequestInputValue
		WantLength int
	}{
		{
			Name:       "Array",
			Input:      EmbeddingRequestInputStringArray{"Hello", "World"},
			WantLength: len("Hello") + len("World"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			assert.Equal(t, tc.WantLength, tc.Input.(EmbeddingRequestInputStringArray).GetDataLength())
		})
	}
}
