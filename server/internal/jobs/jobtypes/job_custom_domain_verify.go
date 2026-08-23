package jobtypes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/customdomain"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/domainverify"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/tlsedge"
)

// customDomainVerifyInterval is how often the re-verification sweep runs.
// Custom domains rarely change hands, so a 6h cadence is ample: a page whose
// DNS breaks enters grace after ~18h (still serving) and is only taken dark
// after ~3 days of uninterrupted failure.
const customDomainVerifyInterval = 6 * time.Hour

// The failure/success thresholds of the lifecycle live in
// internal/customdomain, next to the transition function that applies them:
// customdomain.GraceAfterFailures, .HardDemoteAfterFailures and
// .RepromoteSuccesses.

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

// Run re-runs the CNAME check for every page with a custom domain, then
// reschedules itself. The check is mode-aware: in token mode a page whose
// CNAME has been repointed at the plain shared target fails, which is exactly
// the dangling-CNAME takeover the mode exists to stop.
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

// reverifyOne re-runs the CNAME check for one page, applies the lifecycle
// transition, stamps the outcome, and alerts on a hard demotion. Returns
// whether the check passed.
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

	cnameTarget, mode := customDomainVerifySettings(jctx)
	diag := r.verifier.Diagnose(ctx, *page.CustomDomain, token, cnameTarget, mode)

	now := time.Now()
	current := customdomain.Normalize(
		customdomain.State{
			Lifecycle:  page.CustomDomainState,
			VerifiedAt: page.CustomDomainVerifiedAt,
			Failures:   page.CustomDomainFailures,
			Successes:  page.CustomDomainSuccesses,
			GraceSince: page.CustomDomainGraceSince,
		},
		true, page.CustomDomainCheckedAt != nil,
	)

	outcome := customdomain.Next(current, customdomain.Observation{
		OK: diag.OK,
		// Re-promotion is only offered to a domain we are still holding a
		// usable certificate for. Evaluated lazily: the storage scan is
		// pointless unless the transition could actually use it.
		CertValid: diag.OK && current.Lifecycle == models.CustomDomainStateDemoted &&
			r.holdsValidCertificate(ctx, jctx, *page.CustomDomain, now),
		Now: now,
	})

	summary := diag.String()
	update := &models.StatusPageCustomDomainUpdate{
		Domain:     page.CustomDomain,
		Token:      page.CustomDomainToken,
		CheckedAt:  &now,
		VerifiedAt: outcome.VerifiedAt,
		Failures:   outcome.Failures,
		Successes:  outcome.Successes,
		State:      outcome.Lifecycle,
		GraceSince: outcome.GraceSince,
		LastCheck:  &summary,
	}

	if err := jctx.DBService.UpdateStatusPageCustomDomain(ctx, page.UID, update); err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to update custom-domain verification state",
			"status_page_uid", page.UID, "error", err)

		return diag.OK
	}

	r.announce(ctx, jctx, page, outcome, summary)

	return diag.OK
}

// holdsValidCertificate reports whether tls_storage still holds an unexpired
// certificate for the domain. It is the second half of the re-promotion gate:
// a domain whose certificate we no longer have is one we have not served in a
// long time, and it goes back through an operator's Verify rather than
// resurrecting itself.
func (r *CustomDomainVerifyJobRun) holdsValidCertificate(
	ctx context.Context, jctx *jobdef.JobContext, domain string, now time.Time,
) bool {
	if jctx.DBService == nil {
		return false
	}

	return tlsedge.HasValidStoredCertificate(ctx, jctx.DBService, domain, now)
}

// announce logs every lifecycle transition and, for a HARD DEMOTION only,
// alerts the organization. Entering grace is deliberately quiet: the page is
// still serving, nobody outside can tell, and paging for it would train
// operators to ignore the message that matters.
func (r *CustomDomainVerifyJobRun) announce(
	ctx context.Context, jctx *jobdef.JobContext,
	page *models.StatusPage, outcome customdomain.Outcome, summary string,
) {
	log := jctx.Logger
	domain := *page.CustomDomain

	switch {
	case outcome.HardDemoted:
		log.ErrorContext(ctx, "Custom domain demoted — the status page is no longer served on it",
			"status_page_uid", page.UID, "domain", domain,
			"failures", outcome.Failures, "diagnostic", summary)
		alertCustomDomainDemoted(ctx, jctx, page, outcome.Failures, summary)
	case outcome.EnteredGrace:
		log.WarnContext(ctx, "Custom domain re-verification is failing — entering grace, still serving",
			"status_page_uid", page.UID, "domain", domain,
			"failures", outcome.Failures, "diagnostic", summary)
	case outcome.Recovered:
		log.InfoContext(ctx, "Custom domain re-verification recovered before it ever stopped serving",
			"status_page_uid", page.UID, "domain", domain)
	case outcome.Repromoted:
		log.InfoContext(ctx, "Custom domain re-promoted after consecutive successful re-checks",
			"status_page_uid", page.UID, "domain", domain,
			"successes", customdomain.RepromoteSuccesses)
	}
}

// customDomainVerifySettings resolves the installation CNAME target and the
// verification mode from the job context's app config. A job context without a
// config (unit tests, degraded startup) yields an empty target, which makes
// every check fail closed rather than silently verifying against nothing.
func customDomainVerifySettings(jctx *jobdef.JobContext) (string, domainverify.Mode) {
	if jctx == nil || jctx.AppConfig == nil {
		return "", domainverify.ModeShared
	}

	return jctx.AppConfig.CustomDomainCNAMETarget(), jctx.AppConfig.CustomDomainCNAMEMode()
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
