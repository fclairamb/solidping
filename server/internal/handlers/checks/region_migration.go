package checks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// Region migration errors (spec 2026-08-24-08). All four are validation
// failures the caller can fix, and the handler renders them as 422.
var (
	// ErrRegionMigrationMissingSlug is returned when `from` or `to` is empty.
	ErrRegionMigrationMissingSlug = errors.New("both 'from' and 'to' region slugs are required")
	// ErrRegionMigrationSameSlug is returned when `from` equals `to` — a no-op
	// that is far more likely a typo than an intention.
	ErrRegionMigrationSameSlug = errors.New("'from' and 'to' must be different region slugs")
	// ErrRegionMigrationTargetUnknown is returned when `to` is neither declared
	// in the `regions` system parameter nor served by a live worker. Migrating
	// TO a slug nobody serves would only move the stranding.
	ErrRegionMigrationTargetUnknown = errors.New("target region is not declared and no live worker serves it")
	// ErrRegionMigrationPrivateToCloud is returned for `@private` → `cloud`.
	// A private-region check's secrets live in a region-sealed envelope
	// (checks.config_sealed) that only the region's agents can open; the server
	// cannot re-target it, so the migration would hand a cloud worker a job it
	// can never unseal.
	ErrRegionMigrationPrivateToCloud = errors.New(
		"cannot migrate a private region to a cloud region: sealed configs are encrypted to the private region's keys",
	)
)

// RegionMigrationRequest is the body of POST /api/v1/system/regions/migrate.
type RegionMigrationRequest struct {
	From   string `json:"from"`
	To     string `json:"to"`
	DryRun bool   `json:"dryRun"`
}

// RegionMigrationReport describes what a region migration did — or, with
// dryRun, exactly what it would do. Both modes compute it from the same
// pre-state, so a dry run and the apply that follows agree.
type RegionMigrationReport struct {
	From   string `json:"from"`
	To     string `json:"to"`
	DryRun bool   `json:"dryRun"`
	// ChecksUpdated counts every check the migration touches: its `regions`
	// array was rewritten, or it owned a job stranded under `from`, or both.
	ChecksUpdated int `json:"checksUpdated"`
	// JobsReassigned counts `from` jobs that come back to life under `to`.
	JobsReassigned int `json:"jobsReassigned"`
	// JobsDeleted counts `from` jobs that go away without a replacement —
	// either the check does not (or no longer) declares `to`, it is disabled,
	// or a `to` job already exists for it (the unique (check_uid, region)
	// index case, where reconcile keeps the one that is already correct).
	JobsDeleted int `json:"jobsDeleted"`
	// ByOrg splits ChecksUpdated by organization slug.
	ByOrg map[string]int `json:"byOrg"`
	// OverdueRecovered counts the reassigned jobs whose scheduled_at was
	// already in the past — the stranded backlog the operator actually cares
	// about.
	OverdueRecovered int `json:"overdueRecovered"`
}

// MigrateRegion reassigns every reference to a region slug, server-wide and
// across organizations: `checks.regions` is rewritten in one transaction, then
// each affected check is fed through the existing reconcileCheckJobs so its
// check_jobs are re-materialized under the new slug.
//
// WHY NOT `UPDATE check_jobs SET region = ?`: a check that already carries a
// `to` job would trip the unique (check_uid, region) index, and a raw update
// would also leave the job's phase, plan weight and schedule computed for the
// old region set. reconcileCheckJobs already gets all of that right, so the
// migration reuses it rather than growing a second, subtly different
// materializer.
//
// TRANSACTION SCOPE: the `checks.regions` rewrite is atomic — either every
// array moves or none does. The per-check job reconcile then runs outside that
// transaction, because reconcileCheckJobs works through the db.Service façade
// (which has no caller-supplied transaction). That is deliberate and safe: the
// reconcile is idempotent and convergent, a crash halfway through leaves the
// remaining checks in exactly the state the startup pass
// (ReconcileStaleJobSchedules) now heals on the next boot, and re-running the
// migration finishes the job.
//
// actorUID is recorded in the audit log — a fleet-wide mutation must be
// traceable to a person.
func (s *Service) MigrateRegion(
	ctx context.Context, req RegionMigrationRequest, actorUID string,
) (*RegionMigrationReport, error) {
	from := strings.TrimSpace(req.From)
	to := strings.TrimSpace(req.To)

	if err := s.validateRegionMigration(ctx, from, to); err != nil {
		return nil, err
	}

	affected, err := s.db.ListChecksReferencingRegion(ctx, from)
	if err != nil {
		return nil, fmt.Errorf("list checks referencing region: %w", err)
	}

	report, err := s.buildRegionMigrationReport(ctx, from, to, req.DryRun, affected)
	if err != nil {
		return nil, err
	}

	if req.DryRun {
		return report, nil
	}

	if err := s.applyRegionMigration(ctx, from, to, affected); err != nil {
		return nil, err
	}

	slog.WarnContext(ctx, "region migration applied",
		"actorUid", actorUID,
		"from", from,
		"to", to,
		"checksUpdated", report.ChecksUpdated,
		"jobsReassigned", report.JobsReassigned,
		"jobsDeleted", report.JobsDeleted,
		"overdueRecovered", report.OverdueRecovered,
	)

	return report, nil
}

