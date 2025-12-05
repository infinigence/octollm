package octollm

import (
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/openai/openai-go/v3"
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

// ChatCompletionsHandler handles OpenAI /v1/chat/completions requests
func ChatCompletionsHandler(engine Engine) http.HandlerFunc {
	return errutils.ErrorHandlingMiddleware(func(w http.ResponseWriter, r *http.Request) {
		u := NewRequest(r, APIFormatChatCompletions)
		u.Body.SetParser(&JSONParser[openai.ChatCompletionNewParams]{})
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
				w.Write([]byte("data: "))
				b, err := chunk.Body.Bytes()
				if err != nil {
					logrus.WithContext(r.Context()).Errorf("Read chunk error: %v", err)
					*r = *errutils.WithError(r, err, http.StatusInternalServerError, "Internal Server Error")
					return
				}
				w.Write(b)
				w.Write([]byte("\n\n"))
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				logrus.WithContext(r.Context()).Debugf("Write chunk: len=%d", len(b))
			}
		} else if resp.Body != nil {
			defer resp.Body.Close()
			rd, err := resp.Body.Reader()
			if err != nil {
				logrus.WithContext(r.Context()).Errorf("Read body error: %v", err)
				*r = *errutils.WithError(r, err, http.StatusInternalServerError, "Internal Server Error")
				return
			}
			io.Copy(w, rd)
		}
	})
}

// LegacyCompletionsHandler handles OpenAI /v1/completions requests

// MessagesHandler handles Anthropic /v1/messages requests
