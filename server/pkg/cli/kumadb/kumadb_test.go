package kumadb

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/sqlitedriver"
)

// fixturePath is the real Uptime Kuma 2.5.4 database committed for these
// tests — see the README next to it for provenance.
const fixturePath = "../../../internal/handlers/checks/importers/testdata/uptime-kuma-sqlite/kuma.db"

// updateGolden reports whether the golden snapshot should be regenerated
// instead of asserted against: `UPDATE_GOLDEN=1 go test ./pkg/cli/kumadb/`.
func updateGolden() bool {
	return os.Getenv("UPDATE_GOLDEN") != ""
}

func TestRead_GoldenAgainstFixture(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	actual, err := Read(context.Background(), fixturePath)
	r.NoError(err)

	var pretty map[string]any
	r.NoError(json.Unmarshal(actual, &pretty))

	indented, err := json.MarshalIndent(pretty, "", "  ")
	r.NoError(err)
	indented = append(indented, '\n')

	path := filepath.Join("testdata", "kuma-sqlite.golden.json")

	if updateGolden() {
		r.NoError(os.WriteFile(path, indented, 0o600))

		return
	}

	expected, err := os.ReadFile(path)
	r.NoError(err, "missing golden file %s — re-run with UPDATE_GOLDEN=1", path)
	r.JSONEq(string(expected), string(actual))
}

// TestReadColumnsMatchFixtureSchema cross-checks monitorColumns against
// PRAGMA table_info(monitor) on the real fixture, so a Kuma release that
// renames or drops one of the columns this package relies on fails this test
// loudly instead of silently emitting empty fields.
func TestReadColumnsMatchFixtureSchema(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	r.NotEmpty(sqlitedriver.Name, "this build links no SQLite driver")

	ctx := context.Background()

	db, err := sql.Open(sqlitedriver.Name, "file:"+fixturePath+"?mode=ro")
	r.NoError(err)
	defer db.Close() //nolint:errcheck // read-only test handle

	rows, err := db.QueryContext(ctx, `PRAGMA table_info(monitor)`)
	r.NoError(err)
	defer rows.Close() //nolint:errcheck // read-only test handle

	present := make(map[string]bool)

	for rows.Next() {
		var (
			cid           int
			name, colType string
			notNull, pk   int
			defaultValue  sql.NullString
		)
		r.NoError(rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk))
		present[name] = true
	}
	r.NoError(rows.Err())

	for _, col := range monitorColumns {
		r.True(present[col], "column %q used by kumadb's SELECT is missing from the fixture's monitor table "+
			"(Kuma schema drift — update monitorColumns and mapping.md together)", col)
	}
}

func TestRead_NotSQLite(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	path := filepath.Join(t.TempDir(), "not-a-db.db")
	r.NoError(os.WriteFile(path, []byte("this is definitely not a SQLite database file"), 0o600))

	_, err := Read(context.Background(), path)
	r.ErrorIs(err, ErrNotSQLite)
}

func TestRead_MissingFile(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, err := Read(context.Background(), filepath.Join(t.TempDir(), "does-not-exist.db"))
	r.Error(err)
	r.NotErrorIs(err, ErrNotSQLite) // a missing file is a different failure than a wrong-format one
}

func TestRead_NoMonitorTable(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	r.NotEmpty(sqlitedriver.Name)

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "no-monitor.db")

	db, err := sql.Open(sqlitedriver.Name, "file:"+path)
	r.NoError(err)

	_, err = db.ExecContext(ctx, `CREATE TABLE foo (id INTEGER PRIMARY KEY)`)
	r.NoError(err)
	r.NoError(db.Close())

	_, err = Read(ctx, path)
	r.ErrorIs(err, ErrNoMonitorTable)
}

