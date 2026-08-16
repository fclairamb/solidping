package checks_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// warnEnv is a checks service on a real sqlite DB with a controllable worker
// fleet, so the region capability the warning reads is the real aggregation
// rather than a stub.
type warnEnv struct {
	t     *testing.T
	dbSvc *sqlite.Service
	svc   *checks.Service
	org   *models.Organization
}

func newWarnEnv(t *testing.T) *warnEnv {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("warnorg", "Warn Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	return &warnEnv{t: t, dbSvc: dbSvc, svc: checks.NewService(dbSvc, nil, nil, nil), org: org}
}

// worker registers a live cloud worker in region reporting the given IPv6
// capability. A nil v6 models a worker that never reports.
func (e *warnEnv) worker(slug, region string, v6 *bool) {
	e.t.Helper()

	r := require.New(e.t)
	ctx := e.t.Context()

	worker, err := e.dbSvc.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID: uuid.New().String(), Slug: slug, Name: slug, Region: &region, EgressIPv6: v6,
	})
	r.NoError(err)

	_, err = e.dbSvc.DB().NewUpdate().Model((*models.Worker)(nil)).
		Set("last_active_at = ?", time.Now()).
		Where("uid = ?", worker.UID).Exec(ctx)
	r.NoError(err)
}

// agent seeds an org agent bound to a private region plus the workers row the
// WS handler registers for it (region NULL, exactly as in production).
func (e *warnEnv) agent(orgUID, regionSlug, name string, v6 *bool) {
	e.t.Helper()

	r := require.New(e.t)
	ctx := e.t.Context()

	agent := models.NewAgent(
		orgUID, regions.PrivateRegionSlug(regionSlug), name, "ed-"+name, "x-"+name, "fp-"+name,
	)
	_, err := e.dbSvc.DB().NewInsert().Model(agent).Exec(ctx)
	r.NoError(err)

	worker, err := e.dbSvc.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID: agent.UID, Slug: agentcrypto.WorkerSlug(agent.UID),
		Name: "agent:" + name, EgressIPv6: v6,
	})
	r.NoError(err)

	_, err = e.dbSvc.DB().NewUpdate().Model((*models.Worker)(nil)).
		Set("last_active_at = ?", time.Now()).
		Where("uid = ?", worker.UID).Exec(ctx)
	r.NoError(err)
}

// setRegions installs the global region catalog for the test.
func (e *warnEnv) setRegions(defs []regions.RegionDefinition) {
	e.t.Helper()

	require.NoError(e.t, e.dbSvc.SetSystemParameter(e.t.Context(), regions.ParamRegions, defs, false))
}

func ptrBool(v bool) *bool { return &v }

// TestCreateIPv6CheckInNoRegionSucceedsWithWarning is AC6: the write goes
// through — this is advice, not a gate — and the warning names the region.
func TestCreateIPv6CheckInNoRegionSucceedsWithWarning(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newWarnEnv(t)
	ctx := t.Context()

	env.setRegions([]regions.RegionDefinition{
		{Slug: "eu", Emoji: "🇪🇺", Name: "Europe"},
		{Slug: "us", Emoji: "🇺🇸", Name: "United States"},
	})
	env.worker("wrk-eu", "eu-west-1", ptrBool(false))
	env.worker("wrk-us", "us-east-1", ptrBool(true))

	resp, err := env.svc.CreateCheck(ctx, env.org.Slug, checks.CreateCheckRequest{
		Name:    "v6 check",
		Slug:    "v6-check",
		Type:    "tcp",
		Config:  map[string]any{"host": "example.com", "port": 443, "ipVersion": "ipv6"},
		Regions: []string{"eu"},
	})

	r.NoError(err, "an advertised 'no' must never reject the write")
	r.NotEmpty(resp.UID, "the check must actually be created")
	r.Len(resp.Warnings, 1)
	r.Contains(resp.Warnings[0].Message, "Europe", "the warning must name the region")
	r.Contains(resp.Warnings[0].Message, "United States", "and point at one that does have IPv6")

	// The check really is in the database and really is enabled.
	stored, err := env.svc.GetCheck(ctx, env.org.Slug, "v6-check", checks.GetCheckOptions{})
	r.NoError(err)
	r.NotNil(stored.Enabled)
	r.True(*stored.Enabled)
	r.Empty(stored.Warnings, "warnings are a write-path advisory, not a stored property")
}

