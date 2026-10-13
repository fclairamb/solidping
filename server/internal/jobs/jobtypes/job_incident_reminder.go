package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/maintenance"
	"github.com/fclairamb/solidping/server/internal/notifications"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// IncidentReminderJobConfig configures one reminder for an open,
// unacknowledged incident (spec 2026-10-12-16).
//
// The incidentUid key is what jobsvc.CancelPendingForIncident matches, so ack,
// unack, manual resolve and check deletion drop a pending reminder with no
// extra wiring. Seq is the incident's reminder_count this job expects to bump:
// the compare-and-set on it is what lets exactly one job send per interval,
// and it makes each reminder's config distinct so CreateJob never folds the
// next reminder into the one still running.
type IncidentReminderJobConfig struct {
	OrganizationUID string `json:"organizationUid"`
	IncidentUID     string `json:"incidentUid"`
	Seq             int    `json:"seq"`
}

// IncidentReminderJobDefinition is the factory for incident reminder jobs.
type IncidentReminderJobDefinition struct{}

// Type returns the job type identifier.
func (d *IncidentReminderJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeIncidentReminder
}

// CreateJobRun builds a reminder run from its config.
//
//nolint:ireturn // Factory pattern requires interface return
func (d *IncidentReminderJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg IncidentReminderJobConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, fmt.Errorf("parsing incident reminder config: %w", err)
	}

	if cfg.OrganizationUID == "" {
		return nil, ErrMissingOrganizationUID
	}

	if cfg.IncidentUID == "" {
		return nil, ErrMissingIncidentUID
	}

	return &IncidentReminderJobRun{config: cfg}, nil
}

// IncidentReminderJobRun sends one reminder and schedules the next.
type IncidentReminderJobRun struct {
	config IncidentReminderJobConfig
}

// Run re-notifies the check's channels while the incident is open and
// unacknowledged. Every "stop" path returns nil without rescheduling, which
// ends the reminder chain: the next ack/snooze/unack/reopen starts a new one.
func (r *IncidentReminderJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger.With("incidentUid", r.config.IncidentUID, "seq", r.config.Seq)

	incident, err := jctx.DBService.GetIncident(ctx, r.config.OrganizationUID, r.config.IncidentUID)
	if err != nil {
		log.InfoContext(ctx, "Reminder stopped: incident not found", "error", err)

		return nil
	}

	if incident.Kind != models.IncidentKindCheck {
		return nil
	}

	check, err := jctx.DBService.GetCheck(ctx, incident.OrganizationUID, incident.CheckUID)
	if err != nil || check == nil {
		log.InfoContext(ctx, "Reminder stopped: check gone", "error", err)

		return nil
	}

	now := jctx.ClockNow()

	if !incidentNeedsPaging(incident, now) {
		log.InfoContext(ctx, "Reminder stopped: incident handled")

		return nil
	}

	interval := ReminderInterval(ctx, jctx.DBService, check)
	if interval <= 0 {
		log.InfoContext(ctx, "Reminder stopped: reminders turned off")

		return nil
	}

	if r.paused(ctx, jctx, log, check, now) {
		// Skip the send but keep the chain alive with the same seq, so
		// reminders resume on their own after the pause or the window ends.
		return r.schedule(ctx, jctx, incident, r.config.Seq, now.Add(interval))
	}

	claimed, err := jctx.DBService.ClaimIncidentReminder(ctx, incident.UID, r.config.Seq, now)
	if err != nil {
		return jobdef.NewRetryableError(fmt.Errorf("claim reminder: %w", err))
	}

	if !claimed {
		log.InfoContext(ctx, "Reminder skipped: another reminder job won or the incident was handled")

		return nil
	}

	sent := r.fanOut(ctx, jctx, log, incident, check)
	log.InfoContext(ctx, "Incident reminder sent", "notificationJobs", sent)

	return r.schedule(ctx, jctx, incident, r.config.Seq+1, now.Add(interval))
}

