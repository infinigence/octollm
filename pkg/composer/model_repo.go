package composer

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/infinigence/octollm/pkg/engines"
	"github.com/infinigence/octollm/pkg/engines/client"
	"github.com/infinigence/octollm/pkg/engines/converter"
	"github.com/infinigence/octollm/pkg/octollm"
)

type ModelRepo interface {
	GetBackendNamesByModel(modelName string) []string
	GetEngine(modelName, backendName string) (octollm.Engine, error)
}

type ModelRepoFileBased struct {
	mu         sync.RWMutex
	cliManager *ProxyClientManager
	// configFile    *ConfigFile
	modelBackendConfig map[string]map[string]*Backend       // modelName -> backendName -> Backend
	modelBackendEngine map[string]map[string]octollm.Engine // a cache of modelName -> backendName -> Engine
}

var _ ModelRepo = (*ModelRepoFileBased)(nil)

// ModelRepoOption defines configuration options for ModelRepoFileBased
type ModelRepoOption func(*ModelRepoFileBased)

// WithTransportWrapper configures an HTTP transport wrapper
// Used to add additional functionality like OpenTelemetry tracing, retries, etc.
func WithTransportWrapper(wrapper func(base http.RoundTripper) http.RoundTripper) ModelRepoOption {
	return func(m *ModelRepoFileBased) {
		m.cliManager = NewProxyClientManager(wrapper)
	}
}

func NewModelRepoFileBased(opts ...ModelRepoOption) *ModelRepoFileBased {
	m := &ModelRepoFileBased{
		// Default: no transport wrapper (nil means no wrapping)
		cliManager:         NewProxyClientManager(nil),
		modelBackendConfig: make(map[string]map[string]*Backend),
		modelBackendEngine: make(map[string]map[string]octollm.Engine),
	}

	// Apply all options
	for _, opt := range opts {
		opt(m)
	}

	return m
}

