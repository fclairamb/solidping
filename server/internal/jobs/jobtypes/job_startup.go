package jobtypes

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/defaults"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// StartupJobDefinition is the factory for startup jobs.
type StartupJobDefinition struct{}

// Type returns the job type for startup jobs.
func (d *StartupJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeStartup
}

// StartupJobConfig is the configuration for a startup job.
type StartupJobConfig struct{}

// CreateJobRun creates a new startup job run from the given configuration.
func (d *StartupJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg StartupJobConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, err
	}

	return &StartupJobRun{config: cfg}, nil
}

// StartupJobRun is an executable startup job instance.
type StartupJobRun struct {
	config StartupJobConfig
}

// Run executes the startup job.
func (r *StartupJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	log.InfoContext(ctx, "Starting startup job")
	log.InfoContext(ctx, "Checking if default organization exists")

	if err := r.ensureDefaultOrganization(ctx, jctx); err != nil {
		return err
	}

	// Ensure aggregation jobs exist for all organizations
	if err := r.ensureAggregationJobs(ctx, jctx); err != nil {
		return err
	}

	// Ensure state cleanup job exists (global, not per-org)
	if err := r.ensureStateCleanupJob(ctx, jctx); err != nil {
		return err
	}

	// Ensure snooze sweep job exists (global, not per-org)
	if err := r.ensureSnoozeSweepJob(ctx, jctx); err != nil {
		return err
	}

	// Ensure jobs cleanup job exists (global, not per-org). It self-reschedules
	// daily; this seeds the first run so the jobs table starts self-pruning.
	if err := r.ensureJobsCleanupJob(ctx, jctx); err != nil {
		return err
	}

	// Ensure stuck-job reaper exists (global, not per-org). Running it at
	// startup gives an immediate first sweep after a deploy — exactly when
	// orphaned 'running' jobs appear.
	if err := r.ensureStuckJobReaperJob(ctx, jctx); err != nil {
		return err
	}

	// Ensure the custom-domain re-verification job exists (global). It
	// self-reschedules every 6h; this seeds the first run.
	if err := r.ensureCustomDomainVerifyJob(ctx, jctx); err != nil {
		return err
	}

	// Ensure the platform-agent GC job exists (global). It self-reschedules
	// every 6h; this seeds the first run so an enroll-on-boot fleet's dead
	// rows start being reaped.
	return r.ensureAgentGCJob(ctx, jctx)
}

// ensureDefaultOrganization creates a default organization if none exists.
func (r *StartupJobRun) ensureDefaultOrganization(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	// Check if any organizations exist
	count, err := jctx.DB.NewSelect().
		Model((*models.Organization)(nil)).
		Where("deleted_at IS NULL").
		Count(ctx)
	if err != nil {
		return fmt.Errorf("failed to count organizations: %w", err)
	}

	if count > 0 {
		log.InfoContext(ctx, "Organizations already exist, skipping default org creation", "count", count)
		return nil
	}

	log.InfoContext(ctx, "No organizations found, creating default organization")

	// Create default organization
	defaultOrg := models.NewOrganization(defaults.Organization, "")

	err = jctx.DBService.CreateOrganization(ctx, defaultOrg)
	if err != nil {
		return fmt.Errorf("failed to create default organization: %w", err)
	}

	log.InfoContext(ctx, "Created default organization", "uid", defaultOrg.UID, "slug", defaultOrg.Slug)

	// Create admin user
	adminEmail := defaults.Email
	adminPassword := defaults.Password

	passwordHash, err := passwords.Hash(adminPassword)
	if err != nil {
		return fmt.Errorf("failed to hash admin password: %w", err)
	}

	adminUser := models.NewUser(adminEmail)
	adminUser.PasswordHash = &passwordHash
	adminUser.SuperAdmin = true

	// Deliberately NOT captured as a user_signed_up product event (spec
	// 2026-08-02-08): this is the install-bootstrap admin seeded on every
	// fresh database, not a person signing up. Counting it would add one
	// phantom signup per install and per test-mode database reset.
	if createErr := jctx.DBService.CreateUser(ctx, adminUser); createErr != nil {
		return fmt.Errorf("failed to create admin user: %w", createErr)
	}

	log.InfoContext(ctx, "Created admin user", "uid", adminUser.UID, "email", adminEmail)

	// Create the owner membership for the default organization. The seeded
	// admin is the org's founder, so it gets `owner` — the same role
	// CreateOrg and the login-path bootstrap hand whoever brings an org into
	// existence (spec 2026-08-08-11).
	membership := models.NewOrganizationMember(defaultOrg.UID, adminUser.UID, models.MemberRoleOwner)

	if createErr := jctx.DBService.CreateOrganizationMember(ctx, membership); createErr != nil {
		return fmt.Errorf("failed to create admin membership: %w", createErr)
	}

	log.InfoContext(ctx, "Created owner membership for default organization", "memberUID", membership.UID)

	// Load sample checks if SP_RUN_MODE is not set to "test"
	if err := r.loadSampleChecks(ctx, jctx, defaultOrg.UID); err != nil {
		return fmt.Errorf("failed to load sample checks: %w", err)
	}

	return nil
}

