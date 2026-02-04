package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/vertex"
)

func TestProcessJSONStream(t *testing.T) {
	jsonStream := `[
{
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [
          {
            "text": "**Imagining the Scene**\n\nI'm currently picturing the scene.",
            "thought": true
          }
        ]
      }
    }
  ],
  "usageMetadata": {
    "trafficType": "ON_DEMAND"
  },
  "modelVersion": "gemini-3-pro-image-preview",
  "createTime": "2026-01-27T03:51:36.593894Z",
  "responseId": "SDZ4aeafJLTWsbwP5ui6uA4"
},
{
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [
          {
            "text": "**Crafting the Visuals**\n\nI'm now focusing on setting and details.",
            "thought": true
          }
        ]
      }
    }
  ],
  "usageMetadata": {
    "trafficType": "ON_DEMAND"
  },
  "modelVersion": "gemini-3-pro-image-preview",
  "createTime": "2026-01-27T03:51:36.593894Z",
  "responseId": "SDZ4aeafJLTWsbwP5ui6uA4"
}
]`

	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(jsonStream)),
	}

	ch := make(chan *octollm.StreamChunk, 10)
	parser := &octollm.JSONParser[vertex.StreamGenerateContentResponse]{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	endpoint := NewHTTPEndpoint()
	go endpoint.processJSONStream(ctx, resp, ch, parser, func(value time.Time) {
		// no-op for test
	})

	var chunks []*octollm.StreamChunk
	done := make(chan bool)
	go func() {
		for chunk := range ch {
			chunks = append(chunks, chunk)
		}
		done <- true
	}()

	select {
	case <-done:
		// Success
	case <-ctx.Done():
		t.Fatalf("Test timed out")
	}

	if len(chunks) != 2 {
		t.Errorf("Expected 2 chunks, got %d", len(chunks))
	}

	// Verify first chunk
	if len(chunks) > 0 {
		bytes1, err := chunks[0].Body.Bytes()
		if err != nil {
			t.Fatalf("Failed to get bytes from first chunk: %v", err)
		}

		var obj1 vertex.StreamGenerateContentResponse
		if err := json.Unmarshal(bytes1, &obj1); err != nil {
			t.Errorf("First chunk is not valid JSON: %v", err)
		}

		if len(obj1.Candidates) == 0 {
			t.Error("First chunk has no candidates")
		}
	}

	// Verify second chunk
	if len(chunks) > 1 {
		bytes2, err := chunks[1].Body.Bytes()
		if err != nil {
			t.Fatalf("Failed to get bytes from second chunk: %v", err)
		}

		var obj2 vertex.StreamGenerateContentResponse
		if err := json.Unmarshal(bytes2, &obj2); err != nil {
			t.Errorf("Second chunk is not valid JSON: %v", err)
		}
	}
}
