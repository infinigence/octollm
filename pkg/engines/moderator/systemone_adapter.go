package moderator

import (
	"context"
	"fmt"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/systemone"
)

// SystemOneAdapter extracts text from TypeSafe System One (POST /v1/systemone)
// requests.
type SystemOneAdapter struct{}

var _ TextModeratorAdapter = (*SystemOneAdapter)(nil)

func (a *SystemOneAdapter) ExtractTextFromBody(ctx context.Context, body *octollm.UnifiedBody) ([]rune, error) {
	parsed, err := body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("parse body error: %w", err)
	}
	switch parsed := parsed.(type) {
	case *systemone.SystemOneRequest:
		return []rune(parsed.ExtractText()), nil
	default:
		return nil, fmt.Errorf("unsupported body type: %T", parsed)
	}
}

func (a *SystemOneAdapter) GetReplacementBody(ctx context.Context, body *octollm.UnifiedBody) *octollm.UnifiedBody {
	return nil
}
