package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// --- Zero values survive CREATE, Postgres half (spec 2026-08-30-04) ---------
//
// The SQLite twin is internal/db/sqlite/zero_value_create_test.go, which
// carries the full write-up. The two halves must assert the same predicates:
// whether bun replaces a zero-valued field with the literal `DEFAULT` depends
// on the dialect's insert path and on driver feature flags, so one dialect
// passing proves nothing about the other.

// portZeroValueCreate is this test's own embedded-postgres port, distinct from
// every other one in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portZeroValueCreate = 15504

// zeroValuePG boots an embedded cluster with one organization.
func zeroValuePG(t *testing.T) (context.Context, *Service, string) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portZeroValueCreate, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("zerovalues", "Acme Zero")
	r.NoError(svc.CreateOrganization(ctx, org))

	return ctx, svc, org.UID
}

// TestZeroValuesSurviveCreate_Postgres mirrors the three SQLite cases in one
// test: the embedded cluster is expensive to boot, and the sibling tests in
// this package share one instance per file for the same reason.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres) with its siblings
func TestZeroValuesSurviveCreate_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	r := require.New(t)
	ctx, s, orgUID := zeroValuePG(t)

	// --- StatusPage: the reported bug --------------------------------------
	off := models.NewStatusPage(orgUID, "Off", "off")
	off.Enabled = false
	off.ShowAvailability = false
	off.ShowResponseTime = false
	r.NoError(s.CreateStatusPage(ctx, off))

	gotOff, err := s.GetStatusPageBySlug(ctx, orgUID, "off")
	r.NoError(err)
	r.False(gotOff.Enabled, "enabled=false was rewritten to the DDL default on create")
	r.False(gotOff.ShowAvailability, "showAvailability=false was rewritten to the DDL default on create")
	r.False(gotOff.ShowResponseTime, "showResponseTime=false was rewritten to the DDL default on create")

	// Positive control: NewStatusPage is now the only source of the defaults.
	on := models.NewStatusPage(orgUID, "On", "always-on")
	r.NoError(s.CreateStatusPage(ctx, on))

	gotOn, err := s.GetStatusPageBySlug(ctx, orgUID, "always-on")
	r.NoError(err)
	r.True(gotOn.Enabled)
	r.True(gotOn.ShowAvailability)
	r.True(gotOn.ShowResponseTime)
	r.Equal(models.CustomDomainStateNone, gotOn.CustomDomainState)
	r.Equal(string(models.StatusPagePeriod90d), gotOn.HistoryPeriod)
	r.Equal(90, gotOn.HistoryDays)
	r.Equal(models.StatusPageVisibilityPublic, gotOn.Visibility)

	// --- Check: flapping/escalation tuning ---------------------------------
	flat := models.NewCheck(orgUID, "flat", "http")
	flat.FlappingWindowSeconds = 0
	flat.FlapBackoffFactor = 0
	flat.MaxRecoveryMultiplier = 0
	flat.EscalationThreshold = 0
	r.NoError(s.CreateCheck(ctx, flat))

	gotFlat, err := s.GetCheck(ctx, orgUID, flat.UID)
	r.NoError(err)
	r.Zero(gotFlat.FlappingWindowSeconds, "flapping cannot be turned off at creation time")
	r.Zero(gotFlat.FlapBackoffFactor)
	r.Zero(gotFlat.MaxRecoveryMultiplier)
	r.Zero(gotFlat.EscalationThreshold)

	tuned := models.NewCheck(orgUID, "tuned", "http")
	r.NoError(s.CreateCheck(ctx, tuned))

	gotTuned, err := s.GetCheck(ctx, orgUID, tuned.UID)
	r.NoError(err)
	r.Equal(21600, gotTuned.FlappingWindowSeconds)
	r.Equal(2, gotTuned.FlapBackoffFactor)
	r.Equal(8, gotTuned.MaxRecoveryMultiplier)
	r.Equal(10, gotTuned.EscalationThreshold)

	// --- Integration: created disabled stays disabled -----------------------
	hookOff := models.NewIntegration(orgUID, models.ConnectionTypeWebhook, "Paused hook")
	hookOff.Enabled = false
	r.NoError(s.CreateChannel(ctx, hookOff))

	gotHookOff, err := s.GetChannel(ctx, hookOff.UID)
	r.NoError(err)
	r.False(gotHookOff.Enabled, "an integration created disabled came back enabled")

	hookOn := models.NewIntegration(orgUID, models.ConnectionTypeWebhook, "Live hook")
	r.NoError(s.CreateChannel(ctx, hookOn))

	gotHookOn, err := s.GetChannel(ctx, hookOn.UID)
	r.NoError(err)
	r.True(gotHookOn.Enabled)
}
