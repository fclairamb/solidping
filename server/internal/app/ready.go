package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/defaults"
	"github.com/fclairamb/solidping/server/internal/version"
)

// defaultPostgresPort is appended to a Postgres DSN host that names none, the
// same default the driver applies.
const defaultPostgresPort = "5432"

// The URL schemes the Postgres driver accepts in a DSN.
const (
	schemePostgres   = "postgres"
	schemePostgreSQL = "postgresql"
)

// logReady writes the line an operator waits for once the HTTP listener is
// bound: where to open SolidPing, which version runs, and which database it
// uses. While the seeded admin still has to rotate its password, a second line
// gives the first login. A deported agent serves no HTTP and logs neither.
func (s *Server) logReady(ctx context.Context, logger *slog.Logger, listenAddr net.Addr) {
	if s.config.IsAgentMode() {
		return
	}

	logger.InfoContext(ctx, "SolidPing is ready",
		"url", readyURL(s.config.Server.BaseURL, listenAddr.String()),
		"listen", listenAddr.String(),
		"version", version.Get().Version,
		"database", databaseDescription(&s.config.Database, s.config.RunMode))

	s.logFirstLogin(ctx, logger)
}

// logFirstLogin tells the first login, only while the seeded admin has never
// rotated its password: users.must_change_password is set by the startup seed
// alone and cleared by the forced change, so the seed password is still the
// account's password. That seed password is a constant (defaults.Password,
// never configurable), and it is the only password this line can print.
func (s *Server) logFirstLogin(ctx context.Context, logger *slog.Logger) {
	if s.dbService == nil {
		return
	}

	email := seedAdminEmail(s.config)

	user, err := s.dbService.GetUserByEmail(ctx, email)
	if err != nil || user == nil || !user.MustChangePassword {
		return
	}

	logger.InfoContext(ctx, "First login with the default admin account, you will be asked to change the password",
		"email", email,
		"password", defaults.Password)
}

// seedAdminEmail is the email the startup job seeds the admin account with:
// auth.admin_email (SP_ADMIN_EMAIL) when set, the default otherwise. It mirrors
// jobtypes.seedAdminEmail, which writes the row this lookup reads.
func seedAdminEmail(cfg *config.Config) string {
	if email := strings.ToLower(strings.TrimSpace(cfg.Auth.AdminEmail)); email != "" {
		return email
	}

	return defaults.Email
}

// readyURL is the URL the ready line points at: the configured public base
// URL when an operator set one, otherwise one derived from the address the
// HTTP server listens on. A wildcard or empty host (":4000", "0.0.0.0:4000",
// "[::]:4000") reads as localhost. config.DefaultBaseURL counts as unset: it
// is what every install starts with, so it says nothing about the port this
// instance actually listens on.
func readyURL(baseURL, listenAddr string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL != "" && baseURL != config.DefaultBaseURL {
		return baseURL
	}

	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil || port == "" {
		if baseURL != "" {
			return baseURL
		}

		return config.DefaultBaseURL
	}

	switch host {
	case "", "0.0.0.0", "::":
		host = "localhost"
	}

	return "http://" + net.JoinHostPort(host, port)
}

// databaseDescription names the database for the ready line: the type, plus
// the SQLite file path or the Postgres host:port/dbname. It never includes a
// user name, a password or the DSN itself.
func databaseDescription(dbCfg *config.DatabaseConfig, runMode string) string {
	switch dbCfg.Type {
	case config.DatabaseTypeSQLite:
		path := filepath.Join(dbCfg.Dir, sqlite.DatabaseFilename(runMode))
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}

		return dbCfg.Type + " " + path
	case config.DatabaseTypePostgres:
		if target := postgresTarget(dbCfg.URL); target != "" {
			return dbCfg.Type + " " + target
		}

		return dbCfg.Type
	case config.DatabaseTypePostgresEmbedded:
		return fmt.Sprintf("%s localhost:%d", dbCfg.Type, embeddedPostgresPort)
	default:
		return dbCfg.Type
	}
}

// postgresTarget extracts host:port/dbname from a Postgres URL DSN (the only
// form the driver accepts), or "" when it cannot do so safely. Credentials are
// never read: only the URL's host, path and `host` query parameter are. A DSN
// whose password holds an unescaped "/" or "@" parses into a host or path that
// may carry part of the secret, so anything with "@" in the host or "@" or "/"
// in the database name is refused rather than printed.
func postgresTarget(dsn string) string {
	parsed, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil || (parsed.Scheme != schemePostgres && parsed.Scheme != schemePostgreSQL) {
		return ""
	}

	host := parsed.Host
	if override := parsed.Query().Get("host"); override != "" {
		host = override
	}

	if host == "" || strings.Contains(host, "@") {
		return ""
	}

	if !strings.HasPrefix(host, "/") {
		if _, _, splitErr := net.SplitHostPort(host); splitErr != nil {
			host = net.JoinHostPort(strings.Trim(host, "[]"), defaultPostgresPort)
		}
	}

	dbName := strings.TrimPrefix(parsed.Path, "/")
	if strings.ContainsAny(dbName, "@/") {
		return ""
	}

	if dbName == "" {
		return host
	}

	return host + "/" + dbName
}
