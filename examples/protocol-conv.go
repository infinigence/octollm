package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/infinigence/octollm/pkg/engines"
	"github.com/infinigence/octollm/pkg/engines/client"
	"github.com/infinigence/octollm/pkg/engines/converter"
	"github.com/infinigence/octollm/pkg/octollm"
)

func main() {
	mux := http.NewServeMux()

	// Create a general endpoint to access an OpenAI-compatible API
	ep := client.NewGeneralEndpoint(client.GeneralEndpointConfig{
		BaseURL: "https://cloud.infini-ai.com/maas",
		Endpoints: map[octollm.APIFormat]string{
			octollm.APIFormatChatCompletions: "/v1/chat/completions",
		},
		APIKey: os.Getenv("OCTOLLM_API_KEY"),
	})
	mux.Handle("/v1/chat/completions", octollm.ChatCompletionsHandler(ep))

	// Create a converter to convert OpenAI-compatible API to Claude messages API
	conv := converter.NewChatCompletionsToClaudeMessages(ep)
	mux.Handle("/v1/messages", octollm.MessagesHandler(conv))

	// Create a rewrite engine to force the model to use kimi-k2-instruct
	rewrite := engines.NewRewriteEngine(conv, &engines.RewritePolicy{
		SetKeys: map[string]any{"stream": true},
	}, nil, nil)
	mux.Handle("/force-stream/v1/messages", octollm.MessagesHandler(rewrite))

	// Start the server
	if err := http.ListenAndServe(":8080", mux); err != nil {
		fmt.Printf("failed to start server: %v", err)
	}
}
