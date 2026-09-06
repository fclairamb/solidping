package jobtypes

import (
	"context"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/synthdata"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// Org-parameter keys the demo seeding is idempotent on.
const (
	// ParamDemoEnabled marks an org as THE demo org. Everything that has to
	// find the demo org later — the cleanup job most of all — reads this rather
	// than comparing slugs, so renaming the configured slug cannot orphan a
	// live demo org.
	ParamDemoEnabled = "demo.enabled"
	// ParamDemoBackfilled records that the one-shot synthetic history has been
	// written. Without it a restart would double the history, and a restart
	// loop would multiply it.
	ParamDemoBackfilled = "demo.backfilled"
	// ParamSamplesLoaded is the existing per-org "samples already seeded" flag,
	// reused unchanged for the demo catalog.
	ParamSamplesLoaded = "samples.loaded"
)

// demoBackfillDays is how much synthetic history the demo starts with, so the
// charts are not empty on launch day. Real results take over from there.
const demoBackfillDays = 30

// demoBackfillPeriod is the sampling interval of the synthetic history.
//
// MUCH coarser than the checks' own 60s period, deliberately. 30 days at 60s is
// 43,200 rows PER CHECK — a seed that would write half a million rows before
// the server finishes booting. The aggregation job rolls raw results up to
// hourly buckets anyway, and every chart wider than a day reads from those, so
// an hourly sample is visually identical to a per-minute one at the timescales
// a backfill exists to fill. Real per-minute results take over from the moment
// the workers pick the checks up.
const demoBackfillPeriod = time.Hour

// demoEntitlementHeadroom is how many checks visitors may add on top of the
// seeded catalog before MaxChecks refuses. The cap is what bounds the demo's
// load no matter how many people show up at once.
const demoEntitlementHeadroom = 20

// demoMaxChecksPerMinute caps the whole demo org's aggregate execution rate.
const demoMaxChecksPerMinute = 30

// demoDisplayName is what the Usage page calls the demo's plan.
const demoDisplayName = "Live demo"

// demoDisplayEmoji accompanies it.
const demoDisplayEmoji = "🎬"

// demoSessionMaxDurationSeconds caps a demo session at an hour. Visitors then
// log in again with one click; the credential is on the page.
const demoSessionMaxDurationSeconds = 3600

// ensureDemoOrganization provisions the shared public live demo (spec
// 2026-09-06-02): a real org, a real user, a catalog, a sink escalation
// policy, pinned entitlements and a 30-day backfill.
//
// It runs on EVERY startup and is idempotent at every step, because a
// production demo is provisioned onto a database that already has
// organizations — which is exactly the case ensureDefaultOrganization returns
// early from. Piggy-backing on that function would have meant the demo only
// ever appearing on a virgin database, i.e. never in production.
//
// Every failure below is non-fatal and logged: a broken demo must never keep
// the server from booting.
func (r *StartupJobRun) ensureDemoOrganization(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	cfg := demoConfigFrom(jctx)
	if !cfg.Enabled {
		log.DebugContext(ctx, "Demo disabled, skipping demo org provisioning")

		return nil
	}

	org, err := r.ensureDemoOrg(ctx, jctx, cfg)
	if err != nil {
		log.ErrorContext(ctx, "Failed to provision demo organization (non-fatal)", "error", err)

		return nil
	}

	user, err := r.ensureDemoUser(ctx, jctx, cfg, org)
	if err != nil {
		log.ErrorContext(ctx, "Failed to provision demo user (non-fatal)", "error", err)

		return nil
	}

	if seedErr := r.ensureDemoContent(ctx, jctx, org); seedErr != nil {
		log.ErrorContext(ctx, "Failed to seed demo content (non-fatal)", "error", seedErr)
	}

	// AFTER the catalog, not before: MaxChecks is "the seeded count plus the
	// visitors' headroom", and computing it against an empty org would hand the
	// demo a cap below its own catalog — visitors could then create nothing
	// at all, which is the one thing the demo exists for.
	if entErr := r.ensureDemoEntitlements(ctx, jctx, org); entErr != nil {
		log.ErrorContext(ctx, "Failed to pin demo entitlements (non-fatal)", "error", entErr)
	}

	if jctx.Services != nil && jctx.Services.Jobs != nil {
		if _, cleanupErr := jctx.Services.Jobs.CreateJob(
			ctx, "", string(jobdef.JobTypeDemoCleanup), nil, nil,
		); cleanupErr != nil {
			log.InfoContext(ctx, "Failed to create demo cleanup job (non-fatal)", "error", cleanupErr)
		}
	}

	log.InfoContext(ctx, "Demo organization ready",
		"org", org.Slug, "orgUID", org.UID, "userUID", user.UID)

	return nil
}

// demoConfigFrom reads the demo configuration off the job context, falling back
// to a disabled default when there is no app config (unit tests that build a
// bare JobContext).
func demoConfigFrom(jctx *jobdef.JobContext) config.DemoConfig {
	if jctx == nil || jctx.AppConfig == nil {
		return config.DemoConfig{}
	}

	return jctx.AppConfig.Demo
}

// ensureDemoOrg finds or creates the demo organization, idempotent by slug.
func (r *StartupJobRun) ensureDemoOrg(
	ctx context.Context, jctx *jobdef.JobContext, cfg config.DemoConfig,
) (*models.Organization, error) {
	slug := cfg.ResolvedOrgSlug()

	org, err := jctx.DBService.GetOrganizationBySlug(ctx, slug)
	if err == nil && org != nil {
		return org, r.markDemoOrg(ctx, jctx, org)
	}

	org = models.NewOrganization(slug, "Live demo")
	if createErr := jctx.DBService.CreateOrganization(ctx, org); createErr != nil {
		return nil, fmt.Errorf("failed to create demo organization: %w", createErr)
	}

	jctx.Logger.InfoContext(ctx, "Created demo organization", "slug", slug, "uid", org.UID)

	return org, r.markDemoOrg(ctx, jctx, org)
}

// markDemoOrg sets the demo.enabled flag and the one-hour session cap. Both are
// re-asserted on every boot, so an operator who cleared them by hand gets them
// back rather than a demo that silently stopped behaving like one.
func (r *StartupJobRun) markDemoOrg(
	ctx context.Context, jctx *jobdef.JobContext, org *models.Organization,
) error {
	if err := jctx.DBService.SetOrgParameter(ctx, org.UID, ParamDemoEnabled, true, false); err != nil {
		return fmt.Errorf("failed to set %s: %w", ParamDemoEnabled, err)
	}

	if err := jctx.DBService.SetOrgParameter(
		ctx, org.UID, string(systemconfig.KeySessionMaxDuration), demoSessionMaxDurationSeconds, false,
	); err != nil {
		return fmt.Errorf("failed to cap demo session duration: %w", err)
	}

	return nil
}

// ensureDemoUser finds or creates the demo user and its membership, and
// reconciles the identity on every boot.
//
// Role `user`, never viewer: creating a check is the whole point, and the
// viewer role is not enforced as read-only anywhere today, so leaning on it
// would be leaning on a guarantee that does not exist. Never SuperAdmin. Never
// MustChangePassword — a forced rotation would land on an endpoint the demo
// guard blocks and dead-end the demo on its first click.
func (r *StartupJobRun) ensureDemoUser(
	ctx context.Context, jctx *jobdef.JobContext, cfg config.DemoConfig, org *models.Organization,
) (*models.User, error) {
	email := cfg.ResolvedEmail()

	hash, err := passwords.Hash(cfg.ResolvedPassword())
	if err != nil {
		return nil, fmt.Errorf("failed to hash demo password: %w", err)
	}

	user, err := jctx.DBService.GetUserByEmail(ctx, email)
	if err != nil || user == nil {
		user = models.NewUser(email)
		user.Name = "Live demo"
		user.PasswordHash = &hash
		user.Demo = true

		if createErr := jctx.DBService.CreateUser(ctx, user); createErr != nil {
			return nil, fmt.Errorf("failed to create demo user: %w", createErr)
		}

		jctx.Logger.InfoContext(ctx, "Created demo user", "email", email, "uid", user.UID)
	} else if reconcileErr := ReconcileDemoUser(ctx, jctx.DBService, user, hash); reconcileErr != nil {
		return nil, reconcileErr
	}

	if memberErr := ensureDemoMembership(ctx, jctx, org, user); memberErr != nil {
		return nil, memberErr
	}

	return user, nil
}

// ReconcileDemoUser puts the demo identity back to its intended shape:
// the configured password, the demo flag on, no superadmin, no forced rotation.
//
// Exported because the demo cleanup job runs exactly the same reconciliation
// every 30 minutes — anything that slipped past the write guard, or an operator
// who fat-fingered the demo user in the superadmin UI, is undone within half an
// hour. One function, so boot and sweep can never drift apart.
func ReconcileDemoUser(
	ctx context.Context, dbService demoUserStore, user *models.User, passwordHash string,
) error {
	falseValue := false
	trueValue := true

	update := &models.UserUpdate{
		PasswordHash:       &passwordHash,
		Demo:               &trueValue,
		SuperAdmin:         &falseValue,
		MustChangePassword: &falseValue,
		TOTPEnabled:        &falseValue,
	}

	if err := dbService.UpdateUser(ctx, user.UID, update); err != nil {
		return fmt.Errorf("failed to reconcile demo user: %w", err)
	}

	return nil
}

// demoUserStore is the narrow slice of db.Service ReconcileDemoUser needs.
type demoUserStore interface {
	UpdateUser(ctx context.Context, uid string, update *models.UserUpdate) error
}

// ensureDemoMembership makes the demo user a plain `user` of the demo org, and
// only of the demo org.
func ensureDemoMembership(
	ctx context.Context, jctx *jobdef.JobContext, org *models.Organization, user *models.User,
) error {
	existing, err := jctx.DBService.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	if err == nil && existing != nil {
		return nil
	}

	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleUser)
	now := time.Now()
	member.JoinedAt = &now

	if createErr := jctx.DBService.CreateOrganizationMember(ctx, member); createErr != nil {
		return fmt.Errorf("failed to create demo membership: %w", createErr)
	}

	return nil
}

