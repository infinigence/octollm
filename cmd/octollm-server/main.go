package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	"github.com/infinigence/octollm/pkg/composer"
	"github.com/sirupsen/logrus"
)

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
	r.Use(gzip.Gzip(gzip.DefaultCompression), auth.Handle())
	r.POST("/v1/chat/completions", s.ChatCompletionsHandler())
	r.POST("/v1/messages", s.MessagesHandler())

	log.Println("listening :8080")
	log.Fatal(http.ListenAndServe(":8080", r))
}
