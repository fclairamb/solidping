package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/handlers/incidentpublications"
	"github.com/fclairamb/solidping/server/internal/handlers/statussubscribers"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/realtime"
	clockpkg "github.com/fclairamb/solidping/server/internal/utils/clock"
)

// ErrIncidentPublishConfig is returned when the job config is unusable.
var ErrIncidentPublishConfig = errors.New("incident publish job: incidentUid and statusPageUid are required")

// IncidentPublishJobDefinition is the factory for the auto-publish debounce
// job.
type IncidentPublishJobDefinition struct{}

// Type returns the incident-publish job type.
func (d *IncidentPublishJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeIncidentPublish
}

// IncidentPublishJobConfig identifies which incident to publish where.
//
// It deliberately carries only identifiers. Everything else — the page's
// current auto-publish setting, the incident's state, the maintenance window,
// whether somebody already published by hand — is re-read at fire time, because
// all of it can change during the debounce and the whole purpose of the delay
// is to act on what is true THEN, not on what was true when the timer started.
type IncidentPublishJobConfig struct {
	OrganizationUID string `json:"organizationUid"`
	IncidentUID     string `json:"incidentUid"`
	StatusPageUID   string `json:"statusPageUid"`
}

// CreateJobRun builds an executable instance.
//
//nolint:ireturn // Factory pattern requires interface return
func (d *IncidentPublishJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg IncidentPublishJobConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, err
	}

	if cfg.IncidentUID == "" || cfg.StatusPageUID == "" {
		return nil, ErrIncidentPublishConfig
	}

	return &IncidentPublishJobRun{config: cfg}, nil
}

// IncidentPublishJobRun is one execution of the debounce job.
type IncidentPublishJobRun struct {
	config IncidentPublishJobConfig
}

// Run re-evaluates eligibility and publishes if the incident is still open.
// The publication service is built here from the job context rather than
// injected, so this job has no dependency on the HTTP server's wiring.
func (r *IncidentPublishJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	orgUID := r.config.OrganizationUID
	if orgUID == "" && jctx.OrganizationUID != nil {
		orgUID = *jctx.OrganizationUID
	}

	if orgUID == "" {
		return ErrIncidentPublishConfig
	}

	var (
		clk       clockpkg.Clock
		publisher *realtime.Publisher
	)

	if jctx.Services != nil {
		clk = jctx.Services.Clock
		publisher = jctx.Services.Realtime
	}

	svc := incidentpublications.NewService(jctx.DBService, clk, publisher)
	svc.SetLogger(jctx.Logger)

	if jctx.Services != nil {
		svc.SetJobsService(jctx.Services.Jobs)

		// Unconditional now: webhook/Slack subscriptions deliver even on an
		// instance with no mailer, and the notifier skips only the email leg
		// when the sender is nil.
		baseURL := ""
		if jctx.AppConfig != nil {
			baseURL = jctx.AppConfig.Server.BaseURL
		}

		svc.SetSubscriberNotifier(statussubscribers.NewNotifier(
			jctx.DBService, jctx.Services.EmailSender, jctx.Services.EmailFormatter,
			baseURL, jctx.Logger, jctx.Services.Credentials,
		))
	}

	if err := svc.AutoPublish(ctx, orgUID, r.config.IncidentUID, r.config.StatusPageUID); err != nil {
		return jobdef.NewRetryableError(fmt.Errorf("incident publish: %w", err))
	}

	return nil
}

// IncidentPublishScheduler enqueues the debounce job. It satisfies
// incidentpublications.PublishScheduler without that package importing the job
// registry (which imports it).
type IncidentPublishScheduler struct {
	jobs jobsvc.Service
}

// NewIncidentPublishScheduler builds a scheduler over the job queue.
func NewIncidentPublishScheduler(jobs jobsvc.Service) *IncidentPublishScheduler {
	return &IncidentPublishScheduler{jobs: jobs}
}

// SchedulePublish enqueues the debounce job for `at`.
func (s *IncidentPublishScheduler) SchedulePublish(
	ctx context.Context, orgUID, incidentUID, statusPageUID string, fireAt time.Time,
) error {
	if s == nil || s.jobs == nil {
		return nil
	}

	config, err := json.Marshal(IncidentPublishJobConfig{
		OrganizationUID: orgUID,
		IncidentUID:     incidentUID,
		StatusPageUID:   statusPageUID,
	})
	if err != nil {
		return fmt.Errorf("marshal incident publish config: %w", err)
	}

	scheduledAt := fireAt

	if _, err := s.jobs.CreateJob(
		ctx, orgUID, string(jobdef.JobTypeIncidentPublish), config,
		&jobsvc.JobOptions{ScheduledAt: &scheduledAt},
	); err != nil {
		return fmt.Errorf("enqueue incident publish job: %w", err)
	}

	return nil
}
