package engines

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/infinigence/octollm/pkg/exprenv"
	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRewriteJSON_RemoveKey(t *testing.T) {
	policy := &RewritePolicy{
		RemoveKeys: []string{"key1", "key2"},
	}
	rewriter := &llmJSONRewriter{
		policy:  policy,
		ctx:     context.Background(),
		exprEnv: nil,
	}

	origin := `{"key1": "value1", "key2": "value2", "key3": "value3"}`
	rewritten := rewriter.RewriteJSON([]byte(origin))

	var got map[string]any
	var want map[string]any

	require.NoError(t, json.Unmarshal(rewritten, &got))
	require.NoError(t, json.Unmarshal([]byte(`{"key3": "value3"}`), &want))

	assert.Equal(t, want, got)

	policy = &RewritePolicy{
		RemoveKeys: []string{"key4"},
	}
	rewriter.policy = policy
	rewritten = rewriter.RewriteJSON([]byte(origin))
	require.NoError(t, json.Unmarshal(rewritten, &got))
	require.NoError(t, json.Unmarshal([]byte(origin), &want))
	assert.Equal(t, want, got)
}

func TestRewriteJSON_SetKey(t *testing.T) {
	policy := &RewritePolicy{
		SetKeys: map[string]any{
			"key1": "NewValue1",
		},
	}
	rewriter := &llmJSONRewriter{
		policy:  policy,
		ctx:     context.Background(),
		exprEnv: nil,
	}

	origin := `{"key1": "value1", "key2": "value2", "key3": "value3"}`
	rewritten := rewriter.RewriteJSON([]byte(origin))

	var got map[string]any
	var want map[string]any

	require.NoError(t, json.Unmarshal(rewritten, &got))
	require.NoError(t, json.Unmarshal([]byte(`{"key1": "NewValue1", "key2": "value2", "key3": "value3"}`), &want))
	assert.Equal(t, want, got)

	policy = &RewritePolicy{
		SetKeys: map[string]any{
			"key6": "NewValue6",
		},
	}
	rewriter.policy = policy
	rewritten = rewriter.RewriteJSON([]byte(origin))
	require.NoError(t, json.Unmarshal(rewritten, &got))
	require.NoError(t, json.Unmarshal([]byte(`{"key1": "value1", "key2": "value2", "key3": "value3", "key6": "NewValue6"}`), &want))

	assert.Equal(t, want, got)
}

func TestRewriteJSON_SetKeyByExpr(t *testing.T) {
	policy := &RewritePolicy{
		SetKeysByExpr: map[string]string{
			"stream_options": "req.RawReq().stream == true ? {\"include_usage\": true, \"continuous_usage_stats\": true} : nil",
		},
	}

	// Test 1: when stream is true
	origin := `{"stream": true, "key1": 11, "key2": "value2", "key3": "value3"}`

	req := testhelper.CreateTestRequest(
		testhelper.WithBody(origin),
	)

	rewriter := &llmJSONRewriter{
		policy:  policy,
		ctx:     context.Background(),
		exprEnv: exprenv.Get(req),
	}

	rewritten := rewriter.RewriteJSON([]byte(origin))

	var got map[string]any
	var want map[string]any

	require.NoError(t, json.Unmarshal(rewritten, &got))
	require.NoError(t, json.Unmarshal([]byte(`{"stream": true, "stream_options": {"include_usage": true, "continuous_usage_stats": true}, "key1": 11, "key2": "value2", "key3": "value3"}`), &want))
	assert.Equal(t, want, got)

	// Test 2: when stream is false
	originFalse := `{"stream": false, "key1": 11, "key2": "value2", "key3": "value3"}`

	req2 := testhelper.CreateTestRequest(
		testhelper.WithBody(originFalse),
	)

	rewriter2 := &llmJSONRewriter{
		policy:  policy,
		ctx:     context.Background(),
		exprEnv: exprenv.Get(req2),
	}

	rewritten2 := rewriter2.RewriteJSON([]byte(originFalse))

	var got2 map[string]any
	require.NoError(t, json.Unmarshal(rewritten2, &got2))

	// When expression returns nil, stream_options should not be added (no field)
	assert.Equal(t, false, got2["stream"])
	assert.Equal(t, float64(11), got2["key1"])
	assert.Equal(t, "value2", got2["key2"])
	assert.Equal(t, "value3", got2["key3"])
	// stream_options should not exist when expression returns nil
	_, hasStreamOptions := got2["stream_options"]
	assert.False(t, hasStreamOptions, "stream_options should not be present when expression returns nil")
}
