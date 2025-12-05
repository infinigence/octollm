package composer

import (
	"net/http"
	"net/url"
	"sync"
)

type ProxyClientManager struct {
	mu            sync.Mutex
	cliMap        map[string]*http.Client
	defaultClient *http.Client
	trWrapper     func(base http.RoundTripper) http.RoundTripper
}

// NewProxyClientManager 创建一个ProxyClientManager
// 如果trWrapper非空，使用trWrapper包装配置了proxy的base transport
func NewProxyClientManager(trWrapper func(base http.RoundTripper) http.RoundTripper) *ProxyClientManager {
	pcm := &ProxyClientManager{
		cliMap:    make(map[string]*http.Client),
		trWrapper: trWrapper,
	}
	pcm.defaultClient = pcm.newClient(nil)
	return pcm
}

// GetClient 获取使用指定http proxy的client
// 如果proxyURL为空或者解析失败，返回不使用proxy的client
func (pcm *ProxyClientManager) GetClient(proxyURL string) *http.Client {
	if proxyURL == "" {
		return pcm.defaultClient
	}

	url, err := url.Parse(proxyURL)
	if err != nil {
		// 解析proxyURL失败，返回不使用proxy的client
		return pcm.defaultClient
	}

	pcm.mu.Lock()
	defer pcm.mu.Unlock()

	if cli, ok := pcm.cliMap[proxyURL]; ok {
		return cli
	}

	cli := pcm.newClient(url)
	pcm.cliMap[proxyURL] = cli
	return cli
}

func (pcm *ProxyClientManager) newClient(proxyURL *url.URL) *http.Client {
	baseTr := http.DefaultTransport.(*http.Transport).Clone()
	if proxyURL != nil {
		baseTr.Proxy = http.ProxyURL(proxyURL)
	}

	var tr http.RoundTripper = baseTr
	if pcm.trWrapper != nil {
		tr = pcm.trWrapper(baseTr)
	}

	return &http.Client{Transport: tr}
}
