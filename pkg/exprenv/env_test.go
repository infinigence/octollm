package exprenv_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/infinigence/octollm/pkg/exprenv"
	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
)

func TestRequestExprEnvOriginalFormat(t *testing.T) {
	t.Run("reflects the entry format", func(t *testing.T) {
		req := testhelper.CreateTestRequest(testhelper.WithFormat(octollm.APIFormatClaudeMessages))
		env := exprenv.Get(req)
		assert.Equal(t, string(octollm.APIFormatClaudeMessages), env.ReqEnv.OriginalFormat())
	})

	t.Run("stays the entry format after a converter changes req.Format", func(t *testing.T) {
		req := testhelper.CreateTestRequest(testhelper.WithFormat(octollm.APIFormatClaudeMessages))
		req.Format = octollm.APIFormatChatCompletions

		env := exprenv.Get(req)
		assert.Equal(t, string(octollm.APIFormatClaudeMessages), env.ReqEnv.OriginalFormat())
	})

	t.Run("falls back to req.Format when no original-format value is in context", func(t *testing.T) {
		req := octollm.NewEmptyRequest(testhelper.CreateTestRequest().Context())
		req.Format = octollm.APIFormatChatCompletions

		env := exprenv.Get(req)
		assert.Equal(t, string(octollm.APIFormatChatCompletions), env.ReqEnv.OriginalFormat())
	})

	t.Run("sentinel env does not panic", func(t *testing.T) {
		assert.Equal(t, string(octollm.APIFormatUnknown), exprenv.Sentinel.ReqEnv.OriginalFormat())
	})
}
