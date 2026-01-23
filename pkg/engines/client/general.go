package client

import (
	"fmt"
	"net/http"
	"os"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
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
}

var DefaultURLPathChatCompletions = "/v1/chat/completions"
var DefaultURLPathClaudeMessages = "/v1/messages"

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
				default:
					return "", fmt.Errorf("invalid format: %s", req.Format)
				}
			}
			return conf.BaseURL + endpoint, nil
		}).
		WithParser(
			func(req *octollm.Request) octollm.Parser {
				switch req.Format {
				case octollm.APIFormatClaudeMessages:
					return &octollm.JSONParser[anthropic.Message]{}
				default:
					return &octollm.JSONParser[openai.ChatCompletionResponse]{}
				}
			},
			func(req *octollm.Request) octollm.Parser {
				switch req.Format {
				case octollm.APIFormatClaudeMessages:
					return &octollm.JSONParser[anthropic.BetaRawMessageStreamEventUnion]{}
				default:
					return &octollm.JSONParser[openai.ChatCompletionStreamChunk]{}
				}
			},
		)

	if apiKey != "" {
		httpEndpoint = httpEndpoint.WithRequestModifier(func(req *octollm.Request, httpReq *http.Request) *http.Request {

			if req.Format == octollm.APIFormatClaudeMessages && !conf.AnthropicAPIKeyAsBearer {
				httpReq.Header.Set("x-api-key", apiKey)
			} else {
				httpReq.Header.Set("Authorization", "Bearer "+apiKey)
			}
			return httpReq
		})
	}

	return &GeneralEndpoint{
		HTTPEndpoint: httpEndpoint,
	}
}
