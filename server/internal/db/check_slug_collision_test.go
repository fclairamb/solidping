package db_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// portCheckSlugPG is distinct from every other embedded-Postgres port claimed
// in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portCheckSlugPG = 15485

// Static errors for the hermetic edges below: an ordinary failure, and a
// check-slug violation as the clone path wraps it on its way up
// (`fmt.Errorf("create clone: %w", err)`).
var (
	errNotAViolation   = errors.New("connection reset by peer")
	errPGSlugViolation = errors.New(
		`ERROR: duplicate key value violates unique constraint "checks_slug_idx" (SQLSTATE=23505)`)
	errWrappedSlugViolation = fmt.Errorf("create clone: %w", errPGSlugViolation)
)

// TestCheckSlugCollisionClassification_SQLite pins the classifier against
// SQLite's real wording.
func TestCheckSlugCollisionClassification_SQLite(t *testing.T) {
	t.Parallel()

	tempDir, err := os.MkdirTemp("", "sqlite-check-slug-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })

	svc, err := sqlite.New(t.Context(), sqlite.Config{DataDir: tempDir})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	require.NoError(t, svc.Initialize(t.Context()))

	testCheckSlugCollisionClassification(t, svc)
}

// TestCheckSlugCollisionClassification_Postgres is the real-engine twin. The
// two drivers word the same violation completely differently — Postgres names
// the INDEX, SQLite names the COLUMNS — so neither wording proves the other.
func TestCheckSlugCollisionClassification_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	svc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portCheckSlugPG,
		RunMode:  "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	testCheckSlugCollisionClassification(t, svc)
}

// testCheckSlugCollisionClassification is the engine-agnostic contract behind
// db.IsCheckSlugCollision, run against both backends.
//
// The classifier decides whether the check-create loop
// (checks.insertResolvingSlugRace) may re-resolve a slug and try again. It
// matches on DRIVER ERROR TEXT, which is why its own doc comment insists on
// being fed violations produced by a REAL database: a hand-written string would
// keep passing if a driver ever reworded itself, while the create path silently
// stopped retrying — and the only symptom would be an intermittent 500 under
// concurrency, which is exactly the bug this whole spec chased.
func testCheckSlugCollisionClassification(t *testing.T, svc db.Service) {
	t.Helper()

	ctx := t.Context()

	t.Run("SlugIndexViolationIsRetryable", func(t *testing.T) {
		t.Parallel()
		testSlugIndexViolationIsRetryable(ctx, t, svc)
	})

	t.Run("OrganizationSlugViolationIsNot", func(t *testing.T) {
		t.Parallel()
		testOrganizationSlugViolationIsNotACheckSlug(ctx, t, svc)
	})

	t.Run("IncidentNumberViolationIsNot", func(t *testing.T) {
		t.Parallel()
		testIncidentNumberViolationIsNotACheckSlug(ctx, t, svc)
	})
}

// testSlugIndexViolationIsRetryable is the positive control: the violation the
// create path exists to survive must be recognized on this engine.
func testSlugIndexViolationIsRetryable(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)

	org := models.NewOrganization("chk-slug-cls", "chk-slug-cls")
	r.NoError(svc.CreateOrganization(ctx, org))

	const slug = "duplicated-slug"

	first := models.NewCheck(org.UID, slug, "http")
	r.NoError(svc.CreateCheck(ctx, first))

	// Same org, same slug: checks_slug_idx — not any SELECT — is what actually
	// guarantees uniqueness, and this is it firing.
	second := models.NewCheck(org.UID, slug, "http")

	err := svc.CreateCheck(ctx, second)
	r.Error(err, "reusing a slug in one org must violate the unique index")
	r.True(db.IsUniqueViolation(err), "unexpected driver wording: %v", err)
	r.True(db.IsCheckSlugCollision(err),
		"the create loop must recognize this engine's real wording: %v", err)
}

// testOrganizationSlugViolationIsNotACheckSlug is the adversarial negative:
// `organizations_slug_idx` is another unique index whose name contains the word
// "slug", so a classifier that matched loosely on "slug" would misfire on it
// and make the create loop re-resolve a check slug that was never the problem.
func testOrganizationSlugViolationIsNotACheckSlug(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)

	const orgSlug = "chk-slug-org-dup"

	first := models.NewOrganization(orgSlug, orgSlug)
	r.NoError(svc.CreateOrganization(ctx, first))

	second := models.NewOrganization(orgSlug, orgSlug)

	err := svc.CreateOrganization(ctx, second)
	r.Error(err, "reusing an organization slug must violate its unique index")
	r.True(db.IsUniqueViolation(err), "unexpected driver wording: %v", err)
	r.False(db.IsCheckSlugCollision(err),
		"an organization-slug violation is not a check-slug collision: %v", err)
}

// testIncidentNumberViolationIsNotACheckSlug is the realistic negative: a
// unique violation the create path could plausibly meet and that re-resolving a
// slug can never fix. Retrying it would burn the whole stall budget before
// surfacing an error the caller could have had immediately.
func testIncidentNumberViolationIsNotACheckSlug(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)

	org := models.NewOrganization("chk-slug-inc", "chk-slug-inc")
	r.NoError(svc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "chk-slug-inc-check", "http")
	r.NoError(svc.CreateCheck(ctx, check))

	first := models.NewIncident(org.UID, check.UID, time.Now(), "first")
	r.NoError(svc.CreateIncident(ctx, first))

	// An EXPLICIT number skips the assignment loop, so this insert hits the
	// unique index head-on instead of being retried into a free slot.
	clash := models.NewIncident(org.UID, check.UID, time.Now(), "clash")
	clash.Number = first.Number

	err := svc.CreateIncident(ctx, clash)
	r.Error(err)
	r.True(db.IsUniqueViolation(err), "unexpected driver wording: %v", err)
	r.False(db.IsCheckSlugCollision(err),
		"an incident-number violation is not a check-slug collision: %v", err)
}

// TestIsCheckSlugCollision_Edges covers what a real database cannot produce:
// the nil and non-violation inputs the classifier is called with on every
// successful insert.
func TestIsCheckSlugCollision_Edges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "ordinary failure", err: errNotAViolation, want: false},
		{
			// A check-slug violation the caller wrapped on its way up — the
			// clone path does exactly this, so the classifier must survive
			// wrapping (it reads Error(), which carries the whole chain).
			name: "wrapped postgres slug violation",
			err:  errWrappedSlugViolation,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, db.IsCheckSlugCollision(tt.err))
		})
	}
}
