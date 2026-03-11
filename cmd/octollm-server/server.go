package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/infinigence/octollm/pkg/composer"
	"github.com/infinigence/octollm/pkg/octollm"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type Server struct {
	conf         *composer.ConfigFile
	ruleComposer *composer.RuleComposerFileBased
	modelRepo    *composer.ModelRepoFileBased
}

func NewServer(conf *composer.ConfigFile) *Server {
	// Create ModelRepo with OpenTelemetry transport wrapper
	modelRepo := composer.NewModelRepoFileBased(
		composer.WithTransportWrapper(func(base http.RoundTripper) http.RoundTripper {
			return otelhttp.NewTransport(base)
		}),
	)
	err := modelRepo.UpdateFromConfig(conf)
	if err != nil {
		slog.Error(fmt.Sprintf("failed to update model repo from config: %v", err))
		os.Exit(1)
	}
	ruleComposer := composer.NewRuleRepoFileBased(modelRepo, 10*time.Second, 10)
	err = ruleComposer.UpdateFromConfig(conf)
	if err != nil {
		slog.Error(fmt.Sprintf("failed to update rule composer from config: %v", err))
		os.Exit(1)
	}
	return &Server{
		conf:         conf,
		ruleComposer: ruleComposer,
		modelRepo:    modelRepo,
	}
}

func (s *Server) ChatCompletionsHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		orgName := c.GetString("org")
		userName := c.GetString("user")

		engine := s.ruleComposer.GetEngine(userName, orgName, "")
		handler := octollm.ChatCompletionsHandler(engine)
		handler(c.Writer, c.Request)
	}
}

func (s *Server) MessagesHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		orgName := c.GetString("org")
		userName := c.GetString("user")

		engine := s.ruleComposer.GetEngine(userName, orgName, "")
		handler := octollm.MessagesHandler(engine)
		handler(c.Writer, c.Request)
	}
}

func (s *Server) EmbeddingsHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		orgName := c.GetString("org")
		userName := c.GetString("user")

		engine := s.ruleComposer.GetEngine(userName, orgName, "")
		handler := octollm.EmbeddingsHandler(engine)
		handler(c.Writer, c.Request)
	}
}

func (s *Server) CompletionsHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		orgName := c.GetString("org")
		userName := c.GetString("user")

		engine := s.ruleComposer.GetEngine(userName, orgName, "")
		handler := octollm.CompletionsHandler(engine)
		handler(c.Writer, c.Request)
	}
}

func (s *Server) RerankHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		orgName := c.GetString("org")
		userName := c.GetString("user")

		engine := s.ruleComposer.GetEngine(userName, orgName, "")
		handler := octollm.RerankHandler(engine)
		handler(c.Writer, c.Request)
	}
}

func (s *Server) VertexAIHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		orgName := c.GetString("org")
		userName := c.GetString("user")

		engine := s.ruleComposer.GetEngine(userName, orgName, "")
		handler := octollm.VertexAIHandler(engine)
		handler(c.Writer, c.Request)
	}
}