// ensureDemoEntitlements pins the demo org's limits.
//
// BILLING SAFETY (verified in ../solidping-billing at f583fe7, and the reason
// this spec touches no other repository): every one of billing's three
// entitlement pushes is reachable only from a Polar subscription resolved to a
// customer carrying an org_slug. Reconciler.RunOnce iterates
// polar.ListSubscriptions, never the org table, and reconcileSub returns early
// with "customer has no org_slug in metadata, skipping" when no slug resolves.
// An org with no Polar customer — which the demo org will never have — is
// therefore never pushed to, so this pinned row cannot be overwritten.
//
// That is the assumption to re-check if billing ever grows an all-orgs sweep.
func (r *StartupJobRun) ensureDemoEntitlements(
	ctx context.Context, jctx *jobdef.JobContext, org *models.Organization,
) error {
	seeded := 0
	if _, total, listErr := jctx.DBService.ListChecks(ctx, org.UID, nil); listErr == nil {
		seeded = int(total)
	}

	maxChecks := seeded + demoEntitlementHeadroom
	maxUsers := 1
	perMinute := demoMaxChecksPerMinute
	zero := 0
	displayName := demoDisplayName
	displayEmoji := demoDisplayEmoji

	ent := models.NewOrgEntitlements(org.UID, models.EntitlementSourceAdmin)
	ent.Payload.Limits = models.EntitlementLimits{
		MaxChecks:           &maxChecks,
		MaxUsers:            &maxUsers,
		MaxChecksPerMinute:  &perMinute,
		MaxCustomDomains:    &zero,
		MaxDeportedAgents:   &zero,
		MaxSmsPerMonth:      &zero,
		MaxCallsPerMonth:    &zero,
		MaxWhatsappPerMonth: &zero,
		MaxSlos:             &zero,
	}
	ent.Payload.DisplayName = &displayName
	ent.Payload.DisplayEmoji = &displayEmoji

	reason := "public live demo (spec 2026-09-06-02)"
	audit := models.NewOrgEntitlementAudit(
		org.UID, string(models.EntitlementSourceAdmin), "system:demo-seed", nil, nil, &reason)

	if upsertErr := jctx.DBService.UpsertOrgEntitlements(ctx, ent, audit); upsertErr != nil {
		return fmt.Errorf("failed to pin demo entitlements: %w", upsertErr)
	}

	return nil
}

