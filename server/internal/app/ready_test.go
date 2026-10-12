package app

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

func TestReadyURL(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		baseURL    string
		listenAddr string
		expected   string
	}{
		{"wildcard port only", "", ":4000", "http://localhost:4000"},
		{"ipv4 wildcard", "", "0.0.0.0:4000", "http://localhost:4000"},
		{"ipv6 wildcard as Listener.Addr prints it", "", "[::]:4972", "http://localhost:4972"},
		{"explicit loopback", "", "127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"explicit host", "", "acme.local:4000", "http://acme.local:4000"},
		{"ipv6 loopback", "", "[::1]:4000", "http://[::1]:4000"},
		{"default base url is not a choice", config.DefaultBaseURL, "[::]:4972", "http://localhost:4972"},
		{"configured base url wins", "https://status.acme.com", "[::]:4000", "https://status.acme.com"},
		{"configured base url trailing slash", "https://status.acme.com/", ":4000", "https://status.acme.com"},
		{"configured base url padded", "  https://status.acme.com  ", ":4000", "https://status.acme.com"},
		{"unparseable listen falls back to base url", "", "not-an-address", config.DefaultBaseURL},
		{"unparseable listen keeps default base url", config.DefaultBaseURL, "nope", config.DefaultBaseURL},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.expected, readyURL(tc.baseURL, tc.listenAddr))
		})
	}
}

func TestPostgresTarget(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		dsn      string
		expected string
	}{
		{"full url", "postgres://alice:s3cret@db.acme.com:5433/solidping?sslmode=disable", "db.acme.com:5433/solidping"},
		{"postgresql scheme", "postgresql://alice:s3cret@db.acme.com/solidping", "db.acme.com:5432/solidping"},
		{"no credentials", "postgres://db.acme.com:5432/solidping", "db.acme.com:5432/solidping"},
		{"no database", "postgres://alice:s3cret@db.acme.com:5432", "db.acme.com:5432"},
		{"ipv6 host", "postgres://alice:s3cret@[::1]/solidping", "[::1]:5432/solidping"},
		{
			"host query parameter",
			"postgres://alice:s3cret@/solidping?host=/var/run/postgresql",
			"/var/run/postgresql/solidping",
		},
		{"key value form is refused", "host=db.acme.com user=alice password=s3cret dbname=solidping", ""},
		{"empty", "", ""},
		{"wrong scheme", "mysql://alice:s3cret@db.acme.com/solidping", ""},
		// An unescaped "/" in the password moves part of the secret into the
		// host and the path: refuse rather than print it.
		{"unescaped slash in password", "postgres://alice:12/s3cret@db.acme.com/solidping", ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			target := postgresTarget(tc.dsn)
			require.Equal(t, tc.expected, target)
			require.NotContains(t, target, "s3cret")
			require.NotContains(t, target, "alice")
		})
	}
}

func TestDatabaseDescription(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	testCases := []struct {
		name     string
		dbCfg    *config.DatabaseConfig
		runMode  string
		expected string
	}{
		{
			"sqlite file",
			&config.DatabaseConfig{Type: config.DatabaseTypeSQLite, Dir: dir},
			"",
			"sqlite " + filepath.Join(dir, "solidping.db"),
		},
		{
			"sqlite file in a run mode",
			&config.DatabaseConfig{Type: config.DatabaseTypeSQLite, Dir: dir},
			"test",
			"sqlite " + filepath.Join(dir, "solidping-test.db"),
		},
		{
			"sqlite in memory",
			&config.DatabaseConfig{Type: config.DatabaseTypeSQLiteMemory},
			"",
			"sqlite-memory",
		},
		{
			"postgres never prints credentials",
			&config.DatabaseConfig{Type: config.DatabaseTypePostgres, URL: "postgres://alice:s3cret@db.acme.com:5432/solidping"},
			"",
			"postgres db.acme.com:5432/solidping",
		},
		{
			"postgres with an unusable dsn",
			&config.DatabaseConfig{Type: config.DatabaseTypePostgres, URL: "host=db.acme.com password=s3cret"},
			"",
			"postgres",
		},
		{
			"embedded postgres",
			&config.DatabaseConfig{Type: config.DatabaseTypePostgresEmbedded},
			"",
			"postgres-embedded localhost:5433",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.expected, databaseDescription(tc.dbCfg, tc.runMode))
		})
	}
}

func TestSeedAdminEmail(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("admin@solidping.io", seedAdminEmail(&config.Config{}))

	cfg := &config.Config{}
	cfg.Auth.AdminEmail = "  Ops@Acme.com "
	r.Equal("ops@acme.com", seedAdminEmail(cfg))
}
