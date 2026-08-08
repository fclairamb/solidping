// Package checkclickhouse provides ClickHouse database health checks over the
// native (binary) protocol.
package checkclickhouse

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

// ClickHouseChecker implements the Checker interface for ClickHouse database checks.
type ClickHouseChecker struct{}

// Type returns the check type identifier.
func (c *ClickHouseChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeClickHouse
}

// Validate checks if the configuration is valid.
func (c *ClickHouseChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &ClickHouseConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Name == "" {
		spec.Name = fmt.Sprintf("%s:%d/%s", cfg.Host, cfg.resolvedPort(), cfg.resolvedDatabase())
	}

	if spec.Slug == "" {
		spec.Slug = "clickhouse-" + strings.ReplaceAll(cfg.Host, ".", "-")
	}

	return nil
}

// buildOptions turns the config into native-protocol client options. The
// tunnel dialer, when present, receives the raw host:port so the bastion
// resolves the hostname; untunneled, DialContext stays nil and the driver uses
// its own net.Dialer unchanged.
func buildOptions(ctx context.Context, cfg *ClickHouseConfig) *clickhouse.Options {
	timeout := cfg.resolvedTimeout()

	opts := &clickhouse.Options{
		Protocol: clickhouse.Native,
		Addr:     []string{net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.resolvedPort()))},
		Auth: clickhouse.Auth{
			Database: cfg.resolvedDatabase(),
			Username: cfg.resolvedUsername(),
			Password: cfg.Password,
		},
		DialTimeout:  timeout,
		ReadTimeout:  timeout,
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	}

	if cfg.Secure {
		opts.TLS = &tls.Config{
			ServerName:         cfg.Host,
			InsecureSkipVerify: !cfg.TLSVerify,
		}
	}

	if dialer := checkerdef.TunnelDialerFrom(ctx); dialer != nil {
		opts.DialContext = func(ctx context.Context, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", addr)
		}
	}

	return opts
}

// Execute performs the ClickHouse check and returns the result.
func (c *ClickHouseChecker) Execute(
	ctx context.Context,
	config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*ClickHouseConfig](config)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, cfg.resolvedTimeout())
	defer cancel()

	start := time.Now()

	metrics := map[string]any{}
	output := map[string]any{
		checkerdef.OutputKeyHost: cfg.Host,
		checkerdef.OutputKeyPort: cfg.resolvedPort(),
		fieldDatabase:            cfg.resolvedDatabase(),
		"secure":                 cfg.Secure,
	}

	if checkerdef.TunnelDialerFrom(ctx) != nil {
		output["tunneled"] = true
	}

	conn, err := clickhouse.Open(buildOptions(ctx, cfg))
	if err != nil {
		output[checkerdef.OutputKeyError] = "failed to open connection: " + err.Error()

		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output:   output,
		}, nil
	}

	defer func() { _ = conn.Close() }()

	pingStart := time.Now()

	if pingErr := conn.Ping(ctx); pingErr != nil {
		return failure(ctx, start, nil, output, "ping failed: "+pingErr.Error(), "connection timeout"), nil
	}

	metrics["connection_time_ms"] = durationMs(time.Since(pingStart))

	if version, versionErr := conn.ServerVersion(); versionErr == nil && version != nil {
		output["server_version"] = version.Version.String()
	}

	queryStart := time.Now()

	output["query"] = cfg.resolvedQuery()

	queryResult, err := executeQuery(ctx, conn, cfg.resolvedQuery())
	if err != nil {
		return failure(ctx, start, metrics, output, err.Error(), "query timeout"), nil
	}

	metrics["query_time_ms"] = durationMs(time.Since(queryStart))
	metrics["total_time_ms"] = durationMs(time.Since(start))

	output["result"] = queryResult

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}, nil
}
