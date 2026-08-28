package jobsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/realtime"
)

const (
	// MaxRetryCount is the maximum number of retries allowed for a job (so 3
	// attempts total: original + 2 retries). Exported so the stuck-job reaper
	// and the worker share a single source of truth for the cap instead of
	// duplicating the literal.
	MaxRetryCount = 2

	// eventTypeJobCreated is the notifier event type emitted when a new job is
	// created and ready to run soon. PgEventNotifier converts "." to "_" so the
	// on-the-wire Postgres channel name becomes "job_created".
	// dialect-agnostic — use s.notifier.Notify, never raw SQL.
	eventTypeJobCreated = "job.created"
)

// JobOptions contains options for creating a job.
type JobOptions struct {
	ScheduledAt *time.Time     // If nil, the job will be scheduled at the current time.
	BounceDelay *time.Duration // If provided, the job will be bounced after the delay if already exists.
}

// Service provides job queue operations.
//
//nolint:interfacebloat // Complete job-queue API: create/claim/complete/retry/reap/list.
type Service interface {
	// CreateJob creates a new job and notifies waiting runners.
	// If bounceDelay is provided:
	// - If a duplicate exists, updates its scheduled_at to now + bounceDelay
	// - If no duplicate, schedules the new job at now + bounceDelay
	CreateJob(
		ctx context.Context,
		orgUID, jobType string,
		config json.RawMessage,
		jobOptions *JobOptions,
	) (*models.Job, error)

	// GetJob retrieves a job by UID
	GetJob(ctx context.Context, uid string) (*models.Job, error)

	// ListJobs lists jobs with optional filtering
	ListJobs(ctx context.Context, orgUID string, opts ListJobsOptions) ([]*models.Job, error)

	// CancelJob cancels a pending job (soft delete)
	CancelJob(ctx context.Context, uid string) error

	// IsJobDeleted reports whether the job row has been soft-deleted
	// (canceled). The worker's cancellation watcher polls this while a job
	// runs so a mid-run cancellation (e.g. a stopped discovery scan) aborts
	// the runner instead of letting it hold its slot to completion.
	IsJobDeleted(ctx context.Context, uid string) (bool, error)

	// CancelPendingForIncident soft-deletes all pending jobs whose config
	// references the given incident UID under the "incidentUid" key. Returns
	// the number of jobs canceled. Used by ack / snooze / resolve to stop
	// pending notification jobs from firing on an incident the operator has
	// taken responsibility for.
	//
	// The incident_resolution_notice job type is deliberately exempt: it is not
	// a page, it is the all-clear owed to people who already got one. Sweeping
	// it would mean an ack landing between a resolution and the job's pickup
	// silently cancels the "it is over" message — the exact bug the job exists
	// to fix.
	//
	// If notBefore is non-nil, only jobs scheduled strictly before that
	// timestamp are canceled — used by snooze, where notifications scheduled
	// for after the snooze window are kept (they fire only if the incident
	// is still open then).
	CancelPendingForIncident(ctx context.Context, incidentUID string, notBefore *time.Time) (int64, error)

	// ListForIncident returns EVERY job of one type that references the
	// incident under the "incidentUid" config key — every status, and
	// soft-deleted rows included — oldest scheduled_at first.
	//
	// This is what makes an interrupted escalation cycle recoverable without a
	// new column: the rows ARE the record of where the cycle was when the
	// acknowledgment stopped it. Their config still carries stepUid,
	// repeatIndex and isLastStep, and their scheduled_at still carries when
	// each rung was due.
	//
	// It returns the WHOLE history rather than just the canceled rows on
	// purpose. One incident can accumulate several generations of the same rung
	// across repeated ack → unack cycles, and deciding whether a rung may be
	// resumed requires correlating them: a rung whose LATER generation already
	// fired must never be resurrected from its earlier canceled one. A
	// canceled-rows-only view cannot express that, so the correlation lives in
	// the caller with the full history in hand.
	ListForIncident(ctx context.Context, incidentUID, jobType string) ([]*models.Job, error)

	// GetJobWait waits for and claims the next available job
	// Uses PostgreSQL LISTEN/NOTIFY for efficient waiting
	GetJobWait(ctx context.Context) (*models.Job, error)

	// UpdateJobStatus updates job status to running/success/failed
	UpdateJobStatus(ctx context.Context, job *models.Job, status models.JobStatus, output json.RawMessage) error

	// CompleteRunningJob writes a terminal status from the worker, guarded so it
	// only applies while the job is still 'running'. If the stuck-job reaper (or
	// any other actor) already moved the row out of 'running', the write affects
	// zero rows and ErrJobLeaseLost is returned — the worker lost its job to the
	// reaper and must discard its result rather than clobber the reaper's
	// decision. This is the anti-clobber guard for the lease-collision race.
	CompleteRunningJob(
		ctx context.Context, job *models.Job, status models.JobStatus, output json.RawMessage,
	) error

	// RetryJob creates a new job from a failed job
	// Copies config, increments nb_tries, sets previous_job_uid
	RetryJob(ctx context.Context, job *models.Job) (*models.Job, error)

	// ReapStuckJobs recovers jobs stuck in 'running' whose updated_at is older
	// than now-timeout (orphaned by a dead/redeployed worker, since nothing
	// bumps updated_at mid-run). Each stuck job rides the existing retry chain:
	// if retries remain it is marked 'retried' and a backoff clone is scheduled,
	// otherwise it is marked 'failed' with reason "stuck_timeout". The
	// transition re-asserts the stuck condition atomically so a concurrent
	// reaper run or the original worker finishing is a no-op. Returns per-outcome
	// counts.
	ReapStuckJobs(ctx context.Context, timeout time.Duration) (ReapResult, error)

	// CountQueueDepth returns the number of live (deleted_at IS NULL) jobs in the
	// queue per status, across all organizations. Only "pending" and "running"
	// statuses are returned; both keys are always present (zero-filled) so a
	// drained status reports 0 rather than going stale. Used by the queue-depth
	// metrics sampler.
	CountQueueDepth(ctx context.Context) (map[models.JobStatus]int, error)
}

