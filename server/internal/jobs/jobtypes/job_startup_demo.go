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
	"github.com/fclairamb/solidping/server/internal/regions"
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
	// ParamDemoSeededChecks records how many checks the catalog seeded.
	//
	// It exists so MaxChecks is a CONSTANT. Recomputing the cap from the live
	// check count — which is what the first implementation did — makes it
	// ratchet: every visitor-created check raises the total, the cleanup job
	// re-pins seeded+headroom against that larger total half an hour later, and
	// the ceiling the demo's load bounding depends on climbs monotonically
	// under exactly the traffic it exists to bound. Persisting the seeded count
	// at seed time makes every later re-pin arrive at the same number.
	ParamDemoSeededChecks = "demo.seeded_checks"
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

// demoSeededCheckCount returns the number of checks the catalog seeded, which
// is what MaxChecks is pinned against.
//
// Read order matters:
//  1. the demo.seeded_checks org parameter, written once at seed time. This is
//     the authoritative answer and the reason the cap does not ratchet.
//  2. a count of checks with created_by = NULL, for a demo org seeded by an
//     older binary that never wrote the parameter. NULL-owned is exactly the
//     seeded set — every visitor-created check carries its creator — so this
//     back-fill is precise, and it is persisted so step 1 answers next time.
//
// It never falls back to the live total: that is the ratchet this exists to
// remove.
func (r *StartupJobRun) demoSeededCheckCount(
	ctx context.Context, jctx *jobdef.JobContext, org *models.Organization,
) int {
	if param, err := jctx.DBService.GetOrgParameter(ctx, org.UID, ParamDemoSeededChecks); err == nil && param != nil {
		if count, ok := numericParamValue(param.Value["value"]); ok {
			return count
		}
	}

	seeded := 0

	checks, _, err := jctx.DBService.ListChecks(ctx, org.UID, nil)
	if err != nil {
		return seeded
	}

	for _, check := range checks {
		if check.CreatedBy == nil {
			seeded++
		}
	}

	if setErr := jctx.DBService.SetOrgParameter(ctx, org.UID, ParamDemoSeededChecks, seeded, false); setErr != nil {
		jctx.Logger.WarnContext(ctx, "Failed to persist the demo seeded-check count", "error", setErr)
	}

	return seeded
}

// numericParamValue reads an org parameter back as an int. Parameters
// round-trip through JSON, so an int written on this boot comes back as an int
// but the same value read from the database comes back as a float64.
func numericParamValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
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
	seeded := r.demoSeededCheckCount(ctx, jctx, org)

	// maxSlos is the SEEDED count, not zero: the catalog's two objectives are
	// written straight to the database and so bypass the quota, but a Usage
	// page reading "2 / 0 SLOs" would look like a bug on the one org whose
	// whole job is to look right. Visitors cannot create more — SLO routes are
	// not on the demo write allowlist.
	sloCount := 0
	if slos, sloErr := jctx.DBService.ListSLOs(ctx, org.UID, models.ListSLOsFilter{}); sloErr == nil {
		sloCount = len(slos)
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
		MaxSlos:             &sloCount,
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

	seed := &demoCatalogSeed{
		org:       org,
		opts:      &checkerdef.ListSampleOptions{Type: checkerdef.Demo, BaseURL: demoBaseURL(jctx)},
		group:     group,
		policyUID: policyUID,
		regions:   demoPublicRegions(ctx, jctx),
	}

	count := 0

	for _, checkType := range demoCatalogTypes {
		loaded, loadErr := r.loadDemoSamplesForChecker(ctx, jctx, seed, checkType)
		if loadErr != nil {
			return loadErr
		}

		count += loaded
	}

	log.InfoContext(ctx, "Loaded demo catalog", "checks", count)

	// Pinned here, once, so every later re-pin (startup and the 30-minute
	// cleanup sweep alike) computes MaxChecks from the same constant.
	if paramErr := jctx.DBService.SetOrgParameter(
		ctx, org.UID, ParamDemoSeededChecks, count, false,
	); paramErr != nil {
		return fmt.Errorf("failed to set %s for demo org: %w", ParamDemoSeededChecks, paramErr)
	}

	if pageErr := r.ensureDemoStatusPage(ctx, jctx, org); pageErr != nil {
		log.WarnContext(ctx, "Failed to create demo status page (non-fatal)", "error", pageErr)
	}

	if sloErr := r.ensureDemoSLOs(ctx, jctx, org); sloErr != nil {
		log.WarnContext(ctx, "Failed to create demo SLOs (non-fatal)", "error", sloErr)
	}

	if mwErr := r.ensureDemoMaintenanceWindow(ctx, jctx, org); mwErr != nil {
		log.WarnContext(ctx, "Failed to create demo maintenance window (non-fatal)", "error", mwErr)
	}

	if paramErr := jctx.DBService.SetOrgParameter(ctx, org.UID, ParamSamplesLoaded, true, false); paramErr != nil {
		return fmt.Errorf("failed to set %s for demo org: %w", ParamSamplesLoaded, paramErr)
	}

	return nil
}