// loadSampleChecks creates sample checks for the organization if not already loaded.
// Uses a parameter to track whether samples have been loaded for this organization.
func (r *StartupJobRun) loadSampleChecks(ctx context.Context, jctx *jobdef.JobContext, orgUID string) error {
	log := jctx.Logger

	runMode := os.Getenv("SP_RUN_MODE")

	// Check parameter to see if samples were already loaded
	paramKey := "samples.loaded"

	param, err := jctx.DBService.GetOrgParameter(ctx, orgUID, paramKey)
	if err != nil {
		return fmt.Errorf("failed to check samples.loaded parameter: %w", err)
	}

	if param != nil {
		if loaded, ok := param.Value["value"].(bool); ok && loaded {
			log.InfoContext(ctx, "Samples already loaded, skipping", "org_uid", orgUID)

			return nil
		}
	}

	log.InfoContext(ctx, "Loading sample checks")

	// Determine sample type based on run mode
	var sampleType checkerdef.ListSampleOptionType
	switch runMode {
	case "demo":
		sampleType = checkerdef.Demo
	case "test":
		sampleType = checkerdef.Test
	default:
		sampleType = checkerdef.Default
	}

	baseURL := os.Getenv("SP_SERVER_BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:4000"
	}

	opts := &checkerdef.ListSampleOptions{Type: sampleType, BaseURL: baseURL}

	// Load samples from all checkers
	checkTypes := checkerdef.ListCheckTypes(opts)
	sampleCount := 0

	for _, checkType := range checkTypes {
		count, loadErr := r.loadSamplesForChecker(ctx, jctx, orgUID, checkType, opts)
		if loadErr != nil {
			return loadErr
		}

		sampleCount += count
	}

	log.InfoContext(ctx, "Finished loading sample checks", "total", sampleCount)

	// Mark samples as loaded
	if err := jctx.DBService.SetOrgParameter(ctx, orgUID, paramKey, true, false); err != nil {
		return fmt.Errorf("failed to set samples.loaded parameter: %w", err)
	}

	return nil
}

// loadSamplesForChecker loads sample checks for a specific checker type.
func (r *StartupJobRun) loadSamplesForChecker(
	ctx context.Context,
	jctx *jobdef.JobContext,
	orgUID string,
	checkType checkerdef.CheckType,
	opts *checkerdef.ListSampleOptions,
) (int, error) {
	log := jctx.Logger

	checker, found := registry.GetChecker(checkType)
	if !found {
		log.InfoContext(ctx, "Checker not found, skipping", "type", checkType)

		return 0, nil
	}

	// Check if checker implements CheckerSamplesProvider
	provider, found := checker.(checkerdef.CheckerSamplesProvider)
	if !found {
		log.InfoContext(ctx, "Checker does not provide samples, skipping", "type", checkType)

		return 0, nil
	}

	samples := provider.GetSampleConfigs(opts)
	log.InfoContext(ctx, "Loading samples for checker", "type", checkType, "count", len(samples))

	count := 0

	for i := range samples {
		if err := r.createSampleCheck(ctx, jctx, orgUID, checkType, checker, samples[i]); err != nil {
			return count, err
		}

		count++
	}

	return count, nil
}

