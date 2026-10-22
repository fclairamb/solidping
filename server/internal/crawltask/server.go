package crawltask

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

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/handlers/workers"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// ResultRecorder writes a run's result through the submit path (incidents,
// notifications): workers.Service.
type ResultRecorder interface {
	RecordResult(ctx context.Context, job *models.CheckJob, req *workers.SubmitResultRequest) error
}

// ReportStore keeps the crawl reports (attachments.Service).
type ReportStore interface {
	PutCrawlReport(ctx context.Context, orgUID, checkUID string, report []byte, details models.JSONMap) (string, error)
	LatestCrawlReport(ctx context.Context, orgUID, checkUID string) ([]byte, error)
}

// Deps are what the server side of the kind needs.
type Deps struct {
	DB      db.Service
	Tasks   *workertasks.Service
	Results ResultRecorder
	Reports ReportStore
	// Entitlements reserves the slices' units. Nil reserves nothing.
	Entitlements *entitlements.Service
	// SliceBudget is the wall-clock budget of one slice.
	SliceBudget time.Duration
}

// Server is the server side of the crawl kind.
type Server struct {
	deps       Deps
	getChecker func(checkerdef.CheckType) (checkerdef.Checker, bool)
	logger     *slog.Logger
}

// Register registers the crawl kind on deps.Tasks and returns its server side.
func Register(deps Deps) *Server {
	if deps.SliceBudget <= 0 {
		deps.SliceBudget = scheduling.DefaultBulkSliceBudget
	}

	srv := &Server{
		deps:       deps,
		getChecker: registry.GetChecker,
		logger:     slog.Default().With("component", "crawl_task"),
	}

	deps.Tasks.RegisterKind(workertasks.TaskKind{
		Name:         Kind,
		Materialize:  srv.materialize,
		PrepareClaim: srv.prepareClaim,
		Step:         srv.step,
		Complete:     srv.complete,
		Fail:         srv.fail,
		End:          srv.end,
	})

	return srv
}

func activeTaskStatuses() []models.WorkerTaskStatus {
	return []models.WorkerTaskStatus{models.WorkerTaskStatusPending, models.WorkerTaskStatusClaimed}
}

// materialize turns the claiming worker's due crawl jobs into pending tasks
// and leases each job to its task: no lease_worker_uid (the column is a
// workers foreign key), a lease_expires_at past the run's deadline. An old
// node's bulk claim, which reads only the expiry, then skips the job.
func (s *Server) materialize(ctx context.Context, tx bun.IDB, identity *workertasks.WorkerIdentity) error {
	now := time.Now().UTC()

	var jobs []*models.CheckJob

	activeTask := tx.NewSelect().
		TableExpr("worker_tasks AS wt").
		ColumnExpr("1").
		Where("wt.check_uid = check_job.check_uid").
		Where("wt.status IN (?)", bun.List(activeTaskStatuses())).
		Where("wt.updated_at >= ?", now.Add(-models.WorkerTaskStaleAfter))

	query := tx.NewSelect().Model(&jobs).
		Where("check_job.lane = ?", models.LaneBulk).
		Where("check_job.type = ?", string(checkerdef.CheckTypeCrawl)).
		WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.Where("check_job.scheduled_at <= ?", now).WhereOr("check_job.run_requested_at IS NOT NULL")
		}).
		WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.Where("check_job.lease_expires_at IS NULL").WhereOr("check_job.lease_expires_at < ?", now)
		}).
		Where("NOT EXISTS (?)", activeTask).
		OrderExpr("check_job.scheduled_at ASC").
		Limit(materializeBatch)

	query = scopeJobs(query, identity)

	if _, isPG := tx.Dialect().(*pgdialect.Dialect); isPG {
		query = query.For("UPDATE OF check_job SKIP LOCKED")
	}

	if err := query.Scan(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("select due crawl jobs: %w", err)
	}

	for _, job := range jobs {
		if err := s.startRun(ctx, tx, identity, job, now); err != nil {
			return err
		}
	}

	return nil
}

// scopeJobs restricts the due jobs to the worker's scope: for an agent, its
// exact region and org; for a cloud worker, the cloud regions it serves (the
// same rule as the check_jobs cloud claim: a NULL region, or a region the
// worker's own one starts with), never a private one.
func scopeJobs(query *bun.SelectQuery, identity *workertasks.WorkerIdentity) *bun.SelectQuery {
	if identity.IsAgent {
		return query.
			Where("check_job.region = ?", identity.Region).
			Where("check_job.organization_uid = ?", identity.OrgUID)
	}

	query = query.WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("check_job.region IS NULL").WhereOr("check_job.region NOT LIKE ?", "@%")
	})

	if identity.Region == "" {
		return query
	}

	return query.WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("check_job.region IS NULL").WhereOr("? LIKE check_job.region || '%'", identity.Region)
	})
}