// ListJobsOptions contains filtering options for listing jobs.
type ListJobsOptions struct {
	// Type filters to a single job type. Ignored when Types is set.
	Type string
	// Types filters to any of the given job types (WHERE type IN). Takes
	// precedence over Type when non-empty.
	Types  []string
	Status string
	Limit  int
	Offset int
}

// serviceImpl implements the Service interface.
type serviceImpl struct {
	db       *bun.DB
	dbSvc    db.Service
	notifier notifier.EventNotifier
	// rt publishes coalesced org-scoped `jobs` dashboard hints on job
	// lifecycle changes. Nil-safe: nil when realtime is disabled.
	rt *realtime.Publisher
}

// NewService creates a new job service. rt may be nil (realtime disabled):
// hint publishing through it is a nil-safe no-op.
func NewService(bunDB *bun.DB, dbSvc db.Service, n notifier.EventNotifier, rt *realtime.Publisher) Service {
	return &serviceImpl{db: bunDB, dbSvc: dbSvc, notifier: n, rt: rt}
}

// publishJobsHint emits a coalesced `jobs` dashboard hint for org-scoped jobs.
// System jobs (nil org) have no dashboard to notify per org and are skipped.
func (s *serviceImpl) publishJobsHint(ctx context.Context, orgUID *string) {
	if orgUID == nil || *orgUID == "" {
		return
	}
	s.rt.Publish(ctx, *orgUID, "", realtime.KindJobs)
}