// demoCatalogSeed carries everything every seeded catalog check needs, so the
// per-checker seeding call does not grow an eighth positional parameter.
type demoCatalogSeed struct {
	org       *models.Organization
	opts      *checkerdef.ListSampleOptions
	group     *models.CheckGroup
	policyUID *string
	// regions is the PUBLIC region set, resolved once at seed time. See
	// demoPublicRegions for why the demo pins it explicitly instead of letting
	// region resolution fall through to the defaults.
	regions []string
}

// demoMaxCatalogRegions is how many public regions each catalog check runs
// from. The spec's headline claim is "three or more public regions on every
// check"; more than three multiplies the catalog's per-minute cost against
// demoMaxChecksPerMinute for no extra storytelling.
const demoMaxCatalogRegions = 3

// demoPublicRegions resolves up to demoMaxCatalogRegions PUBLIC region slugs.
//
// Public is structural: GetGlobalRegions reads the system-wide `regions`
// parameter, while private (deported-agent) regions live in a per-org
// `custom_regions` parameter the demo org will never own, so nothing here can
// return an `@`-prefixed slug.
//
// Resolved and pinned onto each check rather than left empty, because an empty
// check.Regions falls through to the org default, then the system default, then
// every defined region. In production that happens to be the full public fleet
// — but on an instance with a narrow default_regions the demo's multi-region
// claim would silently become a single-region one, with nothing to notice it.
//
// Fewer than three defined regions (a dev laptop has exactly one) takes what
// there is: a thin demo is better than a failed boot.
func demoPublicRegions(ctx context.Context, jctx *jobdef.JobContext) []string {
	defs, err := regions.NewService(jctx.DBService).GetGlobalRegions(ctx)
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to resolve public regions for the demo catalog", "error", err)

		return nil
	}

	slugs := make([]string, 0, demoMaxCatalogRegions)

	for i := range defs {
		if regions.IsPrivateRegion(defs[i].Slug) {
			continue
		}

		slugs = append(slugs, defs[i].Slug)

		if len(slugs) == demoMaxCatalogRegions {
			break
		}
	}

	return slugs
}

// loadDemoSamplesForChecker seeds one checker's Demo samples.
func (r *StartupJobRun) loadDemoSamplesForChecker(
	ctx context.Context,
	jctx *jobdef.JobContext,
	seed *demoCatalogSeed,
	checkType checkerdef.CheckType,
) (int, error) {
	checker, found := registry.GetChecker(checkType)
	if !found {
		return 0, nil
	}

	samples := demoSamplesFor(checkType, seed.opts)
	count := 0

	for i := range samples {
		created, err := r.createDemoCatalogCheck(ctx, jctx, seed, checker, &samples[i])
		if err != nil {
			return count, err
		}

		if created {
			count++
		}
	}

	return count, nil
}

