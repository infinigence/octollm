package main

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// contextFieldsHandler is an slog.Handler that injects trace fields
// (trace_id, span_id) from context into log records.
type contextFieldsHandler struct {
	inner slog.Handler
}

func newContextFieldsHandler(inner slog.Handler) slog.Handler {
	return &contextFieldsHandler{inner: inner}
}

func (h *contextFieldsHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *contextFieldsHandler) Handle(ctx context.Context, r slog.Record) error {
	if ctx != nil {
		span := trace.SpanFromContext(ctx)
		if span.IsRecording() {
			sc := span.SpanContext()
			r.AddAttrs(
				slog.String("trace_id", sc.TraceID().String()),
				slog.String("span_id", sc.SpanID().String()),
			)
		}
	}
	return h.inner.Handle(ctx, r)
}

func (h *contextFieldsHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextFieldsHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *contextFieldsHandler) WithGroup(name string) slog.Handler {
	return &contextFieldsHandler{inner: h.inner.WithGroup(name)}
}
