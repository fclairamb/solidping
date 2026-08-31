package checkdependencies

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// portMarginLintPG is distinct from every other embedded-Postgres port claimed
// in the repo.
const portMarginLintPG = 15506

// The confirmation-margin lint (spec 2026-08-31-06).
//
// The strict margin is `parent.confirmation + parent.period +
// parent.timeoutOrDefault`. With the shipped defaults used below —
// confirmation 120s, period 60s, no per-check timeout so the 15s server
// default applies — that is exactly 195s, and the boundary is what these
// cases pin: 194 warns, 195 does not.
const (
	lintParentConfirmation = 120
	lintParentPeriod       = 60 * time.Second
	lintDefaultTimeout     = 15 * time.Second
	lintMarginSeconds      = 195
)

func lintCheck(uid, slug string, confirmationSeconds int) *models.Check {
	name := slug
	slugCopy := slug

	return &models.Check{
		UID:                       uid,
		OrganizationUID:           "org",
		Slug:                      &slugCopy,
		Name:                      &name,
		Config:                    models.JSONMap{},
		Period:                    timeutils.Duration(lintParentPeriod),
		ConfirmationPeriodSeconds: confirmationSeconds,
	}
}

// TestConfirmationMarginWarnings covers the lint's decision table directly:
// the boundary in both directions, the default-timeout substitution (and its
// absence when the parent sets one), and the kinds that are never linted.
func TestConfirmationMarginWarnings(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// childConfirmation is the child's configured confirmation, seconds.
		childConfirmation int
		kind              models.CheckDependencyKind
		// parentTimeout, when non-empty, is written to the parent's config as
		// an explicit `timeout`.
		parentTimeout string
		// defaultTimeout is what the server resolved for
		// scheduling.check_timeout_ms.
		defaultTimeout time.Duration
		wantWarning    bool
		wantRecommend  int
	}{
		{
			name:              "one second under the margin warns",
			childConfirmation: lintMarginSeconds - 1,
			kind:              models.CheckDependencyKindHard,
			defaultTimeout:    lintDefaultTimeout,
			wantWarning:       true,
			wantRecommend:     lintMarginSeconds,
		},
		{
			name:              "exactly at the margin is clean",
			childConfirmation: lintMarginSeconds,
			kind:              models.CheckDependencyKindHard,
			defaultTimeout:    lintDefaultTimeout,
			wantWarning:       false,
		},
		{
			name:              "well over the margin is clean",
			childConfirmation: 600,
			kind:              models.CheckDependencyKindHard,
			defaultTimeout:    lintDefaultTimeout,
			wantWarning:       false,
		},
		{
			// The outage's own configuration: identical parent and child.
			name:              "identical parent and child configuration warns",
			childConfirmation: lintParentConfirmation,
			kind:              models.CheckDependencyKindHard,
			defaultTimeout:    lintDefaultTimeout,
			wantWarning:       true,
			wantRecommend:     lintMarginSeconds,
		},
		{
			// Proves the default is actually consulted rather than hard-coded:
			// the SAME 190s child is clean under a 5s default (margin 185) and
			// would warn under the 15s one (margin 195).
			name:              "lower configured default timeout shrinks the margin",
			childConfirmation: 190,
			kind:              models.CheckDependencyKindHard,
			defaultTimeout:    5 * time.Second,
			wantWarning:       false,
		},
		{
			name:              "same child warns under the shipped default timeout",
			childConfirmation: 190,
			kind:              models.CheckDependencyKindHard,
			defaultTimeout:    lintDefaultTimeout,
			wantWarning:       true,
			wantRecommend:     lintMarginSeconds,
		},
		{
			// An explicit per-check timeout wins over the server default.
			name:              "explicit parent timeout replaces the default",
			childConfirmation: lintMarginSeconds,
			kind:              models.CheckDependencyKindHard,
			parentTimeout:     "30s",
			defaultTimeout:    lintDefaultTimeout,
			wantWarning:       true,
			wantRecommend:     lintParentConfirmation + 60 + 30,
		},
		{
			// A garbage value falls back to the default rather than to zero.
			name:              "unparseable parent timeout falls back to the default",
			childConfirmation: lintMarginSeconds - 1,
			kind:              models.CheckDependencyKindHard,
			parentTimeout:     "not-a-duration",
			defaultTimeout:    lintDefaultTimeout,
			wantWarning:       true,
			wantRecommend:     lintMarginSeconds,
		},
		{
			name:              "soft edges are never linted",
			childConfirmation: 0,
			kind:              models.CheckDependencyKindSoft,
			defaultTimeout:    lintDefaultTimeout,
			wantWarning:       false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			parent := lintCheck("p1", "rabbitmq", lintParentConfirmation)
			if tc.parentTimeout != "" {
				parent.Config[models.CheckConfigKeyTimeout] = tc.parentTimeout
			}

			child := lintCheck("c1", "consumer", tc.childConfirmation)

			svc := &Service{defaultCheckTimeout: tc.defaultTimeout}
			edges := []*models.CheckDependency{{
				UID:            "edge-1",
				ParentCheckUID: parent.UID,
				ChildCheckUID:  child.UID,
				Kind:           tc.kind,
			}}

			got := svc.confirmationMarginWarnings(child, edges,
				map[string]*models.Check{parent.UID: parent, child.UID: child})

			r.NotNil(got, "warnings must be an empty slice, never nil")

			if !tc.wantWarning {
				r.Empty(got)

				return
			}

			r.Len(got, 1)
			r.Equal(WarningCodeConfirmationMarginTooShort, got[0].Code)
			r.Equal("edge-1", got[0].DependencyUID)
			r.Equal("rabbitmq", got[0].ParentCheck.Slug)
			r.Equal(tc.childConfirmation, got[0].ChildConfirmationSeconds)
			r.Equal(tc.wantRecommend, got[0].RecommendedConfirmationSeconds)
			r.NotEmpty(got[0].Message)
		})
	}
}

