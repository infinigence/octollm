package client

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
	"github.com/infinigence/octollm/pkg/types/rerank"
	"github.com/infinigence/octollm/pkg/types/vertex"
)

type GeneralEndpoint struct {
	*HTTPEndpoint
}

// GeneralEndpoint implements octollm.Endpoint
var _ octollm.Engine = (*GeneralEndpoint)(nil)

type GeneralEndpointConfig struct {
	BaseURL   string
	Endpoints map[octollm.APIFormat]string
	APIKey    string

	AnthropicAPIKeyAsBearer bool

	GoogleAPIKeyAsBearer bool

	// RequestCompression specifies the compression algorithm for outbound request bodies.
	// Currently only "gzip" is supported.
	RequestCompression string
}

var DefaultURLPathChatCompletions = "/v1/chat/completions"
var DefaultURLPathCompletions = "/v1/completions"
var DefaultURLPathClaudeMessages = "/v1/messages"
var DefaultURLPathVertex = "v1/models/{modelNameWithAction}" // Path for Vertex AI, {modelNameWithAction} includes action (e.g., "gemini-2.0-flash:generateContent")
var DefaultURLPathEmbeddings = "/v1/embeddings"
var DefaultURLPathRerank = "/v1/rerank"
var DefaultURLPathResponses = "/v1/responses"

