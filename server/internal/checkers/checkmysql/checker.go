// Package checkmysql provides MySQL/MariaDB database health checks.
package checkmysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql" // MySQL driver registration

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

// MySQLChecker implements the Checker interface for MySQL/MariaDB database checks.
type MySQLChecker struct{}

// Type returns the check type identifier.
func (c *MySQLChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeMySQL
}

// Validate checks if the configuration is valid.
func (c *MySQLChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &MySQLConfig{}
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
		if cfg.Database != "" {
			spec.Name = fmt.Sprintf("%s:%d/%s", cfg.Host, port, cfg.Database)
		} else {
			spec.Name = fmt.Sprintf("%s:%d", cfg.Host, port)
		}
	}

	if spec.Slug == "" {
		spec.Slug = "mysql-" + strings.ReplaceAll(cfg.Host, ".", "-")
	}

	return nil
}

type execParams struct {
	timeout time.Duration
	query   string
	port    int
	dsn     string
}

func newExecParams(cfg *MySQLConfig) execParams {
	params := execParams{
		timeout: cfg.Timeout,
		query:   cfg.Query,
		port:    cfg.Port,
		dsn:     cfg.buildDSN(),
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

// Execute performs the MySQL check and returns the result.
func (c *MySQLChecker) Execute(
	ctx context.Context,
	config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*MySQLConfig](config)
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

	conn, err := sql.Open("mysql", params.dsn)
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
