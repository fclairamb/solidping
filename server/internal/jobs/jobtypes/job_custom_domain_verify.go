package jobtypes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/domainverify"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// customDomainVerifyInterval is how often the re-verification sweep runs. Custom
// domains rarely change hands, so a 6h cadence is ample while still catching a
// released/transferred domain within a day (3 failures × 6h).
const customDomainVerifyInterval = 6 * time.Hour

// customDomainReverifyMaxFailures is the consecutive TXT-check failure count at
// which a domain's verification is cleared (takeover protection). Kept in step
// with the handler-side customDomainMaxFailures.
const customDomainReverifyMaxFailures = 3

// CustomDomainVerifyJobDefinition is the factory for the custom-domain
// re-verification sweeper.
type CustomDomainVerifyJobDefinition struct{}

// Type returns the custom-domain verify job type.
func (d *CustomDomainVerifyJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeCustomDomainVerify
}

// CustomDomainVerifyJobConfig is the empty config for the sweeper.
type CustomDomainVerifyJobConfig struct{}

// CreateJobRun builds an executable instance.
func (d *CustomDomainVerifyJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg CustomDomainVerifyJobConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
	}

	return &CustomDomainVerifyJobRun{verifier: domainverify.New()}, nil
}

// CustomDomainVerifyJobRun is the runtime state for one execution.
type CustomDomainVerifyJobRun struct {
	verifier *domainverify.Verifier
}

// Run re-runs the ownership (TXT) check for every page with a custom domain,
// then reschedules itself.
func (r *CustomDomainVerifyJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	if r.verifier == nil {
		r.verifier = domainverify.New()
	}

	pages, err := jctx.DBService.ListStatusPagesWithCustomDomain(ctx)
	if err != nil {
		log.ErrorContext(ctx, "Failed to list custom-domain pages", "error", err)

		return jobdef.NewRetryableError(fmt.Errorf("list custom-domain pages: %w", err))
	}

	reverified := 0

	for _, page := range pages {
		if r.reverifyOne(ctx, jctx, page) {
			reverified++
		}
	}

	if len(pages) > 0 {
		log.InfoContext(ctx, "Re-verified custom domains", "checked", len(pages), "passed", reverified)
	}

	r.rescheduleSelf(ctx, jctx)

	return nil
}

// reverifyOne re-runs the TXT check for one page and stamps the outcome.
// Returns whether the check passed.
func (r *CustomDomainVerifyJobRun) reverifyOne(
	ctx context.Context, jctx *jobdef.JobContext, page *models.StatusPage,
) bool {
	if page.CustomDomain == nil {
		return false
	}

	token := ""
	if page.CustomDomainToken != nil {
		token = *page.CustomDomainToken
	}

	ok, _ := r.verifier.CheckTXT(ctx, *page.CustomDomain, token)

	now := time.Now()
	update := &models.StatusPageCustomDomainUpdate{
		Domain:    page.CustomDomain,
		Token:     page.CustomDomainToken,
		CheckedAt: &now,
		// Default: keep the existing verification state (the sweep never
		// promotes an unverified page — that requires the full CNAME check too).
		VerifiedAt: page.CustomDomainVerifiedAt,
	}

	if ok {
		update.Failures = 0
	} else {
		failures := page.CustomDomainFailures + 1
		update.Failures = failures

		if failures >= customDomainReverifyMaxFailures {
			// Domain likely released/transferred — stop serving it.
			update.VerifiedAt = nil
		}
	}

	if err := jctx.DBService.UpdateStatusPageCustomDomain(ctx, page.UID, update); err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to update custom-domain verification state",
			"status_page_uid", page.UID, "error", err)
	}

	return ok
}

func (r *CustomDomainVerifyJobRun) rescheduleSelf(ctx context.Context, jctx *jobdef.JobContext) {
	if jctx.Services == nil || jctx.Services.Jobs == nil {
		return
	}

	scheduledAt := time.Now().Add(customDomainVerifyInterval)

	_, err := jctx.Services.Jobs.CreateJob(
		ctx, "", string(jobdef.JobTypeCustomDomainVerify), nil, &jobsvc.JobOptions{ScheduledAt: &scheduledAt},
	)
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to reschedule custom-domain verify", "error", err)
	}
}