// startRun creates the run's task, in the claiming worker's region so its
// peers claim it too, and leases the job to it.
func (s *Server) startRun(
	ctx context.Context, tx bun.IDB, identity *workertasks.WorkerIdentity, job *models.CheckJob, now time.Time,
) error {
	task := models.NewWorkerTask(job.OrganizationUID, Kind, identity.Region)
	task.CheckUID = &job.CheckUID
	task.Payload = models.JSONMap{payloadJobUID: job.UID}

	created, err := s.deps.Tasks.CreateIfNone(ctx, tx, task)
	if err != nil {
		return err
	}

	if !created {
		return nil
	}

	_, err = tx.NewUpdate().Model((*models.CheckJob)(nil)).
		Set("lease_worker_uid = NULL").
		Set("lease_expires_at = ?", now.Add(maxRunDurationOf(parseConfig(job.Type, job.Config))+runLeaseMargin)).
		Set("lease_starts = lease_starts + 1").
		// The run answers a parked "run now".
		Set("run_requested_at = NULL").
		Set("capture_claimed_at = NULL").
		Set("updated_at = ?", now).
		Where("uid = ?", job.UID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("lease crawl job to its task: %w", err)
	}

	return nil
}

// parseConfig decodes a crawl config, nil when it does not decode.
func parseConfig(checkType string, raw map[string]any) checkerdef.Config {
	config, ok := registry.ParseConfig(checkerdef.CheckType(checkType))
	if !ok || config.FromMap(raw) != nil {
		return nil
	}

	return config
}

// maxRunDurationOf reads the config's run deadline (default 30 min, max 2 h).
func maxRunDurationOf(config checkerdef.Config) time.Duration {
	hint, ok := config.(checkerdef.MaxRunDurationHint)
	if !ok || hint.MaxRunDuration() <= 0 {
		return defaultMaxRunDuration
	}

	return min(hint.MaxRunDuration(), maxMaxRunDuration)
}

// load returns the task's check and job, nil when gone (deleted, paused).
func (s *Server) load(ctx context.Context, task *models.WorkerTask) (*models.Check, *models.CheckJob, error) {
	if task.CheckUID == nil {
		return nil, nil, nil
	}

	check, err := s.deps.DB.GetCheck(ctx, task.OrganizationUID, *task.CheckUID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && check == nil) {
		return nil, nil, nil
	}

	if err != nil {
		return nil, nil, fmt.Errorf("load crawl check: %w", err)
	}

	jobUID, _ := task.Payload[payloadJobUID].(string)
	if jobUID == "" {
		return check, nil, nil
	}

	job := new(models.CheckJob)

	err = s.deps.DB.DB().NewSelect().Model(job).Where("uid = ?", jobUID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return check, nil, nil
	}

	if err != nil {
		return nil, nil, fmt.Errorf("load crawl job: %w", err)
	}

	job.Check = check

	return check, job, nil
}

func (s *Server) stepChecker(checkType string) (checkerdef.StepChecker, bool) {
	checker, ok := s.getChecker(checkerdef.CheckType(checkType))
	if !ok {
		return nil, false
	}

	stepChecker, ok := checker.(checkerdef.StepChecker)

	return stepChecker, ok
}

// prepareClaim builds a slice: the job's config, the slice budget, the units
// reserved from the org's bucket, and final once the run is past its deadline.
// A paused or deleted check cancels the task.
func (s *Server) prepareClaim(
	ctx context.Context, task *models.WorkerTask, _ json.RawMessage,
) (*workertasks.Prepared, error) {
	check, job, err := s.load(ctx, task)
	if err != nil {
		return nil, err
	}

	if check == nil || !check.Enabled || job == nil {
		return nil, workertasks.ErrCancelTask
	}

	stepChecker, ok := s.stepChecker(job.Type)
	if !ok {
		return nil, workertasks.ErrCancelTask
	}

	granted, deferUntil := s.reserve(ctx, task, job, stepChecker.UnitsPerSlice())
	if deferUntil != nil {
		return &workertasks.Prepared{DeferUntil: deferUntil}, nil
	}

	input, err := json.Marshal(Input{Type: job.Type, Config: job.Config})
	if err != nil {
		return nil, fmt.Errorf("encode crawl input: %w", err)
	}

	final := task.RunStartedAt != nil &&
		time.Since(*task.RunStartedAt) >= maxRunDurationOf(parseConfig(job.Type, job.Config))

	return &workertasks.Prepared{
		Input:       input,
		SliceBudget: s.deps.SliceBudget,
		Units:       granted,
		Final:       final,
	}, nil
}