// minimalMonitorSchema creates just enough of Kuma's schema — every column
// kumadb's SELECT names, plus tag/monitor_tag/monitor_notification — for the
// synthetic tests below. Real column types/constraints are asserted against
// the real fixture by TestReadColumnsMatchFixtureSchema instead.
const minimalMonitorSchema = `
CREATE TABLE monitor (
	id INTEGER PRIMARY KEY,
	name TEXT, description TEXT, type TEXT, active BOOLEAN NOT NULL DEFAULT 1, parent INTEGER,
	url TEXT, hostname TEXT, port INTEGER, method TEXT NOT NULL DEFAULT 'GET', body TEXT, headers TEXT,
	interval INTEGER NOT NULL DEFAULT 60, retry_interval INTEGER NOT NULL DEFAULT 0,
	maxretries INTEGER NOT NULL DEFAULT 0, timeout DOUBLE NOT NULL DEFAULT 0,
	keyword TEXT, invert_keyword BOOLEAN NOT NULL DEFAULT 0, json_path TEXT, expected_value TEXT,
	accepted_statuscodes_json TEXT NOT NULL DEFAULT '["200-299"]',
	ignore_tls BOOLEAN NOT NULL DEFAULT 0, upside_down BOOLEAN NOT NULL DEFAULT 0,
	dns_resolve_server TEXT, dns_resolve_type TEXT,
	docker_container TEXT,
	mqtt_topic TEXT, mqtt_username TEXT, mqtt_password TEXT, mqtt_success_message TEXT,
	database_connection_string TEXT, database_query TEXT,
	grpc_url TEXT, grpc_service_name TEXT, grpc_enable_tls BOOLEAN NOT NULL DEFAULT 0,
	auth_method TEXT, basic_auth_user TEXT, basic_auth_pass TEXT
);
CREATE TABLE tag (id INTEGER PRIMARY KEY, name TEXT NOT NULL, color TEXT NOT NULL);
CREATE TABLE monitor_tag (id INTEGER PRIMARY KEY, monitor_id INTEGER NOT NULL, tag_id INTEGER NOT NULL, value TEXT);
CREATE TABLE notification (id INTEGER PRIMARY KEY, name TEXT, config TEXT);
CREATE TABLE monitor_notification (
	id INTEGER PRIMARY KEY, monitor_id INTEGER NOT NULL, notification_id INTEGER NOT NULL
);
`

func TestRead_NotificationCountOnly(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	r.NotEmpty(sqlitedriver.Name)

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "notified.db")

	db, err := sql.Open(sqlitedriver.Name, "file:"+path)
	r.NoError(err)

	_, err = db.ExecContext(ctx, minimalMonitorSchema)
	r.NoError(err)

	_, err = db.ExecContext(ctx,
		`INSERT INTO monitor (id, name, type) VALUES (1, 'Has binding', 'http'), (2, 'No binding', 'http')`)
	r.NoError(err)

	_, err = db.ExecContext(ctx,
		`INSERT INTO notification (id, name, config) VALUES (1, 'Slack', '{"secret":"should-never-be-read"}')`)
	r.NoError(err)

	_, err = db.ExecContext(ctx, `INSERT INTO monitor_notification (monitor_id, notification_id) VALUES (1, 1)`)
	r.NoError(err)
	r.NoError(db.Close())

	out, err := Read(ctx, path)
	r.NoError(err)
	r.NotContains(string(out), "should-never-be-read", "the notification table's contents must never be read")

	var decoded backup
	r.NoError(json.Unmarshal(out, &decoded))
	r.Len(decoded.MonitorList, 2)

	byName := make(map[string]monitor)
	for _, m := range decoded.MonitorList {
		byName[m.Name] = m
	}

	r.NotEmpty(byName["Has binding"].NotificationIDList, "a monitor with a monitor_notification row must warn")
	r.Empty(byName["No binding"].NotificationIDList, "a monitor with no monitor_notification row must not warn")
}

// TestRead_WALOnlyData proves mode=ro (not immutable=1) was used: a monitor
// that exists only in kuma.db-wal — never checkpointed into the main file —
// must still be visible. The writer connection is kept open for the whole
// test (never checkpoints, never closes before the assertions) so every
// write genuinely lives only in the WAL, mirroring Kuma running live while
// `sp` reads the file.
func TestRead_WALOnlyData(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	r.NotEmpty(sqlitedriver.Name)

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wal.db")

	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(0)"

	writer, err := sql.Open(sqlitedriver.Name, dsn)
	r.NoError(err)

	defer writer.Close() //nolint:errcheck // kept open deliberately; closed at test end

	_, err = writer.ExecContext(ctx, minimalMonitorSchema)
	r.NoError(err)

	_, err = writer.ExecContext(ctx,
		`INSERT INTO monitor (id, name, type, url) VALUES (1, 'WAL-only monitor', 'http', 'https://acme.com')`)
	r.NoError(err)

	_, err = os.Stat(path + "-wal")
	r.NoError(err, "the writer should have created a -wal file — this test is meaningless without one")

	out, err := Read(ctx, path)
	r.NoError(err)

	var decoded backup
	r.NoError(json.Unmarshal(out, &decoded))
	r.Len(decoded.MonitorList, 1)
	r.Equal("WAL-only monitor", decoded.MonitorList[0].Name)
}