// validateRegionMigration enforces the four preconditions. `from` deliberately
// need NOT be declared — cleaning up a slug that no longer exists anywhere is
// the entire point of the endpoint.
func (s *Service) validateRegionMigration(ctx context.Context, from, to string) error {
	if from == "" || to == "" {
		return ErrRegionMigrationMissingSlug
	}

	if from == to {
		return ErrRegionMigrationSameSlug
	}

	if regions.IsPrivateRegion(from) && !regions.IsPrivateRegion(to) {
		return ErrRegionMigrationPrivateToCloud
	}

	known, err := s.knownRegionSlugs(ctx)
	if err != nil {
		return err
	}

	if !slices.Contains(known, to) {
		return fmt.Errorf("%w: %q (known regions: %s)",
			ErrRegionMigrationTargetUnknown, to, strings.Join(known, ", "))
	}

	return nil
}

// knownRegionSlugs is every slug a job could actually be claimed under today:
// the declared cloud regions plus whatever live workers report (which is how a
// private `@region` served by a connected agent qualifies without ever
// appearing in the global parameter).
func (s *Service) knownRegionSlugs(ctx context.Context) ([]string, error) {
	seen := make(map[string]bool)

	defs, err := s.regions.GetGlobalRegions(ctx)
	if err != nil {
		return nil, fmt.Errorf("get global regions: %w", err)
	}

	for _, def := range defs {
		if def.Slug != "" {
			seen[def.Slug] = true
		}
	}

	workers, err := s.db.ListLiveWorkers(ctx, time.Now().Add(-regions.WorkerLivenessWindow))
	if err != nil {
		return nil, fmt.Errorf("list live workers: %w", err)
	}

	for _, worker := range workers {
		if worker.Region != nil && *worker.Region != "" {
			seen[*worker.Region] = true
		}
	}

	out := make([]string, 0, len(seen))
	for slug := range seen {
		out = append(out, slug)
	}

	sort.Strings(out)

	return out, nil
}

// buildRegionMigrationReport derives the whole report from the PRE-migration
// state, which is what lets dryRun and the real run return identical numbers.
func (s *Service) buildRegionMigrationReport(
	ctx context.Context, from, to string, dryRun bool, affected []*models.Check,
) (*RegionMigrationReport, error) {
	report := &RegionMigrationReport{
		From:   from,
		To:     to,
		DryRun: dryRun,
		ByOrg:  map[string]int{},
	}

	if len(affected) == 0 {
		return report, nil
	}

	fromJobs, err := s.db.ListCheckJobsByRegion(ctx, from)
	if err != nil {
		return nil, fmt.Errorf("list check jobs in source region: %w", err)
	}

	jobsByCheck := make(map[string][]*models.CheckJob, len(fromJobs))
	for _, job := range fromJobs {
		jobsByCheck[job.CheckUID] = append(jobsByCheck[job.CheckUID], job)
	}

	toJobs, err := s.db.ListCheckJobsByRegion(ctx, to)
	if err != nil {
		return nil, fmt.Errorf("list check jobs in target region: %w", err)
	}

	hasTargetJob := make(map[string]bool, len(toJobs))
	for _, job := range toJobs {
		hasTargetJob[job.CheckUID] = true
	}

	orgSlugs, err := s.organizationSlugs(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now()

	for _, check := range affected {
		report.ChecksUpdated++
		report.ByOrg[orgSlugs[check.OrganizationUID]]++

		nextRegions, _ := models.MigrateRegionList(check.Regions, from, to)
		// A job survives the reconcile only if the check still wants that
		// region and is enabled — reconcileCheckJobs deletes every job of a
		// disabled check.
		willKeepTarget := check.Enabled && slices.Contains(nextRegions, to)

		for _, job := range jobsByCheck[check.UID] {
			if !willKeepTarget || hasTargetJob[check.UID] {
				report.JobsDeleted++

				continue
			}

			// The target job does not exist yet, so this stranded row comes
			// back as a `to` job. Mark it taken: the unique (check_uid, region)
			// index means only one can.
			hasTargetJob[check.UID] = true
			report.JobsReassigned++

			if job.ScheduledAt != nil && job.ScheduledAt.Before(now) {
				report.OverdueRecovered++
			}
		}
	}

	return report, nil
}

// organizationSlugs maps organization UID to slug for the report's byOrg split.
func (s *Service) organizationSlugs(ctx context.Context) (map[string]string, error) {
	orgs, err := s.db.ListOrganizations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}

	out := make(map[string]string, len(orgs))
	for _, org := range orgs {
		out[org.UID] = org.Slug
	}

	return out, nil
}

// applyRegionMigration performs the writes: the atomic `checks.regions`
// rewrite, then a reconcile per affected check.
func (s *Service) applyRegionMigration(ctx context.Context, from, to string, affected []*models.Check) error {
	updated, err := s.db.MigrateCheckRegionSlug(ctx, from, to)
	if err != nil {
		return fmt.Errorf("migrate check regions: %w", err)
	}

	rewritten := make(map[string]*models.Check, len(updated))
	for _, check := range updated {
		rewritten[check.UID] = check
	}

	for _, check := range affected {
		// Prefer the post-rewrite row so the reconcile materializes jobs for
		// the NEW region set. Checks that were only referenced by a stranded
		// job (their regions never named `from`) are unchanged and reconcile
		// from the row we already loaded.
		target := check
		if fresh, ok := rewritten[check.UID]; ok {
			target = fresh
		}

		if err := s.reconcileCheckJobs(ctx, target, false); err != nil {
			return fmt.Errorf("reconcile check %s: %w", target.UID, err)
		}
	}

	return nil
}
