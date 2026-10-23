package checks_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

type depsFixture struct {
	svc *checks.Service
	db  db.Service
	org string
}

func newDepsFixture(t *testing.T, orgSlug string) *depsFixture {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	r.NoError(dbSvc.CreateOrganization(ctx, models.NewOrganization(orgSlug, orgSlug)))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)

	return &depsFixture{
		svc: checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc),
		db:  dbSvc,
		org: orgSlug,
	}
}

func (f *depsFixture) upsert(t *testing.T, slug string, deps *[]checks.ExportedDependency) error {
	t.Helper()

	_, _, err := f.svc.UpsertCheck(t.Context(), f.org, slug, &checks.UpsertCheckRequest{
		Name:      slug,
		Type:      "http",
		Config:    map[string]any{"url": "https://" + slug + ".acme.com"},
		DependsOn: deps,
	})

	return err
}

func (f *depsFixture) uidOf(t *testing.T, slug string) string {
	t.Helper()

	org, err := f.db.GetOrganizationBySlug(t.Context(), f.org)
	require.NoError(t, err)

	chk, err := f.db.GetCheckByUidOrSlug(t.Context(), org.UID, slug)
	require.NoError(t, err)

	return chk.UID
}

func TestUpsertCheckDependsOnSync(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDepsFixture(t, "deps-sync")

	r.NoError(f.upsert(t, "dbs", nil))
	r.NoError(f.upsert(t, "cache", nil))

	desc := "needs the db"
	r.NoError(f.upsert(t, "api", &[]checks.ExportedDependency{
		{ParentSlug: "dbs", Kind: "hard", Description: desc},
		{ParentSlug: "cache", Kind: "soft"},
	}))

	edges, err := f.db.ListCheckDependencyParents(t.Context(), f.uidOf(t, "api"))
	r.NoError(err)
	r.Len(edges, 2)

	// Change one kind, clear the description of the other, drop nothing.
	r.NoError(f.upsert(t, "api", &[]checks.ExportedDependency{
		{ParentSlug: "dbs", Kind: "soft"},
		{ParentSlug: "cache", Kind: "soft"},
	}))

	byParent := map[string]*models.CheckDependency{}
	edges, err = f.db.ListCheckDependencyParents(t.Context(), f.uidOf(t, "api"))
	r.NoError(err)

	for _, e := range edges {
		byParent[e.ParentCheckUID] = e
	}

	dbEdge := byParent[f.uidOf(t, "dbs")]
	r.NotNil(dbEdge)
	r.Equal(models.CheckDependencyKind("soft"), dbEdge.Kind)
	r.Nil(dbEdge.Description)

	// Re-applying the same list is a no-op; nil leaves edges alone.
	r.NoError(f.upsert(t, "api", &[]checks.ExportedDependency{
		{ParentSlug: "dbs", Kind: "soft"},
		{ParentSlug: "cache", Kind: "soft"},
	}))
	r.NoError(f.upsert(t, "api", nil))

	edges, err = f.db.ListCheckDependencyParents(t.Context(), f.uidOf(t, "api"))
	r.NoError(err)
	r.Len(edges, 2)

	// An empty list removes every edge.
	r.NoError(f.upsert(t, "api", &[]checks.ExportedDependency{}))

	edges, err = f.db.ListCheckDependencyParents(t.Context(), f.uidOf(t, "api"))
	r.NoError(err)
	r.Empty(edges)
}

func TestUpsertCheckDependsOnRejections(t *testing.T) {
	t.Parallel()

	f := newDepsFixture(t, "deps-reject")
	require.NoError(t, f.upsert(t, "aaa", nil))
	require.NoError(t, f.upsert(t, "bbb", &[]checks.ExportedDependency{{ParentSlug: "aaa", Kind: "hard"}}))

	tests := []struct {
		name  string
		slug  string
		deps  []checks.ExportedDependency
		match string
	}{
		{"empty parent", "cc1", []checks.ExportedDependency{{Kind: "hard"}}, "empty parentSlug"},
		{"unknown parent", "cc2", []checks.ExportedDependency{{ParentSlug: "nope", Kind: "hard"}}, "unknown parent slug"},
		{"self edge", "aaa", []checks.ExportedDependency{{ParentSlug: "aaa", Kind: "hard"}}, "self-edge"},
		{"invalid kind", "cc3", []checks.ExportedDependency{{ParentSlug: "aaa", Kind: "weird"}}, "invalid kind"},
		{"duplicate", "cc4", []checks.ExportedDependency{
			{ParentSlug: "aaa", Kind: "hard"}, {ParentSlug: "aaa", Kind: "soft"},
		}, "duplicate parent"},
		{"cycle", "aaa", []checks.ExportedDependency{{ParentSlug: "bbb", Kind: "hard"}}, "cycle detected"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := f.upsert(t, tt.slug, &tt.deps)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.match)
		})
	}
}

