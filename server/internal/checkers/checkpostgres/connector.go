package checkpostgres

import (
	"context"
	"database/sql"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/lib/pq"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// noFile is a path that cannot exist (a child of the null device: stat fails with
// ENOTDIR). lib/pq falls back to `~/.pgpass` and `~/.postgresql/postgresql.{crt,key}`
// when Passfile / SSLCert / SSLKey are empty; pointing them here makes it skip
// both, so a check never presents the worker's own password or client certificate.
var noFile = filepath.Join(os.DevNull, "solidping-check-has-no-file") //nolint:gochecknoglobals // constant path

// buildPQConfig maps the check's fields one to one onto a pq.Config. Nothing is
// parsed (no conninfo string, so no field can add driver options) and nothing
// is read from the worker's environment (no PG* variable, no pgpass, no service
// file, no client certificate from its home directory).
func buildPQConfig(cfg *PostgreSQLConfig, timeout time.Duration) pq.Config {
	port := cfg.Port
	if port == 0 {
		port = defaultPort
	}

	database := cfg.Database
	if database == "" {
		database = defaultDB
	}

	sslMode := cfg.SSLMode
	if sslMode == "" {
		sslMode = defaultSSLMode
	}

	host, sslModeOverride := cfg.Host, ""
	if cfg.Socket != "" {
		// pq's own socket form (a directory host) is never used to connect:
		// the dialer below ignores the address and dials cfg.Socket through
		// the shared gates. Host only has to look like a socket directory.
		host, sslModeOverride = filepath.Dir(cfg.Socket), "disable"
	}

	if sslModeOverride != "" {
		sslMode = sslModeOverride
	}

	return pq.Config{
		Host:     host,
		Port:     uint16(port),
		User:     cfg.Username,
		Password: cfg.Password,
		Passfile: noFile,
		Database: database,
		SSLMode:  pq.SSLMode(sslMode),
		SSLCert:  noFile,
		SSLKey:   noFile,
		SSLSNI:   true,
		// The defaults pq.NewConfig would apply; NewConnectorConfig applies none.
		MinProtocolVersion: "3.0",
		MaxProtocolVersion: "3.0",
		ClientEncoding:     "UTF8",
		Datestyle:          "ISO, MDY",
		ConnectTimeout:     timeout,
	}
}

// openDB opens the PostgreSQL handle from an explicit pq.Config. The dial goes
// through the tunnel dialer, or the egress guard under an enforcing policy (spec
// 2026-09-25-19), or a plain net.Dialer; every one of them refuses a Unix network,
// so only the socket field (through LocalSocketDialer) reaches a socket. The
// driver negotiates TLS above this dialer either way.
func openDB(ctx context.Context, pqCfg *pq.Config, socket string) (*sql.DB, error) {
	connector, err := pq.NewConnectorConfig(*pqCfg)
	if err != nil {
		return nil, err
	}

	if socket != "" {
		// Unix socket (spec 2026-11-24-03): every gate lives in the shared dialer.
		connector.Dialer(pqTunnelDialer{ctx: ctx, dialer: checkerdef.LocalSocketDialer(ctx, socket, pqCfg.ConnectTimeout)})

		return sql.OpenDB(connector), nil
	}

	// Never lib/pq's default dialer: it dials `unix` for a path host, which
	// would bypass the socket allowlist (spec 2026-11-24-08). The base dialer
	// is the tunnel, the enforcing egress guard, or a plain net.Dialer.
	dialer := checkerdef.OutboundDialer(ctx)
	if dialer == nil {
		dialer = &net.Dialer{}
	}

	connector.Dialer(pqTunnelDialer{ctx: ctx, dialer: checkerdef.NoLocalSocketDialer(dialer)})

	return sql.OpenDB(connector), nil
}
