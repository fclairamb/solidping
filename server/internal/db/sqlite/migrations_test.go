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

	// Verify the v0.1.0 consolidated baseline migration is embedded
	assert.Contains(t, files, "migrations/001_v0_1_0.up.sql",
		"v0.1.0 baseline up migration must be embedded")
	assert.Contains(t, files, "migrations/001_v0_1_0.down.sql",
		"v0.1.0 baseline down migration must be embedded")
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

// TestMigrationResultDurationAvg verifies migration 031 adds the duration_avg
// column to the results table.
func TestMigrationResultDurationAvg(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })

	r.NoError(svc.Initialize(ctx))

	type columnInfo struct {
		Name string `bun:"name"`
	}
	var columns []columnInfo
	err = svc.db.NewRaw("SELECT name FROM pragma_table_info('results')").Scan(ctx, &columns)
	r.NoError(err)

	colNames := make([]string, 0, len(columns))
	for _, c := range columns {
		colNames = append(colNames, c.Name)
	}

	assert.Contains(t, colNames, "duration_avg", "duration_avg column must exist after migration 031")
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

// TestMigrationDiscoveredHostsSourceDefault verifies that a row inserted without
// an explicit source defaults to 'lan' (column DEFAULT in the consolidated baseline).
func TestMigrationDiscoveredHostsSourceDefault(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })

	r.NoError(svc.Initialize(ctx))

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
}

// TestMigrationIntegrationsSchemaFinalState verifies that after the consolidated
// v0.1.0 migration the integration-related tables use their final names
// (integrations / check_channels / integration_uid) and the old names are absent.
func TestMigrationIntegrationsSchemaFinalState(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })

	r.NoError(svc.Initialize(ctx))

	tableExists := func(name string) bool {
		var count int
		r.NoError(svc.db.NewRaw(
			"SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", name,
		).Scan(ctx, &count))

		return count == 1
	}

	colExists := func(table, col string) bool {
		type c struct {
			Name string `bun:"name"`
		}
		var cols []c
		r.NoError(svc.db.NewRaw("SELECT name FROM pragma_table_info(?)", table).Scan(ctx, &cols))
		for _, x := range cols {
			if x.Name == col {
				return true
			}
		}

		return false
	}

	// The consolidated baseline uses the final names from day one.
	r.True(tableExists("integrations"), "integrations table should exist after migrations")
	r.True(tableExists("check_channels"), "check_channels table should exist after migrations")
	r.False(tableExists("integration_connections"), "old integration_connections table should not exist")
	r.False(tableExists("check_connections"), "old check_connections table should not exist")
	r.True(colExists("check_channels", "integration_uid"), "check_channels.integration_uid should exist")
	r.False(colExists("check_channels", "connection_uid"), "old connection_uid column should not exist")

	// Verify rows can be inserted using the final names.
	orgUID := uuid.New().String()
	intUID := uuid.New().String()
	_, err = svc.db.NewRaw(
		"INSERT INTO organizations (uid, slug, name) VALUES (?, ?, ?)", orgUID, "ri-org", "RI Org",
	).Exec(ctx)
	r.NoError(err)
	_, err = svc.db.NewRaw(
		"INSERT INTO integrations (uid, organization_uid, type, name) VALUES (?, ?, ?, ?)",
		intUID, orgUID, "webhook", "Hook",
	).Exec(ctx)
	r.NoError(err)

	var name string
	r.NoError(svc.db.NewRaw(
		"SELECT name FROM integrations WHERE uid = ?", intUID,
	).Scan(ctx, &name))
	r.Equal("Hook", name, "row must be readable from integrations table")
}
