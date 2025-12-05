package main

import (
	"fmt"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/infinigence/octollm/pkg/composer"
)

type UserWithOrg struct {
	User string
	Org  string
}

// BearerKeyMW is a middleware that authenticates requests using bearer keys.
// It checks the Authorization header for a valid bearer token and sets the user and org context values if the token is valid.
// If the token is invalid, it sets the user and org context values to empty strings, instead of returning 401 directly.
type BearerKeyMW struct {
	mu      sync.RWMutex
	apiKeys map[string]UserWithOrg
}

func (m *BearerKeyMW) UpdateFromConfig(conf *composer.ConfigFile) error {
	newAPIKeys := make(map[string]UserWithOrg)
	for orgName, org := range conf.Users {
		for user, apiKey := range org.APIKeys {
			if _, ok := newAPIKeys[apiKey]; ok {
				return fmt.Errorf("duplicate api key %s", apiKey)
			}
			newAPIKeys[apiKey] = UserWithOrg{
				User: user,
				Org:  orgName,
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.apiKeys = newAPIKeys
	return nil
}

func (m *BearerKeyMW) Handle() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("user", "")
		c.Set("org", "")

		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			return
		}
		const bearerPrefix = "Bearer "
		if len(authHeader) < len(bearerPrefix) || !strings.HasPrefix(strings.ToLower(authHeader), strings.ToLower(bearerPrefix)) {
			return
		}
		apiKey := authHeader[len(bearerPrefix):]
		if apiKey == "" {
			return
		}

		m.mu.RLock()
		userWithOrg, ok := m.apiKeys[apiKey]
		m.mu.RUnlock()
		if !ok {
			return
		}

		c.Set("user", userWithOrg.User)
		c.Set("org", userWithOrg.Org)
	}
}
