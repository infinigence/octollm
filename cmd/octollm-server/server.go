package main

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/infinigence/octollm/pkg/composer"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/sirupsen/logrus"
)

type Server struct {
	conf         *composer.ConfigFile
	ruleComposer *composer.RuleComposerFileBased
	modelRepo    *composer.ModelRepoFileBased
}

func NewServer(conf *composer.ConfigFile) *Server {
	modelRepo := composer.NewModelRepoFileBased()
	err := modelRepo.UpdateFromConfig(conf)
	if err != nil {
		logrus.WithError(err).Fatal("failed to update model repo from config")
	}
	ruleComposer := composer.NewRuleRepoFileBased(modelRepo, 5*time.Second, 10)
	err = ruleComposer.UpdateFromConfig(conf)
	if err != nil {
		logrus.WithError(err).Fatal("failed to update rule composer from config")
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
