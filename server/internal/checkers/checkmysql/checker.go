// Package checkmysql provides MySQL/MariaDB database health checks.
package checkmysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql" // MySQL driver registration + dial registry

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

// tunnelNetwork is the single well-known go-sql-driver network name whose dial
// func routes through the SSH tunnel. go-sql-driver's dial registry is a
// process-global map that never shrinks, so we register ONE entry once (in
// init) and select it per-check via the DSN — never one entry per check.
const tunnelNetwork = "solidping-tunnel"

var errNoTunnelDialer = errors.New("mysql tunnel dial: no tunnel dialer on connection context")

//nolint:gochecknoinits // one-time process-global registration; the dial func pulls the per-check tunnel dialer from the connection context, so a single registration serves every tunneled check.
func init() {
	mysql.RegisterDialContext(tunnelNetwork, func(ctx context.Context, addr string) (net.Conn, error) {
		dialer := checkerdef.TunnelDialerFrom(ctx)
		if dialer == nil {
			return nil, errNoTunnelDialer
		}

		return dialer.DialContext(ctx, "tcp", addr)
	})
}

// tunneledDSN rewrites the DSN's `tcp` protocol to the registered tunnel network
// so go-sql-driver dials through the bastion via the globally-registered dial
// func (which reads the tunnel dialer off the connection context). buildDSN
// always emits `@tcp(`, so this single replacement is exact.
func tunneledDSN(dsn string) string {
	return strings.Replace(dsn, "@tcp(", "@"+tunnelNetwork+"(", 1)
}

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
		checkerdef.OutputKeyHost: cfg.Host,
		checkerdef.OutputKeyPort: params.port,
	}

	// Tunneled: select the registered tunnel network in the DSN so go-sql-driver
	// dials through the bastion (the raw host:port is handed to the dial func, so
	// the bastion resolves the hostname). Untunneled, the DSN is unchanged.
	dsn := params.dsn
	if checkerdef.TunnelDialerFrom(ctx) != nil {
		dsn = tunneledDSN(params.dsn)
		output["tunneled"] = true
	}

	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output:   map[string]any{checkerdef.OutputKeyError: "failed to open connection: " + err.Error()},
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
			Output:   map[string]any{checkerdef.OutputKeyError: "connection timeout"},
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Output:   map[string]any{checkerdef.OutputKeyError: "ping failed: " + err.Error()},
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
			Output:   map[string]any{checkerdef.OutputKeyError: "query timeout"},
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   map[string]any{checkerdef.OutputKeyError: err.Error()},
	}
}

func durationMs(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / microsecondsPerMilli
}
