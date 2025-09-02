// Package otelsetup initializes OpenTelemetry providers.
package otelsetup

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/fclairamb/solidping/server/internal/config"
)

const protocolHTTP = "http"

// Provider manages OpenTelemetry providers lifecycle.
type Provider struct {
	cfg            config.OTelConfig
	tracerProvider *sdktrace.TracerProvider
	meterProvider  *sdkmetric.MeterProvider
	loggerProvider *sdklog.LoggerProvider
}

// NewProvider creates a new OTel provider from config.
func NewProvider(cfg config.OTelConfig) *Provider {
	return &Provider{cfg: cfg}
}

// Start initializes all configured OTel providers.
// Returns the LoggerProvider (nil if logs are disabled).
func (p *Provider) Start(ctx context.Context) (
	*sdklog.LoggerProvider, error,
) {
	if !p.cfg.Enabled {
		slog.InfoContext(ctx, "OpenTelemetry disabled")

		return nil, nil //nolint:nilnil // nil provider is valid when disabled
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("solidping"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	if p.cfg.Traces {
		if err := p.initTracer(ctx, res); err != nil {
			return nil, fmt.Errorf("otel traces: %w", err)
		}
	}

	if p.cfg.Metrics {
		if err := p.initMeter(ctx, res); err != nil {
			return nil, fmt.Errorf("otel metrics: %w", err)
		}
	}

	if p.cfg.Logs {
		if err := p.initLogger(ctx, res); err != nil {
			return nil, fmt.Errorf("otel logs: %w", err)
		}
	}

	slog.InfoContext(ctx, "OpenTelemetry initialized",
		"endpoint", p.cfg.Endpoint,
		"protocol", p.cfg.Protocol,
		"traces", p.cfg.Traces,
		"metrics", p.cfg.Metrics,
		"logs", p.cfg.Logs,
	)

	return p.loggerProvider, nil
}

// Shutdown gracefully shuts down all providers.
func (p *Provider) Shutdown(ctx context.Context) {
	if p.tracerProvider != nil {
		if err := p.tracerProvider.Shutdown(ctx); err != nil {
			slog.ErrorContext(ctx, "OTel tracer shutdown", "error", err)
		}
	}

	if p.meterProvider != nil {
		if err := p.meterProvider.Shutdown(ctx); err != nil {
			slog.ErrorContext(ctx, "OTel meter shutdown", "error", err)
		}
	}

	if p.loggerProvider != nil {
		if err := p.loggerProvider.Shutdown(ctx); err != nil {
			slog.ErrorContext(ctx, "OTel logger shutdown", "error", err)
		}
	}
}

func (p *Provider) initTracer(
	ctx context.Context, res *resource.Resource,
) error {
	var (
		exp sdktrace.SpanExporter
		err error
	)

	switch p.cfg.Protocol {
	case protocolHTTP:
		opts := []otlptracehttp.Option{
			otlptracehttp.WithEndpoint(p.cfg.Endpoint),
		}
		if p.cfg.Insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}

		exp, err = otlptracehttp.New(ctx, opts...)
	default: // grpc
		opts := []otlptracegrpc.Option{
			otlptracegrpc.WithEndpoint(p.cfg.Endpoint),
		}
		if p.cfg.Insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}

		exp, err = otlptracegrpc.New(ctx, opts...)
	}

	if err != nil {
		return err
	}

	p.tracerProvider = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(p.tracerProvider)

	return nil
}

func (p *Provider) initMeter(
	ctx context.Context, res *resource.Resource,
) error {
	var (
		exp sdkmetric.Exporter
		err error
	)

	switch p.cfg.Protocol {
	case protocolHTTP:
		opts := []otlpmetrichttp.Option{
			otlpmetrichttp.WithEndpoint(p.cfg.Endpoint),
		}
		if p.cfg.Insecure {
			opts = append(opts, otlpmetrichttp.WithInsecure())
		}

		exp, err = otlpmetrichttp.New(ctx, opts...)
	default: // grpc
		opts := []otlpmetricgrpc.Option{
			otlpmetricgrpc.WithEndpoint(p.cfg.Endpoint),
		}
		if p.cfg.Insecure {
			opts = append(opts, otlpmetricgrpc.WithInsecure())
		}

		exp, err = otlpmetricgrpc.New(ctx, opts...)
	}

	if err != nil {
		return err
	}

	p.meterProvider = sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(exp),
		),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(p.meterProvider)

	return nil
}

func (p *Provider) initLogger(
	ctx context.Context, res *resource.Resource,
) error {
	var (
		exp sdklog.Exporter
		err error
	)

	switch p.cfg.Protocol {
	case protocolHTTP:
		opts := []otlploghttp.Option{
			otlploghttp.WithEndpoint(p.cfg.Endpoint),
		}
		if p.cfg.Insecure {
			opts = append(opts, otlploghttp.WithInsecure())
		}

		exp, err = otlploghttp.New(ctx, opts...)
	default: // grpc
		opts := []otlploggrpc.Option{
			otlploggrpc.WithEndpoint(p.cfg.Endpoint),
		}
		if p.cfg.Insecure {
			opts = append(opts, otlploggrpc.WithInsecure())
		}

		exp, err = otlploggrpc.New(ctx, opts...)
	}

	if err != nil {
		return err
	}

	p.loggerProvider = sdklog.NewLoggerProvider(
		sdklog.WithProcessor(
			sdklog.NewBatchProcessor(exp),
		),
		sdklog.WithResource(res),
	)

	return nil
}
