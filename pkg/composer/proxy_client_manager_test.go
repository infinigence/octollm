package composer

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClientManager_GetClient_NoProxy(t *testing.T) {
	cm := NewProxyClientManager(nil)
	cli := cm.GetClient("")
	assert.NotNil(t, cli)

	cli1 := cm.GetClient("")
	assert.Equal(t, cli, cli1)
}

func TestClientManager_GetClient_WithProxy(t *testing.T) {
	cm := NewProxyClientManager(func(base http.RoundTripper) http.RoundTripper { return base })
	cli := cm.GetClient("http://127.0.0.1:8080")
	assert.NotNil(t, cli)

	cli1 := cm.GetClient("http://127.0.0.1:8080")
	assert.Equal(t, cli, cli1)

	cli2 := cm.GetClient("http://127.0.0.1:8081")
	assert.NotEqual(t, cli, cli2)
}
