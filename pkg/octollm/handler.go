package octollm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
	"github.com/infinigence/octollm/pkg/types/rerank"
	"github.com/infinigence/octollm/pkg/types/vertex"
	"github.com/sirupsen/logrus"
)

type contextKey string

const (
	ContextKeyModelName      contextKey = "model_name"
	ContextKeyAction         contextKey = "action"
	ContextKeyReceivedHeader contextKey = "received_header"
)

type Server struct {
	mu     sync.RWMutex
	engine Engine
}

func NewServer(ep Engine) *Server {
	return &Server{engine: ep}
}

func (s *Server) SetEngine(ep Engine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.engine = ep
}

// httpSSEHandler handles HTTP requests with given engine.
// It assumes the request body is in the given format, and the parser can parse the request body.
// For the response, if the engine returns a stream channel, it will write the response as an event stream.
// Otherwise, it will write the response as a plain text.
func httpSSEHandler(engine Engine, format APIFormat, parser Parser) http.HandlerFunc {
	return errutils.ErrorHandlingMiddleware(func(w http.ResponseWriter, r *http.Request) {
		// store received headers in context
		r = r.WithContext(context.WithValue(r.Context(), ContextKeyReceivedHeader, r.Header))
		u := NewRequest(r, format)
		u.Body.SetParser(parser)
		resp, err := engine.Process(u)
		if err != nil {
			logrus.WithContext(r.Context()).Errorf("Do error: %v", err)
			httpErr := &errutils.UpstreamRespError{}
			if errors.As(err, &httpErr) {
				for k, v := range httpErr.Header {
					if k == "Content-Length" {
						continue
					}
					w.Header().Set(k, v[0])
				}
				w.WriteHeader(httpErr.StatusCode)
				w.Write(httpErr.Body)
				return
			}
			handlerErr := &errutils.HandlerError{}
			if errors.As(err, &handlerErr) {
				*r = *errutils.WithHandlerError(r, handlerErr)
				return
			}
			*r = *errutils.WithError(r, err, http.StatusInternalServerError, "Internal Server Error")
			return
		}

		// copy headers
		for k, v := range resp.Header {
			if k == "Content-Length" {
				continue
			}
			w.Header().Set(k, v[0])
		}
		w.WriteHeader(http.StatusOK)
		if resp.Stream != nil {
			defer resp.Stream.Close()
			for chunk := range resp.Stream.Chan() {
				b, err := chunk.Body.Bytes()
				if err != nil {
					logrus.WithContext(r.Context()).Errorf("[httpHandler] Read chunk error: %v", err)
					*r = *errutils.WithError(r, err, http.StatusInternalServerError, "Internal Server Error")
					return
				}

				if event, ok := chunk.Metadata["event"]; ok {
					w.Write([]byte("event: " + event + "\n"))
				}
				if id, ok := chunk.Metadata["id"]; ok {
					w.Write([]byte("id: " + id + "\n"))
				}
				w.Write([]byte("data: "))
				w.Write(b)
				w.Write([]byte("\n\n"))
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				logrus.WithContext(r.Context()).Debugf("[httpHandler] Write chunk: len=%d", len(b))
			}
		} else if resp.Body != nil {
			defer resp.Body.Close()
			rd, err := resp.Body.Reader()
			if err != nil {
				logrus.WithContext(r.Context()).Errorf("[httpHandler] Read body error: %v", err)
				*r = *errutils.WithError(r, err, http.StatusInternalServerError, "Internal Server Error")
				return
			}
			io.Copy(w, rd)
		}
	})
}

// ChatCompletionsHandler handles OpenAI /v1/chat/completions requests
func ChatCompletionsHandler(engine Engine) http.HandlerFunc {
	return httpSSEHandler(engine, APIFormatChatCompletions, &JSONParser[openai.ChatCompletionRequest]{})
}

// CompletionsHandler handles OpenAI /v1/completions requests (legacy)
func CompletionsHandler(engine Engine) http.HandlerFunc {
	return httpSSEHandler(engine, APIFormatCompletions, &JSONParser[openai.CompletionRequest]{})
}

// MessagesHandler handles Anthropic /v1/messages requests
func MessagesHandler(engine Engine) http.HandlerFunc {
	return httpSSEHandler(engine, APIFormatClaudeMessages, &JSONParser[anthropic.ClaudeMessagesRequest]{})
}

