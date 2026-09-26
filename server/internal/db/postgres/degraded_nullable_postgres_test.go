package postgres

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/testsupport"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// The degraded-detection configuration is nullable-with-a-code-default: NULL
// means "not configured" and the documented default (5 / 60, 3 / 6, 0) is
// resolved at read time. Postgres is the dialect the production migration
// applies to, so the shape has to be pinned there and not only on SQLite.
//
// portDegradedNullable is distinct from every other _postgres_test.go file's
// embedded-Postgres port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portDegradedNullable = 15534

func newDegradedNullablePG(t *testing.T) *Service {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	s, err := New(ctx, &Config{Embedded: true, Port: portDegradedNullable, RunMode: runModeTest})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	return s
}

// TestDegradedColumnsAreNullableWithoutDefault_Postgres reads the shape straight
// out of the catalog. It is the one assertion the Go layer cannot make for
// itself: a `not null default 5` left behind on any of the five columns would
// still pass every behavioral test (bun always sends the column, so the SQL
// default never fires), while quietly giving "unset" a second spelling and
// making a NULL insert fail.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction) with its siblings
func TestDegradedColumnsAreNullableWithoutDefault_Postgres(t *testing.T) {
	s := newDegradedNullablePG(t)
	r := require.New(t)
	ctx := t.Context()

	type columnShape struct {
		Column   string  `bun:"column_name"`
		Nullable string  `bun:"is_nullable"`
		Default  *string `bun:"column_default"`
	}

	var shapes []columnShape
	err := s.db.NewRaw(
		`select column_name, is_nullable, column_default
		   from information_schema.columns
		  where table_name = 'checks'
		    and column_name in (?, ?, ?, ?, ?, ?)
		  order by column_name`,
		"degraded_failures", "degraded_failures_window", "degraded_slow",
		"degraded_slow_window", "slow_threshold_ms", "degraded_enabled",
	).Scan(ctx, &shapes)
	r.NoError(err)
	r.Len(shapes, 6)

	byName := make(map[string]columnShape, len(shapes))
	for _, shape := range shapes {
		byName[shape.Column] = shape
	}

	for _, name := range []string{
		"degraded_failures", "degraded_failures_window",
		"degraded_slow", "degraded_slow_window", "slow_threshold_ms",
	} {
		shape, ok := byName[name]
		r.True(ok, "%s must exist", name)
		r.Equal("YES", shape.Nullable, "%s must be nullable — NULL is the unset marker", name)
		r.Nil(shape.Default,
			"%s must have NO default clause: a SQL default would give 'unset' a second spelling", name)
	}

	// degraded_enabled is the deliberate exception: NULL cannot carry the
	// rollout rule (off for every pre-existing row, on for a new check), and a
	// plain bool makes a bypassing insert fail safe: not evaluated.
	enabled := byName["degraded_enabled"]
	r.Equal("NO", enabled.Nullable, "degraded_enabled must stay NOT NULL")
	r.NotNil(enabled.Default)
	r.Contains(*enabled.Default, "false")
}

