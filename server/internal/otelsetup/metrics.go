package otelsetup

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// CheckMetrics holds OTel instruments for check execution.
type CheckMetrics struct {
	checkDuration        metric.Float64Histogram
	checkExecTotal       metric.Int64Counter
	checkExecFailedTotal metric.Int64Counter
}

// NewCheckMetrics creates check execution metric instruments.
func NewCheckMetrics() *CheckMetrics {
	meter := otel.Meter("solidping.check")

	dur, err := meter.Float64Histogram(
		"solidping.check.duration_ms",
		metric.WithDescription(
			"Check execution duration in milliseconds",
		),
		metric.WithUnit("ms"),
	)
	if err != nil {
		slog.Error("OTel: failed to create duration histogram",
			"error", err)
	}

	total, err := meter.Int64Counter(
		"solidping.check.executions_total",
		metric.WithDescription(
			"Total number of check executions",
		),
	)
	if err != nil {
		slog.Error("OTel: failed to create executions counter",
			"error", err)
	}

	failed, err := meter.Int64Counter(
		"solidping.check.executions_failed_total",
		metric.WithDescription(
			"Total number of failed check executions",
		),
	)
	if err != nil {
		slog.Error("OTel: failed to create failed counter",
			"error", err)
	}

	return &CheckMetrics{
		checkDuration:        dur,
		checkExecTotal:       total,
		checkExecFailedTotal: failed,
	}
}

// RecordExecution records a single check execution.
func (m *CheckMetrics) RecordExecution(
	ctx context.Context,
	checkUID, checkSlug, checkName, checkType,
	region, orgUID, status string,
	durationMs float64,
	success bool,
) {
	attrs := metric.WithAttributes(
		attribute.String("check.uid", checkUID),
		attribute.String("check.slug", checkSlug),
		attribute.String("check.name", checkName),
		attribute.String("check.type", checkType),
		attribute.String("region", region),
		attribute.String("organization.uid", orgUID),
		attribute.String("status", status),
	)

	if m.checkDuration != nil {
		m.checkDuration.Record(ctx, durationMs, attrs)
	}

	if m.checkExecTotal != nil {
		m.checkExecTotal.Add(ctx, 1, attrs)
	}

	if !success && m.checkExecFailedTotal != nil {
		m.checkExecFailedTotal.Add(ctx, 1, attrs)
	}
}