func NewGeneralEndpoint(conf GeneralEndpointConfig) *GeneralEndpoint {
	apiKey := conf.APIKey
	if apiKey == "" {
		// read from env
		apiKey = os.Getenv("OCTOLLM_API_KEY")
	}

	httpEndpoint := NewHTTPEndpoint().
		WithURLGetter(func(req *octollm.Request) (string, error) {
			endpoint, ok := conf.Endpoints[req.Format]
			if !ok {
				return "", fmt.Errorf("invalid format: %s", req.Format)
			}
			if endpoint == "" {
				switch req.Format {
				case octollm.APIFormatClaudeMessages:
					endpoint = DefaultURLPathClaudeMessages
				case octollm.APIFormatChatCompletions:
					endpoint = DefaultURLPathChatCompletions
				case octollm.APIFormatCompletions:
					endpoint = DefaultURLPathCompletions
				case octollm.APIFormatGoogleGenerateContent:
					endpoint = DefaultURLPathVertex
				case octollm.APIFormatEmbeddings:
					endpoint = DefaultURLPathEmbeddings
				case octollm.APIFormatRerank:
					endpoint = DefaultURLPathRerank
				case octollm.APIFormatResponses:
					endpoint = DefaultURLPathResponses
				default:
					return "", fmt.Errorf("invalid format: %s", req.Format)
				}
			}

			if _, hasModel := octollm.GetCtxValue[string](req, octollm.ContextKeyModelName); hasModel {
				newEndpoint, err := buildVertexEndpoint(endpoint, req)
				if err != nil {
					return "", fmt.Errorf("failed to build Vertex AI endpoint: %w", err)
				}
				endpoint = newEndpoint
			}

			return conf.BaseURL + endpoint, nil
		}).
		WithParser(
			func(req *octollm.Request) octollm.Parser {
				switch req.Format {
				case octollm.APIFormatChatCompletions:
					return &octollm.JSONParser[openai.ChatCompletionResponse]{}
				case octollm.APIFormatClaudeMessages:
					return &octollm.JSONParser[anthropic.ClaudeMessagesResponse]{}
				case octollm.APIFormatCompletions:
					return &octollm.JSONParser[openai.CompletionResponse]{}
				case octollm.APIFormatGoogleGenerateContent:
					return &octollm.JSONParser[vertex.GenerateContentResponse]{}
				case octollm.APIFormatEmbeddings:
					return &octollm.JSONParser[openai.EmbeddingResponse]{}
				case octollm.APIFormatRerank:
					return &octollm.JSONParser[rerank.RerankResponse]{}
				case octollm.APIFormatResponses:
					return &octollm.JSONParser[openai.ResponsesResponse]{}
				default:
					return &octollm.JSONParser[json.RawMessage]{}
				}
			},
			func(req *octollm.Request) (octollm.Parser, StreamingType) {
				switch req.Format {
				case octollm.APIFormatChatCompletions:
					return &octollm.JSONParser[openai.ChatCompletionStreamChunk]{}, StreamingTypeSSE
				case octollm.APIFormatCompletions:
					return &octollm.JSONParser[openai.CompletionStreamChunk]{}, StreamingTypeSSE
				case octollm.APIFormatClaudeMessages:
					return &octollm.JSONParser[anthropic.ClaudeMessagesStreamEvent]{}, StreamingTypeSSE
				case octollm.APIFormatGoogleGenerateContent:
					if isSSE, _ := octollm.GetCtxValue[bool](req, octollm.ContextKeyIsSSE); isSSE {
						return &octollm.JSONParser[vertex.StreamGenerateContentResponse]{}, StreamingTypeSSE
					} else {
						return &octollm.JSONParser[vertex.StreamGenerateContentResponse]{}, StreamingTypeJSON
					}
				case octollm.APIFormatResponses:
					return &octollm.JSONParser[openai.ResponseStreamChunk]{}, StreamingTypeSSE
				default:
					return &octollm.JSONParser[json.RawMessage]{}, StreamingTypeSSE
				}
			},
		)

	if apiKey != "" {
		httpEndpoint = httpEndpoint.WithRequestModifier(func(req *octollm.Request, httpReq *http.Request) *http.Request {
			if req.Format == octollm.APIFormatClaudeMessages && !conf.AnthropicAPIKeyAsBearer {
				httpReq.Header.Set("x-api-key", apiKey)
			} else if req.Format == octollm.APIFormatGoogleGenerateContent && !conf.GoogleAPIKeyAsBearer {
				httpReq.Header.Set("x-goog-api-key", apiKey)
			} else {
				httpReq.Header.Set("Authorization", "Bearer "+apiKey)
			}
			return httpReq
		})
	}

	if conf.RequestCompression == "gzip" {
		httpEndpoint = httpEndpoint.WithRequestModifier(func(req *octollm.Request, httpReq *http.Request) *http.Request {
			bodyBytes, err := req.Body.Bytes()
			if err != nil {
				slog.WarnContext(req.Context(), fmt.Sprintf("[general-endpoint] failed to read body for gzip compression: %v", err))
				return httpReq
			}
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			gz.Write(bodyBytes)
			gz.Close()
			compressed := buf.Bytes()
			httpReq.Body = io.NopCloser(bytes.NewReader(compressed))
			httpReq.ContentLength = int64(len(compressed))
			httpReq.Header.Set("Content-Encoding", "gzip")
			return httpReq
		})
	}

	return &GeneralEndpoint{
		HTTPEndpoint: httpEndpoint,
	}
}

func buildVertexEndpoint(endpoint string, req *octollm.Request) (string, error) {
	// Get pure model name from context
	modelName, ok := octollm.GetCtxValue[string](req, octollm.ContextKeyModelName)
	if !ok || modelName == "" {
		return "", fmt.Errorf("model name not found in Vertex AI request context")
	}

	// Get action from context if available
	action, hasAction := octollm.GetCtxValue[string](req, octollm.ContextKeyAction)

	// Build model name with action for endpoint replacement
	modelNameWithAction := modelName
	if hasAction && action != "" {
		modelNameWithAction = modelName + ":" + action
	}
	endpoint = strings.ReplaceAll(endpoint, "{modelNameWithAction}", modelNameWithAction)
	if hasAction && action != "" {
		endpoint = strings.ReplaceAll(endpoint, "{action}", action)
	}
	isSSE, ok := octollm.GetCtxValue[bool](req, octollm.ContextKeyIsSSE)
	if ok && isSSE {
		endpoint += "?alt=sse"
	}

	slog.DebugContext(req.Context(), fmt.Sprintf("[buildVertexEndpoint] endpoint: %s", endpoint))

	return endpoint, nil
}
