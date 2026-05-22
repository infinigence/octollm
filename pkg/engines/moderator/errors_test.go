package moderator

import (
	"errors"
	"fmt"
	"testing"
)

func TestModerationErrorHelpers(t *testing.T) {
	t.Run("API failed direct", func(t *testing.T) {
		err := fmt.Errorf("%w: timeout", ErrModerationAPIFailed)
		if !IsModerationAPIFailed(err) {
			t.Fatal("expected IsModerationAPIFailed true")
		}
		if IsModerationBlocked(err) {
			t.Fatal("expected IsModerationBlocked false")
		}
	})

	t.Run("blocked direct", func(t *testing.T) {
		err := fmt.Errorf("%w: label=porn", ErrModerationBlocked)
		if !IsModerationBlocked(err) {
			t.Fatal("expected IsModerationBlocked true")
		}
		if IsModerationAPIFailed(err) {
			t.Fatal("expected IsModerationAPIFailed false")
		}
	})

	t.Run("engine wraps blocked as input not allowed", func(t *testing.T) {
		inner := fmt.Errorf("%w: Aliyun label=foo", ErrModerationBlocked)
		outer := fmt.Errorf("%w: %w", ErrInputNotAllowed, inner)
		if !errors.Is(outer, ErrInputNotAllowed) {
			t.Fatal("expected ErrInputNotAllowed")
		}
		if !errors.Is(outer, ErrModerationBlocked) {
			t.Fatal("expected ErrModerationBlocked in chain")
		}
		if IsModerationAPIFailed(outer) {
			t.Fatal("expected IsModerationAPIFailed false")
		}
		if !IsModerationBlocked(outer) {
			t.Fatal("expected IsModerationBlocked true through wrap chain")
		}
	})

	t.Run("engine wraps API failed as input not allowed", func(t *testing.T) {
		inner := fmt.Errorf("%w: upstream timeout", ErrModerationAPIFailed)
		outer := fmt.Errorf("%w: %w", ErrInputNotAllowed, inner)
		if !errors.Is(outer, ErrInputNotAllowed) {
			t.Fatal("expected ErrInputNotAllowed")
		}
		if !IsModerationAPIFailed(outer) {
			t.Fatal("expected IsModerationAPIFailed true through wrap chain")
		}
		if IsModerationBlocked(outer) {
			t.Fatal("expected IsModerationBlocked false")
		}
	})
}

func TestWeightedModeratorService_NoBackendReturnsAPIFailed(t *testing.T) {
	s := &WeightedModeratorService{backends: nil}
	err := s.Allow(t.Context(), []rune("hello"))
	if !IsModerationAPIFailed(err) {
		t.Fatalf("expected ErrModerationAPIFailed, got %v", err)
	}
}
