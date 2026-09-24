package agents_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/agents"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// Spec 2026-09-25-05 §2: a private location's liveness monitor is created with
// it, removed with it, backfilled, and never recreated after an opt-out.

type monitorSetup struct {
	svc    *agents.Service
	checks *checks.Service
	dbSvc  *sqlite.Service
	org    *models.Organization
}

func newMonitorSetup(t *testing.T, limits *entcore.Limits) *monitorSetup {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	creds, err := credentials.NewService(nil, credentials.ParamStore{})
	r.NoError(err)

	var entSvc *entcore.Service

	if limits != nil {
		entSvc = entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
		r.NoError(entSvc.Set(ctx, org.UID, entcore.Entitlements{
			Limits: *limits, Source: models.EntitlementSourceAdmin,
		}, "user:tester", ""))
	}

	events := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = events.Close() })

	checksSvc := checks.NewService(dbSvc, events, creds, entSvc)

	svc := agents.NewService(dbSvc, creds, entSvc)
	svc.SetLivenessMonitors(checksSvc)

	return &monitorSetup{svc: svc, checks: checksSvc, dbSvc: dbSvc, org: org}
}

// monitors lists every live private-location check of the org.
func (s *monitorSetup) monitors(t *testing.T) []*models.Check {
	t.Helper()

	all := "all"
	found, _, err := s.dbSvc.ListChecks(t.Context(), s.org.UID, &models.ListChecksFilter{
		Types: []string{string(checkerdef.CheckTypePrivateLocation)}, Internal: &all,
	})
	require.NoError(t, err)

	return found
}

func (s *monitorSetup) region(t *testing.T, slug string) *regions.RegionDefinition {
	t.Helper()

	defs, err := regions.NewService(s.dbSvc).GetOrgCustomRegions(t.Context(), s.org.UID)
	require.NoError(t, err)

	for i := range defs {
		if defs[i].Slug == slug {
			return &defs[i]
		}
	}

	return nil
}

func TestCreatingALocationCreatesItsMonitor(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newMonitorSetup(t, nil)
	ctx := t.Context()

	// A default integration is attached to the monitor like to any check.
	channel := models.NewIntegration(s.org.UID, models.ConnectionTypeWebhook, "ops")
	channel.IsDefault = true
	r.NoError(s.dbSvc.CreateChannel(ctx, channel))

	resp, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "office", Name: "Office"})
	r.NoError(err)
	r.NotNil(resp.LivenessMonitor)

	monitors := s.monitors(t)
	r.Len(monitors, 1)

	monitor := monitors[0]
	r.Equal("Private location: Office", *monitor.Name)
	r.Equal("private-location-office", *monitor.Slug)
	r.Equal(map[string]any{"region": "@office"}, map[string]any(monitor.Config))
	r.Equal(time.Minute, time.Duration(monitor.Period))
	r.True(monitor.Enabled)
	r.False(monitor.Internal, "a real, visible check")
	r.Empty(monitor.Regions)

	jobs, err := s.dbSvc.ListCheckJobsByCheckUID(ctx, monitor.UID)
	r.NoError(err)
	r.Len(jobs, 1)
	r.Nil(jobs[0].Region, "evaluated on the jobs node, never inside the location")

	conns, err := s.dbSvc.ListCheckConnectionsWithSettings(ctx, monitor.UID)
	r.NoError(err)
	r.Len(conns, 1, "default integrations are copied on create")

	// Deleting the location deletes its monitor, and records no opt-out.
	r.NoError(s.svc.DeletePrivateRegion(ctx, "acme", "office"))
	r.Empty(s.monitors(t))
	r.Nil(s.region(t, "office"))
}

