package client

import (
	"fmt"
	"net/http"
	"os"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/openai/openai-go/v3"
)

type OpenAIChatCompletionsEndpoint struct {
	*HTTPEndpoint
}

// OpenAIChatCompletionsEndpoint implements octollm.Endpoint
var _ octollm.Engine = (*OpenAIChatCompletionsEndpoint)(nil)

func NewOpenAIChatCompletionsEndpoint(baseAddr, endpoint, apiKey string) *OpenAIChatCompletionsEndpoint {
	if apiKey == "" {
		// read from env
		apiKey = os.Getenv("OCTOLLM_API_KEY")
	}
	httpEndpoint := NewHTTPEndpoint().
		WithURLGetter(func(req *octollm.Request) (string, error) {
			if req.Format != octollm.APIFormatChatCompletions && req.Format != octollm.APIFormatUnknown {
				return "", fmt.Errorf("invalid format: %s", req.Format)
			}
			return baseAddr + endpoint, nil
		}).
		WithRequestModifier(func(req *octollm.Request, httpReq *http.Request) *http.Request {
			httpReq.Header.Set("Authorization", "Bearer "+apiKey)
			return httpReq
		}).
		WithParser(
			func(req *octollm.Request) octollm.Parser { return &octollm.JSONParser[openai.ChatCompletion]{} },
			func(req *octollm.Request) octollm.Parser { return &octollm.JSONParser[openai.ChatCompletionChunk]{} },
		)
	return &OpenAIChatCompletionsEndpoint{
		HTTPEndpoint: httpEndpoint,
	}
}