func (m *ModelRepoFileBased) UpdateFromConfig(conf *ConfigFile) error {
	newBackends := make(map[string]map[string]*Backend)
	for modelName, model := range conf.Models {
		if _, ok := newBackends[modelName]; !ok {
			newBackends[modelName] = make(map[string]*Backend)
		}
		for backendName, backend := range model.Backends {
			var finalBackend Backend
			if backend.Use != "" {
				if globalBackend, ok := conf.GlobalBackends[backend.Use]; ok {
					finalBackend = *globalBackend
				}
			}
			if backend.BaseURL != "" {
				finalBackend.BaseURL = backend.BaseURL
			}
			if backend.HTTPProxy != nil {
				finalBackend.HTTPProxy = backend.HTTPProxy
			}
			if backend.APIKey != nil {
				finalBackend.APIKey = backend.APIKey
			}
			if backend.AnthropicAPIKeyAsBearer != nil {
				finalBackend.AnthropicAPIKeyAsBearer = backend.AnthropicAPIKeyAsBearer
			}
			if backend.GoogleAPIKeyAsBearer != nil {
				finalBackend.GoogleAPIKeyAsBearer = backend.GoogleAPIKeyAsBearer
			}
			if backend.ExtraHeaders != nil {
				if finalBackend.ExtraHeaders == nil {
					finalBackend.ExtraHeaders = make(map[string]string)
				}
				for k, v := range backend.ExtraHeaders {
					finalBackend.ExtraHeaders[k] = v
				}
			}
			if backend.PassThroughHeaders != nil {
				finalBackend.PassThroughHeaders = backend.PassThroughHeaders
			}
			if backend.ExtraHeadersByExpr != nil {
				if finalBackend.ExtraHeadersByExpr == nil {
					finalBackend.ExtraHeadersByExpr = make(map[string]string)
				}
				for k, v := range backend.ExtraHeadersByExpr {
					finalBackend.ExtraHeadersByExpr[k] = v
				}
			}
			if backend.URLPathChat != nil {
				finalBackend.URLPathChat = backend.URLPathChat
			}
			if backend.URLPathCompletions != nil {
				finalBackend.URLPathCompletions = backend.URLPathCompletions
			}
			if backend.URLPathMessages != nil {
				finalBackend.URLPathMessages = backend.URLPathMessages
			}
			if backend.URLPathVertex != nil {
				finalBackend.URLPathVertex = backend.URLPathVertex
			}
			if backend.URLPathEmbedding != nil {
				finalBackend.URLPathEmbedding = backend.URLPathEmbedding
			}
			if backend.URLPathRerank != nil {
				finalBackend.URLPathRerank = backend.URLPathRerank
			}
			if backend.URLPathResponses != nil {
				finalBackend.URLPathResponses = backend.URLPathResponses
			}
			if backend.RequestCompression != "" {
				finalBackend.RequestCompression = backend.RequestCompression
			}
			if backend.ConvertToChat != "" {
				finalBackend.ConvertToChat = backend.ConvertToChat
			}
			if backend.ConvertToMessages != "" {
				finalBackend.ConvertToMessages = backend.ConvertToMessages
			}
			if backend.ConvertToVertex != "" {
				finalBackend.ConvertToVertex = backend.ConvertToVertex
			}

			finalBackend.RequestRewrites = finalBackend.RequestRewrites.Merge(backend.RequestRewrites)
			finalBackend.ResponseRewrites = finalBackend.ResponseRewrites.Merge(backend.ResponseRewrites)
			finalBackend.StreamChunkRewrites = finalBackend.StreamChunkRewrites.Merge(backend.StreamChunkRewrites)

			finalBackend.PostRequestRewrites = finalBackend.PostRequestRewrites.Merge(backend.PostRequestRewrites)

			newBackends[modelName][backendName] = &finalBackend
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.modelBackendConfig = newBackends
	m.modelBackendEngine = make(map[string]map[string]octollm.Engine)
	return nil
}

func (m *ModelRepoFileBased) GetBackendNamesByModel(modelName string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	modelBackends, ok := m.modelBackendConfig[modelName]
	if !ok {
		return nil
	}

	backendNames := make([]string, 0, len(modelBackends))
	for backendName := range modelBackends {
		backendNames = append(backendNames, backendName)
	}
	return backendNames
}

func (m *ModelRepoFileBased) GetEngine(modelName, backendName string) (octollm.Engine, error) {
	m.mu.RLock()
	if engine, ok := m.modelBackendEngine[modelName][backendName]; ok {
		m.mu.RUnlock()
		return engine, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	if engine, ok := m.modelBackendEngine[modelName][backendName]; ok {
		return engine, nil
	}

	b, ok := m.modelBackendConfig[modelName][backendName]
	if !ok {
		return nil, fmt.Errorf("model backend (%s/%s) not found", modelName, backendName)
	}

	engine, err := m.BuildEngineByBackend(b)
	if err != nil {
		return nil, fmt.Errorf("new engine error: %w", err)
	}
	if _, ok := m.modelBackendEngine[modelName]; !ok {
		m.modelBackendEngine[modelName] = make(map[string]octollm.Engine)
	}
	m.modelBackendEngine[modelName][backendName] = engine
	return engine, nil
}

// BuildEngine build an engine for the backend config
func (m *ModelRepoFileBased) BuildEngineByBackend(b *Backend) (octollm.Engine, error) {
	var llmEngine octollm.Engine

	generalConf := &client.GeneralEndpointConfig{
		BaseURL:   b.BaseURL,
		Endpoints: make(map[octollm.APIFormat]string),
	}
	if b.APIKey != nil {
		generalConf.APIKey = *b.APIKey
	}
	if b.AnthropicAPIKeyAsBearer != nil {
		generalConf.AnthropicAPIKeyAsBearer = *b.AnthropicAPIKeyAsBearer
	}
	if b.GoogleAPIKeyAsBearer != nil {
		generalConf.GoogleAPIKeyAsBearer = *b.GoogleAPIKeyAsBearer
	}
	if b.URLPathChat != nil {
		if *b.URLPathChat != "" {
			generalConf.Endpoints[octollm.APIFormatChatCompletions] = *b.URLPathChat
		}
	} else {
		generalConf.Endpoints[octollm.APIFormatChatCompletions] = "" // will use default
	}
	if b.URLPathCompletions != nil {
		if *b.URLPathCompletions != "" {
			generalConf.Endpoints[octollm.APIFormatCompletions] = *b.URLPathCompletions
		}
	} else {
		generalConf.Endpoints[octollm.APIFormatCompletions] = "" // will use default
	}
	if b.URLPathMessages != nil {
		if *b.URLPathMessages != "" {
			generalConf.Endpoints[octollm.APIFormatClaudeMessages] = *b.URLPathMessages
		}
	} else {
		generalConf.Endpoints[octollm.APIFormatClaudeMessages] = "" // will use default
	}
	if b.URLPathEmbedding != nil {
		if *b.URLPathEmbedding != "" {
			generalConf.Endpoints[octollm.APIFormatEmbeddings] = *b.URLPathEmbedding
		}
	} else {
		generalConf.Endpoints[octollm.APIFormatEmbeddings] = "" // will use default
	}
	if b.URLPathRerank != nil {
		if *b.URLPathRerank != "" {
			generalConf.Endpoints[octollm.APIFormatRerank] = *b.URLPathRerank
		}
	} else {
		generalConf.Endpoints[octollm.APIFormatRerank] = "" // will use default
	}
	if b.URLPathResponses != nil {
		if *b.URLPathResponses != "" {
			generalConf.Endpoints[octollm.APIFormatResponses] = *b.URLPathResponses
		}
	} else {
		generalConf.Endpoints[octollm.APIFormatResponses] = "" // will use default
	}
	if b.URLPathVertex != nil {
		if *b.URLPathVertex != "" {
			generalConf.Endpoints[octollm.APIFormatGoogleGenerateContent] = *b.URLPathVertex
		}
	} else {
		generalConf.Endpoints[octollm.APIFormatGoogleGenerateContent] = "" // will use default
	}
	if len(generalConf.Endpoints) == 0 {
		return nil, fmt.Errorf("backend must specify at least one URL path (chat, messages, vertex, embedding, rerank, or responses)")
	}
	if b.RequestCompression != "" {
		generalConf.RequestCompression = b.RequestCompression
	}

	llmGE := client.NewGeneralEndpoint(*generalConf)
	// Always use cliManager to get a client with transport wrapper for trace propagation
	// GetClient("") returns the default client if no proxy is specified
	var proxyURL string
	if b.HTTPProxy != nil {
		proxyURL = *b.HTTPProxy
	}
	httpCli := m.cliManager.GetClient(proxyURL)
	llmEngine = llmGE.WithClient(httpCli)

	if b.PostRequestRewrites != nil {
		llmEngine = engines.NewRewriteEngine(
			llmEngine,
			b.PostRequestRewrites,
			nil,
			nil)
	}

	if len(b.ExtraHeaders) > 0 || len(b.PassThroughHeaders) > 0 {
		llmEngine = &engines.AddHeaderEngine{
			PassThroughHeaders: b.PassThroughHeaders,
			SetHeaders:         b.ExtraHeaders,
			Next:               llmEngine,
		}
	}

	// Add dynamic headers based on expressions
	if len(b.ExtraHeadersByExpr) > 0 {
		addHeaderByExprEngine, err := engines.NewAddHeaderByExprEngine(b.ExtraHeadersByExpr, llmEngine)
		if err != nil {
			return nil, fmt.Errorf("failed to create AddHeaderByExprEngine: %w", err)
		}
		llmEngine = addHeaderByExprEngine
	}

	if b.ConvertToMessages != "" {
		oriEngine := llmEngine
		var convEngine octollm.Engine
		if b.ConvertToMessages == "from_chat" {
			convEngine = converter.NewChatCompletionToClaudeMessages(oriEngine)
		}
		if convEngine != nil {
			conv := func(req *octollm.Request) (*octollm.Response, error) {
				if req.Format != octollm.APIFormatClaudeMessages {
					return oriEngine.Process(req)
				}
				return convEngine.Process(req)
			}
			llmEngine = octollm.EngineFunc(conv)
		}
	}

	if b.RequestRewrites != nil || b.ResponseRewrites != nil || b.StreamChunkRewrites != nil {
		llmEngine = engines.NewRewriteEngine(
			llmEngine,
			b.RequestRewrites,
			b.ResponseRewrites,
			b.StreamChunkRewrites)
	}
	return llmEngine, nil
}
