package octollm

import (
	"log/slog"
	"runtime/debug"
)

// SafeGo runs fn in a new goroutine with a deferred panic recovery.
// Use SafeGo instead of a bare go statement for goroutines spawned inside request handlers:
// middleware-level recovery (e.g. gin.Recovery) only catches panics on the handler goroutine
// and cannot recover panics that occur on separately spawned goroutines.
// Any panic is logged as an error with its stack trace using the request's context and does
// not propagate — callers that need to know about failures should use an explicit error channel.
func SafeGo(req *Request, fn func()) {
	go func() {
		defer func() {
			if err := recover(); err != nil {
				slog.ErrorContext(req.Context(), "panic in goroutine",
					"err", err,
					"stack", string(debug.Stack()),
				)
			}
		}()
		fn()
	}()
}