// CreateJob creates a new job and notifies waiting runners.
// Uses JobOptions to configure scheduling behavior:
// - If ScheduledAt is provided, job is scheduled at that time.
// - If BounceDelay is provided, job is scheduled at now + bounceDelay.
// - If both are nil, job is scheduled immediately.
// - ScheduledAt takes precedence over BounceDelay if both are provided.
// If a duplicate pending job exists, updates its scheduled_at to the new time.
func (s *serviceImpl) CreateJob(
	ctx context.Context,
	orgUID, jobType string,
	config json.RawMessage,
	jobOptions *JobOptions,
) (*models.Job, error) {
	scheduledAt := s.calculateScheduledTime(jobOptions)

	configMap, err := s.parseJobConfig(config)
	if err != nil {
		return nil, err
	}

	// Try to find and update existing job
	existing, found, err := s.findAndUpdateExistingJob(ctx, orgUID, jobType, configMap, scheduledAt)
	if err != nil {
		return nil, err
	}

	if found {
		s.publishJobsHint(ctx, existing.OrganizationUID)

		return existing, nil
	}

	// Create new job
	return s.createNewJob(ctx, orgUID, jobType, configMap, scheduledAt)
}

func (s *serviceImpl) calculateScheduledTime(jobOptions *JobOptions) time.Time {
	scheduledAt := time.Now()

	switch {
	case jobOptions != nil && jobOptions.ScheduledAt != nil:
		scheduledAt = *jobOptions.ScheduledAt
	case jobOptions != nil && jobOptions.BounceDelay != nil:
		scheduledAt = scheduledAt.Add(*jobOptions.BounceDelay)
	}

	return scheduledAt
}

func (s *serviceImpl) parseJobConfig(config json.RawMessage) (models.JSONMap, error) {
	var configMap models.JSONMap
	if len(config) > 0 {
		if err := json.Unmarshal(config, &configMap); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidJobConfig, err)
		}
	} else {
		configMap = make(models.JSONMap)
	}

	return configMap, nil
}

func (s *serviceImpl) findAndUpdateExistingJob(
	ctx context.Context,
	orgUID, jobType string,
	configMap models.JSONMap,
	scheduledAt time.Time,
) (*models.Job, bool, error) {
	var existing models.Job

	query := s.db.NewSelect().
		Model(&existing).
		Where("type = ?", jobType).
		Where("config = ?", configMap).
		Where("status = ?", models.JobStatusPending).
		Where("deleted_at IS NULL")

	if orgUID == "" {
		query = query.Where("organization_uid IS NULL")
	} else {
		query = query.Where("organization_uid = ?", orgUID)
	}

	err := query.Scan(ctx)
	if err == nil {
		// Found existing pending job, update its scheduled_at
		existing.ScheduledAt = scheduledAt
		existing.UpdatedAt = time.Now()

		_, err = s.db.NewUpdate().
			Model(&existing).
			Column("scheduled_at", "updated_at").
			Where("uid = ?", existing.UID).
			Exec(ctx)
		if err != nil {
			return nil, false, fmt.Errorf("failed to update existing job: %w", err)
		}

		return &existing, true, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("failed to check existing job: %w", err)
	}

	return nil, false, nil
}

func (s *serviceImpl) createNewJob(
	ctx context.Context,
	orgUID, jobType string,
	configMap models.JSONMap,
	scheduledAt time.Time,
) (*models.Job, error) {
	var orgUIDPtr *string
	if orgUID != "" {
		orgUIDPtr = &orgUID
	}

	job := models.NewJob(orgUIDPtr, jobType)
	job.Config = configMap
	job.ScheduledAt = scheduledAt

	_, err := s.db.NewInsert().
		Model(job).
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create job: %w", err)
	}

	// Wake up waiting job runners when the job is due within 15 minutes.
	if time.Until(job.ScheduledAt) <= 15*time.Minute {
		_ = s.notifier.Notify(ctx, eventTypeJobCreated, "{}")
	}

	s.publishJobsHint(ctx, job.OrganizationUID)

	return job, nil
}

// queueDepthCount is a scan target for the queue-depth GROUP BY status query.
type queueDepthCount struct {
	Status string `bun:"status"`
	Count  int    `bun:"count"`
}

