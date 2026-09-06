package jobtypes

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// This file covers §1, §4 and §6 of spec 2026-09-06-02: what the startup job
// provisions, that it is idempotent, and that the sweeper expires the right
// checks and heals a tampered identity.

// fakeCheckDeleter records the checks a sweep asked the service to delete, and
// removes them, so the sweep's SELECTION can be asserted without dragging the
// whole handler layer into this package.
type fakeCheckDeleter struct {
	dbSvc   db.Service
	deleted []string
}

func (f *fakeCheckDeleter) DeleteCheck(ctx context.Context, _, identifier string) error {
	f.deleted = append(f.deleted, identifier)

	return f.dbSvc.DeleteCheck(ctx, identifier)
}

// newDemoTestContext wires an in-memory database, a real job service and a demo
// configuration, so both the seeding and the sweep run their full paths.
func newDemoTestContext(t *testing.T, enabled bool) (*jobdef.JobContext, db.Service, *fakeCheckDeleter) {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	jobSvc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	deleter := &fakeCheckDeleter{dbSvc: dbSvc}

	cfg := &config.Config{}
	cfg.Demo = config.DemoConfig{
		Enabled:         enabled,
		OrgSlug:         "demo",
		Email:           "demo@solidping.example",
		Password:        "demo",
		CheckTTL:        time.Hour,
		CleanupInterval: 30 * time.Minute,
	}
	cfg.Server.BaseURL = "http://localhost:4000"

	jctx := &jobdef.JobContext{
		Services:  &services.Registry{Jobs: jobSvc, Checks: deleter},
		DB:        dbSvc.DB(),
		DBService: dbSvc,
		Logger:    slog.Default(),
		AppConfig: cfg,
	}

	return jctx, dbSvc, deleter
}

// TestDemoSeedProvisionsTheWholeAccount covers §1 and §4 in one pass: the org,
// its demo flag, the one-hour session cap, the user (role `user`, not
// superadmin, not forced to rotate), the pinned entitlements and the catalog.
func TestDemoSeedProvisionsTheWholeAccount(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jctx, dbSvc, _ := newDemoTestContext(t, true)
	ctx := t.Context()

	run := &StartupJobRun{}
	r.NoError(run.ensureDemoOrganization(ctx, jctx))

	org, err := dbSvc.GetOrganizationBySlug(ctx, "demo")
	r.NoError(err)
	r.NotNil(org)

	flag, err := dbSvc.GetOrgParameter(ctx, org.UID, ParamDemoEnabled)
	r.NoError(err)
	r.NotNil(flag, "the demo org must be findable by its flag, not by its slug")
	r.Equal(true, flag.Value["value"])

	session, err := dbSvc.GetOrgParameter(ctx, org.UID, string(systemconfig.KeySessionMaxDuration))
	r.NoError(err)
	r.NotNil(session, "demo sessions must be capped at an hour")

	user, err := dbSvc.GetUserByEmail(ctx, "demo@solidping.example")
	r.NoError(err)
	r.NotNil(user)
	r.True(user.Demo, "the demo user must carry users.demo — the guard keys off it")
	r.False(user.SuperAdmin, "the demo user must never be a superadmin")
	r.False(user.MustChangePassword,
		"a forced rotation would land on a blocked endpoint and dead-end the demo")
	r.NotNil(user.PasswordHash)
	r.True(passwords.Verify("demo", *user.PasswordHash))

	member, err := dbSvc.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	r.NoError(err)
	r.NotNil(member)
	r.Equal(models.MemberRoleUser, member.Role,
		"role `user`, not viewer: viewer is not enforced as read-only anywhere")

	ent, err := dbSvc.GetOrgEntitlements(ctx, org.UID)
	r.NoError(err)
	r.NotNil(ent, "the demo org's limits must be pinned, not left on the deployment defaults")
	r.NotNil(ent.Payload.Limits.MaxChecks)
	r.NotNil(ent.Payload.Limits.MaxChecksPerMinute)
	r.Equal(demoMaxChecksPerMinute, *ent.Payload.Limits.MaxChecksPerMinute)
	r.NotNil(ent.Payload.Limits.MaxUsers)
	r.Equal(1, *ent.Payload.Limits.MaxUsers)
	r.NotNil(ent.Payload.DisplayName)
	r.Equal(demoDisplayName, *ent.Payload.DisplayName)

	checks, total, err := dbSvc.ListChecks(ctx, org.UID, nil)
	r.NoError(err)
	r.Positive(total, "the demo catalog must not be empty")

	for _, check := range checks {
		r.Nilf(check.CreatedBy,
			"seeded check %s must have created_by = NULL — that is what makes it immutable", check.UID)
	}

	// The catalog's headroom is what visitors get to use.
	r.Equal(int(total)+demoEntitlementHeadroom, *ent.Payload.Limits.MaxChecks)
}

