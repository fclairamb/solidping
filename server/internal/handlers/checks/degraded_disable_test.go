package checks_test

// Turning degraded detection off closes the open degraded incident in the same
// request (spec 2026-09-24-08). The evaluator only sweeps checks with degraded
// detection on, so without this an incident opened while the check was on would
// stay open forever. Proven through PATCH (UpdateCheck, which MCP update_check
// also calls) and through /apply, each with a positive control where the flag
// stays on and the incident stays open.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

type degradedDisableWorld struct {
	dbSvc     *sqlite.Service
	checks    *checks.Service
	incidents *incidents.Service
	org       *models.Organization
}

// newDegradedDisableWorld wires the checks service to a real incidents service,
// the way app/server.go and mcp.NewHandler do.
func newDegradedDisableWorld(t *testing.T, slug string) *degradedDisableWorld {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization(slug, "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	incidentsSvc := incidents.NewService(dbSvc, jobs, clock.Real{}, nil)

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)
	svc.SetDegradedIncidentResolver(incidentsSvc)

	return &degradedDisableWorld{dbSvc: dbSvc, checks: svc, incidents: incidentsSvc, org: org}
}

// openDegraded opens a degraded incident on the check, as the evaluator would.
func (w *degradedDisableWorld) openDegraded(t *testing.T, checkUID string) *models.Incident {
	t.Helper()
	r := require.New(t)

	check, err := w.dbSvc.GetCheck(t.Context(), w.org.UID, checkUID)
	r.NoError(err)

	incident, err := w.incidents.OpenDegradedIncident(t.Context(), &incidents.OpenDegradedIncidentRequest{
		Check:     check,
		StartedAt: time.Now().Add(-10 * time.Minute),
		Title:     "acme is degraded: 7 failures in the last 60 probes",
		Snapshot: &incidents.DegradedSnapshot{
			Failures: 5, FailuresWindow: 60, FailureSlots: 60, FailureMatches: 7,
			FailuresFired: true, ResolveWindow: 60,
		},
	})
	r.NoError(err)
	r.NotNil(incident)

	return incident
}

func (w *degradedDisableWorld) incident(t *testing.T, uid string) *models.Incident {
	t.Helper()

	incident, err := w.dbSvc.GetIncident(t.Context(), w.org.UID, uid)
	require.NoError(t, err)

	return incident
}

func TestDisablingDegradedResolvesOpenIncident_Patch(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	world := newDegradedDisableWorld(t, "degraded-off-patch")

	check := models.NewCheck(world.org.UID, "acme", "http")
	r.True(check.DegradedEnabled, "a new check starts with degraded detection on")
	r.NoError(world.dbSvc.CreateCheck(ctx, check))

	opened := world.openDegraded(t, check.UID)

	// Positive control: a write that keeps the flag on leaves the incident open.
	on := true
	_, err := world.checks.UpdateCheck(ctx, world.org.Slug, check.UID, &checks.UpdateCheckRequest{
		DegradedEnabled: &on,
		SlowThresholdMs: intPtr(1500),
	})
	r.NoError(err)
	r.Equal(models.IncidentStateActive, world.incident(t, opened.UID).State,
		"keeping degraded detection on must not close the incident")

	// Turning it off closes it, and says why.
	off := false
	updated, err := world.checks.UpdateCheck(ctx, world.org.Slug, check.UID, &checks.UpdateCheckRequest{
		DegradedEnabled: &off,
	})
	r.NoError(err)
	r.False(updated.DegradedEnabled)

	resolved := world.incident(t, opened.UID)
	r.Equal(models.IncidentStateResolved, resolved.State)
	r.NotNil(resolved.ResolvedAt)
	r.NotNil(resolved.ResolutionType)
	r.Equal(models.ResolutionTypeDisabled, *resolved.ResolutionType,
		"resolved because degraded detection was turned off, not because the check recovered")
	r.Equal(true, resolved.Details["degraded_turned_off"])

	// A second "off" write with nothing open is a no-op, not an error.
	_, err = world.checks.UpdateCheck(ctx, world.org.Slug, check.UID, &checks.UpdateCheckRequest{
		DegradedEnabled: &off,
	})
	r.NoError(err)
}

func TestDisablingDegradedResolvesOpenIncident_Apply(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	world := newDegradedDisableWorld(t, "degraded-off-apply")

	manifest := func(name string, degradedEnabled bool) *checks.ExportDocument {
		check := manifestCheck("api")
		check.Name = name
		check.DegradedEnabled = degradedEnabled

		return doc(world.org.Slug, check)
	}

	_, err := world.checks.ApplyChecks(ctx, world.org.Slug, manifest("api", true), checks.ApplyOptions{})
	r.NoError(err)

	check, err := world.dbSvc.GetCheckByUidOrSlug(ctx, world.org.UID, "api")
	r.NoError(err)
	r.True(check.DegradedEnabled)

	opened := world.openDegraded(t, check.UID)

	// Positive control: an applied update that keeps the flag on.
	res, err := world.checks.ApplyChecks(ctx, world.org.Slug, manifest("api renamed", true), checks.ApplyOptions{})
	r.NoError(err)
	r.Equal(1, res.Updated)
	r.Equal(models.IncidentStateActive, world.incident(t, opened.UID).State,
		"an apply that keeps degraded detection on must not close the incident")

	// Applying degradedEnabled: false closes it.
	res, err = world.checks.ApplyChecks(ctx, world.org.Slug, manifest("api renamed", false), checks.ApplyOptions{})
	r.NoError(err)
	r.Equal(1, res.Updated)

	resolved := world.incident(t, opened.UID)
	r.Equal(models.IncidentStateResolved, resolved.State)
	r.NotNil(resolved.ResolutionType)
	r.Equal(models.ResolutionTypeDisabled, *resolved.ResolutionType)
}

// TestDisablingDegradedLeavesOutageAlone pins the scope: only the degraded
// incident closes. A real outage on the same check is none of this hook's
// business.
func TestDisablingDegradedLeavesOutageAlone(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	world := newDegradedDisableWorld(t, "degraded-off-outage")

	check := models.NewCheck(world.org.UID, "acme", "http")
	r.NoError(world.dbSvc.CreateCheck(ctx, check))

	outage := models.NewIncident(world.org.UID, check.UID, time.Now().Add(-time.Minute), "acme is down")
	r.NoError(world.dbSvc.CreateIncident(ctx, outage))

	off := false
	_, err := world.checks.UpdateCheck(ctx, world.org.Slug, check.UID, &checks.UpdateCheckRequest{
		DegradedEnabled: &off,
	})
	r.NoError(err)
	r.Equal(models.IncidentStateActive, world.incident(t, outage.UID).State)
}
