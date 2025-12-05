package composer

import (
	"fmt"
	"sync"

	"github.com/infinigence/octollm/pkg/engines"
	"github.com/infinigence/octollm/pkg/engines/client"
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

func NewModelRepoFileBased() *ModelRepoFileBased {
	return &ModelRepoFileBased{
		cliManager:         NewProxyClientManager(nil),
		modelBackendConfig: make(map[string]map[string]*Backend),
		modelBackendEngine: make(map[string]map[string]octollm.Engine),
	}
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
			if backend.HTTPProxy != "" {
				finalBackend.HTTPProxy = backend.HTTPProxy
			}
			if backend.APIKey != "" {
				finalBackend.APIKey = backend.APIKey
			}
			if backend.ExtraHeaders != nil {
				if finalBackend.ExtraHeaders == nil {
					finalBackend.ExtraHeaders = make(map[string]string)
				}
				for k, v := range backend.ExtraHeaders {
					finalBackend.ExtraHeaders[k] = v
				}
			}
			if backend.URLPathChat != "" {
				finalBackend.URLPathChat = backend.URLPathChat
			}
			if backend.URLPathMessages != "" {
				finalBackend.URLPathMessages = backend.URLPathMessages
			}
			if backend.URLPathVertex != "" {
				finalBackend.URLPathVertex = backend.URLPathVertex
			}

			finalBackend.RequestRewrites = finalBackend.RequestRewrites.Merge(backend.RequestRewrites)
			finalBackend.ResponseRewrites = finalBackend.ResponseRewrites.Merge(backend.ResponseRewrites)
			finalBackend.StreamChunkRewrites = finalBackend.StreamChunkRewrites.Merge(backend.StreamChunkRewrites)

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

	// TODO: other API formats
	httpCli := m.cliManager.GetClient(b.HTTPProxy)
	llmCli := client.NewOpenAIChatCompletionsEndpoint(b.BaseURL, b.URLPathChat, b.APIKey)
	llmCli.WithClient(httpCli)
	llmEngine = llmCli

	if len(b.ExtraHeaders) > 0 {
		llmEngine = &engines.AddHeaderEngine{
			Header: b.ExtraHeaders,
			Next:   llmEngine,
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