// paused reports whether the check is paused or inside an active maintenance
// window. A resolver error counts as "not in maintenance": a database blip
// must not silence a reminder.
func (r *IncidentReminderJobRun) paused(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, check *models.Check, now time.Time,
) bool {
	if !check.Enabled {
		return true
	}

	active, err := maintenance.NewResolver(jctx.DBService, clock.Real{}).IsActiveAt(ctx, check.UID, now)
	if err != nil {
		log.WarnContext(ctx, "Reminder: maintenance lookup failed", "error", err)

		return false
	}

	return active
}

// fanOut queues one `incident.reminder` notification per enabled channel of
// the check that accepts reminders. Escalation-policy targets are not
// reminded: policies have their own repeat settings.
func (r *IncidentReminderJobRun) fanOut(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, check *models.Check,
) int {
	connections, err := jctx.DBService.ListChannelsForCheck(ctx, check.UID)
	if err != nil {
		log.WarnContext(ctx, "Reminder: failed to list channels", "error", err)

		return 0
	}

	sent := 0

	for _, conn := range connections {
		if !conn.Enabled {
			continue
		}

		if !notifications.AcceptsEventType(conn.Type, string(models.EventTypeIncidentReminder)) {
			continue
		}

		cfg := &NotificationJobConfig{
			ConnectionUID: conn.UID,
			IncidentUID:   incident.UID,
			EventType:     string(models.EventTypeIncidentReminder),
			ReminderSeq:   r.config.Seq + 1,
		}

		if queueNotificationWithAudit(ctx, jctx, log, incident, cfg, conn,
			models.IncidentNotificationSourceCheckConnection, nil, nil) {
			sent++
		}
	}

	return sent
}

func (r *IncidentReminderJobRun) schedule(
	ctx context.Context, jctx *jobdef.JobContext, incident *models.Incident, seq int, dueAt time.Time,
) error {
	if jctx.Services == nil || jctx.Services.Jobs == nil {
		return nil
	}

	if err := createReminderJob(ctx, jctx.Services.Jobs, incident, seq, dueAt); err != nil {
		return jobdef.NewRetryableError(err)
	}

	return nil
}

// queueNotificationWithAudit creates one notification job and its pending
// incident_notifications audit row. conn may be nil, in which case it is
// loaded by cfg.ConnectionUID for the audit row's channel type. Returns true
// when the job was created.
func queueNotificationWithAudit(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, cfg *NotificationJobConfig, conn *models.Integration,
	source string, stepUID *string, repeatIndex *int,
) bool {
	raw, err := json.Marshal(cfg)
	if err != nil {
		log.WarnContext(ctx, "failed to marshal notification config",
			"connectionUid", cfg.ConnectionUID, "error", err)

		return false
	}

	if jctx.Services == nil || jctx.Services.Jobs == nil {
		return false
	}

	job, err := jctx.Services.Jobs.CreateJob(
		ctx, incident.OrganizationUID, string(jobdef.JobTypeNotification), raw, nil,
	)
	if err != nil {
		log.WarnContext(ctx, "failed to create notification job",
			"connectionUid", cfg.ConnectionUID, "error", err)

		return false
	}

	if conn == nil {
		loaded, connErr := jctx.DBService.GetChannel(ctx, cfg.ConnectionUID)
		if connErr != nil {
			log.WarnContext(ctx, "failed to load connection for audit row",
				"connectionUid", cfg.ConnectionUID, "error", connErr)

			return true
		}

		conn = loaded
	}

	if auditErr := jctx.DBService.CreateIncidentNotification(ctx, models.NewIncidentNotificationForJob(
		incident.OrganizationUID, incident.UID, cfg.EventType, source,
		conn.UID, job.UID, string(conn.Type), stepUID, repeatIndex,
	)); auditErr != nil {
		log.WarnContext(ctx, "failed to create notification audit row", "error", auditErr)
	}

	return true
}