// EmbeddingsHandler handles OpenAI /v1/embeddings requests
func EmbeddingsHandler(engine Engine) http.HandlerFunc {
	return httpSSEHandler(engine, APIFormatEmbeddings, &JSONParser[openai.EmbeddingRequest]{})
}

// RerankHandler handles rerank requests
func RerankHandler(engine Engine) http.HandlerFunc {
	return httpSSEHandler(engine, APIFormatRerank, &JSONParser[rerank.RerankRequest]{})
}

// httpJSONArrayHandler handles HTTP requests with given engine.
// It is like httpSSEHandler, but if the engine returns a stream channel, it will write the response as a streamed JSON array.
// This is the format used by Google Vertex AI API.
func httpJSONArrayHandler(engine Engine, format APIFormat, parser Parser) http.HandlerFunc {
	return errutils.ErrorHandlingMiddleware(func(w http.ResponseWriter, r *http.Request) {
		// store received headers in context
		r = r.WithContext(context.WithValue(r.Context(), ContextKeyReceivedHeader, r.Header))
		u := NewRequest(r, format)
		u.Body.SetParser(parser)
		resp, err := engine.Process(u)
		if err != nil {
			logrus.WithContext(r.Context()).Errorf("Do error: %v", err)
			httpErr := &errutils.UpstreamRespError{}
			if errors.As(err, &httpErr) {
				for k, v := range httpErr.Header {
					if k == "Content-Length" {
						continue
					}
					w.Header().Set(k, v[0])
				}
				w.WriteHeader(httpErr.StatusCode)
				w.Write(httpErr.Body)
				return
			}
			handlerErr := &errutils.HandlerError{}
			if errors.As(err, &handlerErr) {
				*r = *errutils.WithHandlerError(r, handlerErr)
				return
			}
			*r = *errutils.WithError(r, err, http.StatusInternalServerError, "Internal Server Error")
			return
		}

		// copy headers
		for k, v := range resp.Header {
			if k == "Content-Length" {
				continue
			}
			w.Header().Set(k, v[0])
		}
		w.WriteHeader(http.StatusOK)
		if resp.Stream != nil {
			w.Write([]byte("["))
			firstChunk := true
			defer resp.Stream.Close()
			for chunk := range resp.Stream.Chan() {
				b, err := chunk.Body.Bytes()
				if err != nil {
					logrus.WithContext(r.Context()).Errorf("[httpHandler] Read chunk error: %v", err)
					*r = *errutils.WithError(r, err, http.StatusInternalServerError, "Internal Server Error")
					return
				}

				if !firstChunk {
					w.Write([]byte(",\n"))
				}
				w.Write(b)
				firstChunk = false // Mark that we've written the first chunk
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				logrus.WithContext(r.Context()).Debugf("[httpHandler] Write chunk: len=%d", len(b))
			}
			w.Write([]byte("]"))
		} else if resp.Body != nil {
			defer resp.Body.Close()
			rd, err := resp.Body.Reader()
			if err != nil {
				logrus.WithContext(r.Context()).Errorf("[httpHandler] Read body error: %v", err)
				*r = *errutils.WithError(r, err, http.StatusInternalServerError, "Internal Server Error")
				return
			}
			io.Copy(w, rd)
		}
	})
}

func IsStreamAction(action string) bool {
	return strings.HasPrefix(strings.ToLower(action), "stream")
}

// VertexAIHandler handles Google VertexAI/Gemini generateContent requests
func VertexAIHandler(engine Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		modelName, action := extractVertexModelFromURL(r.URL.Path)

		ctx = context.WithValue(ctx, ContextKeyModelName, modelName)
		ctx = context.WithValue(ctx, ContextKeyAction, action)

		r = r.WithContext(ctx)
		httpJSONArrayHandler(engine, APIFormatGoogleGenerateContent, &JSONParser[vertex.GenerateContentRequest]{})(w, r)
	}
}

func extractVertexModelFromURL(path string) (modelName string, action string) {
	modelsIdx := strings.LastIndex(path, "/models/")
	if modelsIdx == -1 {
		return "", ""
	}

	// Extract everything after "/models/"
	afterModels := path[modelsIdx+8:]

	colonIdx := strings.Index(afterModels, ":")
	if colonIdx == -1 {
		return "", ""
	}

	modelName = afterModels[:colonIdx]
	action = afterModels[colonIdx+1:]

	return modelName, action
}