// ensureDemoContent seeds the catalog, the sink escalation policy and the
// backfill — each behind its own idempotence flag.
func (r *StartupJobRun) ensureDemoContent(
	ctx context.Context, jctx *jobdef.JobContext, org *models.Organization,
) error {
	policyUID, err := r.ensureDemoSinkPolicy(ctx, jctx, org)
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to seed demo sink escalation policy (non-fatal)", "error", err)
	}

	if loadErr := r.loadDemoCatalog(ctx, jctx, org, policyUID); loadErr != nil {
		return loadErr
	}

	return r.backfillDemoHistory(ctx, jctx, org)
}

// ensureDemoSinkPolicy creates the webhook integration and the one-step
// escalation policy that make the demo's incidents produce a VISIBLE
// notification trail without a byte reaching a real person.
//
// The webhook points at our own /api/v1/fake. Demo sessions cannot create or
// edit integrations (the write guard refuses every route but the four check
// ones), so this cannot be widened from the inside into a way of making the
// server POST at a target of a visitor's choosing.
func (r *StartupJobRun) ensureDemoSinkPolicy(
	ctx context.Context, jctx *jobdef.JobContext, org *models.Organization,
) (*string, error) {
	policies, err := jctx.DBService.ListEscalationPolicies(ctx, org.UID)
	if err == nil {
		for _, policy := range policies {
			if policy.Name == demoPolicyName {
				uid := policy.UID

				return &uid, nil
			}
		}
	}

	sink := models.NewIntegration(org.UID, models.ConnectionTypeWebhook, demoSinkIntegrationName)
	sink.Settings["url"] = demoBaseURL(jctx) + "/api/v1/fake?supportedMethod=POST"
	sink.IsDefault = true

	if createErr := jctx.DBService.CreateChannel(ctx, sink); createErr != nil {
		return nil, fmt.Errorf("failed to create demo sink integration: %w", createErr)
	}

	policy := models.NewEscalationPolicy(org.UID, demoPolicyName)
	description := "Notifications from the live demo go to a sink, never to a person."
	policy.Description = &description

	if createErr := jctx.DBService.CreateEscalationPolicy(ctx, policy); createErr != nil {
		return nil, fmt.Errorf("failed to create demo escalation policy: %w", createErr)
	}

	step := models.NewEscalationPolicyStep(policy.UID, 0, 0)
	target := models.NewEscalationPolicyTarget(step.UID, models.EscalationTargetConnection, &sink.UID, 0)

	if replaceErr := jctx.DBService.ReplaceEscalationPolicySteps(
		ctx, policy.UID,
		[]*models.EscalationPolicyStep{step},
		map[int][]*models.EscalationPolicyTarget{0: {target}},
	); replaceErr != nil {
		return nil, fmt.Errorf("failed to attach demo escalation step: %w", replaceErr)
	}

	uid := policy.UID

	return &uid, nil
}