// TestDegradedNullRoundTrip_Postgres proves the NULL round-trip on Postgres: a
// check inserted without any degraded configuration reads back with five NULL
// columns that resolve to the documented defaults, while an explicit 0 survives
// as 0 and keeps meaning "this rule is off".
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction) with its siblings
func TestDegradedNullRoundTrip_Postgres(t *testing.T) {
	s := newDegradedNullablePG(t)
	r := require.New(t)
	ctx := t.Context()

	org := models.NewOrganization("degraded-null-pg", "Degraded Null PG")
	r.NoError(s.CreateOrganization(ctx, org))

	unset := models.NewCheck(org.UID, "dn-unset", "http")
	r.NoError(s.CreateCheck(ctx, unset))

	stored, err := s.GetCheckByUidOrSlug(ctx, org.UID, unset.UID)
	r.NoError(err)
	r.Nil(stored.DegradedFailures, "NewCheck must not write a default into the row")
	r.Nil(stored.DegradedFailuresWindow)
	r.Nil(stored.DegradedSlow)
	r.Nil(stored.DegradedSlowWindow)
	r.Nil(stored.SlowThresholdMs)

	r.Equal(5, stored.EffectiveDegradedFailures())
	r.Equal(60, stored.EffectiveDegradedFailuresWindow())
	r.Equal(3, stored.EffectiveDegradedSlow())
	r.Equal(6, stored.EffectiveDegradedSlowWindow())
	r.Equal(0, stored.EffectiveSlowThresholdMs())
	r.True(stored.DegradedEnabled, "on for a new check")

	// An explicit 0 through the UPDATE path: the pointer-gated setters must
	// write the zero rather than skip it.
	zero := 0
	r.NoError(s.UpdateCheck(ctx, unset.UID, &models.CheckUpdate{
		DegradedFailures: &zero,
		DegradedSlow:     &zero,
	}))

	off, err := s.GetCheckByUidOrSlug(ctx, org.UID, unset.UID)
	r.NoError(err)
	r.NotNil(off.DegradedFailures)
	r.Equal(0, *off.DegradedFailures)
	r.Equal(0, off.EffectiveDegradedFailures(), "a stored 0 must not be defaulted back to 5")
	r.Equal(0, off.EffectiveDegradedSlow())
	// Untouched columns are still NULL, so they still default.
	r.Nil(off.DegradedFailuresWindow)
	r.Equal(60, off.EffectiveDegradedFailuresWindow())

	// A hand-built row that never heard of NewCheck: this is the insert path the
	// nullable columns exist for. Before, it stored 0 for all five and ran with
	// every rule silently off.
	bareName, bareSlug := "dn-bare", "dn-bare"
	now := time.Now()
	bare := &models.Check{
		UID:             uuid.New().String(),
		OrganizationUID: org.UID,
		Name:            &bareName,
		Slug:            &bareSlug,
		Type:            "http",
		Config:          models.JSONMap{"url": "https://acme.com"},
		Enabled:         true,
		Period:          timeutils.Duration(time.Minute),
		Status:          models.CheckStatusCreated,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	r.NoError(s.CreateCheck(ctx, bare))

	bareStored, err := s.GetCheckByUidOrSlug(ctx, org.UID, bare.UID)
	r.NoError(err)
	r.Nil(bareStored.DegradedFailures)
	r.Equal(5, bareStored.EffectiveDegradedFailures())
	r.False(bareStored.DegradedEnabled,
		"degraded_enabled is not nullable, so a bypassing insert fails safe: not evaluated")
}

// TestDegradedSweepSkipsDisabledChecks_Postgres pins the sweep's work queue
// (spec 2026-09-24-08): a check with degraded detection off is not listed at
// all, an enabled one still is, and the dry-run stamp column is gone.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction) with its siblings
func TestDegradedSweepSkipsDisabledChecks_Postgres(t *testing.T) {
	s := newDegradedNullablePG(t)
	r := require.New(t)
	ctx := t.Context()

	var stampColumns int
	r.NoError(s.db.NewRaw(
		`select count(*) from information_schema.columns
		  where table_name = 'checks' and column_name = 'degraded_would_fire_at'`,
	).Scan(ctx, &stampColumns))
	r.Zero(stampColumns, "the dry-run stamp column is dropped by 024")

	org := models.NewOrganization("degraded-sweep-pg", "Degraded Sweep PG")
	r.NoError(s.CreateOrganization(ctx, org))

	on := models.NewCheck(org.UID, "ds-on", "http")
	r.NoError(s.CreateCheck(ctx, on))

	off := models.NewCheck(org.UID, "ds-off", "http")
	off.DegradedEnabled = false
	r.NoError(s.CreateCheck(ctx, off))

	queue, err := s.ListChecksForDegradedEval(ctx, 0)
	r.NoError(err)

	uids := make([]string, 0, len(queue))
	for _, check := range queue {
		uids = append(uids, check.UID)
	}

	r.Contains(uids, on.UID, "an enabled check is swept")
	r.NotContains(uids, off.UID, "a disabled check is not evaluated at all")
}
