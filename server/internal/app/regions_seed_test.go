package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// newRegionsSeedServer builds a minimal Server around an in-memory SQLite DB —
// SeedRegionsFromEnv only touches dbService.
func newRegionsSeedServer(ctx context.Context, t *testing.T) (*Server, *sqlite.Service) {
	t.Helper()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	return &Server{dbService: dbSvc}, dbSvc
}

// TestSeedRegionsFromEnv covers the SP_REGIONS boot seeding: valid lists are
// persisted (and re-seeds overwrite), invalid values are logged and skipped
// without failing the boot, and an unset env leaves the DB alone. Not
// parallel: the subtests mutate the process env via t.Setenv.
func TestSeedRegionsFromEnv(t *testing.T) {
	ctx := context.Background()

	t.Run("unset env is a no-op", func(t *testing.T) {
		r := require.New(t)
		srv, dbSvc := newRegionsSeedServer(ctx, t)

		t.Setenv(envRegions, "")
		r.NoError(srv.SeedRegionsFromEnv(ctx))

		defs, err := regions.NewService(dbSvc).GetGlobalRegions(ctx)
		r.NoError(err)
		r.Equal(regions.DefaultRegions(), defs, "no parameter written, fallback stays")
	})

	t.Run("valid list is seeded and readable", func(t *testing.T) {
		r := require.New(t)
		srv, dbSvc := newRegionsSeedServer(ctx, t)

		t.Setenv(envRegions, `[
			{"slug": "default", "emoji": "🇪🇺", "name": "EU1 (default)"},
			{"slug": "us-1", "emoji": "🇺🇸", "name": "US1"},
			{"slug": "eu-2", "emoji": "🇪🇺", "name": "EU2"}
		]`)
		r.NoError(srv.SeedRegionsFromEnv(ctx))

		defs, err := regions.NewService(dbSvc).GetGlobalRegions(ctx)
		r.NoError(err)
		r.Equal([]regions.RegionDefinition{
			{Slug: "default", Emoji: "🇪🇺", Name: "EU1 (default)"},
			{Slug: "us-1", Emoji: "🇺🇸", Name: "US1"},
			{Slug: "eu-2", Emoji: "🇪🇺", Name: "EU2"},
		}, defs)
	})

	t.Run("re-seed overwrites an existing parameter", func(t *testing.T) {
		r := require.New(t)
		srv, dbSvc := newRegionsSeedServer(ctx, t)

		// Simulate the real-deployment state: a parameter whose name equals the slug.
		r.NoError(dbSvc.SetSystemParameter(ctx, regions.ParamRegions,
			[]regions.RegionDefinition{{Slug: "default", Emoji: "📍", Name: "default"}}, false))

		t.Setenv(envRegions, `[{"slug": "default", "emoji": "🇪🇺", "name": "EU1 (default)"}]`)
		r.NoError(srv.SeedRegionsFromEnv(ctx))

		defs, err := regions.NewService(dbSvc).GetGlobalRegions(ctx)
		r.NoError(err)
		r.Equal([]regions.RegionDefinition{
			{Slug: "default", Emoji: "🇪🇺", Name: "EU1 (default)"},
		}, defs, "env seed replaces the stale DB value")
	})

	t.Run("empty name falls back to the slug", func(t *testing.T) {
		r := require.New(t)
		srv, dbSvc := newRegionsSeedServer(ctx, t)

		t.Setenv(envRegions, `[{"slug": "us-1", "emoji": "🇺🇸"}]`)
		r.NoError(srv.SeedRegionsFromEnv(ctx))

		defs, err := regions.NewService(dbSvc).GetGlobalRegions(ctx)
		r.NoError(err)
		r.Equal([]regions.RegionDefinition{{Slug: "us-1", Emoji: "🇺🇸", Name: "us-1"}}, defs)
	})

	// Invalid values must not fail the boot AND must not touch the parameter:
	// an existing (good) DB value survives a bad env.
	invalid := map[string]string{
		"malformed JSON":  `{not json`,
		"empty list":      `[]`,
		"entry w/o slug":  `[{"emoji": "🇺🇸", "name": "US1"}]`,
		"duplicate slugs": `[{"slug": "us-1", "name": "A"}, {"slug": "us-1", "name": "B"}]`,
	}
	for name, value := range invalid {
		t.Run("invalid value is skipped: "+name, func(t *testing.T) {
			r := require.New(t)
			srv, dbSvc := newRegionsSeedServer(ctx, t)

			prior := []regions.RegionDefinition{{Slug: "default", Emoji: "📍", Name: "Prior"}}
			r.NoError(dbSvc.SetSystemParameter(ctx, regions.ParamRegions, prior, false))

			t.Setenv(envRegions, value)
			r.NoError(srv.SeedRegionsFromEnv(ctx), "invalid SP_REGIONS must not fail the boot")

			defs, err := regions.NewService(dbSvc).GetGlobalRegions(ctx)
			r.NoError(err)
			r.Equal(prior, defs, "invalid SP_REGIONS must not overwrite the stored regions")
		})
	}
}
