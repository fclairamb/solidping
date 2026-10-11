package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// schemaAndMigrations returns everything Initialize is responsible for: every
// schema object with its SQL, and the applied migrations with their checksums.
func schemaAndMigrations(t *testing.T, svc *Service) ([]string, []string) {
	t.Helper()

	ctx := context.Background()

	var schema []string
	require.NoError(t, svc.db.NewRaw(
		"SELECT type || ' ' || name || ' ' || coalesce(sql, '') FROM sqlite_master ORDER BY type, name",
	).Scan(ctx, &schema))

	var migrations []string
	require.NoError(t, svc.db.NewRaw("SELECT name FROM bun_migrations ORDER BY name").Scan(ctx, &migrations))

	return schema, migrations
}

func newInMemory(t *testing.T) *Service {
	t.Helper()

	svc, err := New(context.Background(), Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	return svc
}

// A database restored from the template must be indistinguishable from one
// that ran every migration. Not parallel: it reads the process-wide restore
// counter.
//
//nolint:paralleltest // asserts on the process-wide memTemplateRestores counter
func TestInMemoryInitializeFromTemplateMatchesMigrations(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	first := newInMemory(t)
	r.NoError(first.Initialize(ctx))

	before := memTemplateRestores.Load()

	second := newInMemory(t)
	r.NoError(second.Initialize(ctx))

	r.Equal(before+1, memTemplateRestores.Load(), "the second in-memory database should come from the template")

	firstSchema, firstMigrations := schemaAndMigrations(t, first)
	secondSchema, secondMigrations := schemaAndMigrations(t, second)

	r.NotEmpty(firstSchema)
	r.NotEmpty(firstMigrations)
	r.Equal(firstSchema, secondSchema)
	r.Equal(firstMigrations, secondMigrations)

	// The restored database is a private copy: writing to it leaves the
	// template, and the next database, untouched.
	_, err := second.db.ExecContext(ctx, "CREATE TABLE only_in_second (id INTEGER)")
	r.NoError(err)

	third := newInMemory(t)
	r.NoError(third.Initialize(ctx))

	thirdSchema, _ := schemaAndMigrations(t, third)
	r.Equal(firstSchema, thirdSchema)
}

// A database that already holds a table is not empty, so it must run the
// real migrations rather than be overwritten by the template.
//
//nolint:paralleltest // asserts on the process-wide memTemplateRestores counter
func TestInMemoryInitializeOnNonEmptyDatabaseMigrates(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	warm := newInMemory(t)
	r.NoError(warm.Initialize(ctx)) // make sure the template exists

	svc := newInMemory(t)
	_, err := svc.db.ExecContext(ctx, "CREATE TABLE pre_existing (id INTEGER)")
	r.NoError(err)

	before := memTemplateRestores.Load()
	r.NoError(svc.Initialize(ctx))
	r.Equal(before, memTemplateRestores.Load(), "a non-empty database must not be restored from the template")

	var kept int
	r.NoError(svc.db.NewRaw(
		"SELECT count(*) FROM sqlite_master WHERE name = 'pre_existing'",
	).Scan(ctx, &kept))
	r.Equal(1, kept, "the pre-existing table must survive Initialize")
}
