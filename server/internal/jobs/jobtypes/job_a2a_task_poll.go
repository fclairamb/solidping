package jobtypes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/notifications"
)

// ErrMissingA2ATaskID is returned when an a2a_task_poll config names no task.
var ErrMissingA2ATaskID = errors.New("a2a_task_poll: integrationUid, a2aIncidentUid and taskId are required")

// A2ATaskPollJobDefinition is the factory for a2a_task_poll jobs (spec
// 2026-10-18-02): one GetTask call per run, then either the result goes on
// the incident timeline or the run schedules its successor.
type A2ATaskPollJobDefinition struct{}

// Type returns the job type identifier.
func (d *A2ATaskPollJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeA2ATaskPoll
}

// CreateJobRun parses the poll state carried by the job.
//
//nolint:ireturn // Factory pattern requires interface return
func (d *A2ATaskPollJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg notifications.A2ATaskPollConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, fmt.Errorf("parsing a2a_task_poll config: %w", err)
	}

	if cfg.IntegrationUID == "" || cfg.A2AIncidentUID == "" || cfg.TaskID == "" {
		return nil, ErrMissingA2ATaskID
	}

	return &A2ATaskPollJobRun{config: cfg}, nil
}

// A2ATaskPollJobRun is one poll of one A2A task.
type A2ATaskPollJobRun struct {
	config notifications.A2ATaskPollConfig
	// now is the clock; nil means time.Now. Tests set it.
	now func() time.Time
}

func (r *A2ATaskPollJobRun) clock() time.Time {
	if r.now != nil {
		return r.now()
	}

	return time.Now()
}

// Run makes one GetTask call and decides what happens next. A vanished
// integration or incident ends the chain silently; an unreachable agent is
// retried on the backoff until the deadline.
func (r *A2ATaskPollJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger
	cfg := r.config

	integration, err := jctx.DBService.GetChannel(ctx, cfg.IntegrationUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}

		return jobdef.NewRetryableError(fmt.Errorf("load a2a integration: %w", err))
	}

	if integration.Type != models.ConnectionTypeA2A ||
		(cfg.OrgUID != "" && integration.OrganizationUID != cfg.OrgUID) {
		return nil
	}

	if _, incErr := jctx.DBService.GetIncident(ctx, integration.OrganizationUID, cfg.A2AIncidentUID); incErr != nil {
		if errors.Is(incErr, sql.ErrNoRows) {
			return nil
		}

		return jobdef.NewRetryableError(fmt.Errorf("load incident: %w", incErr))
	}

	if integration.SettingsPrivate != nil && *integration.SettingsPrivate != "" {
		merged, decErr := credentials.OpenConnectionSettings(ctx, jctx.Services.Credentials, integration)
		if decErr != nil {
			return fmt.Errorf("decrypt a2a integration settings: %w", decErr)
		}

		integration.Settings = models.JSONMap(merged)
	}

	task, err := notifications.A2AGetTask(ctx, jctx, integration, cfg.TaskID)
	if err != nil {
		if !notifications.IsA2ARetryable(err) {
			log.WarnContext(ctx, "AI agent task poll failed, giving up",
				"integrationUid", integration.UID, "taskId", cfg.TaskID, "error", err)

			return nil
		}

		log.InfoContext(ctx, "AI agent unreachable, polling again later",
			"integrationUid", integration.UID, "taskId", cfg.TaskID, "error", err)
		r.next(ctx, jctx, integration)

		return nil
	}

	if notifications.A2ATaskSettled(task.Status.State) {
		notifications.RecordA2AComment(ctx, jctx, integration, cfg.A2AIncidentUID, notifications.A2ATaskComment(task))

		return nil
	}

	r.next(ctx, jctx, integration)

	return nil
}

// next schedules the following poll, or records the timeout note once the
// deadline has passed.
func (r *A2ATaskPollJobRun) next(ctx context.Context, jctx *jobdef.JobContext, integration *models.Integration) {
	cfg := r.config

	if !cfg.Deadline.IsZero() && !r.clock().Before(cfg.Deadline) {
		notifications.RecordA2AComment(ctx, jctx, integration, cfg.A2AIncidentUID, notifications.A2ATimeoutNote)

		return
	}

	cfg.Attempt++
	cfg.OrgUID = integration.OrganizationUID

	notifications.EnqueueA2ATaskPoll(ctx, jctx, &cfg, notifications.A2APollDelay(cfg.Attempt))
}
