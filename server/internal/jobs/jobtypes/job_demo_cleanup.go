package jobtypes

import (
	"context"
	"encoding/json"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// demo_cleanup is the public live demo's sweeper and self-healer (spec
// 2026-09-06-02), built on the events_cleanup shape: global, self-rescheduling,
// interval and TTL resolved from configuration on every run.
//
// It does two jobs that look unrelated and are not. The demo is a shared,
// writable sandbox on the production server; both halves are about it going
// back to a known state on its own:
//
//  1. Expired visitor checks are removed, so the demo does not accumulate a
//     thousand "test123" checks over a weekend.
//  2. The demo IDENTITY is reconciled — password, flags, entitlements, session
//     cap. Anything that ever slipped past the write guard, and any operator
//     who fat-fingered the demo user in the superadmin UI, is undone within
//     half an hour rather than silently persisting.

// DemoCleanupJobDefinition is the factory for the demo sweeper.
type DemoCleanupJobDefinition struct{}

// Type returns the demo cleanup job type.
func (d *DemoCleanupJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeDemoCleanup
}

// DemoCleanupJobConfig is the empty config for the sweeper.
type DemoCleanupJobConfig struct{}

// CreateJobRun builds an executable instance.
func (d *DemoCleanupJobDefinition) CreateJobRun(configRaw json.RawMessage) (jobdef.JobRunner, error) {
	var cfg DemoCleanupJobConfig
	if len(configRaw) > 0 {
		if err := json.Unmarshal(configRaw, &cfg); err != nil {
			return nil, err
		}
	}

	return &DemoCleanupJobRun{}, nil
}

// DemoCleanupJobRun is the runtime state for one sweep.
type DemoCleanupJobRun struct{}

// Run sweeps the demo org and reschedules itself.
//
// It ALWAYS reschedules, including when the demo is off: the alternative is
// that turning the demo on requires a restart to re-seed the job, which is
// exactly the operational trap the platform watchdog's comment warns about. A
// disabled sweep costs one cheap job row per interval.
func (r *DemoCleanupJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	cfg := demoConfigFrom(jctx)
	if !cfg.Enabled {
		log.DebugContext(ctx, "Demo disabled, skipping demo cleanup")
		r.rescheduleSelf(ctx, jctx)

		return nil
	}

	// A missing demo org is not an error to retry: the seed simply has not run
	// yet (or the demo was turned on since the last boot), and the next sweep
	// will find it. Retrying would only fill the jobs table with failures.
	org, orgErr := jctx.DBService.GetOrganizationBySlug(ctx, cfg.ResolvedOrgSlug())
	if orgErr != nil || org == nil {
		log.InfoContext(ctx, "Demo org not found; nothing to clean", "error", orgErr)
		r.rescheduleSelf(ctx, jctx)

		return nil
	}

	// Belt and braces: only ever sweep an org that carries the demo flag. The
	// slug is configuration and configuration can be wrong; deleting checks out
	// of a real customer's org because somebody set SP_DEMO_ORG_SLUG to their
	// slug is not a mistake that can be walked back.
	if !isDemoOrg(ctx, jctx, org) {
		log.WarnContext(ctx, "Configured demo org is not flagged demo.enabled; refusing to sweep it",
			"org", org.Slug)
		r.rescheduleSelf(ctx, jctx)

		return nil
	}

	// Same reasoning as the org above.
	user, userErr := jctx.DBService.GetUserByEmail(ctx, cfg.ResolvedEmail())
	if userErr != nil || user == nil {
		log.InfoContext(ctx, "Demo user not found; skipping demo cleanup", "error", userErr)
		r.rescheduleSelf(ctx, jctx)

		return nil
	}

	deleted := r.deleteExpiredChecks(ctx, jctx, org, user, cfg.ResolvedCheckTTL())
	log.InfoContext(ctx, "Demo cleanup finished", "deletedChecks", deleted)

	r.reconcileIdentity(ctx, jctx, org, user)
	r.rescheduleSelf(ctx, jctx)

	return nil
}

// isDemoOrg reports whether the org carries the demo.enabled parameter.
func isDemoOrg(ctx context.Context, jctx *jobdef.JobContext, org *models.Organization) bool {
	param, err := jctx.DBService.GetOrgParameter(ctx, org.UID, ParamDemoEnabled)
	if err != nil || param == nil {
		return false
	}

	enabled, ok := param.Value["value"].(bool)

	return ok && enabled
}

// deleteExpiredChecks removes every check the demo USER created more than one
// TTL ago, and returns how many went.
//
// Two things it deliberately does not touch: checks with created_by = NULL (the
// seeded catalog — nobody created them, so nothing expires them) and checks
// created by anyone else (there is nobody else in the demo org today, and if
// there ever is, their rows are not this sweep's business).
//
// Deletion goes through the checks SERVICE, not a raw soft-delete, so an
// expiring check is torn down exactly the way a user's DELETE tears one down:
// open incidents resolved, check jobs removed, realtime subscribers notified.
// A raw UPDATE would leave a scheduler row pointing at a deleted check and an
// incident nobody can ever close.
func (r *DemoCleanupJobRun) deleteExpiredChecks(
	ctx context.Context,
	jctx *jobdef.JobContext,
	org *models.Organization,
	user *models.User,
	ttl time.Duration,
) int {
	if jctx.Services == nil || jctx.Services.Checks == nil {
		jctx.Logger.InfoContext(ctx, "No check service available; skipping demo check expiry")

		return 0
	}

	checks, _, err := jctx.DBService.ListChecks(ctx, org.UID, nil)
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to list demo checks", "error", err)

		return 0
	}

	cutoff := time.Now().Add(-ttl)
	deleted := 0

	for _, check := range checks {
		if check.CreatedBy == nil || *check.CreatedBy != user.UID {
			continue
		}

		if !check.CreatedAt.Before(cutoff) {
			continue
		}

		if delErr := jctx.Services.Checks.DeleteCheck(ctx, org.Slug, check.UID); delErr != nil {
			jctx.Logger.WarnContext(ctx, "Failed to delete expired demo check",
				"checkUID", check.UID, "error", delErr)

			continue
		}

		deleted++
	}

	return deleted
}

