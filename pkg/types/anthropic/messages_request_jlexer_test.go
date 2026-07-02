package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/mailru/easyjson/jlexer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeMessagesRequestJLexerParser_Parse(t *testing.T) {
	testCases := []struct {
		Name string
		JSON string
	}{
		{
			Name: "StringContent",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":"Hello, Claude!"}]}`,
		},
		{
			Name: "ArrayContent",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"text","text":"Hello, Claude!"}]}]}`,
		},
		{
			Name: "ObjectContent",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":{"type":"text","text":"Hello, Claude!"}}]}`,
		},
		{
			Name: "SystemString",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"system":"You are a helpful assistant","messages":[{"role":"user","content":"Hello, Claude!"}]}`,
		},
		{
			Name: "SystemBlocks",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"system":[{"type":"text","text":"You are a helpful assistant"},{"type":"text","text":"Be concise"}],"messages":[{"role":"user","content":"Hello, Claude!"}]}`,
		},
		{
			Name: "ToolResult",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":[{"tool_use_id":"call_4cba1ad8bc8e4ba2983278ac","type":"tool_result","content":[{"type":"text","text":"API Error: 404"},{"type":"text","text":"agentId: a168307"}]},{"type":"text","text":"[Request interrupted by user]"},{"type":"text","text":"帮我看看这个项目是在做什么","cache_control":{"type":"ephemeral"}}]}]}`,
		},
		{
			Name: "TopLevelNull",
			JSON: `null`,
		},
		{
			Name: "Empty",
			JSON: `{}`,
		},
		{
			Name: "StreamTrue",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"Hello, Claude!"}]}`,
		},
		{
			Name: "WithThinking",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":5000},"messages":[{"role":"user","content":"Hello"}]}`,
		},
		{
			Name: "WithTools",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"tools":[{"name":"get_weather","description":"Get weather","input_schema":{"type":"object","properties":{"location":{"type":"string"}}}}],"tool_choice":{"type":"auto","disable_parallel_tool_use":true},"messages":[{"role":"user","content":"What's the weather?"}]}`,
		},
		{
			Name: "WithMetadata",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"metadata":{"user_id":"user_123"},"messages":[{"role":"user","content":"Hello"}]}`,
		},
		{
			Name: "WithStopSequences",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"stop_sequences":["END","STOP"],"messages":[{"role":"user","content":"Hello"}]}`,
		},
		{
			Name: "WithTemperature",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"temperature":0.7,"top_p":0.9,"top_k":40,"messages":[{"role":"user","content":"Hello"}]}`,
		},
		{
			Name: "NullStringFields",
			JSON: `{"model":null,"max_tokens":1024,"messages":[{"role":null,"content":"hi","name":null}]}`,
		},
		{
			Name: "NullStringFields2",
			JSON: `{"model":null,"max_tokens":1024,"messages":[{"content":"hi"}]}`,
		},
		{
			Name: "EmptyContent",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":[]}]}`,
		},
		{
			Name: "NullContentBlockStringFields",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":[{"type":null,"text":null}]}]}`,
		},
		{
			Name: "NullThinkingFields",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":null,"budget_tokens":1024}}`,
		},
		{
			Name: "NullToolDefinitionFields",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":null,"description":null,"input_schema":null,"type":null}]}`,
		},
		{
			Name: "SystemBlocksWithCacheControl",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"system":[{"type":"text","text":"System prompt","cache_control":{"type":"ephemeral","ttl":"5m"}}],"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			Name: "SystemBlocksWithCitations",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"system":[{"type":"text","text":"System prompt","citations":["cite1","cite2"]}],"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			Name: "SystemNull",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"system":null,"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			Name: "SystemEmptyArray",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"system":[],"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			Name: "ToolDefinitionWithAllFields",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"tools":[{"name":"web_search","description":"Search the web","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"},"max_characters":1000,"type":"web_search_20250305","display_width_px":1024,"display_height_px":768,"display_number":1,"allowed_domains":["example.com"],"blocked_domains":["bad.com"],"max_uses":5,"user_location":{"type":"approximate","country":"US","city":"NYC","region":"NY","timezone":"America/New_York"}}],"messages":[{"role":"user","content":"search"}]}`,
		},
		{
			Name: "ToolDefinitionNullFields",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"tools":[{"name":"tool1","input_schema":null,"cache_control":null,"display_number":null,"user_location":null}],"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			Name: "ToolDefinitionNullAllowedAndBlockedDomains",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"tools":[{"name":"tool1","allowed_domains":null,"blocked_domains":null}],"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			Name: "ToolChoiceWithName",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"tool_choice":{"type":"tool","name":"get_weather"},"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			Name: "ToolChoiceNullFields",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"tool_choice":{"type":"auto","name":null,"disable_parallel_tool_use":null},"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			Name: "MetadataNullUserID",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"metadata":{"user_id":null},"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			Name: "ThinkingNullBudgetTokens",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":null},"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			Name: "NullStopSequences",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"stop_sequences":null,"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			Name: "EmptyStopSequences",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"stop_sequences":[],"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			Name: "NullToolsAndToolChoice",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"tools":null,"tool_choice":null,"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			Name: "NullMetadataAndThinking",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"metadata":null,"thinking":null,"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			Name: "NullTemperatureTopKTopP",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"temperature":null,"top_k":null,"top_p":null,"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			Name: "NullStream",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"stream":null,"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			Name: "UnknownFieldsIgnored",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"extra":"field","nested":{"a":1}}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			var stdReq, jReq ClaudeMessagesRequest
			err := json.Unmarshal([]byte(tc.JSON), &stdReq)
			require.NoError(t, err, "standard json.Unmarshal should not error")

			in := jlexer.Lexer{Data: []byte(tc.JSON)}
			jReq.ParseJLexer(&in)
			require.NoError(t, in.Error(), "jlexer parser should not error")

			assert.Equal(t, stdReq, jReq, "jlexer parser output should match standard json.Unmarshal")
		})
	}
}

func TestClaudeMessagesRequestJLexer_AllBlockTypes(t *testing.T) {
	testCases := []struct {
		Name string
		JSON string
	}{
		{
			Name: "ImageBlockBase64",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}]}`,
		},
		{
			Name: "ImageBlockURL",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.com/img.png"}}]}]}`,
		},
		{
			Name: "DocumentBlock",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="},"title":"doc.pdf","context":"A document","citations":{"enabled":true}}]}]}`,
		},
		{
			Name: "ThinkingBlock",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"thinking","thinking":"Let me think...","signature":"sig_abc"}]}]}`,
		},
		{
			Name: "RedactedThinkingBlock",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"redacted_thinking","data":"base64data"}]}]}`,
		},
		{
			Name: "ToolUseBlock",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"tool_use","id":"toolu_123","name":"get_weather","input":{"location":"SF"}}]}]}`,
		},
		{
			Name: "ToolUseBlockEmptyInput",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"tool_use","id":"toolu_456","name":"no_args","input":{}}]}]}`,
		},
		{
			Name: "ToolResultBlockStringContent",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_123","content":"15 degrees"}]}]}`,
		},
		{
			Name: "ToolResultBlockArrayContent",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_123","content":[{"type":"text","text":"15 degrees"}]}]}]}`,
		},
		{
			Name: "ToolResultBlockNoContent",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_123"}]}]}`,
		},
		{
			Name: "ToolResultBlockIsError",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_123","content":"error occurred","is_error":true}]}]}`,
		},
		{
			Name: "GeneralBlockUnknownType",
			JSON: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"custom_block"}]}]}`,
		},
		{
			Name: "TextBlockWithCitations",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"text","text":"Cited text","citations":[{"type":"char_location","cited_text":"quote","document_index":0,"document_title":"doc.pdf","start_char_index":10,"end_char_index":20,"start_page":1,"end_page":2,"start_block_index":0,"end_block_index":1}]}]}]}`,
		},
		{
			Name: "TextBlockWithEmptyCitations",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"text","text":"text","citations":[]}]}]}`,
		},
		{
			Name: "TextBlockWithNullCitations",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"text","text":"text","citations":null}]}]}`,
		},
		{
			Name: "TextBlockWithCacheControl",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"text","text":"text","cache_control":{"type":"ephemeral"}}]}]}`,
		},
		{
			Name: "ImageBlockWithCacheControl",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR="},"cache_control":{"type":"ephemeral"}}]}]}`,
		},
		{
			Name: "ImageBlockNullSourceAndCacheControl",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"image","source":null,"cache_control":null}]}]}`,
		},
		{
			Name: "DocumentBlockWithAllFields",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="},"title":"doc.pdf","context":"A doc","citations":{"enabled":true},"cache_control":{"type":"ephemeral"}}]}]}`,
		},
		{
			Name: "DocumentBlockNullFields",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"document","source":null,"citations":null,"cache_control":null}]}]}`,
		},
		{
			Name: "ToolUseBlockWithCacheControl",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"tool_use","id":"toolu_1","name":"tool1","input":{"key":"val"},"cache_control":{"type":"ephemeral"}}]}]}`,
		},
		{
			Name: "ToolUseBlockNullInputAndCacheControl",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"tool_use","id":"toolu_1","name":"tool1","input":null,"cache_control":null}]}]}`,
		},
		{
			Name: "ToolResultBlockWithCacheControlAndIsError",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"result","is_error":false,"cache_control":{"type":"ephemeral"}}]}]}`,
		},
		{
			Name: "ToolResultBlockNullCacheControlAndIsError",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","cache_control":null,"is_error":null}]}]}`,
		},
		{
			Name: "SourceWithNullContent",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"content","content":null}}]}]}`,
		},
		{
			Name: "SourceWithEmptyContent",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"content","content":[]}}]}]}`,
		},
		{
			Name: "SourceWithNullData",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":null}}]}]}`,
		},
		{
			Name: "SourceWithNullUrl",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":null}}]}]}`,
		},
		{
			Name: "CitationWithNullFields",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"text","text":"text","citations":[{"type":null,"cited_text":null,"document_index":0,"document_title":null,"start_char_index":null,"end_char_index":null,"start_page":null,"end_page":null,"start_block_index":null,"end_block_index":null}]}]}]}`,
		},
		{
			Name: "NullContentBlockInArray",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":[null]}]}`,
		},
		{
			Name: "ObjectContentDocument",
			JSON: `{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":{"type":"document","source":{"type":"url","url":"https://example.com/doc.pdf"}}}]}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			var stdReq, jReq ClaudeMessagesRequest
			err := json.Unmarshal([]byte(tc.JSON), &stdReq)
			require.NoError(t, err, "standard json.Unmarshal should not error")

			in := jlexer.Lexer{Data: []byte(tc.JSON)}
			jReq.ParseJLexer(&in)
			require.NoError(t, in.Error(), "jlexer parser should not error")

			assert.Equal(t, stdReq, jReq, "jlexer parser output should match standard json.Unmarshal")
		})
	}
}
