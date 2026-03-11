package main

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel"
	// "go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// initOpenTelemetry initializes OpenTelemetry with a stdout exporter.
// In production, you should replace this with OTLP exporter or other backends.
func initOpenTelemetry(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	// Create stdout exporter
	// exporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	// if err != nil {
	// 	return nil, err
	// }

	// Create resource with service name
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, err
	}

	// Create tracer provider
	tp := sdktrace.NewTracerProvider(
		// sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		// Uncomment to sample all traces in development
		// sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	// Set global tracer provider
	otel.SetTracerProvider(tp)

	// CRITICAL: Set global propagator to enable trace context propagation
	// This allows trace IDs to be extracted from incoming HTTP requests
	// and injected into outgoing HTTP requests
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, // W3C Trace Context format (traceparent header)
		propagation.Baggage{},      // W3C Baggage format
	))

	slog.Info("OpenTelemetry initialized", slog.String("service_name", serviceName))

	// Return shutdown function
	return tp.Shutdown, nil
}
