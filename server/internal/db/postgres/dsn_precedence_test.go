package postgres

import (
	"testing"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun/driver/pgdriver"
)

// TestSocketHostOverridesAuthorityHost pins libpq precedence: a `host=` query
// parameter naming a socket directory wins over the URL's authority host, for
// both drivers the server uses (pgdriver for the pool, lib/pq for the notifier).
func TestSocketHostOverridesAuthorityHost(t *testing.T) {
	t.Parallel()

	dsns := map[string]string{
		"sslmode before host": "postgres://u:p@localhost:5432/db?sslmode=disable&host=/var/run/postgresql",
		"sslmode after host":  "postgres://u:p@localhost:5432/db?host=/var/run/postgresql&sslmode=disable",
	}

	for name, dsn := range dsns {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			conf := pgdriver.NewConnector(pgdriver.WithDSN(DriverDSN(dsn))).Config()
			r.Equal("unix", conf.Network)
			r.Equal("/var/run/postgresql/.s.PGSQL.5432", conf.Addr)
			r.Equal("db", conf.Database)

			pqConf, err := pq.NewConfig(ListenerDSN(dsn))
			r.NoError(err)
			r.Equal("/var/run/postgresql", pqConf.Host)
			r.EqualValues(5432, pqConf.Port)

			r.True(SocketOverridesAuthorityHost(dsn))
		})
	}
}

func TestListenerDSN_DropsAuthorityHost(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("postgres://u:p@/db?host=%2Fvar%2Frun%2Fpostgresql&port=5433",
		ListenerDSN("postgres://u:p@localhost:5433/db?host=/var/run/postgresql"))
	r.Equal("postgres://u:p@/db?host=%2Ftmp&port=6000",
		ListenerDSN("postgres://u:p@localhost:5432/db?host=/tmp/.s.PGSQL.6000"))
	r.False(SocketOverridesAuthorityHost("postgres://u:p@/db?host=/tmp"))
	r.False(SocketOverridesAuthorityHost("postgres://u:p@localhost:5432/db"))
}