// reconcileIdentity puts the demo account, its membership, its entitlements and
// its session cap back to what the startup job provisioned.
func (r *DemoCleanupJobRun) reconcileIdentity(
	ctx context.Context, jctx *jobdef.JobContext, org *models.Organization, user *models.User,
) {
	cfg := demoConfigFrom(jctx)

	hash, err := passwords.Hash(cfg.ResolvedPassword())
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to hash demo password during reconciliation", "error", err)

		return
	}

	if reconcileErr := ReconcileDemoUser(ctx, jctx.DBService, user, hash); reconcileErr != nil {
		jctx.Logger.WarnContext(ctx, "Failed to reconcile demo user", "error", reconcileErr)
	}

	if memberErr := reconcileDemoMemberships(ctx, jctx, org, user); memberErr != nil {
		jctx.Logger.WarnContext(ctx, "Failed to reconcile demo memberships", "error", memberErr)
	}

	startup := &StartupJobRun{}
	if entErr := startup.ensureDemoEntitlements(ctx, jctx, org); entErr != nil {
		jctx.Logger.WarnContext(ctx, "Failed to re-pin demo entitlements", "error", entErr)
	}

	if paramErr := jctx.DBService.SetOrgParameter(
		ctx, org.UID, string(systemconfig.KeySessionMaxDuration), demoSessionMaxDurationSeconds, false,
	); paramErr != nil {
		jctx.Logger.WarnContext(ctx, "Failed to re-apply demo session cap", "error", paramErr)
	}
}

// reconcileDemoMemberships makes the demo user's memberships exactly
// {demo org: user}: the role is put back to `user` if it drifted, and any
// membership of ANOTHER org is removed — a demo credential is public, and a
// public credential that has quietly acquired access to a real organization is
// the failure this undoes.
func reconcileDemoMemberships(
	ctx context.Context, jctx *jobdef.JobContext, org *models.Organization, user *models.User,
) error {
	members, err := jctx.DBService.ListMembersByUser(ctx, user.UID)
	if err != nil {
		return err
	}

	found := false

	for _, member := range members {
		if member.OrganizationUID == org.UID {
			found = true

			if member.Role != models.MemberRoleUser {
				role := models.MemberRoleUser
				if updateErr := jctx.DBService.UpdateOrganizationMember(
					ctx, member.UID, models.OrganizationMemberUpdate{Role: &role},
				); updateErr != nil {
					jctx.Logger.WarnContext(ctx, "Failed to reset demo membership role", "error", updateErr)
				}
			}

			continue
		}

		if delErr := jctx.DBService.DeleteOrganizationMember(ctx, member.UID); delErr != nil {
			jctx.Logger.WarnContext(ctx, "Failed to remove foreign demo membership",
				"memberUID", member.UID, "error", delErr)
		}
	}

	if !found {
		return ensureDemoMembership(ctx, jctx, org, user)
	}

	return nil
}

// rescheduleSelf queues the next sweep.
func (r *DemoCleanupJobRun) rescheduleSelf(ctx context.Context, jctx *jobdef.JobContext) {
	if jctx.Services == nil || jctx.Services.Jobs == nil {
		return
	}

	scheduledAt := time.Now().Add(demoConfigFrom(jctx).ResolvedCleanupInterval())

	if _, err := jctx.Services.Jobs.CreateJob(
		ctx, "", string(jobdef.JobTypeDemoCleanup), nil, &jobsvc.JobOptions{ScheduledAt: &scheduledAt},
	); err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to reschedule demo cleanup", "error", err)
	}
}
