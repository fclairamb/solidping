package sqlite

import (
	"io/fs"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigrationsEmbedded verifies that all expected migration files are embedded.
func TestMigrationsEmbedded(t *testing.T) {
	t.Parallel()

	var files []string
	err := fs.WalkDir(migrationsFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, path)
			// Log file content size for debugging
			data, readErr := fs.ReadFile(migrationsFS, path)
			if readErr != nil {
				t.Logf("  %s: error reading: %v", path, readErr)
			} else {
				t.Logf("  %s: %d bytes", path, len(data))
			}
		}
		return nil
	})
	require.NoError(t, err)
	t.Logf("Found %d embedded migration files", len(files))

	// Verify the initial migration is embedded
	assert.Contains(t, files, "migrations/001_initial.up.sql",
		"initial up migration must be embedded")
	assert.Contains(t, files, "migrations/001_initial.down.sql",
		"initial down migration must be embedded")
}

// TestMigrationCreatesIncidentColumns verifies that after running migrations,
// the incidents table has the relapse_count and last_reopened_at columns.
func TestMigrationCreatesIncidentColumns(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	svc, err := New(ctx, Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	err = svc.Initialize(ctx)
	require.NoError(t, err, "Initialize must succeed")

	// Query the incidents table schema
	type columnInfo struct {
		Name string `bun:"name"`
	}
	var columns []columnInfo
	err = svc.db.NewRaw("SELECT name FROM pragma_table_info('incidents')").Scan(ctx, &columns)
	require.NoError(t, err)

	colNames := make([]string, 0, len(columns))
	for _, c := range columns {
		colNames = append(colNames, c.Name)
	}
	t.Logf("Incidents table columns: %v", colNames)

	assert.Contains(t, colNames, "relapse_count", "relapse_count column must exist after migration")
	assert.Contains(t, colNames, "last_reopened_at", "last_reopened_at column must exist after migration")
	assert.Contains(t, colNames, "caused_by_incident_uid",
		"caused_by_incident_uid column must exist after migration")
	assert.Contains(t, colNames, "paging_suppressed",
		"paging_suppressed column must exist after migration")
}

// TestMigrationCreatesCheckDependencies verifies the check_dependencies table.
func TestMigrationCreatesCheckDependencies(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	svc, err := New(ctx, Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	err = svc.Initialize(ctx)
	require.NoError(t, err, "Initialize must succeed")

	type tableInfo struct {
		Name string `bun:"name"`
	}
	var tables []tableInfo
	err = svc.db.NewRaw(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='check_dependencies'",
	).Scan(ctx, &tables)
	require.NoError(t, err)
	require.Len(t, tables, 1, "check_dependencies table must exist")

	type columnInfo struct {
		Name string `bun:"name"`
	}
	var columns []columnInfo
	err = svc.db.NewRaw("SELECT name FROM pragma_table_info('check_dependencies')").Scan(ctx, &columns)
	require.NoError(t, err)

	colNames := make([]string, 0, len(columns))
	for _, c := range columns {
		colNames = append(colNames, c.Name)
	}

	for _, expected := range []string{"uid", "organization_uid", "parent_check_uid", "child_check_uid", "kind"} {
		assert.Contains(t, colNames, expected, "%s column must exist", expected)
	}
}

// TestMigrationDiscoveredHostsSource verifies migration 030 adds the source
// column and backfills existing rows to 'lan'.
func TestMigrationDiscoveredHostsSource(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	svc, err := New(ctx, Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	err = svc.Initialize(ctx)
	require.NoError(t, err, "Initialize must succeed")

	// The source column must exist after migration 030.
	type columnInfo struct {
		Name    string `bun:"name"`
		Notnull int    `bun:"notnull"`
	}
	var columns []columnInfo
	err = svc.db.NewRaw(`SELECT name, "notnull" FROM pragma_table_info('discovered_hosts')`).Scan(ctx, &columns)
	require.NoError(t, err)

	var sourceCol *columnInfo
	for i := range columns {
		if columns[i].Name == "source" {
			sourceCol = &columns[i]
		}
	}
	require.NotNil(t, sourceCol, "source column must exist after migration 030")
	require.Equal(t, 1, sourceCol.Notnull, "source column must be NOT NULL")

	// The per-source unique index must exist; the old per-ip one must be gone.
	type indexInfo struct {
		Name string `bun:"name"`
	}
	var indexes []indexInfo
	err = svc.db.NewRaw(
		"SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='discovered_hosts'",
	).Scan(ctx, &indexes)
	require.NoError(t, err)

	idxNames := make([]string, 0, len(indexes))
	for _, i := range indexes {
		idxNames = append(idxNames, i.Name)
	}
	assert.Contains(t, idxNames, "idx_discovered_hosts_org_ip_source_active",
		"per-source unique index must exist")
	assert.NotContains(t, idxNames, "idx_discovered_hosts_org_ip_active",
		"old per-ip unique index must be dropped")
}

// TestMigrationDiscoveredHostsSourceRoundTrip verifies that (a) a row inserted
// without an explicit source is backfilled to 'lan' by the column DEFAULT, and
// (b) the 030 down migration rolls back cleanly and 030 can be re-applied.
func TestMigrationDiscoveredHostsSourceRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })

	r.NoError(svc.Initialize(ctx))

	// Seed an org + job + a discovered_hosts row WITHOUT specifying source, so
	// only the DEFAULT 'lan' applies — the backfill path for pre-existing rows.
	orgUID := uuid.New().String()
	jobUID := uuid.New().String()
	_, err = svc.db.NewRaw(
		"INSERT INTO organizations (uid, slug, name) VALUES (?, ?, ?)",
		orgUID, "rt-org", "RT Org",
	).Exec(ctx)
	r.NoError(err)
	_, err = svc.db.NewRaw(
		"INSERT INTO jobs (uid, organization_uid, type, status) VALUES (?, ?, ?, ?)",
		jobUID, orgUID, "network_discovery", "success",
	).Exec(ctx)
	r.NoError(err)
	_, err = svc.db.NewRaw(
		"INSERT INTO discovered_hosts (uid, organization_uid, job_uid, ip) VALUES (?, ?, ?, ?)",
		uuid.New().String(), orgUID, jobUID, "192.168.1.99",
	).Exec(ctx)
	r.NoError(err)

	var src string
	r.NoError(svc.db.NewRaw("SELECT source FROM discovered_hosts WHERE ip = ?", "192.168.1.99").Scan(ctx, &src))
	r.Equal("lan", src, "rows inserted without a source must default to 'lan'")

	hasSource := func() bool {
		type col struct {
			Name string `bun:"name"`
		}
		var cols []col
		r.NoError(svc.db.NewRaw("SELECT name FROM pragma_table_info('discovered_hosts')").Scan(ctx, &cols))
		for _, c := range cols {
			if c.Name == "source" {
				return true
			}
		}

		return false
	}

	// Verify the 030 down + up SQL by executing them directly against the live
	// schema: down must drop the column cleanly, up must re-add it.
	downSQL, err := fs.ReadFile(migrationsFS, "migrations/030_discovered_hosts_source.down.sql")
	r.NoError(err)
	upSQL, err := fs.ReadFile(migrationsFS, "migrations/030_discovered_hosts_source.up.sql")
	r.NoError(err)

	_, err = svc.db.ExecContext(ctx, string(downSQL))
	r.NoError(err, "030 down SQL must execute cleanly")
	r.False(hasSource(), "source column must be gone after down migration")

	_, err = svc.db.ExecContext(ctx, string(upSQL))
	r.NoError(err, "030 up SQL must re-apply cleanly")
	r.True(hasSource(), "source column must be back after re-applying up migration")
}