// reserve draws up to n units from the org's checks-per-minute bucket. A
// drained bucket defers the slice: a run in progress by a minute, a run not
// yet started to the job's next tick.
func (s *Server) reserve(ctx context.Context, task *models.WorkerTask, job *models.CheckJob, n int) (int, *time.Time) {
	ent := s.deps.Entitlements
	if ent == nil || job.IsInternal() {
		return n, nil
	}

	granted, err := ent.ReserveCheckExecutionsUpTo(ctx, task.OrganizationUID, n)
	if err != nil {
		s.logger.WarnContext(ctx, "ReserveCheckExecutionsUpTo failed; running the slice anyway", "error", err)

		return n, nil
	}

	if granted > 0 {
		return granted, nil
	}

	prommetrics.ChecksRateLimited.WithLabelValues(task.OrganizationUID).Inc()
	ent.RecordRateLimitedSkip(ctx, task.OrganizationUID)

	s.logger.InfoContext(ctx, "Crawl slice rate-limited; deferring", "check_uid", job.CheckUID, "task", task.UID)

	next := time.Now().Add(rateDeferral)
	if task.SliceCount == 0 && task.StateFileUID == nil {
		next = nextTick(job)
	}

	return 0, &next
}

// nextTick is the job's next period after its current due time.
func nextTick(job *models.CheckJob) time.Time {
	now := time.Now()
	period := time.Duration(job.Period)

	if job.ScheduledAt != nil {
		if next := job.ScheduledAt.Add(period); next.After(now) {
			return next
		}
	}

	return now.Add(period)
}

func (s *Server) refund(ctx context.Context, task *models.WorkerTask, used int) {
	unused := task.UnitsGranted - min(max(used, 0), task.UnitsGranted)
	if unused <= 0 || s.deps.Entitlements == nil {
		return
	}

	s.deps.Entitlements.RefundCheckExecutions(ctx, task.OrganizationUID, unused)
}

// step refunds what a non-final slice did not use.
func (s *Server) step(ctx context.Context, task *models.WorkerTask, req workertasks.StepRequest) error {
	s.refund(ctx, task, req.Units)

	return nil
}

// complete refunds the last slice's unused units, diffs the report against
// the previous run's, stores it, and writes the run's one result.
func (s *Server) complete(ctx context.Context, task *models.WorkerTask, body json.RawMessage) (string, error) {
	var res Result
	if err := json.Unmarshal(body, &res); err != nil {
		s.refund(ctx, task, 0)

		return codeInvalidResult, s.writeError(ctx, task, "the worker sent an unreadable crawl result")
	}

	s.refund(ctx, task, res.Units)

	check, job, err := s.load(ctx, task)
	if err != nil {
		return "", err
	}

	if res.Output == nil {
		res.Output = map[string]any{}
	}

	out := &checkerdef.StepOutput{
		Done:   true,
		Report: res.Report,
		Result: &checkerdef.Result{
			Status:  checkerdef.Status(res.Status),
			Metrics: res.Metrics,
			Output:  res.Output,
		},
	}

	s.diffReport(ctx, task, job, out)

	if len(out.Report) > 0 {
		fileUID, putErr := s.deps.Reports.PutCrawlReport(ctx, task.OrganizationUID, *task.CheckUID, out.Report,
			models.JSONMap{
				attachments.DetailKeyRunUID:     task.UID,
				attachments.DetailKeyCheckUID:   *task.CheckUID,
				attachments.DetailKeyCapturedAt: time.Now().UTC().Format(time.RFC3339),
			})
		if putErr != nil {
			s.logger.WarnContext(ctx, "Failed to store the crawl report", "task", task.UID, "error", putErr)
		} else {
			out.Result.Output[OutputKeyReportFileUID] = fileUID
		}
	}

	return "", s.deps.Results.RecordResult(ctx, resultJob(task, check, job), &workers.SubmitResultRequest{
		WorkerUID: s.resultWorker(ctx, task),
		Status:    int(out.Result.Status),
		Duration:  float32(res.DurationMs),
		Metrics:   out.Result.Metrics,
		Output:    out.Result.Output,
		FromProbe: true,
	})
}