const (
	demoSinkIntegrationName = "Demo sink (goes nowhere)"
	demoPolicyName          = "Demo escalation (sink)"
	demoCheckGroupName      = "Demo services"
	demoStatusPageName      = "Live demo status"
	demoStatusPageSlug      = "demo"
)

// demoBaseURL is the instance's own base URL, which every demo target is under.
func demoBaseURL(jctx *jobdef.JobContext) string {
	if jctx != nil && jctx.AppConfig != nil && jctx.AppConfig.Server.BaseURL != "" {
		return jctx.AppConfig.Server.BaseURL
	}

	return "http://localhost:4000"
}

// loadDemoCatalog seeds the checkerdef.Demo samples into the demo org,
// exactly as loadSampleChecks seeds Default into `default`, plus the demo's
// group, labels, status page and escalation policy.
//
// Guarded by the same samples.loaded org parameter, so a restart never
// duplicates it. Checks created here go through db.CreateCheck directly and so
// carry created_by = NULL — which is precisely what makes them untouchable to a
// demo session, with no "protected" column anywhere.
func (r *StartupJobRun) loadDemoCatalog(
	ctx context.Context, jctx *jobdef.JobContext, org *models.Organization, policyUID *string,
) error {
	log := jctx.Logger

	param, err := jctx.DBService.GetOrgParameter(ctx, org.UID, ParamSamplesLoaded)
	if err != nil {
		return fmt.Errorf("failed to read %s for demo org: %w", ParamSamplesLoaded, err)
	}

	if param != nil {
		if loaded, ok := param.Value["value"].(bool); ok && loaded {
			return nil
		}
	}

	group := models.NewCheckGroup(org.UID, demoCheckGroupName, "demo-services")
	if groupErr := jctx.DBService.CreateCheckGroup(ctx, group); groupErr != nil {
		log.WarnContext(ctx, "Failed to create demo check group (non-fatal)", "error", groupErr)

		group = nil
	}

	opts := &checkerdef.ListSampleOptions{Type: checkerdef.Demo, BaseURL: demoBaseURL(jctx)}

	count := 0

	for _, checkType := range demoCatalogTypes {
		loaded, loadErr := r.loadDemoSamplesForChecker(ctx, jctx, org, checkType, opts, group, policyUID)
		if loadErr != nil {
			return loadErr
		}

		count += loaded
	}

	log.InfoContext(ctx, "Loaded demo catalog", "checks", count)

	if pageErr := r.ensureDemoStatusPage(ctx, jctx, org); pageErr != nil {
		log.WarnContext(ctx, "Failed to create demo status page (non-fatal)", "error", pageErr)
	}

	if paramErr := jctx.DBService.SetOrgParameter(ctx, org.UID, ParamSamplesLoaded, true, false); paramErr != nil {
		return fmt.Errorf("failed to set %s for demo org: %w", ParamSamplesLoaded, paramErr)
	}

	return nil
}