// ReminderInterval resolves the effective reminder interval of a check:
// the check's own value when set, else the org parameter
// `incidents.reminder_interval_seconds`, else 0 (off). Shared by the incidents
// service, which schedules the first reminder, and the reminder job, which
// re-reads it on every run so turning reminders off stops a running chain.
//
// A read error resolves to 0: unlike traceroute, the default here is OFF, so
// the safe fallback is silence rather than a reminder nobody asked for.
func ReminderInterval(ctx context.Context, dbSvc db.Service, check *models.Check) time.Duration {
	if check == nil {
		return 0
	}

	if check.ReminderIntervalSeconds != nil {
		return reminderSeconds(*check.ReminderIntervalSeconds)
	}

	param, err := dbSvc.GetOrgParameter(ctx, check.OrganizationUID, models.ParamKeyIncidentReminderInterval)
	if err != nil || param == nil {
		return 0
	}

	seconds, ok := ParameterInt(param.Value[models.ParameterValueKey])
	if !ok {
		return 0
	}

	return reminderSeconds(seconds)
}

// reminderSeconds turns a stored value into a duration, treating anything
// outside the allowed range as off.
func reminderSeconds(seconds int) time.Duration {
	if seconds <= 0 || !models.ValidReminderIntervalSeconds(seconds) {
		return 0
	}

	return time.Duration(seconds) * time.Second
}

// ParameterInt reads an integer parameter value, whether it is still the Go
// int the writer stored or the float64 a JSON round trip turned it into.
func ParameterInt(raw any) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		if value != math.Trunc(value) {
			return 0, false
		}

		return int(value), true
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, false
		}

		return int(parsed), true
	default:
		return 0, false
	}
}

// ScheduleIncidentReminder schedules the next reminder of an incident at
// from + the check's effective interval. It does nothing when reminders are
// off for the check, when the incident is not a check outage (degraded and
// SLO burn incidents never remind), or when the incident does not need paging
// at `from` (acknowledged, resolved, snoozed, rolled up): the fire-time guard
// would drop such a job anyway, and not creating it keeps the jobs list
// honest.
func ScheduleIncidentReminder(
	ctx context.Context, jobs jobsvc.Service, dbSvc db.Service,
	incident *models.Incident, check *models.Check, from time.Time,
) error {
	if jobs == nil || incident == nil || check == nil {
		return nil
	}

	if incident.Kind != models.IncidentKindCheck {
		return nil
	}

	if !incidentNeedsPaging(incident, from) {
		return nil
	}

	interval := ReminderInterval(ctx, dbSvc, check)
	if interval <= 0 {
		return nil
	}

	return createReminderJob(ctx, jobs, incident, incident.ReminderCount, from.Add(interval))
}

func createReminderJob(
	ctx context.Context, jobs jobsvc.Service, incident *models.Incident, seq int, dueAt time.Time,
) error {
	cfg, err := json.Marshal(IncidentReminderJobConfig{
		OrganizationUID: incident.OrganizationUID,
		IncidentUID:     incident.UID,
		Seq:             seq,
	})
	if err != nil {
		return fmt.Errorf("marshal incident reminder config: %w", err)
	}

	if _, err := jobs.CreateJob(
		ctx, incident.OrganizationUID, string(jobdef.JobTypeIncidentReminder), cfg,
		&jobsvc.JobOptions{ScheduledAt: &dueAt},
	); err != nil {
		return fmt.Errorf("schedule incident reminder: %w", err)
	}

	return nil
}

// CancelIncidentReminders soft-deletes every pending reminder of an incident,
// whatever its due time. Snooze uses it: the generic sweep only cancels jobs
// due before the snooze ends, and a reminder left behind would only be dropped
// by the fire-time guard.
func CancelIncidentReminders(ctx context.Context, jobs jobsvc.Service, incidentUID string) (int, error) {
	if jobs == nil {
		return 0, nil
	}

	history, err := jobs.ListForIncident(ctx, incidentUID, string(jobdef.JobTypeIncidentReminder))
	if err != nil {
		return 0, fmt.Errorf("list incident reminders: %w", err)
	}

	canceled := 0

	for _, job := range history {
		if job.Status != models.JobStatusPending || job.DeletedAt != nil {
			continue
		}

		if err := jobs.CancelJob(ctx, job.UID); err != nil {
			if errors.Is(err, jobsvc.ErrJobNotPending) {
				continue
			}

			return canceled, fmt.Errorf("cancel incident reminder: %w", err)
		}

		canceled++
	}

	return canceled, nil
}