// CountQueueDepth runs one cheap GROUP BY status query over the jobs table and
// returns per-status live counts. Both "pending" and "running" keys are always
// present (zero-filled) so absent statuses report 0 instead of a stale value.
func (s *serviceImpl) CountQueueDepth(ctx context.Context) (map[models.JobStatus]int, error) {
	// Zero-fill the statuses we expose so a drained status reports 0.
	depth := map[models.JobStatus]int{
		models.JobStatusPending: 0,
		models.JobStatusRunning: 0,
	}

	var counts []queueDepthCount

	err := s.db.NewSelect().
		Model((*models.Job)(nil)).
		Column("status").
		ColumnExpr("COUNT(*) AS count").
		Where("deleted_at IS NULL").
		Where("status IN (?)", bun.List([]models.JobStatus{models.JobStatusPending, models.JobStatusRunning})).
		Group("status").
		Scan(ctx, &counts)
	if err != nil {
		return nil, fmt.Errorf("failed to count queue depth: %w", err)
	}

	for _, c := range counts {
		status := models.JobStatus(c.Status)
		if _, ok := depth[status]; ok {
			depth[status] = c.Count
		}
	}

	return depth, nil
}

// GetJob retrieves a job by UID.
func (s *serviceImpl) GetJob(ctx context.Context, uid string) (*models.Job, error) {
	var job models.Job

	err := s.db.NewSelect().
		Model(&job).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Scan(ctx)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrJobNotFound, uid)
	} else if err != nil {
		return nil, fmt.Errorf("failed to get job: %w", err)
	}

	return &job, nil
}

// ListJobs lists jobs with optional filtering.
func (s *serviceImpl) ListJobs(
	ctx context.Context,
	orgUID string,
	opts ListJobsOptions,
) ([]*models.Job, error) {
	query := s.db.NewSelect().
		Model((*models.Job)(nil)).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Order("created_at DESC")

	if len(opts.Types) > 0 {
		query = query.Where("type IN (?)", bun.List(opts.Types))
	} else if opts.Type != "" {
		query = query.Where("type = ?", opts.Type)
	}

	if opts.Status != "" {
		query = query.Where("status = ?", opts.Status)
	}

	if opts.Limit > 0 {
		query = query.Limit(opts.Limit)
	}

	if opts.Offset > 0 {
		query = query.Offset(opts.Offset)
	}

	var jobs []*models.Job

	err := query.Scan(ctx, &jobs)
	if err != nil {
		return nil, fmt.Errorf("failed to list jobs: %w", err)
	}

	return jobs, nil
}

// jobTypeIncidentResolutionNotice is the one job type CancelPendingForIncident
// must never sweep — see the interface doc for why.
//
// Spelled as a literal rather than jobdef.JobTypeIncidentResolutionNotice
// because jobdef imports app/services, which imports this package; the two
// constants are pinned to each other by TestCancelPendingExemptsResolutionNotice.
const jobTypeIncidentResolutionNotice = "incident_resolution_notice"