func TestValidateCheckDependsOn(t *testing.T) {
	t.Parallel()

	f := newDepsFixture(t, "deps-validate")
	require.NoError(t, f.upsert(t, "aaa", nil))
	require.NoError(t, f.upsert(t, "bbb", &[]checks.ExportedDependency{{ParentSlug: "aaa", Kind: "hard"}}))

	validate := func(orgSlug, slug string, deps []checks.ExportedDependency) []base.ValidationErrorField {
		resp, err := f.svc.ValidateCheck(t.Context(), orgSlug, &checks.ValidateCheckRequest{
			Type:      "http",
			Slug:      slug,
			Config:    map[string]any{"url": "https://x.acme.com"},
			DependsOn: deps,
		})
		require.NoError(t, err)

		return resp.Fields
	}

	hasMsg := func(findings []base.ValidationErrorField, msg string) bool {
		for _, finding := range findings {
			if strings.HasPrefix(finding.Name, "dependsOn") && strings.Contains(finding.Message, msg) {
				return true
			}
		}

		return false
	}

	tests := []struct {
		name string
		slug string
		deps []checks.ExportedDependency
		msg  string
	}{
		{"missing parent slug", "new", []checks.ExportedDependency{{Kind: "hard"}}, "parentSlug is required"},
		{"unknown parent", "new", []checks.ExportedDependency{{ParentSlug: "zzz", Kind: "hard"}}, "unknown parent slug"},
		{"self edge on existing", "aaa", []checks.ExportedDependency{{ParentSlug: "aaa", Kind: "hard"}}, "self-edge"},
		{"bad kind", "new", []checks.ExportedDependency{{ParentSlug: "aaa", Kind: "x"}}, "invalid kind"},
		{"duplicate", "new", []checks.ExportedDependency{
			{ParentSlug: "aaa", Kind: "hard"}, {ParentSlug: "aaa", Kind: "hard", Description: "again"},
		}, "duplicate parent"},
		{"cycle on existing", "aaa", []checks.ExportedDependency{{ParentSlug: "bbb", Kind: "hard"}}, "cycle"},
	}

	for _, tt := range tests {
		findings := validate("deps-validate", tt.slug, tt.deps)
		require.True(t, hasMsg(findings, tt.msg), "%s: %+v", tt.name, findings)
	}

	// A valid create-flow and a valid update-flow produce no dependsOn finding.
	require.False(t, hasMsg(validate("deps-validate", "fresh", []checks.ExportedDependency{
		{ParentSlug: "aaa", Kind: "hard", Description: "ok"},
	}), ""))
	require.False(t, hasMsg(validate("deps-validate", "bbb", []checks.ExportedDependency{
		{ParentSlug: "aaa", Kind: "soft"},
	}), ""))

	// Unknown org is reported as a field error, not a server error.
	require.True(t, hasMsg(validate("no-such-org", "x", []checks.ExportedDependency{
		{ParentSlug: "aaa", Kind: "hard"},
	}), "organization not found"))
}

func TestListChecksPagination(t *testing.T) {
	t.Parallel()

	f := newDepsFixture(t, "deps-list")

	for _, slug := range []string{"chk1", "chk2", "chk3"} {
		require.NoError(t, f.upsert(t, slug, nil))
	}

	for _, sortMode := range []string{"", "group", "targetHost"} {
		seen := map[string]bool{}
		cursor := ""

		for range 5 {
			resp, err := f.svc.ListChecks(t.Context(), f.org, checks.ListChecksOptions{
				Limit: 1, Cursor: cursor, Sort: sortMode,
			})
			require.NoError(t, err, sortMode)

			for i := range resp.Data {
				seen[resp.Data[i].UID] = true
			}

			if resp.Pagination.Cursor == "" {
				break
			}

			cursor = resp.Pagination.Cursor
		}

		require.Len(t, seen, 3, "sort %q must page through every check", sortMode)

		_, err := f.svc.ListChecks(t.Context(), f.org, checks.ListChecksOptions{
			Limit: 1, Cursor: "!!not-a-cursor!!", Sort: sortMode,
		})
		require.ErrorIs(t, err, checks.ErrInvalidCursor, sortMode)
	}
}