// TestDemoSeedProvisionsTheCatalog covers §5: what a visitor actually LOOKS at.
//
// The two assertions that matter most are the negative ones. "Own targets
// only" is a decision with a real failure mode — walking every registered
// checker would have seeded the Minecraft, NTP-pool and DNSBL samples and
// pointed the whole public region fleet at strangers' servers, forever — and
// the sink rule is what keeps a demo incident from paging a human.
func TestDemoSeedProvisionsTheCatalog(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jctx, dbSvc, _ := newDemoTestContext(t, true)
	ctx := t.Context()

	r.NoError((&StartupJobRun{}).ensureDemoOrganization(ctx, jctx))

	org, err := dbSvc.GetOrganizationBySlug(ctx, "demo")
	r.NoError(err)

	checks, _, err := dbSvc.ListChecks(ctx, org.UID, nil)
	r.NoError(err)
	r.NotEmpty(checks)

	allowedTypes := map[string]bool{
		"http": true, "tcp": true, "icmp": true, "dns": true, "ssl": true, "heartbeat": true,
	}

	for _, check := range checks {
		r.Truef(allowedTypes[check.Type],
			"the demo catalog seeded a %s check; only the demo-owned types may be seeded", check.Type)
		r.NotNilf(check.Slug, "check %s has no slug", check.UID)
		r.Truef(strings.HasPrefix(*check.Slug, "demo-"),
			"check %s is not from the Demo catalog — a checker that ignores ListSampleOptions "+
				"leaked its DEFAULT samples, which probe third-party hosts", *check.Slug)

		// Every check must be grouped and carry the sink escalation policy, or
		// the demo shows an incident with no visible notification trail.
		r.NotNilf(check.CheckGroupUID, "check %s is ungrouped", *check.Slug)
		r.NotNilf(check.EscalationPolicyUID, "check %s has no escalation policy", *check.Slug)
	}

	// The escalation policy must target a SINK, never a person.
	integrations, err := dbSvc.ListChannels(ctx, &models.ListIntegrationsFilter{OrganizationUID: org.UID})
	r.NoError(err)
	r.Len(integrations, 1, "the demo org must have exactly one integration: the sink")
	r.Equal(models.ConnectionTypeWebhook, integrations[0].Type)
	r.Contains(integrations[0].Settings["url"], "/api/v1/fake",
		"the demo's only notification target must be our own fake endpoint")

	pages, err := dbSvc.ListStatusPages(ctx, org.UID)
	r.NoError(err)
	r.NotEmpty(pages, "the demo must publish a status page")

	slos, err := dbSvc.ListSLOs(ctx, org.UID, models.ListSLOsFilter{})
	r.NoError(err)
	r.Len(slos, 2, "the demo needs a healthy SLO and a burning one, not just one of each mood")

	windows, err := dbSvc.ListMaintenanceWindows(ctx, org.UID, models.ListMaintenanceWindowsFilter{})
	r.NoError(err)
	r.Len(windows, 1)
	r.Equal(models.RecurrenceWeekly, windows[0].Recurrence)

	attached, err := dbSvc.ListMaintenanceWindowChecks(ctx, windows[0].UID)
	r.NoError(err)
	r.NotEmpty(attached,
		"a maintenance window with no attached check suppresses NOTHING and is pure decoration")

	// And the pinned entitlements must admit the SLOs the seed just wrote,
	// or the Usage page reads "2 / 0".
	ent, err := dbSvc.GetOrgEntitlements(ctx, org.UID)
	r.NoError(err)
	r.NotNil(ent.Payload.Limits.MaxSlos)
	r.Equal(len(slos), *ent.Payload.Limits.MaxSlos)
}

