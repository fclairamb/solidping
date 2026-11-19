package postgres

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/postgres/embeddedpg"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

func TestDriverDSN(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ in, want string }{
		"socket dir, default port": {
			in:   "postgres://u:p@/db?host=/var/run/postgresql&sslmode=disable",
			want: "postgres://u:p@/db?host=%2Fvar%2Frun%2Fpostgresql%2F.s.PGSQL.5432&sslmode=disable",
		},
		"socket dir, port in URL": {
			in:   "postgres://u:p@localhost:5433/db?host=/var/run/postgresql",
			want: "postgres://u:p@localhost:5433/db?host=%2Fvar%2Frun%2Fpostgresql%2F.s.PGSQL.5433",
		},
		"socket dir, port in query": {
			in:   "postgresql://u:p@/db?port=6000&host=/run/pg/",
			want: "postgresql://u:p@/db?host=%2Frun%2Fpg%2F.s.PGSQL.6000",
		},
		"socket file already": {
			in:   "postgres://u:p@/db?host=/tmp/.s.PGSQL.5432",
			want: "postgres://u:p@/db?host=%2Ftmp%2F.s.PGSQL.5432",
		},
		"tcp host untouched": {
			in:   "postgres://u:p@db.example.com:5432/db?sslmode=require",
			want: "postgres://u:p@db.example.com:5432/db?sslmode=require",
		},
		"unix scheme untouched": {
			in:   "unix://u:p@db/var/run/postgresql/.s.PGSQL.5432",
			want: "unix://u:p@db/var/run/postgresql/.s.PGSQL.5432",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, DriverDSN(tc.in))
		})
	}
}

func TestListenerDSN(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("postgres://u:p@/db?host=%2Ftmp&port=5433",
		ListenerDSN("postgres://u:p@/db?host=/tmp/.s.PGSQL.5433"))

	dirForm := "postgres://u:p@/db?host=/var/run/postgresql&sslmode=disable"
	r.Equal(dirForm, ListenerDSN(dirForm), "lib/pq handles the directory form natively")

	tcp := "postgres://u:p@localhost:5432/db"
	r.Equal(tcp, ListenerDSN(tcp))
}

// TestUnixSocketDSN_Postgres connects to a real Postgres listening only on a
// Unix socket (TCP off), with the libpq-style DSN operators write (`host=<socket dir>`),
// through both drivers the server uses: bun's pgdriver (New) and lib/pq (the
// LISTEN notifier).
func TestUnixSocketDSN_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	if runtime.GOOS == "windows" {
		t.Skip("Unix-domain sockets are not used on Windows")
	}

	r := require.New(t)

	// A short path: a socket path is capped at ~104 bytes on macOS, and
	// os.TempDir() there is already ~50.
	socketDir, err := os.MkdirTemp("/tmp", "sp-sock-")
	r.NoError(err)
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })

	port := testsupport.FreePort(t)

	inst, err := embeddedpg.Start(&embeddedpg.Options{
		Suite:     "unixsocket",
		Port:      port,
		Database:  "solidping_test",
		Username:  "postgres",
		Password:  "postgres",
		SocketDir: socketDir,
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}
	t.Cleanup(func() { _ = inst.Stop() })

	// embedded-postgres puts the socket in a private "ep-*" child of SocketDir.
	children, err := filepath.Glob(filepath.Join(socketDir, "ep-*"))
	r.NoError(err)
	r.Len(children, 1)

	dsn := fmt.Sprintf("postgres://postgres:postgres@/solidping_test?host=%s&port=%d&sslmode=disable",
		children[0], port)

	svc, err := New(t.Context(), &Config{DSN: dsn, RunMode: runModeTest})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.DB().PingContext(t.Context()), "pgdriver over the Unix socket")

	connector, err := pq.NewConnector(ListenerDSN(dsn))
	r.NoError(err)

	pqDB := sql.OpenDB(connector)
	t.Cleanup(func() { _ = pqDB.Close() })
	r.NoError(pqDB.PingContext(t.Context()), "lib/pq over the Unix socket")
}