func TestBackfillCreatesExactlyOneAndRespectsTheOptOut(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newMonitorSetup(t, nil)
	ctx := t.Context()

	// Two locations that predate the monitor.
	r.NoError(regions.NewService(s.dbSvc).SetOrgCustomRegions(ctx, s.org.UID, []regions.RegionDefinition{
		{Slug: "office", Name: "Office"}, {Slug: "lab", Name: "Lab"},
	}))
	r.Empty(s.monitors(t))

	created, err := s.checks.BackfillPrivateLocationMonitors(ctx)
	r.NoError(err)
	r.Equal(2, created)
	r.Len(s.monitors(t), 2)

	created, err = s.checks.BackfillPrivateLocationMonitors(ctx)
	r.NoError(err)
	r.Zero(created, "idempotent")
	r.Len(s.monitors(t), 2)

	// The user deletes the office monitor: the opt-out is recorded on the
	// location and neither the backfill nor an enrollment recreates it.
	office, err := s.checks.PrivateLocationMonitor(ctx, s.org.UID, "office")
	r.NoError(err)
	r.NoError(s.checks.DeleteCheck(ctx, "acme", office.UID))
	r.True(s.region(t, "office").LivenessMonitorOff)

	created, err = s.checks.BackfillPrivateLocationMonitors(ctx)
	r.NoError(err)
	r.Zero(created)

	ensured, err := s.checks.EnsurePrivateLocationMonitor(ctx, s.org.UID, "office")
	r.NoError(err)
	r.Nil(ensured)
	r.Len(s.monitors(t), 1)

	list, err := s.svc.ListPrivateRegions(ctx, "acme")
	r.NoError(err)

	byslug := map[string]agents.PrivateRegionResponse{}
	for _, row := range list.Data {
		byslug[row.Slug] = row
	}

	r.True(byslug["office"].LivenessMonitorOff)
	r.Nil(byslug["office"].LivenessMonitor)
	r.False(byslug["lab"].LivenessMonitorOff)
	r.NotNil(byslug["lab"].LivenessMonitor)

	// One click turns it back on.
	enabled, err := s.svc.EnableLivenessMonitor(ctx, "acme", "office")
	r.NoError(err)
	r.True(enabled.Enabled)
	r.False(s.region(t, "office").LivenessMonitorOff)
	r.Len(s.monitors(t), 2)
}

func TestDisabledMonitorReadsOffAndIsReEnabled(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newMonitorSetup(t, nil)
	ctx := t.Context()

	_, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "office"})
	r.NoError(err)

	monitor, err := s.checks.PrivateLocationMonitor(ctx, s.org.UID, "office")
	r.NoError(err)

	disabled := false
	_, err = s.checks.UpdateCheck(ctx, "acme", monitor.UID, &checks.UpdateCheckRequest{Enabled: &disabled})
	r.NoError(err)

	list, err := s.svc.ListPrivateRegions(ctx, "acme")
	r.NoError(err)
	r.True(list.Data[0].LivenessMonitorOff)
	r.False(s.region(t, "office").LivenessMonitorOff, "disabling is not the delete opt-out")

	// The backfill leaves a disabled monitor alone.
	created, err := s.checks.BackfillPrivateLocationMonitors(ctx)
	r.NoError(err)
	r.Zero(created)

	enabled, err := s.svc.EnableLivenessMonitor(ctx, "acme", "office")
	r.NoError(err)
	r.True(enabled.Enabled)
	r.Equal(monitor.UID, enabled.UID, "the same monitor, re-enabled")
}

func TestAnOrgCannotWatchAnotherOrgsLocation(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newMonitorSetup(t, nil)
	ctx := t.Context()

	_, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "office"})
	r.NoError(err)

	other := models.NewOrganization("other", "Other")
	r.NoError(s.dbSvc.CreateOrganization(ctx, other))

	_, err = s.checks.CreateCheck(ctx, "other", checks.CreateCheckRequest{
		Type:   string(checkerdef.CheckTypePrivateLocation),
		Config: map[string]any{"region": "@office"},
	})
	r.Error(err, "`@office` is acme's, not other's")
	r.Contains(err.Error(), "private locations")

	// Positive control: acme may create one for its own location.
	_, err = s.checks.CreateCheck(ctx, "acme", checks.CreateCheckRequest{
		Slug:   "office-watch",
		Type:   string(checkerdef.CheckTypePrivateLocation),
		Config: map[string]any{"region": "@office"},
	})
	r.NoError(err)

	// And a monitor cannot be repointed.
	monitor, err := s.checks.PrivateLocationMonitor(ctx, s.org.UID, "office")
	r.NoError(err)

	_, err = s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "lab"})
	r.NoError(err)

	repoint := map[string]any{"region": "@lab"}
	_, err = s.checks.UpdateCheck(ctx, "acme", monitor.UID, &checks.UpdateCheckRequest{Config: &repoint})
	r.ErrorIs(err, checks.ErrPrivateLocationRegionImmutable)
}

