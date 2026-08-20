package jobtypes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/slos"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/uptimereport"
)

// uptimeReportInterval is how often the sweep looks for a closed period.
//
// Hourly rather than daily because a schedule's period closes at local
// midnight and there are timezones on non-hour offsets; an hourly sweep puts
// every schedule's report within an hour of its own period boundary without
// any per-timezone scheduling. Re-running inside the same closed period is
// free: MarkReportScheduleRun refuses the second claim.
const uptimeReportInterval = time.Hour

// UptimeReportJobDefinition is the factory for the scheduled uptime-report job.
type UptimeReportJobDefinition struct{}

// Type returns the uptime report job type.
func (d *UptimeReportJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeUptimeReport
}

// UptimeReportJobConfig configures the sweep.
type UptimeReportJobConfig struct {
	// IntervalSeconds overrides the reschedule cadence. Zero (the seeded
	// default) means uptimeReportInterval.
	IntervalSeconds int `json:"intervalSeconds,omitempty"`
}

// CreateJobRun builds a runner from the stored config.
func (d *UptimeReportJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg UptimeReportJobConfig

	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, fmt.Errorf("failed to parse uptime report config: %w", err)
		}
	}

	return &UptimeReportJobRun{config: cfg}, nil
}

// UptimeReportJobRun is one execution of the sweep.
type UptimeReportJobRun struct {
	config UptimeReportJobConfig
}

func (r *UptimeReportJobRun) interval() time.Duration {
	if r.config.IntervalSeconds > 0 {
		return time.Duration(r.config.IntervalSeconds) * time.Second
	}

	return uptimeReportInterval
}

// Run emits every report whose period has closed since the last sweep.
func (r *UptimeReportJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	if jctx.DBService == nil {
		jctx.Logger.InfoContext(ctx, "Skipping uptime report sweep (no database service)")

		return nil
	}

	schedules, err := jctx.DBService.ListEnabledReportSchedules(ctx)
	if err != nil {
		return jobdef.NewRetryableError(fmt.Errorf("list report schedules: %w", err))
	}

	now := jctx.ClockNow()

	builder := uptimereport.NewBuilder(
		jctx.DBService, jctx.AppConfig,
		slos.NewService(jctx.DBService, jctx.AppConfig, nil),
	)

	sent := 0

	for _, schedule := range schedules {
		emitted, runErr := r.runSchedule(ctx, jctx, builder, schedule, now)
		if runErr != nil {
			// One broken schedule must not stop the others: a report is a
			// digest, and losing every org's month because one org has a
			// malformed scope would be a far worse failure.
			jctx.Logger.WarnContext(ctx, "Uptime report failed for schedule",
				"schedule_uid", schedule.UID, "error", runErr)

			continue
		}

		sent += emitted
	}

	if sent > 0 {
		jctx.Logger.InfoContext(ctx, "Uptime reports enqueued", "emails", sent)
	}

	r.rescheduleSelf(ctx, jctx)

	return nil
}

// runSchedule emits one schedule's report if its period has closed and nobody
// has claimed that period yet. Returns how many emails were enqueued.
func (r *UptimeReportJobRun) runSchedule(
	ctx context.Context, jctx *jobdef.JobContext, builder *uptimereport.Builder,
	schedule *models.ReportSchedule, now time.Time,
) (int, error) {
	if len(schedule.Recipients) == 0 {
		return 0, nil
	}

	window, _ := uptimereport.Window(schedule, now)

	// Duplicate-run suppression. Two replicas that both notice the same closed
	// period race into this UPDATE and exactly one sees a row affected; the
	// loser returns here having sent nothing. A schedule that has already
	// reported this period also lands here, so a restart storm cannot re-mail
	// last month.
	claimed, err := jctx.DBService.MarkReportScheduleRun(ctx, schedule.UID, window.Start, now)
	if err != nil {
		return 0, fmt.Errorf("claim report period: %w", err)
	}

	if !claimed {
		return 0, nil
	}

	org, err := jctx.DBService.GetOrganization(ctx, schedule.OrganizationUID)
	if err != nil {
		return 0, fmt.Errorf("get organization: %w", err)
	}

	data, err := builder.Build(ctx, org, schedule, window, now)
	if err != nil {
		return 0, fmt.Errorf("build report: %w", err)
	}

	sent := 0

	for _, recipient := range schedule.Recipients {
		// The suppression list is authoritative for every outbound address.
		// The "" check scope matters: a digest is org-wide mail, so only an
		// org-wide suppression (or a matching check-scoped one) blocks it.
		suppressed, supErr := jctx.DBService.IsEmailSuppressed(ctx, org.UID, recipient, "")
		if supErr != nil {
			return sent, fmt.Errorf("check suppression: %w", supErr)
		}

		if suppressed {
			continue
		}

		if enqueueErr := EnqueueUptimeReportEmail(ctx, jctx.Services, org.UID, recipient, data); enqueueErr != nil {
			return sent, enqueueErr
		}

		sent++
	}

	return sent, nil
}

// rescheduleSelf keeps the sweep running.
func (r *UptimeReportJobRun) rescheduleSelf(ctx context.Context, jctx *jobdef.JobContext) {
	if jctx.Services == nil || jctx.Services.Jobs == nil {
		return
	}

	scheduledAt := time.Now().Add(r.interval())

	_, err := jctx.Services.Jobs.CreateJob(
		ctx, "", string(jobdef.JobTypeUptimeReport), nil, &jobsvc.JobOptions{ScheduledAt: &scheduledAt},
	)
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to reschedule uptime report sweep", "error", err)
	}
}
