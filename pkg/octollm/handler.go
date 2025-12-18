package octollm

import (
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
	"github.com/sirupsen/logrus"
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

func httpHandler(engine Engine, format APIFormat, parser Parser) http.HandlerFunc {
	return errutils.ErrorHandlingMiddleware(func(w http.ResponseWriter, r *http.Request) {
		u := NewRequest(r, format)
		u.Body.SetParser(parser)
		resp, err := engine.Process(u)
		if err != nil {
			logrus.WithContext(r.Context()).Errorf("Do error: %v", err)
			httpErr := &errutils.UpstreamRespError{}
			if errors.As(err, &httpErr) {
				w.WriteHeader(httpErr.StatusCode)
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
	return httpHandler(engine, APIFormatChatCompletions, &JSONParser[openai.ChatCompletionNewParams]{})
}

// LegacyCompletionsHandler handles OpenAI /v1/completions requests

// MessagesHandler handles Anthropic /v1/messages requests
func MessagesHandler(engine Engine) http.HandlerFunc {
	return httpHandler(engine, APIFormatClaudeMessages, &JSONParser[anthropic.MessageNewParams]{})
}