// TestCreateIPv6CheckInUnknownRegionDoesNotWarn is the AC2 conflation guard on
// the warning path: a region nobody has reported for must not be described as
// lacking IPv6.
func TestCreateIPv6CheckInUnknownRegionDoesNotWarn(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newWarnEnv(t)
	ctx := t.Context()

	env.setRegions([]regions.RegionDefinition{{Slug: "eu", Emoji: "🇪🇺", Name: "Europe"}})
	env.worker("wrk-eu", "eu-west-1", nil) // live, but never reports

	resp, err := env.svc.CreateCheck(ctx, env.org.Slug, checks.CreateCheckRequest{
		Name:    "v6 check",
		Slug:    "v6-unknown",
		Type:    "tcp",
		Config:  map[string]any{"host": "example.com", "port": 443, "ipVersion": "ipv6"},
		Regions: []string{"eu"},
	})

	r.NoError(err)
	r.Empty(resp.Warnings, "'unknown' must never be reported as 'no'")

	// Positive control: flip the same worker to an explicit "no" and the
	// warning appears, so the assertion above is about the null value rather
	// than about a warning that never fires.
	env.worker("wrk-eu", "eu-west-1", ptrBool(false))

	resp, err = env.svc.CreateCheck(ctx, env.org.Slug, checks.CreateCheckRequest{
		Name:    "v6 check 2",
		Slug:    "v6-known-no",
		Type:    "tcp",
		Config:  map[string]any{"host": "example.com", "port": 443, "ipVersion": "ipv6"},
		Regions: []string{"eu"},
	})
	r.NoError(err)
	r.Len(resp.Warnings, 1)
}

// TestCreateAutoCheckNeverWarns — the warning is scoped to a pinned ipv6.
func TestCreateAutoCheckNeverWarns(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newWarnEnv(t)
	ctx := t.Context()

	env.setRegions([]regions.RegionDefinition{{Slug: "eu", Emoji: "🇪🇺", Name: "Europe"}})
	env.worker("wrk-eu", "eu-west-1", ptrBool(false))

	resp, err := env.svc.CreateCheck(ctx, env.org.Slug, checks.CreateCheckRequest{
		Name:    "auto check",
		Slug:    "auto-check",
		Type:    "tcp",
		Config:  map[string]any{"host": "example.com", "port": 443},
		Regions: []string{"eu"},
	})

	r.NoError(err)
	r.Empty(resp.Warnings)
}

// TestValidateReturnsWarningAndStaysValid is the live-form half: the config is
// VALID (the form must not block save) and carries the advisory.
func TestValidateReturnsWarningAndStaysValid(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newWarnEnv(t)
	ctx := t.Context()

	env.setRegions([]regions.RegionDefinition{{Slug: "eu", Emoji: "🇪🇺", Name: "Europe"}})
	env.worker("wrk-eu", "eu-west-1", ptrBool(false))

	resp, err := env.svc.ValidateCheck(ctx, env.org.Slug, &checks.ValidateCheckRequest{
		Type:    "tcp",
		Config:  map[string]any{"host": "example.com", "port": 443, "ipVersion": "ipv6"},
		Regions: []string{"eu"},
	})

	r.NoError(err)
	r.True(resp.Valid, "a capability hint must never make a config invalid")
	r.Len(resp.Warnings, 1)
	r.Equal("ipVersion", resp.Warnings[0].Name)
	r.Contains(resp.Warnings[0].Message, "Europe")
}

// TestPrivateLocationWarningIsOrgScoped: the warning for a private location
// must read THIS org's agents. Another org's identically named location with
// IPv6 must not silence it.
func TestPrivateLocationWarningIsOrgScoped(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newWarnEnv(t)
	ctx := t.Context()

	other := models.NewOrganization("otherorg", "Other Org")
	r.NoError(env.dbSvc.CreateOrganization(ctx, other))

	regionSvc := regions.NewService(env.dbSvc)
	r.NoError(regionSvc.SetOrgCustomRegions(ctx, env.org.UID, []regions.RegionDefinition{
		{Slug: "hq", Name: "Head office", Emoji: "🏢"},
	}))
	r.NoError(regionSvc.SetOrgCustomRegions(ctx, other.UID, []regions.RegionDefinition{
		{Slug: "hq", Name: "Head office", Emoji: "🏢"},
	}))

	env.agent(env.org.UID, "hq", "mine", ptrBool(false))
	env.agent(other.UID, "hq", "theirs", ptrBool(true))

	resp, err := env.svc.ValidateCheck(ctx, env.org.Slug, &checks.ValidateCheckRequest{
		Type:    "tcp",
		Config:  map[string]any{"host": "internal.example.com", "port": 443, "ipVersion": "ipv6"},
		Regions: []string{regions.PrivateRegionSlug("hq")},
	})

	r.NoError(err)
	r.True(resp.Valid)
	r.Len(resp.Warnings, 1, "another org's IPv6-capable location must not silence this warning")
	r.Contains(resp.Warnings[0].Message, "Head office")
}
