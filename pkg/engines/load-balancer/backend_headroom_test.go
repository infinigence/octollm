package loadbalancer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
)

func staticDenom(n int) ShardMaxConcurrencyFn {
	return func(*octollm.Request) int { return n }
}

func TestBackendHeadroom(t *testing.T) {
	req := testhelper.CreateTestRequest()

	t.Run("enabled and exceeded truth table", func(t *testing.T) {
		tests := []struct {
			name     string
			ceiling  float64
			denom    int
			count    int64
			enabled  bool
			exceeded bool
		}{
			{
				name:     "at ceiling is allowed",
				ceiling:  0.9,
				denom:    10,
				count:    9,
				enabled:  true,
				exceeded: false,
			},
			{
				name:     "over ceiling is denied",
				ceiling:  0.9,
				denom:    10,
				count:    10,
				enabled:  true,
				exceeded: true,
			},
			{
				name:     "ceiling 1.0 disables even when full",
				ceiling:  1.0,
				denom:    10,
				count:    100,
				enabled:  false,
				exceeded: false,
			},
			{
				name:     "ceiling 0 disables",
				ceiling:  0,
				denom:    10,
				count:    100,
				enabled:  false,
				exceeded: false,
			},
			{
				name:     "zero denom denies miss when enabled",
				ceiling:  0.9,
				denom:    0,
				count:    0,
				enabled:  true,
				exceeded: true,
			},
			{
				name:     "negative denom denies miss when enabled",
				ceiling:  0.9,
				denom:    -1,
				count:    5,
				enabled:  true,
				exceeded: true,
			},
			{
				name:     "zero denom with disabled ceiling does not deny",
				ceiling:  1.0,
				denom:    0,
				count:    0,
				enabled:  false,
				exceeded: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				h := backendHeadroom{
					maxConcurrencyFn:        staticDenom(tt.denom),
					cacheMissMaxUtilization: tt.ceiling,
				}
				assert.Equal(t, tt.enabled, h.enabled())
				assert.Equal(t, tt.denom, h.denom(req))
				assert.Equal(t, tt.exceeded, h.exceeded(req, tt.count))
			})
		}
	})

	t.Run("nil maxConcurrencyFn is denom 0", func(t *testing.T) {
		h := backendHeadroom{cacheMissMaxUtilization: 0.9}
		assert.Equal(t, 0, h.denom(req))
		assert.True(t, h.exceeded(req, 10))
		h.cacheMissMaxUtilization = 1.0
		assert.False(t, h.exceeded(req, 10))
	})
}

func TestNewBackendHeadroom(t *testing.T) {
	req := testhelper.CreateTestRequest()

	t.Run("wraps Weight when MaxConcurrencyFn is nil", func(t *testing.T) {
		items := []BackendItem{
			{Weight: 7, CacheMissMaxUtilization: 0.8},
			{Weight: 3, CacheMissMaxUtilization: 0.5},
		}
		heads := make([]backendHeadroom, len(items))
		for i, item := range items {
			heads[i] = newBackendHeadroom(item)
		}
		assert.Equal(t, 7, heads[0].denom(req))
		assert.Equal(t, 3, heads[1].denom(req))
		assert.Equal(t, 0.8, heads[0].cacheMissMaxUtilization)
		assert.Equal(t, 0.5, heads[1].cacheMissMaxUtilization)
	})

	t.Run("MaxConcurrencyFn including 0 is used directly", func(t *testing.T) {
		h := newBackendHeadroom(BackendItem{
			Weight:                  10,
			MaxConcurrencyFn:        staticDenom(0),
			CacheMissMaxUtilization: 0.9,
		})
		assert.Equal(t, 0, h.denom(req))
		assert.True(t, h.exceeded(req, 0))
	})

	t.Run("keeps a positive MaxConcurrencyFn over Weight", func(t *testing.T) {
		h := newBackendHeadroom(BackendItem{
			Weight:                  10,
			MaxConcurrencyFn:        staticDenom(4),
			CacheMissMaxUtilization: 0.5,
		})
		require.True(t, h.enabled())
		assert.Equal(t, 4, h.denom(req))
		assert.False(t, h.exceeded(req, 2))
		assert.True(t, h.exceeded(req, 3))
	})
}