// loadDemoSamplesForChecker seeds one checker's Demo samples.
func (r *StartupJobRun) loadDemoSamplesForChecker(
	ctx context.Context,
	jctx *jobdef.JobContext,
	org *models.Organization,
	checkType checkerdef.CheckType,
	opts *checkerdef.ListSampleOptions,
	group *models.CheckGroup,
	policyUID *string,
) (int, error) {
	samples := demoSamplesFor(checkType, opts)
	count := 0

	for i := range samples {
		check := models.NewCheck(org.UID, samples[i].Slug, string(checkType))
		name := samples[i].Name
		check.Name = &name
		check.Config = samples[i].Config
		check.Enabled = true
		check.Period = timeutils.Duration(samples[i].Period)
		check.EscalationPolicyUID = policyUID

		if group != nil {
			check.CheckGroupUID = &group.UID
		}

		// created_by stays NULL: nobody created these, and that is what makes
		// them immutable to a demo session.
		if createErr := jctx.DBService.CreateCheck(ctx, check); createErr != nil {
			return count, fmt.Errorf("failed to create demo check %s: %w", samples[i].Slug, createErr)
		}

		if label, labelErr := jctx.DBService.GetOrCreateLabel(ctx, org.UID, "env", "demo"); labelErr == nil {
			_ = jctx.DBService.SetCheckLabels(ctx, check.UID, []string{label.UID})
		}

		count++
	}

	return count, nil
}

// demoCatalogTypes is the ONLY set of check types the demo catalog is drawn
// from, and it is enumerated rather than derived from ListCheckTypes.
//
// That is not belt-and-braces, it is a correctness requirement. Most checkers'
// GetSampleConfigs takes `_ *ListSampleOptions` and returns its default
// samples unconditionally — walking every registered type would therefore seed
// the demo org with the Minecraft, NTP-pool, DNSBL and browser samples, every
// one of which probes somebody else's servers from every public region,
// forever. "Own targets only" is the spec's decision and this list is what
// enforces it.
//
// It also matches the demo's write allowlist (checks.DemoAllowedCheckTypes)
// plus heartbeat, which the catalog may seed but a visitor may not create.
//
//nolint:gochecknoglobals // Effectively a constant table; Go has no const slices.
var demoCatalogTypes = []checkerdef.CheckType{
	checkerdef.CheckTypeHTTP,
	checkerdef.CheckTypeTCP,
	checkerdef.CheckTypeICMP,
	checkerdef.CheckTypeDNS,
	checkerdef.CheckTypeSSL,
	checkerdef.CheckTypeHeartbeat,
}

