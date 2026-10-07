package otelsetup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

func TestProviderStartDisabled(t *testing.T) {
	t.Parallel()

	p := NewProvider(config.OTelConfig{})
	lp, err := p.Start(context.Background())
	require.NoError(t, err)
	require.Nil(t, lp)

	// Shutdown with nothing initialized is a no-op.
	p.Shutdown(context.Background())
}

//nolint:paralleltest // providers set process-global otel state
func TestProviderStartProtocols(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
	}{
		{"grpc", "grpc"},
		{"http", protocolHTTP},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewProvider(config.OTelConfig{
				Enabled:  true,
				Endpoint: "127.0.0.1:1",
				Protocol: tt.protocol,
				Insecure: true,
				Logs:     true,
				Traces:   true,
				Metrics:  true,
			})

			lp, err := p.Start(context.Background())
			require.NoError(t, err)
			require.NotNil(t, lp)
			require.NotNil(t, p.tracerProvider)
			require.NotNil(t, p.meterProvider)

			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()

			p.Shutdown(ctx)
		})
	}
}

//nolint:paralleltest // providers set process-global otel state
func TestProviderStartNoSignals(t *testing.T) {
	p := NewProvider(config.OTelConfig{Enabled: true, Endpoint: "127.0.0.1:1"})
	lp, err := p.Start(context.Background())
	require.NoError(t, err)
	require.Nil(t, lp)
}

func TestCheckMetricsRecordExecution(t *testing.T) {
	t.Parallel()

	m := NewCheckMetrics()
	require.NotNil(t, m)

	m.RecordExecution(context.Background(), "u", "s", "n", "http", "eu", "o", "up", 12.5, true)
	m.RecordExecution(context.Background(), "u", "s", "n", "http", "eu", "o", "down", 3, false)

	// Nil instruments must not panic.
	(&CheckMetrics{}).RecordExecution(context.Background(), "", "", "", "", "", "", "", 0, false)
}
