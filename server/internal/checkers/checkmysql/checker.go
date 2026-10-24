// Package checkmysql provides MySQL/MariaDB database health checks.
package checkmysql

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql" // MySQL driver: connector + config

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkmysql/config"
)

const microsecondsPerMilli = 1000.0

//nolint:gochecknoinits // one-time process-wide registration of the driver logger
func init() {
	// SetLogger only fails on a nil logger.
	_ = mysql.SetLogger(mysqlLogger{})
}

// buildDriverConfig turns the check configuration into a go-sql-driver
// *mysql.Config by plain field assignment. The values never pass through
// mysql.ParseDSN, so a crafted username, password or database cannot inject
// driver parameters (e.g. `allowAllFiles=true`, which lets a hostile server
// read the worker's files over LOCAL INFILE) nor shift the parsed address
// (spec 2026-10-16-04).
//
// dialer, when non-nil (SSH tunnel or enforcing egress guard), becomes the
// connector's DialFunc: go-sql-driver calls it for every dial whatever Net
// says, so no field value can shed the tunnel or the guard. When nil the
// driver dials with its default net.Dialer, as before.
func buildDriverConfig(cfg *MySQLConfig, dialer checkerdef.ContextDialer) *mysql.Config {
	port := cfg.Port
	if port == 0 {
		port = defaultPort
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	driverCfg := mysql.NewConfig()
	driverCfg.User = cfg.Username
	driverCfg.Passwd = cfg.Password
	driverCfg.Net = "tcp"
	driverCfg.Addr = net.JoinHostPort(cfg.Host, strconv.Itoa(port))
	driverCfg.DBName = cfg.Database
	driverCfg.Timeout = timeout
	driverCfg.Logger = mysqlLogger{}
	// Never let a server pull local files over LOAD DATA LOCAL INFILE.
	driverCfg.AllowAllFiles = false

	if dialer != nil {
		driverCfg.DialFunc = func(ctx context.Context, _, addr string) (net.Conn, error) {
			// Always TCP: the network is ours, not the driver config's.
			return dialer.DialContext(ctx, "tcp", addr)
		}
	}

	return driverCfg
}

// MySQLChecker implements the Checker interface for MySQL/MariaDB database checks.
type MySQLChecker struct{}

// Type returns the check type identifier.
func (c *MySQLChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeMySQL
}

// Validate checks if the configuration is valid. Every rule lives in the light
// `config` sub-package so an offline validator (`sp checks validate`) can run it
// without linking this checker's execution client.
func (c *MySQLChecker) Validate(spec *checkerdef.CheckSpec) error {
	return checkconfig.ValidateSpec(spec)
}

type execParams struct {
	timeout time.Duration
	query   string
	port    int
}

func newExecParams(cfg *MySQLConfig) execParams {
	params := execParams{
		timeout: cfg.Timeout,
		query:   cfg.Query,
		port:    cfg.Port,
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

	if checkerdef.TunnelDialerFrom(ctx) != nil {
		output["tunneled"] = true
	}

	// Tunneled or under an enforcing egress policy (spec 2026-09-25-19), the
	// outbound dialer governs every dial through the connector's DialFunc; the
	// tunnel hands the raw host:port to the bastion, which resolves it.
	connector, err := mysql.NewConnector(buildDriverConfig(cfg, checkerdef.OutboundDialer(ctx)))
	if err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output:   map[string]any{checkerdef.OutputKeyError: "failed to open connection: " + err.Error()},
		}, nil
	}

	conn := sql.OpenDB(connector)

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