// createDemoCatalogCheck seeds one catalog check.
//
// It runs the checker's Validate first, exactly as createSampleCheck does for
// the `default` org's samples. That is not cosmetic: Validate is where the
// heartbeat checker MINTS ITS PING TOKEN, so a demo seeded without it lands a
// heartbeat with an empty config and no ping URL to copy — and `rotate-token`
// is not on the demo write allowlist, so it could not be repaired from inside
// the demo either. It also gets the config validated, the check.created audit
// event emitted and the runners woken, all of which this path used to skip.
func (r *StartupJobRun) createDemoCatalogCheck(
	ctx context.Context,
	jctx *jobdef.JobContext,
	seed *demoCatalogSeed,
	checker checkerdef.Checker,
	sample *checkerdef.CheckSpec,
) (bool, error) {
	log := jctx.Logger

	// Validate BEFORE reading sample.Config: it mutates the spec (heartbeat
	// tokens, defaulted fields), and the check must carry the mutated version.
	if validationErr := checker.Validate(sample); validationErr != nil {
		log.InfoContext(ctx, "Demo sample config validation failed, skipping",
			"type", checker.Type(), "name", sample.Name, "error", validationErr)

		return false, nil
	}

	check := models.NewCheck(seed.org.UID, sample.Slug, string(checker.Type()))
	name := sample.Name
	check.Name = &name
	check.Config = sample.Config
	check.Enabled = true
	check.Period = timeutils.Duration(sample.Period)
	check.EscalationPolicyUID = seed.policyUID
	check.Regions = seed.regions

	if seed.group != nil {
		check.CheckGroupUID = &seed.group.UID
	}

	// created_by stays NULL: nobody created these, and that is what makes
	// them immutable to a demo session.
	if createErr := jctx.DBService.CreateCheck(ctx, check); createErr != nil {
		return false, fmt.Errorf("failed to create demo check %s: %w", sample.Slug, createErr)
	}

	if label, labelErr := jctx.DBService.GetOrCreateLabel(ctx, seed.org.UID, "env", "demo"); labelErr == nil {
		_ = jctx.DBService.SetCheckLabels(ctx, check.UID, []string{label.UID})
	}

	r.recordDemoCheckCreated(ctx, jctx, seed.org.UID, check)

	return true, nil
}