// TestConfirmationMarginWarningsMissingParent guards the soft-delete case the
// rest of this endpoint already defends against: an edge whose parent row is
// gone must not panic or invent a warning.
func TestConfirmationMarginWarningsMissingParent(t *testing.T) {
	t.Parallel()

	svc := &Service{defaultCheckTimeout: lintDefaultTimeout}
	child := lintCheck("c1", "consumer", 0)

	got := svc.confirmationMarginWarnings(child,
		[]*models.CheckDependency{{UID: "edge-1", ParentCheckUID: "gone", ChildCheckUID: "c1",
			Kind: models.CheckDependencyKindHard}},
		map[string]*models.Check{"c1": child})

	require.Empty(t, got)
}

// TestNewServiceDefaultTimeoutFallback pins that a caller passing a
// non-positive default still lints against the shipped 15s ceiling instead of
// against zero (which would silently under-report every edge).
func TestNewServiceDefaultTimeoutFallback(t *testing.T) {
	t.Parallel()

	require.Equal(t, DefaultCheckTimeoutFallback, NewService(nil, 0).defaultCheckTimeout)
	require.Equal(t, DefaultCheckTimeoutFallback, NewService(nil, -time.Second).defaultCheckTimeout)
	require.Equal(t, 9*time.Second, NewService(nil, 9*time.Second).defaultCheckTimeout)
}

// listForCheckLintCase drives the warning all the way through the real
// ListForCheck read path against a real database, so a shape that never
// reaches the API response cannot pass.
func listForCheckLintCase(t *testing.T, dbSvc db.Service, orgSlug string) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	org := models.NewOrganization(orgSlug, "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	newCheck := func(slug string, confirmation int) *models.Check {
		check := models.NewCheck(org.UID, slug, "tcp")
		check.Period = timeutils.Duration(lintParentPeriod)
		check.ConfirmationPeriodSeconds = confirmation
		r.NoError(dbSvc.CreateCheck(ctx, check))

		return check
	}

	parent := newCheck("rabbitmq", lintParentConfirmation)
	// The outage's shape: identical configuration on both sides.
	tooTight := newCheck("consumer-tight", lintParentConfirmation)
	// Configured with the strict margin: clean.
	roomy := newCheck("consumer-roomy", lintMarginSeconds)

	r.NoError(dbSvc.CreateCheckDependency(ctx,
		models.NewCheckDependency(org.UID, parent.UID, tooTight.UID, models.CheckDependencyKindHard, nil)))
	r.NoError(dbSvc.CreateCheckDependency(ctx,
		models.NewCheckDependency(org.UID, parent.UID, roomy.UID, models.CheckDependencyKindHard, nil)))

	svc := NewService(dbSvc, lintDefaultTimeout)

	tight, err := svc.ListForCheck(ctx, org.Slug, tooTight.UID)
	r.NoError(err)
	r.Len(tight.DependsOn, 1)
	r.Len(tight.Warnings, 1, "a child confirming as fast as its parent must be flagged")
	r.Equal(WarningCodeConfirmationMarginTooShort, tight.Warnings[0].Code)
	r.Equal(parent.UID, tight.Warnings[0].ParentCheck.UID)
	r.Equal(lintMarginSeconds, tight.Warnings[0].RecommendedConfirmationSeconds)

	clean, err := svc.ListForCheck(ctx, org.Slug, roomy.UID)
	r.NoError(err)
	r.Len(clean.DependsOn, 1)
	r.Empty(clean.Warnings, "a child at the strict margin must not be flagged")

	// The PARENT's own view lists the two edges as dependedOnBy — which are
	// never linted (the lint is about this check's own confirmation).
	parentView, err := svc.ListForCheck(ctx, org.Slug, parent.UID)
	r.NoError(err)
	r.Len(parentView.DependedOnBy, 2)
	r.Empty(parentView.Warnings)
}

func TestListForCheckWarnings_SQLite(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	listForCheckLintCase(t, dbSvc, "acme")
}

// TestListForCheckWarnings_Postgres is the dialect sibling. Self-skips under
// `-short` (the default `make test` / CI mode) and on any embedded-startup
// error, mirroring the other Postgres siblings in the repo.
func TestListForCheckWarnings_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portMarginLintPG,
		RunMode:  "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	listForCheckLintCase(t, dbSvc, "lint-pg")
}
