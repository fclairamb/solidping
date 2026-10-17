package checkpostgres

import (
	"context"
	"database/sql"
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

	return pq.Config{
		Host:     cfg.Host,
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
// 2026-09-25-19); without either it keeps lib/pq's default dialer. The driver
// negotiates TLS above this dialer either way.
func openDB(ctx context.Context, pqCfg *pq.Config) (*sql.DB, error) {
	connector, err := pq.NewConnectorConfig(*pqCfg)
	if err != nil {
		return nil, err
	}

	if dialer := checkerdef.OutboundDialer(ctx); dialer != nil {
		connector.Dialer(pqTunnelDialer{ctx: ctx, dialer: dialer})
	}

	return sql.OpenDB(connector), nil
}