// TestDemoSeedIsIdempotent is the restart property. It matters more than it
// looks: the seed runs on EVERY boot, against a production database that
// already has organizations, so a non-idempotent step would duplicate the
// catalog (and its history) once per deploy.
func TestDemoSeedIsIdempotent(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jctx, dbSvc, _ := newDemoTestContext(t, true)
	ctx := t.Context()

	run := &StartupJobRun{}
	r.NoError(run.ensureDemoOrganization(ctx, jctx))

	org, err := dbSvc.GetOrganizationBySlug(ctx, "demo")
	r.NoError(err)

	_, firstCount, err := dbSvc.ListChecks(ctx, org.UID, nil)
	r.NoError(err)

	firstResults := countRawResults(t, dbSvc)

	// Three more boots.
	for range 3 {
		r.NoError(run.ensureDemoOrganization(ctx, jctx))
	}

	_, secondCount, err := dbSvc.ListChecks(ctx, org.UID, nil)
	r.NoError(err)
	r.Equal(firstCount, secondCount, "a restart duplicated the demo catalog")

	r.Equal(firstResults, countRawResults(t, dbSvc), "a restart duplicated the backfilled history")

	// And still exactly one org and one user.
	orgs, err := dbSvc.ListOrganizations(ctx)
	r.NoError(err)

	demoOrgs := 0

	for _, o := range orgs {
		if o.Slug == "demo" {
			demoOrgs++
		}
	}

	r.Equal(1, demoOrgs)
}

// TestDemoSeedBackfillsHistory covers §5's backfill: the charts must not be
// empty on launch day.
func TestDemoSeedBackfillsHistory(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jctx, dbSvc, _ := newDemoTestContext(t, true)
	ctx := t.Context()

	r.NoError((&StartupJobRun{}).ensureDemoOrganization(ctx, jctx))

	org, err := dbSvc.GetOrganizationBySlug(ctx, "demo")
	r.NoError(err)

	r.Positive(countRawResults(t, dbSvc), "the demo must start with synthetic history")

	backfilled, err := dbSvc.GetOrgParameter(ctx, org.UID, ParamDemoBackfilled)
	r.NoError(err)
	r.NotNil(backfilled, "the backfill must be guarded so a restart never doubles it")
}

// TestDemoSeedIsANoOpWhenDisabled is the self-hosted protection: pulling a new
// image must never wake up with a world-readable account on it.
func TestDemoSeedIsANoOpWhenDisabled(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jctx, dbSvc, _ := newDemoTestContext(t, false)
	ctx := t.Context()

	r.NoError((&StartupJobRun{}).ensureDemoOrganization(ctx, jctx))

	org, err := dbSvc.GetOrganizationBySlug(ctx, "demo")
	r.True(err != nil || org == nil, "a disabled demo must create no organization")

	user, err := dbSvc.GetUserByEmail(ctx, "demo@solidping.example")
	r.True(err != nil || user == nil, "a disabled demo must create no user")
}

// TestDemoCleanupExpiresOnlyOwnedAndExpiredChecks is §6.1. Three checks, one of
// each shape, and only one may go.
func TestDemoCleanupExpiresOnlyOwnedAndExpiredChecks(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jctx, dbSvc, deleter := newDemoTestContext(t, true)
	ctx := t.Context()

	r.NoError((&StartupJobRun{}).ensureDemoOrganization(ctx, jctx))

	org, err := dbSvc.GetOrganizationBySlug(ctx, "demo")
	r.NoError(err)

	user, err := dbSvc.GetUserByEmail(ctx, "demo@solidping.example")
	r.NoError(err)

	old := time.Now().Add(-2 * time.Hour)
	fresh := time.Now().Add(-5 * time.Minute)

	expired := seedDemoCheck(t, dbSvc, org.UID, "visitor-expired", &user.UID, old)
	recent := seedDemoCheck(t, dbSvc, org.UID, "visitor-recent", &user.UID, fresh)
	seededUID := seedDemoCheck(t, dbSvc, org.UID, "seeded-old", nil, old)

	r.NoError((&DemoCleanupJobRun{}).Run(ctx, jctx))

	r.Equal([]string{expired}, deleter.deleted,
		"the sweep must delete exactly the expired, demo-owned check")
	r.NotContains(deleter.deleted, recent, "a fresh visitor check must survive")
	r.NotContains(deleter.deleted, seededUID,
		"a seeded (created_by NULL) check must never expire, however old")
}

