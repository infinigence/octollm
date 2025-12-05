package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/infinigence/octollm/pkg/composer"
	"github.com/infinigence/octollm/pkg/engines/moderator"
	"github.com/sirupsen/logrus"
)

func ginHandler(h http.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		h(c.Writer, c.Request)
	}
}

type MockTextModerator struct{}

func (m *MockTextModerator) Allow(ctx context.Context, text []rune) error {
	logrus.WithContext(ctx).Debugf("moderate text: %s", string(text))
	if strings.Contains(string(text), "shot") {
		return fmt.Errorf("%w: %s", moderator.ErrOutputNotAllowed, string(text))
	}
	return nil
}

func (m *MockTextModerator) MaxRuneLen() int {
	return 25
}

var _ moderator.TextModeratorService = (*MockTextModerator)(nil)

func main() {
	var configFile string
	flag.StringVar(&configFile, "c", "./config.yaml", "config file path")
	flag.Parse()

	logrus.SetLevel(logrus.DebugLevel)
	r := gin.Default()

	logrus.Infof("Using config file: %s", configFile)
	conf, err := composer.ReadConfigFile(configFile)
	if err != nil {
		logrus.WithError(err).Fatal("failed to read config file")
	}

	s := NewServer(conf)

	auth := &BearerKeyMW{}
	err = auth.UpdateFromConfig(conf)
	if err != nil {
		logrus.WithError(err).Fatal("failed to update auth from config")
	}

	// Register routes
	r.Use(auth.Handle())
	r.POST("/v1/chat/completions", s.ChatCompletionsHandler())

	log.Println("listening :8080")
	log.Fatal(http.ListenAndServe(":8080", r))
}
