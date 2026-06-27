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

// TestMigrationDiscoveredChecks verifies migration 003 replaces discovered_hosts
// with discovered_checks: the host table is gone, the check table and its
// identity index exist.
func TestMigrationDiscoveredChecks(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })

	r.NoError(svc.Initialize(ctx), "Initialize must succeed")

	tableExists := func(name string) bool {
		var count int
		r.NoError(svc.db.NewRaw(
			"SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", name,
		).Scan(ctx, &count))

		return count == 1
	}

	r.False(tableExists("discovered_hosts"), "discovered_hosts must be dropped by migration 003")
	r.True(tableExists("discovered_checks"), "discovered_checks must exist after migration 003")

	// The required columns exist.
	type columnInfo struct {
		Name string `bun:"name"`
	}
	var columns []columnInfo
	r.NoError(svc.db.NewRaw("SELECT name FROM pragma_table_info('discovered_checks')").Scan(ctx, &columns))

	colNames := make([]string, 0, len(columns))
	for _, c := range columns {
		colNames = append(colNames, c.Name)
	}
	for _, expected := range []string{
		"uid", "organization_uid", "job_uid", "source", "group_key", "group_label",
		"name", "slug", "type", "config", "metadata", "promoted_to_check_uid",
		"discovered_at", "created_at", "updated_at", "deleted_at",
	} {
		assert.Contains(t, colNames, expected, "%s column must exist", expected)
	}

	// The identity unique index must exist.
	type indexInfo struct {
		Name string `bun:"name"`
	}
	var indexes []indexInfo
	r.NoError(svc.db.NewRaw(
		"SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='discovered_checks'",
	).Scan(ctx, &indexes))

	idxNames := make([]string, 0, len(indexes))
	for _, i := range indexes {
		idxNames = append(idxNames, i.Name)
	}
	assert.Contains(t, idxNames, "idx_discovered_checks_identity_active",
		"per-group identity unique index must exist")
}

// TestMigrationDiscoveredChecksDefaultConfig verifies a row inserted without an
// explicit config defaults to an empty JSON object (column DEFAULT).
func TestMigrationDiscoveredChecksDefaultConfig(t *testing.T) {
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
		"INSERT INTO discovered_checks "+
			"(uid, organization_uid, job_uid, source, group_key, group_label, name, slug, type) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		uuid.New().String(), orgUID, jobUID, "lan", "192.168.1.99", "192.168.1.99",
		"192.168.1.99 · ICMP", "192-168-1-99-icmp", "icmp",
	).Exec(ctx)
	r.NoError(err)

	var cfg string
	r.NoError(svc.db.NewRaw(
		"SELECT config FROM discovered_checks WHERE group_key = ?", "192.168.1.99",
	).Scan(ctx, &cfg))
	r.Equal("{}", cfg, "rows inserted without a config must default to an empty object")
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