// CancelPendingForIncident soft-deletes pending jobs that reference an
// incident UID in their config. The expression varies by dialect because
// PostgreSQL uses ->> and SQLite uses json_extract.
func (s *serviceImpl) CancelPendingForIncident(
	ctx context.Context, incidentUID string, notBefore *time.Time,
) (int64, error) {
	if incidentUID == "" {
		return 0, nil
	}

	now := time.Now()

	var configExpr string
	if _, isPostgres := s.db.Dialect().(*pgdialect.Dialect); isPostgres {
		configExpr = "config->>'incidentUid' = ?"
	} else {
		configExpr = "json_extract(config, '$.incidentUid') = ?"
	}

	query := s.db.NewUpdate().
		Model((*models.Job)(nil)).
		Set("deleted_at = ?", now).
		Set("updated_at = ?", now).
		Where("status = ?", models.JobStatusPending).
		Where("deleted_at IS NULL").
		Where("type != ?", jobTypeIncidentResolutionNotice).
		Where(configExpr, incidentUID)

	if notBefore != nil {
		query = query.Where("scheduled_at < ?", *notBefore)
	}

	result, err := query.Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to cancel jobs for incident: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get rows affected: %w", err)
	}

	// Also cancel any pending audit rows for this incident.
	if s.dbSvc != nil {
		canceledCount, auditErr := s.dbSvc.CancelIncidentNotificationsForIncident(ctx, incidentUID, now)
		if auditErr != nil {
			slog.WarnContext(ctx, "failed to cancel notification audit rows",
				"incident_uid", incidentUID, "error", auditErr)
		} else {
			slog.DebugContext(ctx, "canceled notification audit rows",
				"incident_uid", incidentUID, "count", canceledCount)
		}
	}

	return rows, nil
}

// ListForIncident returns every job of one type for an incident — all
// statuses, soft-deleted rows included — oldest scheduled_at first.
//
// The deliberate absence of a `deleted_at IS NULL` filter is the point: this is
// the ONLY read in the codebase that wants canceled rows back, because
// CancelPendingForIncident soft-deletes rather than erasing and those rows are
// the record of an interrupted escalation cycle. Statuses are returned
// unfiltered for the same reason — the caller needs to see which generations
// of a rung already ran before it can decide which may be resumed.
func (s *serviceImpl) ListForIncident(
	ctx context.Context, incidentUID, jobType string,
) ([]*models.Job, error) {
	if incidentUID == "" || jobType == "" {
		return nil, nil
	}

	var configExpr string
	if _, isPostgres := s.db.Dialect().(*pgdialect.Dialect); isPostgres {
		configExpr = "config->>'incidentUid' = ?"
	} else {
		configExpr = "json_extract(config, '$.incidentUid') = ?"
	}

	var jobs []*models.Job

	err := s.db.NewSelect().
		Model(&jobs).
		Where("type = ?", jobType).
		Where(configExpr, incidentUID).
		Order("scheduled_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list jobs for incident: %w", err)
	}

	return jobs, nil
}

// IsJobDeleted reports whether the job row has been soft-deleted (canceled).
func (s *serviceImpl) IsJobDeleted(ctx context.Context, uid string) (bool, error) {
	count, err := s.db.NewSelect().
		TableExpr("jobs").
		Where("uid = ?", uid).
		Where("deleted_at IS NOT NULL").
		Count(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to check job deletion: %w", err)
	}

	return count > 0, nil
}

// CancelJob cancels a pending job (soft delete).
func (s *serviceImpl) CancelJob(ctx context.Context, uid string) error {
	now := time.Now()

	result, err := s.db.NewUpdate().
		Model((*models.Job)(nil)).
		Set("deleted_at = ?", now).
		Set("updated_at = ?", now).
		Where("uid = ?", uid).
		Where("status = ?", models.JobStatusPending).
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to cancel job: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rows == 0 {
		return ErrJobNotPending
	}

	// Live hint: fetch the org of the canceled job (cheap, cancel is a rare
	// operator action) so watching dashboards drop it from the pending list.
	var canceled models.Job
	if err := s.db.NewSelect().Model(&canceled).Column("organization_uid").
		Where("uid = ?", uid).Scan(ctx); err == nil {
		s.publishJobsHint(ctx, canceled.OrganizationUID)
	}

	return nil
}