func TestTheMonitorDoesNotCountTowardQuota(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newMonitorSetup(t, &entcore.Limits{MaxChecks: entcore.Int(1)})
	ctx := t.Context()

	_, err := s.checks.CreateCheck(ctx, "acme", checks.CreateCheckRequest{
		Slug: "web", Type: "http", Config: map[string]any{"url": "https://acme.com"},
	})
	r.NoError(err)

	// At the cap: the monitor is still created with the location.
	_, err = s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "office"})
	r.NoError(err)
	r.Len(s.monitors(t), 1)

	ent := entcore.NewService(s.dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)

	usage, err := ent.Usage(ctx, s.org.UID)
	r.NoError(err)
	r.Equal(1, usage.Checks, "the monitor is not counted")
	r.InDelta(1.0, usage.ChecksPerMinute, 0.001, "nor in the checks-per-minute figure")

	// Positive control: the http check alone still fills the cap.
	_, err = s.checks.CreateCheck(ctx, "acme", checks.CreateCheckRequest{
		Slug: "web-2", Type: "http", Config: map[string]any{"url": "https://acme.com/2"},
	})
	r.ErrorIs(err, entcore.ErrEntitlementExceeded)
}

func TestAMonitorAwaitingItsFirstAgentIsNeverAStaleCandidate(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newMonitorSetup(t, nil)
	ctx := t.Context()

	_, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "office"})
	r.NoError(err)

	monitor, err := s.checks.PrivateLocationMonitor(ctx, s.org.UID, "office")
	r.NoError(err)

	// Positive control: an ordinary check that never produced a result.
	web, err := s.checks.CreateCheck(ctx, "acme", checks.CreateCheckRequest{
		Slug: "web", Type: "http", Config: map[string]any{"url": "https://acme.com"},
	})
	r.NoError(err)

	old := time.Now().Add(-time.Hour)
	_, err = s.dbSvc.DB().NewUpdate().Model((*models.Check)(nil)).
		Set("created_at = ?", old).Where("organization_uid = ?", s.org.UID).Exec(ctx)
	r.NoError(err)

	candidates, err := s.dbSvc.ListStaleCandidates(ctx, time.Now(), 100)
	r.NoError(err)

	uids := map[string]bool{}
	for _, c := range candidates {
		uids[c.UID] = true
	}

	r.True(uids[web.UID])
	r.False(uids[monitor.UID], "a location with no agent yet is not stale, it is empty")
}

func TestAgentsAndLocationsCarryTheSharedLivenessRule(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	s := newMonitorSetup(t, nil)
	ctx := t.Context()

	_, err := s.svc.CreatePrivateRegion(ctx, "acme", &agents.CreatePrivateRegionRequest{Slug: "office"})
	r.NoError(err)

	insert := func(name string, lastSeen time.Time) {
		agent := models.NewAgent(s.org.UID, "@office", name, "ed-"+name, "age1"+name, "fp-"+name)
		agent.LastSeenAt = &lastSeen
		_, insErr := s.dbSvc.DB().NewInsert().Model(agent).Exec(ctx)
		r.NoError(insErr)
	}

	insert("office-1", time.Now().Add(-30*time.Second))
	insert("office-2", time.Now().Add(-10*time.Minute))

	agentsResp, err := s.svc.ListAgents(ctx, "acme")
	r.NoError(err)

	online := map[string]bool{}
	for _, agent := range agentsResp.Data {
		online[agent.Name] = agent.Online
	}

	r.True(online["office-1"])
	r.False(online["office-2"])

	list, err := s.svc.ListPrivateRegions(ctx, "acme")
	r.NoError(err)
	r.Len(list.Data, 1)
	r.Equal(2, list.Data[0].AgentCount)
	r.Equal(1, list.Data[0].OnlineAgentCount)
	r.Equal(agents.LocationStateDegraded, list.Data[0].State)
	r.NotNil(list.Data[0].LivenessMonitor)
	r.Equal("private-location-office", list.Data[0].LivenessMonitor.Slug)
}
