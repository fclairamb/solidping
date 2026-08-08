package checkclickhouse_test

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkclickhouse"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// startDeadListener opens a real TCP listener that accepts and immediately
// closes every connection — a port that answers but speaks no ClickHouse.
func startDeadListener(t *testing.T) (string, int) {
	t.Helper()
	r := require.New(t)

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	r.NoError(err)

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			_ = conn.Close()
		}
	}()

	host, portStr, err := net.SplitHostPort(listener.Addr().String())
	r.NoError(err)

	port, err := strconv.Atoi(portStr)
	r.NoError(err)

	return host, port
}

func execute(ctx context.Context, t *testing.T, config map[string]any) *checkerdef.Result {
	t.Helper()
	r := require.New(t)

	cfg := &checkclickhouse.ClickHouseConfig{}
	r.NoError(cfg.FromMap(config))

	result, err := (&checkclickhouse.ClickHouseChecker{}).Execute(ctx, cfg)
	r.NoError(err)
	r.NotNil(result)

	return result
}

// A handshake that never completes is the target's fault, not ours: down, with
// the driver's error preserved in the output.
func TestExecuteAgainstNonClickHousePortIsDown(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	host, port := startDeadListener(t)

	result := execute(t.Context(), t, map[string]any{
		"host":    host,
		"port":    port,
		"timeout": "3s",
	})

	r.Equal(checkerdef.StatusDown, result.Status)
	r.Contains(result.Output, checkerdef.OutputKeyError)
}

// A closed port fails at dial time — still down, never up.
func TestExecuteAgainstClosedPortIsDown(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := execute(t.Context(), t, map[string]any{
		"host":    "127.0.0.1",
		"port":    1,
		"timeout": "3s",
	})

	r.Equal(checkerdef.StatusDown, result.Status)
}

// An already-canceled context must surface as a timeout, not as a down target.
func TestExecuteWithCanceledContextIsTimeout(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	host, port := startDeadListener(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	result := execute(ctx, t, map[string]any{
		"host":    host,
		"port":    port,
		"timeout": "3s",
	})

	r.Equal(checkerdef.StatusTimeout, result.Status)
}

// Execute must reject a config belonging to another checker rather than panic.
func TestExecuteRejectsForeignConfig(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result, err := (&checkclickhouse.ClickHouseChecker{}).Execute(t.Context(), &foreignConfig{})
	r.Error(err)
	r.Nil(result)
}

type foreignConfig struct{}

func (c *foreignConfig) FromMap(_ map[string]any) error { return nil }

func (c *foreignConfig) GetConfig() map[string]any { return map[string]any{} }

func TestValidateFillsNameAndSlug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   map[string]any
		wantName string
		wantSlug string
	}{
		{
			name:     "defaults port and database",
			config:   map[string]any{"host": "ch.example.com"},
			wantName: "ch.example.com:9000/default",
			wantSlug: "clickhouse-ch-example-com",
		},
		{
			name:     "secure defaults to the TLS port",
			config:   map[string]any{"host": "ch.example.com", "secure": true},
			wantName: "ch.example.com:9440/default",
			wantSlug: "clickhouse-ch-example-com",
		},
		{
			name:     "explicit port and database",
			config:   map[string]any{"host": "db.internal", "port": 9001, "database": "metrics"},
			wantName: "db.internal:9001/metrics",
			wantSlug: "clickhouse-db-internal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			checker := &checkclickhouse.ClickHouseChecker{}
			spec := &checkerdef.CheckSpec{Config: tt.config}

			r.NoError(checker.Validate(spec))
			r.Equal(tt.wantName, spec.Name)
			r.Equal(tt.wantSlug, spec.Slug)
		})
	}
}

// An explicit name/slug must survive Validate untouched.
func TestValidateKeepsExplicitNameAndSlug(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &checkclickhouse.ClickHouseChecker{}
	spec := &checkerdef.CheckSpec{
		Name:   "Analytics cluster",
		Slug:   "analytics",
		Config: map[string]any{"host": "ch.example.com"},
	}

	r.NoError(checker.Validate(spec))
	r.Equal("Analytics cluster", spec.Name)
	r.Equal("analytics", spec.Slug)
}

func TestCheckerType(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &checkclickhouse.ClickHouseChecker{}
	r.Equal(checkerdef.CheckTypeClickHouse, checker.Type())
}

func TestSampleConfigsAreValid(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &checkclickhouse.ClickHouseChecker{}

	samples := checker.GetSampleConfigs(&checkerdef.ListSampleOptions{})
	r.NotEmpty(samples)

	for _, sample := range samples {
		spec := sample
		r.NoError(checker.Validate(&spec))
	}
}