// diffReport computes newFindings against the previous run's report.
func (s *Server) diffReport(
	ctx context.Context, task *models.WorkerTask, job *models.CheckJob, out *checkerdef.StepOutput,
) {
	checkType := string(checkerdef.CheckTypeCrawl)
	if job != nil {
		checkType = job.Type
	}

	checker, ok := s.getChecker(checkerdef.CheckType(checkType))
	if !ok {
		return
	}

	differ, ok := checker.(checkerdef.ReportDiffer)
	if !ok {
		return
	}

	previous, err := s.deps.Reports.LatestCrawlReport(ctx, task.OrganizationUID, *task.CheckUID)
	if err != nil {
		s.logger.WarnContext(ctx, "Previous crawl report unreadable", "task", task.UID, "error", err)
	}

	differ.DiffAgainst(previous, out)
}

// fail writes the error result of a run the mechanism ended (3 failed
// slices, 3 lost leases, a lost state).
func (s *Server) fail(ctx context.Context, task *models.WorkerTask, errorCode string) error {
	message := "the website crawl failed 3 slices in a row (" + errorCode + ")"

	switch errorCode {
	case workertasks.ErrorMaxAttempts:
		message = "the website crawl lost its worker 3 times in a row"
	case workertasks.ErrorStateMissing:
		message = "the website crawl lost its saved state"
	}

	return s.writeError(ctx, task, message)
}

func (s *Server) writeError(ctx context.Context, task *models.WorkerTask, message string) error {
	check, job, err := s.load(ctx, task)
	if err != nil {
		return err
	}

	return s.deps.Results.RecordResult(ctx, resultJob(task, check, job), &workers.SubmitResultRequest{
		WorkerUID: s.resultWorker(ctx, task),
		Status:    int(checkerdef.StatusError),
		Metrics:   map[string]any{},
		Output:    map[string]any{checkerdef.OutputKeyError: message},
	})
}

// resultJob is the job a result is written for: the run's job, or, when it
// is gone, one rebuilt from the task. The result's region is the job's, or
// the task's for a job with none.
func resultJob(task *models.WorkerTask, check *models.Check, job *models.CheckJob) *models.CheckJob {
	if job == nil {
		job = &models.CheckJob{OrganizationUID: task.OrganizationUID, CheckUID: *task.CheckUID, Check: check}
	}

	if job.Region == nil && task.Region != "" {
		region := task.Region
		job.Region = &region
	}

	return job
}

// resultWorker is the workers row the result names: the task's worker when
// it has one (a cloud worker, or an agent's row), none otherwise.
func (s *Server) resultWorker(ctx context.Context, task *models.WorkerTask) string {
	if task.WorkerUID == nil {
		return ""
	}

	if worker, err := s.deps.DB.GetWorker(ctx, *task.WorkerUID); err != nil || worker == nil {
		return ""
	}

	return *task.WorkerUID
}

// end releases the job of a run that ended: the lease is dropped and the job
// is due at its next period anchored on the run's start, or at a "run now"
// parked during the run.
func (s *Server) end(ctx context.Context, task *models.WorkerTask) {
	check, job, err := s.load(ctx, task)
	if err != nil || job == nil {
		if err != nil {
			s.logger.WarnContext(ctx, "Crawl job not released", "task", task.UID, "error", err)
		}

		return
	}

	anchor := time.Now()
	if task.RunStartedAt != nil {
		anchor = *task.RunStartedAt
	}

	next := nextRunAt(job, check, anchor)

	_, err = s.deps.DB.DB().NewUpdate().Model((*models.CheckJob)(nil)).
		Set("lease_worker_uid = NULL").
		Set("lease_expires_at = NULL").
		Set("lease_starts = 0").
		Set("scheduled_at = COALESCE(run_requested_at, ?)", next).
		Set("effective_scheduled_at = COALESCE(run_requested_at, ?)", next).
		Set("run_requested_at = NULL").
		Set("capture_claimed_at = NULL").
		Set("updated_at = ?", time.Now()).
		Where("uid = ?", job.UID).
		// Never a real worker's lease (an old node's slice).
		Where("lease_worker_uid IS NULL").
		Exec(ctx)
	if err != nil {
		s.logger.WarnContext(ctx, "Crawl job not released", "task", task.UID, "error", err)
	}
}

// nextRunAt is the job's next aligned run after anchor, the same phase the
// check worker computes (spec 2026-07-20-05).
func nextRunAt(job *models.CheckJob, check *models.Check, anchor time.Time) time.Time {
	jobPeriod := time.Duration(job.Period)

	if check != nil && check.Period > 0 && jobPeriod > 0 {
		basePeriod := time.Duration(check.Period)
		spread := scheduling.RegionSpread(basePeriod, len(check.Regions), check.RegionSpreadDuration())

		return scheduling.NextAligned(anchor, basePeriod, jobPeriod, job.CheckUID, job.Region, check.Regions, spread)
	}

	return anchor.Add(jobPeriod)
}