// createSampleCheck creates a single sample check in the database.
func (r *StartupJobRun) createSampleCheck(
	ctx context.Context,
	jctx *jobdef.JobContext,
	orgUID string,
	checkType checkerdef.CheckType,
	checker checkerdef.Checker,
	sample checkerdef.CheckSpec,
) error {
	log := jctx.Logger

	// Use slug from the sample (sample prefix is added by the sample definition)
	slug := sample.Slug

	// Create check model
	check := models.NewCheck(orgUID, slug, string(checkType))
	check.Name = &sample.Name
	check.Config = sample.Config
	check.Enabled = true
	check.Period = timeutils.Duration(sample.Period)

	// Validate the sample config
	if validationErr := checker.Validate(&sample); validationErr != nil {
		log.InfoContext(ctx, "Sample config validation failed, skipping",
			"type", checkType, "name", sample.Name, "error", validationErr)

		return nil
	}

	// Create check in database
	if createErr := jctx.DBService.CreateCheck(ctx, check); createErr != nil {
		return fmt.Errorf("failed to create sample check %s: %w", slug, createErr)
	}

	log.InfoContext(ctx, "Created sample check", "type", checkType, "slug", slug, "name", sample.Name)

	// Emit check.created audit event
	event := models.NewEvent(orgUID, models.EventTypeCheckCreated, models.ActorTypeSystem)
	event.CheckUID = &check.UID
	event.Payload = models.JSONMap{
		"check_uid":  check.UID,
		"check_slug": check.Slug,
		"check_name": check.Name,
		"check_type": check.Type,
		"source":     "startup_samples",
	}
	if createErr := jctx.DBService.CreateEvent(ctx, event); createErr != nil {
		log.InfoContext(ctx, "Failed to create check.created event (non-fatal)", "error", createErr)
	}

	// Notify check runners to wake up and claim the new check immediately
	// This prevents the ~60 second delay on first startup
	if jctx.Services != nil && jctx.Services.EventNotifier != nil {
		if err := jctx.Services.EventNotifier.Notify(ctx, string(models.EventTypeCheckCreated), "{}"); err != nil {
			// Best-effort notification, log but don't fail the job
			log.InfoContext(ctx, "Failed to notify check runners (non-fatal)", "error", err)
		}
	}

	return nil
}

// ensureAggregationJobs provisions aggregation jobs for all organizations.
func (r *StartupJobRun) ensureAggregationJobs(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	// Skip if services are not available (e.g., in tests without full service setup)
	if jctx.Services == nil || jctx.Services.Jobs == nil {
		log.InfoContext(ctx, "Skipping aggregation job provisioning (services not available)")
		return nil
	}

	log.InfoContext(ctx, "Ensuring aggregation jobs exist for all organizations")

	// List all organizations
	orgs, err := jctx.DBService.ListOrganizations(ctx)
	if err != nil {
		return fmt.Errorf("failed to list organizations: %w", err)
	}

	log.InfoContext(ctx, "Found organizations", "count", len(orgs))

	// Create aggregation job for each organization
	for _, org := range orgs {
		_, err := jctx.Services.Jobs.CreateJob(ctx, org.UID, string(jobdef.JobTypeAggregation), nil, nil)
		if err != nil {
			// Log error but continue with other organizations
			log.InfoContext(ctx, "Failed to create aggregation job (non-fatal)", "org_uid", org.UID, "error", err)
		} else {
			log.InfoContext(ctx, "Ensured aggregation job exists", "org_uid", org.UID)
		}
	}

	return nil
}

// ensureStateCleanupJob provisions a global state cleanup job.
func (r *StartupJobRun) ensureStateCleanupJob(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	// Skip if services are not available (e.g., in tests without full service setup)
	if jctx.Services == nil || jctx.Services.Jobs == nil {
		log.InfoContext(ctx, "Skipping state cleanup job provisioning (services not available)")
		return nil
	}

	log.InfoContext(ctx, "Ensuring state cleanup job exists")

	// Create state cleanup job (empty org_uid = global job)
	_, err := jctx.Services.Jobs.CreateJob(ctx, "", string(jobdef.JobTypeStateCleanup), nil, nil)
	if err != nil {
		// Log error but don't fail the startup
		log.InfoContext(ctx, "Failed to create state cleanup job (non-fatal)", "error", err)
	} else {
		log.InfoContext(ctx, "Ensured state cleanup job exists")
	}

	return nil
}

