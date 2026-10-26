package jobtypes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/handlers/slos"
	"github.com/fclairamb/solidping/server/internal/i18n/recipient"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/uptimereport"
)

// ReportTestSendJobDefinition is the factory for the "send a test report" job.
type ReportTestSendJobDefinition struct{}

// Type returns the report test send job type.
func (d *ReportTestSendJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeReportTestSend
}

// ReportTestSendJobConfig is the payload of one test send. The endpoint has
// already validated the schedule, the address and the delivery setup.
type ReportTestSendJobConfig struct {
	OrgUID      string    `json:"orgUid"`
	ScheduleUID string    `json:"scheduleUid"`
	Address     string    `json:"address"`
	Lang        string    `json:"lang,omitempty"`
	Timezone    string    `json:"timezone,omitempty"`
	Now         time.Time `json:"now"`
}

// CreateJobRun builds a runner from the stored config.
func (d *ReportTestSendJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg ReportTestSendJobConfig

	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse report test send config: %w", err)
	}

	return &ReportTestSendJobRun{config: cfg}, nil
}

// ReportTestSendJobRun is one execution of a test send.
type ReportTestSendJobRun struct {
	config ReportTestSendJobConfig
}

// Run builds the report and enqueues one email for the configured address.
// It deliberately does not fan out to the schedule's recipients, and, unlike
// the scheduled sweep, it mails an empty report: the user asked to see it.
func (r *ReportTestSendJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	cfg := r.config

	org, err := jctx.DBService.GetOrganization(ctx, cfg.OrgUID)
	if err != nil {
		return fmt.Errorf("get organization: %w", err)
	}

	// A schedule deleted since the click is a permanent failure, not a retry.
	schedule, err := jctx.DBService.GetReportSchedule(ctx, org.UID, cfg.ScheduleUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("report schedule %s no longer exists: %w", cfg.ScheduleUID, err)
		}

		return jobdef.NewRetryableError(fmt.Errorf("get report schedule: %w", err))
	}

	// Checked again here: the address may have unsubscribed since the click.
	suppressed, err := jctx.DBService.IsEmailSuppressed(ctx, org.UID, cfg.Address, "")
	if err != nil {
		return jobdef.NewRetryableError(fmt.Errorf("check suppression: %w", err))
	}

	if suppressed {
		jctx.Logger.InfoContext(ctx, "Skipping report test send to a suppressed address",
			"schedule_uid", schedule.UID)

		return nil
	}

	window, _ := uptimereport.Window(schedule, cfg.Now)

	builder := uptimereport.NewBuilder(
		jctx.DBService, jctx.AppConfig,
		slos.NewService(jctx.DBService, jctx.AppConfig, nil),
	)

	data, err := builder.Build(ctx, org, schedule, window, cfg.Now)
	if err != nil {
		return jobdef.NewRetryableError(fmt.Errorf("build report: %w", err))
	}

	// Even a test send carries the unsubscribe link: a preview missing the
	// footer would hide exactly the part an operator most needs to check.
	unsubscribeURL := uptimereport.UnsubscribeURL(jctx.AppConfig, org.Slug, cfg.Address, cfg.Now)
	hint := recipient.Hint{Locale: cfg.Lang, Timezone: cfg.Timezone}

	return EnqueueUptimeReportEmail(ctx, jctx.Services, org.UID, cfg.Address, hint, data, unsubscribeURL)
}