// recordDemoCheckCreated emits the check.created audit event and wakes the
// runners, mirroring createSampleCheck. Both are best-effort.
func (r *StartupJobRun) recordDemoCheckCreated(
	ctx context.Context, jctx *jobdef.JobContext, orgUID string, check *models.Check,
) {
	log := jctx.Logger

	event := newCheckCreatedEvent(orgUID, check, "demo_catalog")
	if createErr := jctx.DBService.CreateEvent(ctx, event); createErr != nil {
		log.InfoContext(ctx, "Failed to create demo check.created event (non-fatal)", "error", createErr)
	}

	if jctx.Services != nil && jctx.Services.EventNotifier != nil {
		if err := jctx.Services.EventNotifier.Notify(
			ctx, string(models.EventTypeCheckCreated), "{}",
		); err != nil {
			log.InfoContext(ctx, "Failed to notify check runners (non-fatal)", "error", err)
		}
	}
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

// ensureDemoSLOs seeds two objectives: one comfortably met and one that the
// permanently-down catalog check is burning through.
//
// TWO, not one, and that is the point of the pair: an SLO page showing a single
// green 100% says nothing about what the feature is for. A visitor needs to see
// an error budget being spent — the burn rate, the projected exhaustion, the
// alert policy that would fire — and that only exists next to a healthy one to
// compare it against.
func (r *StartupJobRun) ensureDemoSLOs(
	ctx context.Context, jctx *jobdef.JobContext, org *models.Organization,
) error {
	checks, _, err := jctx.DBService.ListChecks(ctx, org.UID, nil)
	if err != nil {
		return fmt.Errorf("failed to list demo checks for SLOs: %w", err)
	}

	healthy := findDemoCheck(checks, demoSlugAPIHealth)
	burning := findDemoCheck(checks, demoSlugHardDown)

	if healthy != nil {
		slo := models.NewSLO(org.UID, "API availability", "demo-api-availability", demoHealthySLOTarget)
		slo.CheckUID = &healthy.UID

		if createErr := jctx.DBService.CreateSLO(ctx, slo); createErr != nil {
			return fmt.Errorf("failed to create the healthy demo SLO: %w", createErr)
		}
	}

	if burning != nil {
		slo := models.NewSLO(org.UID, "Legacy billing availability", "demo-legacy-billing", demoBurningSLOTarget)
		slo.CheckUID = &burning.UID

		if createErr := jctx.DBService.CreateSLO(ctx, slo); createErr != nil {
			return fmt.Errorf("failed to create the burning demo SLO: %w", createErr)
		}
	}

	return nil
}

// ensureDemoMaintenanceWindow seeds one RECURRING window on a single check, so
// the demo shows what a planned outage looks like — suppressed notifications, a
// banner on the status page, probes excluded from the SLO denominator — rather
// than only what an unplanned one does.
//
// Deliberately attached to exactly one check: an empty window suppresses
// nothing at all, and an org-wide one would hide the very incidents the demo
// exists to display.
func (r *StartupJobRun) ensureDemoMaintenanceWindow(
	ctx context.Context, jctx *jobdef.JobContext, org *models.Organization,
) error {
	checks, _, err := jctx.DBService.ListChecks(ctx, org.UID, nil)
	if err != nil {
		return fmt.Errorf("failed to list demo checks for the maintenance window: %w", err)
	}

	target := findDemoCheck(checks, demoSlugSlowEndpoint)
	if target == nil {
		return nil
	}

	// Anchored on the next whole hour so the window is always in the future on
	// a freshly seeded instance, and repeats weekly from there.
	start := time.Now().Truncate(time.Hour).Add(time.Hour)
	window := models.NewMaintenanceWindow(
		org.UID, "Weekly database maintenance", start, start.Add(demoMaintenanceDuration))
	window.Recurrence = models.RecurrenceWeekly
	description := "A recurring planned window. Alerts are suppressed and these probes " +
		"are excluded from the SLO denominator while it is open."
	window.Description = &description

	if createErr := jctx.DBService.CreateMaintenanceWindow(ctx, window); createErr != nil {
		return fmt.Errorf("failed to create the demo maintenance window: %w", createErr)
	}

	if attachErr := jctx.DBService.SetMaintenanceWindowChecks(
		ctx, window.UID, []string{target.UID}, nil,
	); attachErr != nil {
		return fmt.Errorf("failed to attach the demo maintenance window: %w", attachErr)
	}

	return nil
}

// findDemoCheck locates one seeded catalog check by slug.
func findDemoCheck(checks []*models.Check, slug string) *models.Check {
	for _, check := range checks {
		if check.Slug != nil && *check.Slug == slug {
			return check
		}
	}

	return nil
}

// Catalog slugs the SLOs and the maintenance window attach to. They must match
// the samples in internal/checkers/checkhttp/samples.go; a rename there simply
// means the objective is not seeded (findDemoCheck returns nil), never a
// startup failure.
const (
	demoSlugAPIHealth    = "demo-api-health"
	demoSlugHardDown     = "demo-legacy-billing"
	demoSlugSlowEndpoint = "demo-slow-endpoint"
)

const (
	// demoHealthySLOTarget is comfortably met by the always-up catalog check.
	demoHealthySLOTarget = 99.0
	// demoBurningSLOTarget is set against the permanently-down one, so its
	// error budget is visibly exhausted.
	demoBurningSLOTarget = 99.9
	// demoMaintenanceDuration is how long the recurring window stays open.
	demoMaintenanceDuration = 2 * time.Hour
)

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