// TestDemoCleanupHealsATamperedIdentity is §6.2 — the self-healing half.
// Whatever slipped past the guard, and whatever an operator fat-fingered, is
// undone within one interval.
func TestDemoCleanupHealsATamperedIdentity(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jctx, dbSvc, _ := newDemoTestContext(t, true)
	ctx := t.Context()

	r.NoError((&StartupJobRun{}).ensureDemoOrganization(ctx, jctx))

	user, err := dbSvc.GetUserByEmail(ctx, "demo@solidping.example")
	r.NoError(err)

	// Tamper: rotate the password, grant superadmin, force a rotation, and
	// clear the demo flag — i.e. every way the shared account could be turned
	// into something dangerous or unusable.
	tampered, err := passwords.Hash("hijacked")
	r.NoError(err)

	yes := true
	no := false
	r.NoError(dbSvc.UpdateUser(ctx, user.UID, &models.UserUpdate{
		PasswordHash:       &tampered,
		SuperAdmin:         &yes,
		MustChangePassword: &yes,
		Demo:               &no,
	}))

	r.NoError((&DemoCleanupJobRun{}).Run(ctx, jctx))

	healed, err := dbSvc.GetUser(ctx, user.UID)
	r.NoError(err)
	r.True(passwords.Verify("demo", *healed.PasswordHash), "the published password must be restored")
	r.False(healed.SuperAdmin)
	r.False(healed.MustChangePassword)
	r.True(healed.Demo, "clearing users.demo would produce an UNGUARDED shared account")
}

// TestDemoCleanupIsANoOpWhenDisabled pins §6.3.
func TestDemoCleanupIsANoOpWhenDisabled(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jctx, dbSvc, deleter := newDemoTestContext(t, true)
	ctx := t.Context()

	r.NoError((&StartupJobRun{}).ensureDemoOrganization(ctx, jctx))

	org, err := dbSvc.GetOrganizationBySlug(ctx, "demo")
	r.NoError(err)

	user, err := dbSvc.GetUserByEmail(ctx, "demo@solidping.example")
	r.NoError(err)

	seedDemoCheck(t, dbSvc, org.UID, "visitor-expired", &user.UID, time.Now().Add(-2*time.Hour))

	// Flip the demo off and sweep again.
	jctx.AppConfig.Demo.Enabled = false
	r.NoError((&DemoCleanupJobRun{}).Run(ctx, jctx))
	r.Empty(deleter.deleted, "a disabled demo must delete nothing")
}

// TestDemoCleanupRefusesAnOrgThatIsNotFlagged is the blast-radius guard: the
// slug is configuration, configuration can be wrong, and deleting checks out of
// a real customer's organization is not a mistake that can be walked back.
func TestDemoCleanupRefusesAnOrgThatIsNotFlagged(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jctx, dbSvc, deleter := newDemoTestContext(t, true)
	ctx := t.Context()

	// A REAL org that happens to sit on the configured demo slug, with no
	// demo.enabled parameter on it.
	org := models.NewOrganization("demo", "Definitely Not The Demo")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("demo@solidping.example")
	user.Demo = true
	r.NoError(dbSvc.CreateUser(ctx, user))

	seedDemoCheck(t, dbSvc, org.UID, "customer-check", &user.UID, time.Now().Add(-2*time.Hour))

	r.NoError((&DemoCleanupJobRun{}).Run(ctx, jctx))
	r.Empty(deleter.deleted, "the sweep deleted checks out of an org that is not flagged as the demo")
}

// countRawResults totals the raw result rows in the database. The demo org is
// the only org in these fixtures, so an instance-wide count is the org's count.
func countRawResults(t *testing.T, dbSvc db.Service) int64 {
	t.Helper()

	byType, err := dbSvc.CountResultsByPeriodType(t.Context())
	require.NoError(t, err)

	return byType["raw"]
}