// demoSamplesFor returns a checker's Demo sample configs, or nothing when the
// checker provides none. Only the types in demoCatalogTypes are ever asked —
// see that list for why walking the whole registry would be wrong.
func demoSamplesFor(checkType checkerdef.CheckType, opts *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	checker, found := registry.GetChecker(checkType)
	if !found {
		return nil
	}

	provider, ok := checker.(checkerdef.CheckerSamplesProvider)
	if !ok {
		return nil
	}

	return provider.GetSampleConfigs(opts)
}

// ensureDemoStatusPage publishes one public status page listing the catalog.
func (r *StartupJobRun) ensureDemoStatusPage(
	ctx context.Context, jctx *jobdef.JobContext, org *models.Organization,
) error {
	page := models.NewStatusPage(org.UID, demoStatusPageName, demoStatusPageSlug)
	page.IsDefault = true

	return jctx.DBService.CreateStatusPage(ctx, page)
}

// backfillDemoHistory writes 30 days of synthetic results for the seeded
// catalog, once, so the charts are not empty on launch day. Real results take
// over from the moment the workers pick the checks up.
//
// Guarded by its own org parameter rather than by samples.loaded: the two
// answer different questions ("does the catalog exist" vs "has history been
// written"), and a restart between them must not skip the second.
func (r *StartupJobRun) backfillDemoHistory(
	ctx context.Context, jctx *jobdef.JobContext, org *models.Organization,
) error {
	param, err := jctx.DBService.GetOrgParameter(ctx, org.UID, ParamDemoBackfilled)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", ParamDemoBackfilled, err)
	}

	if param != nil {
		if done, ok := param.Value["value"].(bool); ok && done {
			return nil
		}
	}

	checks, _, err := jctx.DBService.ListChecks(ctx, org.UID, nil)
	if err != nil {
		return fmt.Errorf("failed to list demo checks for backfill: %w", err)
	}

	start := time.Now().Add(-demoBackfillDays * 24 * time.Hour)
	total := 0

	for i, check := range checks {
		written, genErr := synthdata.Generate(ctx, jctx.DBService, &synthdata.Options{
			OrganizationUID: org.UID,
			CheckUID:        check.UID,
			Start:           start,
			Period:          demoBackfillPeriod,
			AvgDurationMs:   demoBackfillAvgDurationMs(i),
			FailureRate:     demoBackfillFailureRate(i),
			FailureBurstSec: demoBackfillBurstSeconds,
			// A fixed seed per check keeps a re-seeded demo (a fresh database,
			// a new environment) looking the same, which makes screenshots and
			// docs reproducible.
			Seed: int64(i + 1),
		})
		if genErr != nil {
			jctx.Logger.WarnContext(ctx, "Demo backfill failed for a check (non-fatal)",
				"checkUID", check.UID, "error", genErr)

			continue
		}

		total += written
	}

	jctx.Logger.InfoContext(ctx, "Backfilled demo history", "results", total, "days", demoBackfillDays)

	if paramErr := jctx.DBService.SetOrgParameter(ctx, org.UID, ParamDemoBackfilled, true, false); paramErr != nil {
		return fmt.Errorf("failed to set %s: %w", ParamDemoBackfilled, paramErr)
	}

	return nil
}

// demoBackfillBurstSeconds clusters synthetic failures into outage-shaped runs
// rather than static, so the history reads like incidents.
const demoBackfillBurstSeconds = 1800

// demoBackfillFailureRate varies the synthetic availability per check so the
// demo's uptime column is not a wall of identical numbers.
func demoBackfillFailureRate(index int) float64 {
	rates := []float64{0, 0.002, 0.02, 0.005, 0.08, 0.001}

	return rates[index%len(rates)]
}

// demoBackfillAvgDurationMs varies the synthetic response times likewise.
func demoBackfillAvgDurationMs(index int) float64 {
	durations := []float64{120, 240, 90, 1500, 300, 60}

	return durations[index%len(durations)]
}
