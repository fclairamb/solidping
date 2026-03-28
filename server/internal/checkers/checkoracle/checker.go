package checkoracle

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/sijms/go-ora/v2" // Oracle driver registration

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

// OracleChecker implements the Checker interface for Oracle Database checks.
type OracleChecker struct{}

// Type returns the check type identifier.
func (c *OracleChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeOracle
}

// Validate checks if the configuration is valid.
func (c *OracleChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &OracleConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	port := cfg.Port
	if port == 0 {
		port = defaultPort
	}

	if spec.Name == "" {
		serviceName := cfg.ServiceName
		if serviceName == "" && cfg.SID != "" {
			serviceName = cfg.SID
		} else if serviceName == "" {
			serviceName = defaultServiceName
		}

		spec.Name = fmt.Sprintf("%s:%d/%s", cfg.Host, port, serviceName)
	}

	if spec.Slug == "" {
		spec.Slug = "oracle-" + strings.ReplaceAll(cfg.Host, ".", "-")
	}

	return nil
}

type execParams struct {
	timeout time.Duration
	query   string
	port    int
	connURL string
}

func newExecParams(cfg *OracleConfig) execParams {
	params := execParams{
		timeout: cfg.Timeout,
		query:   cfg.Query,
		port:    cfg.Port,
		connURL: cfg.buildConnURL(),
	}

	if params.timeout == 0 {
		params.timeout = defaultTimeout
	}

	if params.query == "" {
		params.query = defaultQuery
	}

	if params.port == 0 {
		params.port = defaultPort
	}

	return params
}

// Execute performs the Oracle check and returns the result.
func (c *OracleChecker) Execute(
	ctx context.Context,
	config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*OracleConfig](config)
	if err != nil {
		return nil, err
	}

	params := newExecParams(cfg)

	ctx, cancel := context.WithTimeout(ctx, params.timeout)
	defer cancel()

	start := time.Now()

	metrics := map[string]any{}
	output := map[string]any{
		"host": cfg.Host,
		"port": params.port,
	}

	if cfg.ServiceName != "" {
		output["service_name"] = cfg.ServiceName
	}

	conn, err := sql.Open("oracle", params.connURL)
	if err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output:   map[string]any{"error": "failed to open connection: " + err.Error()},
		}, nil
	}

	defer func() { _ = conn.Close() }()

	pingStart := time.Now()

	if pingErr := conn.PingContext(ctx); pingErr != nil {
		return handlePingError(ctx, pingErr, start), nil
	}

	metrics["connection_time_ms"] = durationMs(time.Since(pingStart))

	queryResult, err := executeQuery(ctx, conn, params.query)
	if err != nil {
		return handleQueryError(ctx, err, start, metrics), nil
	}

	metrics["total_time_ms"] = durationMs(time.Since(start))

	output["query"] = params.query
	output["result"] = queryResult

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}, nil
}

func executeQuery(
	ctx context.Context,
	conn *sql.DB,
	query string,
) (string, error) {
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return "", fmt.Errorf("query failed: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var result string

	if rows.Next() {
		if err := rows.Scan(&result); err != nil {
			return "", fmt.Errorf("failed to scan result: %w", err)
		}
	}

	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("row iteration error: %w", err)
	}

	return result, nil
}

func handlePingError(ctx context.Context, err error, start time.Time) *checkerdef.Result {
	if ctx.Err() != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: time.Since(start),
			Output:   map[string]any{"error": "connection timeout"},
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Output:   map[string]any{"error": "ping failed: " + err.Error()},
	}
}

func handleQueryError(
	ctx context.Context,
	err error,
	start time.Time,
	metrics map[string]any,
) *checkerdef.Result {
	if ctx.Err() != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   map[string]any{"error": "query timeout"},
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   map[string]any{"error": err.Error()},
	}
}

func durationMs(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / microsecondsPerMilli
}