// seedDemoCheck inserts one check with an explicit creator and creation time.
func seedDemoCheck(
	t *testing.T, dbSvc db.Service, orgUID, slug string, createdBy *string, createdAt time.Time,
) string {
	t.Helper()

	check := models.NewCheck(orgUID, slug, "http")
	name := slug
	check.Name = &name
	check.Config = models.JSONMap{"url": "https://example.com"}
	check.CreatedBy = createdBy
	check.CreatedAt = createdAt

	require.NoError(t, dbSvc.CreateCheck(t.Context(), check))

	return check.UID
}

// TestDemoMaxChecksDoesNotRatchetUnderTraffic is §4's cap, stated as the
// property that actually matters.
//
// The first implementation computed maxChecks from the LIVE check total plus
// the headroom, and the cleanup job re-pinned on the same formula every thirty
// minutes. Under exactly the traffic the cap exists to bound, that climbs
// monotonically: every visitor check raises the total, the next sweep raises
// the ceiling, and the demo's load bounding quietly stops bounding anything.
//
// The cap must be a CONSTANT — seeded count plus headroom — so this asserts the
// number is unchanged after visitors have created checks and a sweep has run.
func TestDemoMaxChecksDoesNotRatchetUnderTraffic(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jctx, dbSvc, _ := newDemoTestContext(t, true)
	ctx := t.Context()

	r.NoError((&StartupJobRun{}).ensureDemoOrganization(ctx, jctx))

	org, err := dbSvc.GetOrganizationBySlug(ctx, "demo")
	r.NoError(err)

	user, err := dbSvc.GetUserByEmail(ctx, "demo@solidping.example")
	r.NoError(err)

	before, err := dbSvc.GetOrgEntitlements(ctx, org.UID)
	r.NoError(err)
	r.NotNil(before.Payload.Limits.MaxChecks)

	seededCount, _, err := dbSvc.ListChecks(ctx, org.UID, nil)
	r.NoError(err)
	r.Equal(len(seededCount)+demoEntitlementHeadroom, *before.Payload.Limits.MaxChecks,
		"the cap must be the seeded count plus the headroom")

	// Ten visitors do the one thing the demo is for.
	for i := range 10 {
		seedDemoCheck(t, dbSvc, org.UID, "visitor-check-"+string(rune('a'+i)), &user.UID, time.Now())
	}

	// A sweep (which re-pins) and another boot (which also re-pins).
	r.NoError((&DemoCleanupJobRun{}).Run(ctx, jctx))
	r.NoError((&StartupJobRun{}).ensureDemoOrganization(ctx, jctx))

	after, err := dbSvc.GetOrgEntitlements(ctx, org.UID)
	r.NoError(err)
	r.NotNil(after.Payload.Limits.MaxChecks)
	r.Equalf(*before.Payload.Limits.MaxChecks, *after.Payload.Limits.MaxChecks,
		"maxChecks ratcheted from %d to %d under visitor traffic",
		*before.Payload.Limits.MaxChecks, *after.Payload.Limits.MaxChecks)
}

// TestDemoSeededCheckCountSurvivesAMissingParameter covers the back-fill path:
// a demo org seeded by a binary that predates demo.seeded_checks must still
// pin a constant cap, derived from the NULL-owned (i.e. seeded) rows rather
// than from the live total.
func TestDemoSeededCheckCountSurvivesAMissingParameter(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jctx, dbSvc, _ := newDemoTestContext(t, true)
	ctx := t.Context()

	r.NoError((&StartupJobRun{}).ensureDemoOrganization(ctx, jctx))

	org, err := dbSvc.GetOrganizationBySlug(ctx, "demo")
	r.NoError(err)

	user, err := dbSvc.GetUserByEmail(ctx, "demo@solidping.example")
	r.NoError(err)

	seeded, _, err := dbSvc.ListChecks(ctx, org.UID, nil)
	r.NoError(err)

	// Simulate the older schema: drop the parameter, then add visitor traffic.
	r.NoError(dbSvc.SetOrgParameter(ctx, org.UID, ParamDemoSeededChecks, nil, false))
	seedDemoCheck(t, dbSvc, org.UID, "visitor-one", &user.UID, time.Now())
	seedDemoCheck(t, dbSvc, org.UID, "visitor-two", &user.UID, time.Now())

	r.Equal(len(seeded), (&StartupJobRun{}).demoSeededCheckCount(ctx, jctx, org),
		"the fallback counted visitor checks as seeded ones")
}

