package sqlite

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// --- Zero values survive CREATE (spec 2026-08-30-04) ------------------------
//
// bun emits the literal `DEFAULT` in the VALUES clause for a field whose bun
// tag declares a `default:` and whose Go value is the zero value, so the DDL
// default silently won and `showAvailability: false` was stored as `true`.
// The fix drops the `default:` clauses from the model tags; these tests are
// what proves the fix at the layer where the bug lived.
//
// Its Postgres twin is TestZeroValuesSurviveCreate_Postgres in
// internal/db/postgres/zero_value_create_postgres_test.go, and the two must
// assert the same predicates: `DEFAULT`-placeholder handling is a bun/driver
// concern that the two dialects reach by different paths, so one dialect
// passing says nothing about the other. If either side drops a case, one
// deployment silently regains a bug the other does not have.
//
// Not covered here, deliberately: models whose zero value no create path can
// express today (IncidentMemberCheck is written by tests only, and every
// UserNotificationRoute writer goes through NewUserNotificationRoute, which
// hard-codes Enabled: true). The tag guard in internal/db/models keeps those
// honest; a round-trip test would be asserting on a caller that does not exist.

func newZeroValueTestService(t *testing.T) *Service {
	t.Helper()

	ctx := t.Context()
	s, err := New(ctx, Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	return s
}

// TestStatusPageZeroFlagsSurviveCreate is the reported bug: a page created with
// the three display flags off must read back off.
func TestStatusPageZeroFlagsSurviveCreate(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s := newZeroValueTestService(t)

	org := models.NewOrganization("zero-sqlite", "Acme")
	r.NoError(s.CreateOrganization(ctx, org))

	off := models.NewStatusPage(org.UID, "Off", "off")
	off.Enabled = false
	off.ShowAvailability = false
	off.ShowResponseTime = false
	r.NoError(s.CreateStatusPage(ctx, off))

	got, err := s.GetStatusPageBySlug(ctx, org.UID, "off")
	r.NoError(err)
	r.False(got.Enabled, "enabled=false was rewritten to the DDL default on create")
	r.False(got.ShowAvailability, "showAvailability=false was rewritten to the DDL default on create")
	r.False(got.ShowResponseTime, "showResponseTime=false was rewritten to the DDL default on create")

	// Positive control: with the `default:` clauses gone, NewStatusPage is the
	// only thing left supplying the true-by-default. If it ever stops, this
	// fails rather than the flags quietly flipping for every new page.
	on := models.NewStatusPage(org.UID, "On", "always-on")
	r.NoError(s.CreateStatusPage(ctx, on))

	gotOn, err := s.GetStatusPageBySlug(ctx, org.UID, "always-on")
	r.NoError(err)
	r.True(gotOn.Enabled)
	r.True(gotOn.ShowAvailability)
	r.True(gotOn.ShowResponseTime)
	r.Equal(models.CustomDomainStateNone, gotOn.CustomDomainState)
	r.Equal(string(models.StatusPagePeriod90d), gotOn.HistoryPeriod)
	r.Equal(90, gotOn.HistoryDays)
	r.Equal(models.StatusPageVisibilityPublic, gotOn.Visibility)
}

// TestCheckZeroTuningSurvivesCreate covers the same class on checks. The
// comment above Check.FlappingWindowSeconds says FlapBackoffFactor==1 or
// FlappingWindowSeconds==0 turns adaptive recovery off — which `default:21600`
// / `default:2` made impossible to express at creation time.
func TestCheckZeroTuningSurvivesCreate(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s := newZeroValueTestService(t)

	org := models.NewOrganization("zero-chk-sqlite", "Acme")
	r.NoError(s.CreateOrganization(ctx, org))

	flat := models.NewCheck(org.UID, "flat", "http")
	flat.FlappingWindowSeconds = 0
	flat.FlapBackoffFactor = 0
	flat.MaxRecoveryMultiplier = 0
	flat.EscalationThreshold = 0
	r.NoError(s.CreateCheck(ctx, flat))

	got, err := s.GetCheck(ctx, org.UID, flat.UID)
	r.NoError(err)
	r.Zero(got.FlappingWindowSeconds, "flapping cannot be turned off at creation time")
	r.Zero(got.FlapBackoffFactor)
	r.Zero(got.MaxRecoveryMultiplier)
	r.Zero(got.EscalationThreshold)

	// Positive control: NewCheck still supplies the tuned defaults.
	tuned := models.NewCheck(org.UID, "tuned", "http")
	r.NoError(s.CreateCheck(ctx, tuned))

	gotTuned, err := s.GetCheck(ctx, org.UID, tuned.UID)
	r.NoError(err)
	r.Equal(21600, gotTuned.FlappingWindowSeconds)
	r.Equal(2, gotTuned.FlapBackoffFactor)
	r.Equal(8, gotTuned.MaxRecoveryMultiplier)
	r.Equal(10, gotTuned.EscalationThreshold)
}

// TestIntegrationCreatedDisabledStaysDisabled: `default:true` on
// Integration.Enabled meant an integration could not be created disabled.
func TestIntegrationCreatedDisabledStaysDisabled(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s := newZeroValueTestService(t)

	org := models.NewOrganization("zero-int-sqlite", "Acme")
	r.NoError(s.CreateOrganization(ctx, org))

	off := models.NewIntegration(org.UID, models.ConnectionTypeWebhook, "Paused hook")
	off.Enabled = false
	r.NoError(s.CreateChannel(ctx, off))

	got, err := s.GetChannel(ctx, off.UID)
	r.NoError(err)
	r.False(got.Enabled, "an integration created disabled came back enabled")

	on := models.NewIntegration(org.UID, models.ConnectionTypeWebhook, "Live hook")
	r.NoError(s.CreateChannel(ctx, on))

	gotOn, err := s.GetChannel(ctx, on.UID)
	r.NoError(err)
	r.True(gotOn.Enabled)
}