// ensureSnoozeSweepJob provisions a global snooze sweep job. The job
// reschedules itself; this just ensures the very first one exists.
func (r *StartupJobRun) ensureSnoozeSweepJob(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	if jctx.Services == nil || jctx.Services.Jobs == nil {
		log.InfoContext(ctx, "Skipping snooze sweep job provisioning (services not available)")
		return nil
	}

	log.InfoContext(ctx, "Ensuring snooze sweep job exists")

	_, err := jctx.Services.Jobs.CreateJob(ctx, "", string(jobdef.JobTypeSnoozeSweep), nil, nil)
	if err != nil {
		log.InfoContext(ctx, "Failed to create snooze sweep job (non-fatal)", "error", err)
	} else {
		log.InfoContext(ctx, "Ensured snooze sweep job exists")
	}

	return nil
}

// ensureJobsCleanupJob provisions a global jobs-cleanup job. The job
// reschedules itself daily; this just ensures the very first one exists.
// CreateJob dedupes on type+config+org+pending, so a restart won't stack a
// duplicate.
func (r *StartupJobRun) ensureJobsCleanupJob(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	if jctx.Services == nil || jctx.Services.Jobs == nil {
		log.InfoContext(ctx, "Skipping jobs cleanup provisioning (services not available)")
		return nil
	}

	log.InfoContext(ctx, "Ensuring jobs cleanup job exists")

	_, err := jctx.Services.Jobs.CreateJob(ctx, "", string(jobdef.JobTypeJobsCleanup), nil, nil)
	if err != nil {
		log.InfoContext(ctx, "Failed to create jobs cleanup job (non-fatal)", "error", err)
	} else {
		log.InfoContext(ctx, "Ensured jobs cleanup job exists")
	}

	return nil
}

// ensureStuckJobReaperJob provisions a global stuck-job reaper. The job
// reschedules itself; this just ensures the very first one exists.
func (r *StartupJobRun) ensureStuckJobReaperJob(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	if jctx.Services == nil || jctx.Services.Jobs == nil {
		log.InfoContext(ctx, "Skipping stuck-job reaper provisioning (services not available)")
		return nil
	}

	log.InfoContext(ctx, "Ensuring stuck-job reaper exists")

	_, err := jctx.Services.Jobs.CreateJob(ctx, "", string(jobdef.JobTypeStuckJobReaper), nil, nil)
	if err != nil {
		log.InfoContext(ctx, "Failed to create stuck-job reaper job (non-fatal)", "error", err)
	} else {
		log.InfoContext(ctx, "Ensured stuck-job reaper exists")
	}

	return nil
}

// ensureCustomDomainVerifyJob provisions the global custom-domain re-verify
// job. It reschedules itself every 6h; this just ensures the first one exists.
// CreateJob dedupes on type+config+org+pending, so a restart won't stack a
// duplicate.
func (r *StartupJobRun) ensureCustomDomainVerifyJob(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	if jctx.Services == nil || jctx.Services.Jobs == nil {
		log.InfoContext(ctx, "Skipping custom-domain verify provisioning (services not available)")

		return nil
	}

	log.InfoContext(ctx, "Ensuring custom-domain verify job exists")

	_, err := jctx.Services.Jobs.CreateJob(ctx, "", string(jobdef.JobTypeCustomDomainVerify), nil, nil)
	if err != nil {
		log.InfoContext(ctx, "Failed to create custom-domain verify job (non-fatal)", "error", err)
	} else {
		log.InfoContext(ctx, "Ensured custom-domain verify job exists")
	}

	return nil
}

// ensureAgentGCJob provisions the global platform-agent GC job. It reschedules
// itself every 6h; this just ensures the very first one exists. CreateJob
// dedupes on type+config+org+pending, so a restart won't stack a duplicate.
func (r *StartupJobRun) ensureAgentGCJob(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	if jctx.Services == nil || jctx.Services.Jobs == nil {
		log.InfoContext(ctx, "Skipping agent GC provisioning (services not available)")

		return nil
	}

	log.InfoContext(ctx, "Ensuring agent GC job exists")

	_, err := jctx.Services.Jobs.CreateJob(ctx, "", string(jobdef.JobTypeAgentGC), nil, nil)
	if err != nil {
		log.InfoContext(ctx, "Failed to create agent GC job (non-fatal)", "error", err)
	} else {
		log.InfoContext(ctx, "Ensured agent GC job exists")
	}

	return nil
}
