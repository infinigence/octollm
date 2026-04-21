package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "net/http/pprof"

	"github.com/gin-contrib/gzip"
	ginslog "github.com/gin-contrib/slog"
	"github.com/gin-gonic/gin"
	"github.com/mattn/go-isatty"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"

	"github.com/infinigence/octollm/pkg/composer"
	ruleengine "github.com/infinigence/octollm/pkg/engines/rule-engine"
	exprenv "github.com/infinigence/octollm/pkg/exprenv"
)

func main() {
	var configFile string
	var verbose bool
	var jsonLog bool
	flag.StringVar(&configFile, "c", "./config.yaml", "config file path")
	flag.BoolVar(&verbose, "v", false, "enable verbose logging")
	flag.BoolVar(&jsonLog, "json", false, "use JSON logging format")
	flag.Parse()

	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	var logHandler slog.Handler
	if isatty.IsTerminal(os.Stdout.Fd()) && !jsonLog {
		logHandler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	} else {
		logHandler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level, AddSource: true})
	}
	// Wrap with contextFieldsHandler to inject OpenTelemetry trace info
	logHandler = newContextFieldsHandler(logHandler)
	slog.SetDefault(slog.New(logHandler))

	// Initialize OpenTelemetry
	ctx := context.Background()
	otelShutdown, err := initOpenTelemetry(ctx, "octollm-server")
	if err != nil {
		slog.Error(fmt.Sprintf("failed to initialize OpenTelemetry: %v", err))
		os.Exit(1)
	}
	defer func() {
		if err := otelShutdown(ctx); err != nil {
			slog.Error(fmt.Sprintf("failed to shutdown OpenTelemetry: %v", err))
		}
	}()

	r := gin.New()
	r.Use(otelgin.Middleware("octollm-server"))
	r.Use(ginslog.SetLogger(ginslog.WithLogger(func(c *gin.Context, l *slog.Logger) *slog.Logger {
		return slog.Default()
	})), gin.Recovery())

	slog.Info(fmt.Sprintf("Using config file: %s", configFile))
	conf, err := composer.ReadConfigFile(configFile)
	if err != nil {
		slog.Error(fmt.Sprintf("failed to read config file: %v", err), slog.String("config_file", configFile))
		os.Exit(1)
	}

	// Start pprof server
	if conf.PprofAddr != "" {
		go func() {
			err := http.ListenAndServe(conf.PprofAddr, nil)
			slog.Error(fmt.Sprintf("pprof server exited with error: %v", err))
		}()
	}

	exprenv.RegisterDefaultExtractor("promptTextLen", &ruleengine.PromptTextLenExtractor{})
	exprenv.RegisterDefaultExtractor("prefix20", &ruleengine.PrefixHashExtractor{Length: 20})
	exprenv.RegisterDefaultExtractor("suffix20", &ruleengine.SuffixHashExtractor{Length: 20})
	exprenv.RegisterDefaultExtractor("message5Hash", &ruleengine.Message5HashExtractor{})
	exprenv.RegisterDefaultExtractor("message5HashArray", &ruleengine.Message5HashArrayExtractor{})

	s := NewServer(conf)

	auth := &BearerKeyMW{}
	err = auth.UpdateFromConfig(conf)
	if err != nil {
		slog.Error(fmt.Sprintf("failed to update auth from config: %v", err))
		os.Exit(1)
	}

	// Register routes
	r.Use(gzip.Gzip(gzip.DefaultCompression), auth.Handle())
	r.POST("/v1/chat/completions", s.ChatCompletionsHandler())
	r.POST("/v1/completions", s.CompletionsHandler())
	r.POST("/v1/messages", s.MessagesHandler())
	r.POST("/v1/embeddings", s.EmbeddingsHandler())
	r.POST("/v1/rerank", s.RerankHandler())
	r.POST("/v1/responses", s.ResponsesHandler())
	// Vertex AI / Gemini API endpoints
	// modelName includes the action suffix (e.g., "gemini-2.0-flash:generateContent")
	// This matches Google's API format where the action is part of the model identifier
	r.POST("/v1/models/:modelNameWithAction", s.VertexAIHandler())

	if conf.ListenAddr == "" {
		conf.ListenAddr = ":8080"
	}

	srv := &http.Server{
		Addr:    conf.ListenAddr,
		Handler: r,
	}

	go func() {
		slog.Info(fmt.Sprintf("listening %s", conf.ListenAddr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error(fmt.Sprintf("server exited with error: %v", err))
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	<-quit
	slog.Info("shutting down server, press Ctrl+C again to force exit")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		<-quit
		slog.Info("force exit requested")
		cancel()
	}()

	select {
	// Delay for 5 seconds to allow k8s endpoint to be updated
	case <-time.After(5 * time.Second):
		if err := srv.Shutdown(ctx); err != nil {
			slog.Error(fmt.Sprintf("server shutdown error: %v", err))
			os.Exit(1)
		}
		slog.Info("server exited gracefully")
	case <-ctx.Done():
	}
}