// TestDemoSeededHeartbeatCarriesAPingToken is the concrete defect behind §5's
// catalog: the heartbeat sample ships an empty config on purpose, because
// Validate is what MINTS the token. The demo seeding path used to be the only
// seeding path that skipped Validate, so the demo's heartbeat landed with no
// ping URL to copy — and rotate-token is not on the demo write allowlist, so it
// could not be repaired from inside the demo either.
func TestDemoSeededHeartbeatCarriesAPingToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jctx, dbSvc, _ := newDemoTestContext(t, true)
	ctx := t.Context()

	r.NoError((&StartupJobRun{}).ensureDemoOrganization(ctx, jctx))

	org, err := dbSvc.GetOrganizationBySlug(ctx, "demo")
	r.NoError(err)

	checks, _, err := dbSvc.ListChecks(ctx, org.UID, nil)
	r.NoError(err)

	found := 0

	for _, check := range checks {
		if check.Type != "heartbeat" {
			continue
		}

		found++

		token, ok := check.Config["token"].(string)
		r.Truef(ok, "the seeded heartbeat %s has no token in its config", *check.Slug)
		r.NotEmptyf(token, "the seeded heartbeat %s has an EMPTY ping token", *check.Slug)
	}

	r.Positive(found, "the demo catalog must seed a heartbeat — it is the clearest dead-man's-switch demo")
}

// TestDemoCatalogPinsPublicRegions is §5's "three or more public regions on
// every check", asserted rather than assumed.
//
// Leaving check.Regions empty resolves through the org default, then the system
// default, then every defined region. In production that happens to be the full
// public fleet — but on an instance with a narrow default_regions the demo's
// headline claim would silently become a single-region one. The fixture here
// defines four regions precisely so "it got three" is a real result and not an
// artifact of there being only one to pick.
func TestDemoCatalogPinsPublicRegions(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jctx, dbSvc, _ := newDemoTestContext(t, true)
	ctx := t.Context()

	defs := []regions.RegionDefinition{
		{Slug: "eu2", Name: "Europe"},
		{Slug: "us1", Name: "United States"},
		{Slug: "jp1", Name: "Japan"},
		{Slug: "default", Name: "Default"},
	}
	r.NoError(dbSvc.SetSystemParameter(ctx, regions.ParamRegions, defs, false))

	r.NoError((&StartupJobRun{}).ensureDemoOrganization(ctx, jctx))

	org, err := dbSvc.GetOrganizationBySlug(ctx, "demo")
	r.NoError(err)

	checks, _, err := dbSvc.ListChecks(ctx, org.UID, nil)
	r.NoError(err)
	r.NotEmpty(checks)

	for _, check := range checks {
		r.Lenf(check.Regions, 3, "check %s does not run from three public regions", *check.Slug)

		for _, region := range check.Regions {
			r.Falsef(regions.IsPrivateRegion(region),
				"check %s runs from the PRIVATE region %s", *check.Slug, region)
		}
	}
}

// TestDemoCatalogTakesWhatRegionsExist is the dev-laptop case: one defined
// region must produce a one-region catalog, not a failed boot.
func TestDemoCatalogTakesWhatRegionsExist(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jctx, dbSvc, _ := newDemoTestContext(t, true)
	ctx := t.Context()

	r.NoError((&StartupJobRun{}).ensureDemoOrganization(ctx, jctx))

	org, err := dbSvc.GetOrganizationBySlug(ctx, "demo")
	r.NoError(err)

	checks, _, err := dbSvc.ListChecks(ctx, org.UID, nil)
	r.NoError(err)
	r.NotEmpty(checks, "seeding must succeed with fewer than three regions defined")

	for _, check := range checks {
		r.NotEmptyf(check.Regions, "check %s was left with no region at all", *check.Slug)
	}
}
