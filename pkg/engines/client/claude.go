package client

import (
	"fmt"
	"net/http"
	"os"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/infinigence/octollm/pkg/octollm"
)

type ClaudeMessagesEndpoint struct {
	*HTTPEndpoint
}

// ClaudeMessagesEndpoint implements octollm.Endpoint
var _ octollm.Engine = (*ClaudeMessagesEndpoint)(nil)

func NewClaudeMessagesEndpoint(baseAddr, endpoint, apiKey string) *ClaudeMessagesEndpoint {
	if apiKey == "" {
		// read from env
		apiKey = os.Getenv("OCTOLLM_API_KEY")
	}
	httpEndpoint := NewHTTPEndpoint().
		WithURLGetter(func(req *octollm.Request) (string, error) {
			if req.Format != octollm.APIFormatClaudeMessages && req.Format != octollm.APIFormatUnknown {
				return "", fmt.Errorf("invalid format: %s", req.Format)
			}
			return baseAddr + endpoint, nil
		}).
		WithRequestModifier(func(req *octollm.Request, httpReq *http.Request) *http.Request {
			httpReq.Header.Set("Authorization", "Bearer "+apiKey)
			return httpReq
		}).
		WithParser(
			func(req *octollm.Request) octollm.Parser { return &octollm.JSONParser[anthropic.Message]{} },
			func(req *octollm.Request) octollm.Parser {
				return &octollm.JSONParser[anthropic.BetaRawMessageStreamEventUnion]{}
			},
		)
	return &ClaudeMessagesEndpoint{
		HTTPEndpoint: httpEndpoint,
	}
}
