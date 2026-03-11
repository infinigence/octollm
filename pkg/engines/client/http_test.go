package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/anthropic"
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
	req := testhelper.CreateTestRequest()

	go endpoint.processJSONStream(ctx, req, resp, ch, parser)

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

func TestProcessSSEStream_DoubleNewline(t *testing.T) {
	// two newlines between events
	sseStream := `event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}


event: message_stop
data: {"type":"message_stop"}


`
	sseStreamNormalized := strings.ReplaceAll(sseStream, "\n\n\n", "\n\n")

	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(sseStream)),
	}

	ch := make(chan *octollm.StreamChunk, 10)
	parser := &octollm.JSONParser[anthropic.ClaudeMessagesStreamEvent]{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := octollm.NewStreamChan(ch, cancel)

	endpoint := NewHTTPEndpoint()
	req := testhelper.CreateTestRequest(
		testhelper.WithBody(
			anthropic.ClaudeMessagesRequest{
				Model:     "claude-sonnet-4-6",
				MaxTokens: 1024,
				Messages: []*anthropic.MessageParam{
					{Role: "user", Content: []anthropic.MessageContent{anthropic.MessageContentString("Hello")}},
				},
			},
		),
	)

	go endpoint.processSSEStream(ctx, req, resp, ch, parser)

	got, err := testhelper.CollectSSEStream(stream, 5*time.Second)
	assert.NoError(t, err)
	assert.Equal(t, string(got), sseStreamNormalized)
}