// GetJobWait waits for and claims the next available job. It subscribes to
// job.created events via the EventNotifier so new-job insertions wake the
// runner within milliseconds on both SQLite (LocalEventNotifier channels)
// and Postgres (LISTEN/NOTIFY). A 5-minute fallback ticker handles missed
// signals and jobs whose scheduled_at passed without a signal.
//
// Only sql.ErrNoRows means "no work": every other error is returned to the
// caller unchanged and unwrapped-through, which is what lets the runner
// classify it (internal/db/dbfault) and treat a structural fault as terminal.
// Deciding here would be wrong — the same error means "back off" to one caller
// and "stop the process" to another (spec 2026-08-12-05).
func (s *serviceImpl) GetJobWait(ctx context.Context) (*models.Job, error) {
	wakeup := s.notifier.Listen(eventTypeJobCreated)
	// GetJobWait subscribes on every call (job runners loop over it), so it MUST
	// deregister on return — otherwise one listener channel leaks per processed
	// job, the notifier's forward loop slows linearly, and on Postgres the
	// LISTEN/NOTIFY pipeline eventually wedges and silences the realtime WS.
	defer s.notifier.Unlisten(eventTypeJobCreated, wakeup)

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		job, err := s.claimNextJob(ctx)
		if err == nil {
			return job, nil
		}

		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wakeup:
			// Wake-up signal from CreateJob — try to claim immediately.
		case <-ticker.C:
			// Fallback poll: handles missed signals and past-due jobs.
		}
	}
}

