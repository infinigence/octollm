package moderator

import "errors"

var (
	// Engine-layer errors: returned by TextModeratorEngine when moderation blocks a request.
	ErrInputNotAllowed        = errors.New("input not allowed")
	ErrOutputNotAllowed       = errors.New("output not allowed")
	ErrModeratorInternalError = errors.New("moderator internal error")

	// Provider-layer errors: returned by TextModeratorService.Allow implementations.
	ErrModerationBlocked   = errors.New("content blocked by moderation")
	ErrModerationAPIFailed = errors.New("moderation api failed")
)

// IsModerationAPIFailed reports whether err indicates a third-party moderation API
// failure (network, timeout, unexpected HTTP/business status), as opposed to content
// being blocked by policy.
func IsModerationAPIFailed(err error) bool {
	return errors.Is(err, ErrModerationAPIFailed)
}

// IsModerationBlocked reports whether err indicates content was blocked by moderation policy.
func IsModerationBlocked(err error) bool {
	return errors.Is(err, ErrModerationBlocked)
}
