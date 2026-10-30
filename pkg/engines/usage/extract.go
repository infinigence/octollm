package usage

import (
	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
	"github.com/infinigence/octollm/pkg/types/rerank"
	"github.com/infinigence/octollm/pkg/types/systemone"
	"github.com/infinigence/octollm/pkg/types/vertex"
)

// extract returns the usage reported by a parsed response body or stream
// chunk, or nil when it carries none. The result is always an untyped nil
// rather than a Usage wrapping a nil pointer.
func extract(parsed any) Usage {
	switch b := parsed.(type) {
	case *openai.ChatCompletionResponse:
		return chatCompletions(b.Usage)
	case *openai.ChatCompletionStreamChunk:
		return chatCompletions(b.Usage)
	case *openai.CompletionResponse: // also *openai.CompletionStreamChunk
		return chatCompletions(b.Usage)
	case *openai.ResponsesResponse:
		return responses(b.Usage)
	case *openai.ResponseStreamChunk:
		if b.Response == nil {
			return nil
		}
		return responses(b.Response.Usage)
	case *openai.DecisionsResponse:
		return responses(b.Usage)
	case *anthropic.ClaudeMessagesResponse:
		return claude(b.Usage)
	case *anthropic.ClaudeMessagesStreamEvent:
		// Usage only ever appears in message_start.message.usage or in the
		// top-level usage of later events (message_delta).
		if b.Type == "message_start" && b.Message != nil {
			return claude(b.Message.Usage)
		}
		return claude(b.Usage)
	case *vertex.GenerateContentResponse:
		return vertexUsage(b.UsageMetadata)
	case *vertex.StreamGenerateContentResponse:
		return vertexUsage(b.UsageMetadata)
	case *openai.EmbeddingResponse:
		return (*Embedding)(&b.Usage)
	case *rerank.RerankResponse:
		return (*Rerank)(&b.Usage)
	case *systemone.SystemOneResponse:
		return systemOne(b.Usage)
	default:
		return nil
	}
}

// The constructors below keep a nil wire pointer an untyped nil Usage instead
// of a non-nil Usage wrapping nil.

func chatCompletions(u *openai.Usage) Usage {
	if u == nil {
		return nil
	}
	return (*ChatCompletions)(u)
}

func responses(u *openai.ResponsesUsage) Usage {
	if u == nil {
		return nil
	}
	return (*Responses)(u)
}

func claude(u *anthropic.Usage) Usage {
	if u == nil {
		return nil
	}
	return (*Claude)(u)
}

func vertexUsage(u *vertex.UsageMetadata) Usage {
	if u == nil {
		return nil
	}
	return (*Vertex)(u)
}

func systemOne(u *systemone.SystemOneUsage) Usage {
	if u == nil {
		return nil
	}
	return (*SystemOne)(u)
}