// claimNextJob attempts to claim the next available job.
// Uses SELECT FOR UPDATE SKIP LOCKED on PostgreSQL for efficiency.
// Uses optimistic locking on SQLite (no FOR UPDATE support).
func (s *serviceImpl) claimNextJob(ctx context.Context) (*models.Job, error) {
	var job models.Job

	// Check if database is PostgreSQL (supports FOR UPDATE SKIP LOCKED)
	_, isPostgres := s.db.Dialect().(*pgdialect.Dialect)

	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, transaction bun.Tx) error {
		// Select next available job
		query := transaction.NewSelect().
			Model(&job).
			Where("status = ?", models.JobStatusPending).
			Where("scheduled_at <= ?", time.Now()).
			Where("deleted_at IS NULL").
			Order("scheduled_at ASC").
			Limit(1)

		// PostgreSQL: Use FOR UPDATE SKIP LOCKED for efficient row-level locking
		// SQLite: Will use optimistic locking instead
		if isPostgres {
			query = query.For("UPDATE SKIP LOCKED")
		}

		err := query.Scan(ctx)
		if err != nil {
			return err
		}

		// Update status to running (with optimistic locking check for SQLite)
		now := time.Now()

		result, err := transaction.NewUpdate().
			Model(&job).
			Set("status = ?", models.JobStatusRunning).
			Set("updated_at = ?", now).
			Where("uid = ?", job.UID).
			Where("status = ?", models.JobStatusPending). // Optimistic lock: only update if still pending
			Exec(ctx)
		if err != nil {
			return err
		}

		// For SQLite: Verify the update succeeded (optimistic locking)
		if !isPostgres {
			rows, err := result.RowsAffected()
			if err != nil {
				return err
			}

			if rows == 0 {
				// Job was claimed by another runner, return ErrNoRows to trigger retry
				return sql.ErrNoRows
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	// Live hint: the job flipped pending -> running.
	s.publishJobsHint(ctx, job.OrganizationUID)

	return &job, nil
}

// UpdateJobStatus updates job status to running/success/failed.
func (s *serviceImpl) UpdateJobStatus(
	ctx context.Context,
	job *models.Job,
	status models.JobStatus,
	output json.RawMessage,
) error {
	_, err := s.writeJobStatus(ctx, job, status, output, "")

	return err
}

// CompleteRunningJob writes a terminal status guarded on the job still being
// 'running'. Zero rows affected means the reaper (or another actor) already
// transitioned the job — return ErrJobLeaseLost so the worker discards its
// result instead of clobbering the reaper's decision.
func (s *serviceImpl) CompleteRunningJob(
	ctx context.Context,
	job *models.Job,
	status models.JobStatus,
	output json.RawMessage,
) error {
	rows, err := s.writeJobStatus(ctx, job, status, output, models.JobStatusRunning)
	if err != nil {
		return err
	}

	if rows == 0 {
		return ErrJobLeaseLost
	}

	return nil
}

// writeJobStatus performs the status update. When expectedStatus is non-empty
// the update re-asserts `status = expectedStatus` in the WHERE clause (the
// optimistic guard) and the caller can inspect RowsAffected to detect a lost
// race. On success the in-memory job is updated to match.
func (s *serviceImpl) writeJobStatus(
	ctx context.Context,
	job *models.Job,
	status models.JobStatus,
	output json.RawMessage,
	expectedStatus models.JobStatus,
) (int64, error) {
	now := time.Now()

	update := s.db.NewUpdate().
		Model(job).
		Set("status = ?", status).
		Set("updated_at = ?", now).
		Where("uid = ?", job.UID)

	if expectedStatus != "" {
		update = update.Where("status = ?", expectedStatus)
	}

	if output != nil {
		var outputMap models.JSONMap
		if err := json.Unmarshal(output, &outputMap); err != nil {
			return 0, fmt.Errorf("invalid output: %w", err)
		}

		update = update.Set("output = ?", outputMap)
	}

	result, err := update.Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to update job status: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get rows affected: %w", err)
	}

	// Only reflect the change in memory when a row actually moved.
	if rows > 0 {
		job.Status = status
		job.UpdatedAt = now
		s.publishJobsHint(ctx, job.OrganizationUID)
	}

	return rows, nil
}

// RetryJob creates a new job from a failed job.
// Copies config, increments retry_count, sets previous_job_uid.
func (s *serviceImpl) RetryJob(ctx context.Context, job *models.Job) (*models.Job, error) {
	if job.RetryCount >= MaxRetryCount {
		return nil, ErrMaxRetriesReached
	}

	// Mark original job as retried
	err := s.UpdateJobStatus(ctx, job, models.JobStatusRetried, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to mark job as retried: %w", err)
	}

	return s.createRetryClone(ctx, job)
}

// createRetryClone inserts a backoff clone of job with retry_count+1 and
// previous_job_uid set. Used by both RetryJob (worker path) and ReapStuckJobs
// (reaper path) so the clone shape and backoff schedule stay identical.
func (s *serviceImpl) createRetryClone(ctx context.Context, job *models.Job) (*models.Job, error) {
	retryJob := models.NewJob(job.OrganizationUID, job.Type)
	retryJob.Config = job.Config
	retryJob.RetryCount = job.RetryCount + 1
	retryJob.PreviousJobUID = &job.UID

	// Schedule with exponential backoff: 1min, 5min, 15min
	backoff := []time.Duration{1 * time.Minute, 5 * time.Minute, 15 * time.Minute}
	if job.RetryCount < len(backoff) {
		retryJob.ScheduledAt = time.Now().Add(backoff[job.RetryCount])
	}

	if _, err := s.db.NewInsert().Model(retryJob).Exec(ctx); err != nil {
		return nil, fmt.Errorf("failed to create retry job: %w", err)
	}

	s.publishJobsHint(ctx, retryJob.OrganizationUID)

	return retryJob, nil
}

// ReapResult reports how many stuck jobs were recovered in one sweep, split by
// terminal outcome.
type ReapResult struct {
	// Retried is the number of stuck jobs flipped to 'retried' with a backoff
	// clone scheduled (retries remained).
	Retried int
	// Failed is the number of stuck jobs flipped to 'failed' with reason
	// "stuck_timeout" (retry cap reached).
	Failed int
}

// stuckTimeoutReasonFmt is the output written on a job failed by the reaper for
// exhausting retries while stuck. The "reason" key keeps stuck-timeout failures
// machine-distinguishable from error-failures without inventing a new status.
const stuckTimeoutReasonFmt = `{"error":"stuck: no update within %s","reason":"stuck_timeout"}`

// ReapStuckJobs recovers jobs stuck in 'running' beyond the timeout. See the
// interface doc for the contract. Each transition re-asserts the stuck
// condition atomically (RowsAffected check), so a concurrent reaper run or the
// original worker finishing first is a no-op rather than a double-action.
func (s *serviceImpl) ReapStuckJobs(ctx context.Context, timeout time.Duration) (ReapResult, error) {
	cutoff := time.Now().Add(-timeout)

	var stuck []*models.Job

	err := s.db.NewSelect().
		Model(&stuck).
		Where("status = ?", models.JobStatusRunning).
		Where("updated_at < ?", cutoff).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return ReapResult{}, fmt.Errorf("failed to list stuck jobs: %w", err)
	}

	var result ReapResult

	for _, job := range stuck {
		outcome, reapErr := s.reapOneStuckJob(ctx, job, cutoff, timeout)
		if reapErr != nil {
			// Log and continue: one bad row must not abort the whole sweep.
			slog.WarnContext(ctx, "failed to reap stuck job", "job_uid", job.UID, "error", reapErr)

			continue
		}

		//nolint:exhaustive // reapOneStuckJob only ever returns retried, failed, or "" (lost race).
		switch outcome {
		case models.JobStatusRetried:
			result.Retried++
		case models.JobStatusFailed:
			result.Failed++
		default:
			// Lost the race (already transitioned) — counted as neither.
		}
	}

	return result, nil
}

