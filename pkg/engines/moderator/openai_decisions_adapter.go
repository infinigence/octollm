package moderator

import (
	"context"
	"fmt"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
)

// OpenAIDecisionsAdapter extracts text from OpenAI Decisions
// (POST /v1/decisions) requests.
type OpenAIDecisionsAdapter struct{}

var _ TextModeratorAdapter = (*OpenAIDecisionsAdapter)(nil)

func (a *OpenAIDecisionsAdapter) ExtractTextFromBody(ctx context.Context, body *octollm.UnifiedBody) ([]rune, error) {
	parsed, err := body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("parse body error: %w", err)
	}
	switch parsed := parsed.(type) {
	case *openai.DecisionsRequest:
		return []rune(parsed.ExtractText()), nil
	default:
		return nil, fmt.Errorf("unsupported body type: %T", parsed)
	}
}

func (a *OpenAIDecisionsAdapter) GetReplacementBody(ctx context.Context, body *octollm.UnifiedBody) *octollm.UnifiedBody {
	return nil
}
