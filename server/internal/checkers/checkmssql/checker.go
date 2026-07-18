// Package checkmssql provides Microsoft SQL Server health checks.
package checkmssql

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strings"
	"time"

	mssql "github.com/microsoft/go-mssqldb" // MSSQL driver registration + connector API

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

// mssqlTunnelDialer adapts a checkerdef.ContextDialer to go-mssqldb's HostDialer.
// Implementing HostDialer (not just Dialer) is what makes the driver SKIP its own
// local DNS resolution and dial the raw host:port through the tunnel — the
// bastion resolves the hostname (see tcpDialer.DialSqlConnection). HostName is
// required by the interface but not called on the dial path.
type mssqlTunnelDialer struct {
	host   string
	dialer checkerdef.ContextDialer
}

func (d mssqlTunnelDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.dialer.DialContext(ctx, network, addr)
}

func (d mssqlTunnelDialer) HostName() string { return d.host }

// openMSSQL opens the SQL Server handle. Tunneled, it builds a connector wired to
// dial through the bastion; untunneled, it is the byte-for-byte `sql.Open`.
func openMSSQL(ctx context.Context, connURL, host string) (*sql.DB, error) {
	dialer := checkerdef.TunnelDialerFrom(ctx)
	if dialer == nil {
		return sql.Open("sqlserver", connURL)
	}

	connector, err := mssql.NewConnector(connURL)
	if err != nil {
		return nil, err
	}

	connector.Dialer = mssqlTunnelDialer{host: host, dialer: dialer}

	return sql.OpenDB(connector), nil
}

// MSSQLChecker implements the Checker interface for Microsoft SQL Server checks.
type MSSQLChecker struct{}

// Type returns the check type identifier.
func (c *MSSQLChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeMSSQL
}

// Validate checks if the configuration is valid.
func (c *MSSQLChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &MSSQLConfig{}
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
		database := cfg.Database
		if database == "" {
			database = defaultDatabase
		}

		spec.Name = fmt.Sprintf("%s:%d/%s", cfg.Host, port, database)
	}

	if spec.Slug == "" {
		spec.Slug = "mssql-" + strings.ReplaceAll(cfg.Host, ".", "-")
	}

	return nil
}

type execParams struct {
	timeout time.Duration
	query   string
	port    int
	connURL string
}

func newExecParams(cfg *MSSQLConfig) execParams {
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

// Execute performs the MSSQL check and returns the result.
func (c *MSSQLChecker) Execute(
	ctx context.Context,
	config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*MSSQLConfig](config)
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

	if checkerdef.TunnelDialerFrom(ctx) != nil {
		output["tunneled"] = true
	}

	conn, err := openMSSQL(ctx, params.connURL, cfg.Host)
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