// reapOneStuckJob atomically claims one stuck job (re-asserting status='running'
// AND updated_at < cutoff) and transitions it. Returns the terminal status
// applied, or an empty status when the row was already transitioned by someone
// else (RowsAffected == 0).
func (s *serviceImpl) reapOneStuckJob(
	ctx context.Context, job *models.Job, cutoff time.Time, timeout time.Duration,
) (models.JobStatus, error) {
	if job.RetryCount < MaxRetryCount {
		// Retries remain: atomically claim the row by flipping running -> retried,
		// re-asserting the stuck condition so a concurrent transition is a no-op.
		claimed, err := s.claimStuckJob(ctx, job, models.JobStatusRetried, cutoff, nil)
		if err != nil {
			return "", err
		}

		if !claimed {
			return "", nil
		}

		if _, err := s.createRetryClone(ctx, job); err != nil {
			return "", err
		}

		return models.JobStatusRetried, nil
	}

	// Retry cap reached: fail the job with a machine-readable reason.
	output := json.RawMessage(fmt.Sprintf(stuckTimeoutReasonFmt, timeout.String()))

	claimed, err := s.claimStuckJob(ctx, job, models.JobStatusFailed, cutoff, output)
	if err != nil {
		return "", err
	}

	if !claimed {
		return "", nil
	}

	return models.JobStatusFailed, nil
}

// claimStuckJob performs the guarded transition of a single stuck job to status,
// re-asserting `status='running' AND updated_at < cutoff`. Returns true when the
// row moved (claimed by us), false when it was already transitioned. On a claim
// the in-memory job is updated so callers (e.g. createRetryClone) see the new
// state.
func (s *serviceImpl) claimStuckJob(
	ctx context.Context,
	job *models.Job,
	status models.JobStatus,
	cutoff time.Time,
	output json.RawMessage,
) (bool, error) {
	now := time.Now()

	update := s.db.NewUpdate().
		Model(job).
		Set("status = ?", status).
		Set("updated_at = ?", now).
		Where("uid = ?", job.UID).
		Where("status = ?", models.JobStatusRunning).
		Where("updated_at < ?", cutoff)

	if output != nil {
		var outputMap models.JSONMap
		if err := json.Unmarshal(output, &outputMap); err != nil {
			return false, fmt.Errorf("invalid output: %w", err)
		}

		update = update.Set("output = ?", outputMap)
	}

	res, err := update.Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to claim stuck job: %w", err)
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rows == 0 {
		return false, nil
	}

	job.Status = status
	job.UpdatedAt = now
	s.publishJobsHint(ctx, job.OrganizationUID)

	return true, nil
}
