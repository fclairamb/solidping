package workertasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
)

// StateStore keeps the state a task carries between slices. The attachment
// rail implements it (tasks/<uid>/step-state, keep 2).
type StateStore interface {
	PutTaskState(ctx context.Context, orgUID, taskUID string, envelope []byte, details models.JSONMap) (string, error)
	ReadTaskState(ctx context.Context, orgUID, taskUID, fileUID string) ([]byte, error)
	PurgeTaskState(ctx context.Context, orgUID, taskUID string) error
}

// Service is the server side of worker tasks.
type Service struct {
	db     bun.IDB
	isPG   bool
	logger *slog.Logger

	mu    sync.RWMutex
	kinds map[string]TaskKind
	store StateStore
}

// NewService builds the service over the database.
func NewService(db bun.IDB) *Service {
	_, isPG := db.Dialect().(*pgdialect.Dialect)

	return &Service{
		db:     db,
		isPG:   isPG,
		logger: slog.Default().With("component", "worker_tasks"),
		kinds:  make(map[string]TaskKind),
	}
}

// SetStateStore installs the state store. Set once at startup.
func (s *Service) SetStateStore(store StateStore) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.store = store
}

// RegisterKind registers (or replaces) a kind's server hooks.
func (s *Service) RegisterKind(kind TaskKind) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.kinds[kind.Name] = kind
}

// KnownKinds intersects requested kinds with the registry, without duplicates.
// A kind the server does not know is ignored, never an error: an agent newer
// than its server may run kinds this server has not heard of.
func (s *Service) KnownKinds(requested []string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]string, 0, len(requested))
	seen := make(map[string]bool, len(requested))

	for _, name := range requested {
		if _, ok := s.kinds[name]; ok && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}

	return out
}

func (s *Service) kind(name string) (TaskKind, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	kind, ok := s.kinds[name]

	return kind, ok
}

func (s *Service) stateStore() (StateStore, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.store == nil {
		return nil, ErrNoStateStore
	}

	return s.store, nil
}

func now() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// Create inserts a pending task. A check's stale active task (untouched for
// models.WorkerTaskStaleAfter) is failed first, so lazy expiry never blocks a
// new run; a live one makes the insert fail on the one-active-run index.
func (s *Service) Create(ctx context.Context, task *models.WorkerTask) error {
	_, err := s.create(ctx, s.db, task, false)

	return err
}

// CreateIfNone is Create on idb (a claim's transaction) that reports false,
// without an error, when the check already has a live active task.
func (s *Service) CreateIfNone(ctx context.Context, idb bun.IDB, task *models.WorkerTask) (bool, error) {
	return s.create(ctx, idb, task, true)
}

func (s *Service) create(ctx context.Context, idb bun.IDB, task *models.WorkerTask, ignoreActive bool) (bool, error) {
	if task.Kind == "" || task.OrganizationUID == "" || (task.CheckUID == nil) == (task.JobUID == nil) {
		return false, ErrInvalidTask
	}

	current := now()

	if task.NotBefore.IsZero() {
		task.NotBefore = current
	}

	task.NotBefore = task.NotBefore.UTC()
	task.Status = models.WorkerTaskStatusPending
	task.CreatedAt = current
	task.UpdatedAt = current

	if task.CheckUID != nil {
		if _, err := idb.NewUpdate().Model((*models.WorkerTask)(nil)).
			Set("status = ?", models.WorkerTaskStatusFailed).
			Set("error_code = ?", models.WorkerTaskErrorExpired).
			Set("worker_uid = NULL").
			Set("lease_until = NULL").
			Set("updated_at = ?", current).
			Where("check_uid = ?", *task.CheckUID).
			Where("status IN (?)", bun.List(activeStatuses())).
			Where("updated_at < ?", current.Add(-models.WorkerTaskStaleAfter)).
			Exec(ctx); err != nil {
			return false, fmt.Errorf("expire stale worker task: %w", err)
		}
	}

	insert := idb.NewInsert().Model(task)
	if ignoreActive {
		// The only unique constraint besides the primary key is the
		// one-active-run index: a conflict means a live run exists.
		insert = insert.On("CONFLICT DO NOTHING")
	}

	res, err := insert.Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("create worker task: %w", err)
	}

	n, _ := res.RowsAffected()

	return n > 0, nil
}

// Get returns a task as readers see it: a stale active task reads as failed
// with models.WorkerTaskErrorExpired (lazy expiry, there is no sweeper).
func (s *Service) Get(ctx context.Context, uid string) (*models.WorkerTask, error) {
	task := new(models.WorkerTask)
	if err := s.db.NewSelect().Model(task).Where("uid = ?", uid).Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTaskGone
		}

		return nil, fmt.Errorf("get worker task: %w", err)
	}

	if task.IsStale(time.Now()) {
		code := models.WorkerTaskErrorExpired
		task.Status = models.WorkerTaskStatusFailed
		task.ErrorCode = &code
	}

	return task, nil
}

func activeStatuses() []models.WorkerTaskStatus {
	return []models.WorkerTaskStatus{models.WorkerTaskStatusPending, models.WorkerTaskStatusClaimed}
}

// Claim takes one task for the worker, or returns nil when there is none.
//
// One atomic statement takes the oldest eligible row: a kind in Kinds, the
// worker's exact region, the agent's org (agents only), not_before reached,
// pending or claimed under an expired lease, and the worker holding fewer
// live leases than Slots. Postgres locks the candidate FOR UPDATE SKIP LOCKED;
// SQLite runs the same UPDATE … WHERE uid = (SELECT … LIMIT 1) RETURNING.
func (s *Service) Claim(ctx context.Context, identity *WorkerIdentity) (*Claim, error) {
	if identity.UID == "" || (identity.IsAgent && identity.OrgUID == "") {
		return nil, ErrInvalidIdentity
	}

	// A cloud worker never serves a private region, whatever its config says.
	if !identity.IsAgent && strings.HasPrefix(identity.Region, "@") {
		return nil, nil //nolint:nilnil // no task
	}

	kinds := s.KnownKinds(identity.Kinds)
	if len(kinds) == 0 {
		return nil, nil //nolint:nilnil // no task
	}

	slots := max(identity.Slots, 1)

	for range maxClaimTries {
		task, err := s.claimRow(ctx, identity, kinds, slots)
		if err != nil || task == nil {
			return nil, err
		}

		claim, err := s.prepare(ctx, task, identity)
		if err != nil || claim != nil {
			return claim, err
		}
	}

	return nil, nil //nolint:nilnil // every row tried failed or deferred
}

// claimRow materializes the kinds' due tasks, then claims one row, in one
// transaction.
func (s *Service) claimRow(
	ctx context.Context, identity *WorkerIdentity, kinds []string, slots int,
) (*models.WorkerTask, error) {
	var task *models.WorkerTask

	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		for _, name := range kinds {
			if kind, ok := s.kind(name); ok && kind.Materialize != nil {
				if err := kind.Materialize(ctx, tx, identity); err != nil {
					return fmt.Errorf("materialize %s tasks: %w", name, err)
				}
			}
		}

		var err error

		task, err = s.claimRowIn(ctx, tx, identity, kinds, slots)

		return err
	})
	if err != nil {
		return nil, err
	}

	return task, nil
}

func (s *Service) claimRowIn(
	ctx context.Context, idb bun.IDB, identity *WorkerIdentity, kinds []string, slots int,
) (*models.WorkerTask, error) {
	current := now()

	held := idb.NewSelect().
		TableExpr("worker_tasks AS held").
		ColumnExpr("count(*)").
		Where("held.worker_uid = ?", identity.UID).
		Where("held.status = ?", models.WorkerTaskStatusClaimed).
		Where("held.lease_until >= ?", current)

	candidate := idb.NewSelect().
		TableExpr("worker_tasks AS cand").
		ColumnExpr("cand.uid").
		Where("cand.kind IN (?)", bun.List(kinds)).
		Where("cand.region = ?", identity.Region).
		Where("cand.not_before <= ?", current).
		Where("cand.updated_at >= ?", current.Add(-models.WorkerTaskStaleAfter)).
		WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.
				Where("cand.status = ?", models.WorkerTaskStatusPending).
				WhereOr("(cand.status = ? AND cand.lease_until < ?)", models.WorkerTaskStatusClaimed, current)
		}).
		Where("(?) < ?", held, slots).
		OrderExpr("cand.not_before ASC, cand.created_at ASC").
		Limit(1)

	if identity.IsAgent {
		candidate = candidate.Where("cand.organization_uid = ?", identity.OrgUID)
	}

	if s.isPG {
		candidate = candidate.For("UPDATE SKIP LOCKED")
	}

	task := new(models.WorkerTask)

	err := idb.NewUpdate().
		Model(task).
		Set("status = ?", models.WorkerTaskStatusClaimed).
		Set("worker_uid = ?", identity.UID).
		Set("lease_until = ?", current.Add(LeaseDuration)).
		Set("attempts = attempts + 1").
		Set("run_started_at = coalesce(run_started_at, ?)", current).
		Set("updated_at = ?", current).
		Where("uid = (?)", candidate).
		Returning("*").
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // no task
	}

	if err != nil {
		return nil, fmt.Errorf("claim worker task: %w", err)
	}

	if task.UID == "" {
		return nil, nil //nolint:nilnil // no row updated
	}

	return task, nil
}

// prepare turns a claimed row into a Claim. It returns (nil, nil) when the
// row failed or deferred on the way and the caller should try the next one.
func (s *Service) prepare(ctx context.Context, task *models.WorkerTask, identity *WorkerIdentity) (*Claim, error) {
	worker := *task.WorkerUID

	if task.Attempts > MaxAttempts {
		return nil, s.terminate(ctx, task, worker, models.WorkerTaskStatusFailed, ErrorMaxAttempts, true)
	}

	kind, ok := s.kind(task.Kind)
	if !ok {
		return nil, ErrUnknownKind
	}

	state, err := s.readState(ctx, task)
	if err != nil {
		s.logger.WarnContext(ctx, "Worker task state unreadable", "task", task.UID, "error", err)

		return nil, s.terminate(ctx, task, worker, models.WorkerTaskStatusFailed, ErrorStateMissing, true)
	}

	prepared, err := s.prepareKind(ctx, kind, task, identity, state)
	if err != nil || prepared == nil {
		return nil, err
	}

	if prepared.DeferUntil != nil {
		return nil, s.release(ctx, task, worker, prepared.DeferUntil.UTC(), true)
	}

	// The slice's grant is kept on the row, so the kind refunds what the
	// worker did not use from the server's own number, never the worker's.
	if prepared.Units != task.UnitsGranted {
		if err := execFenced(ctx, s.fencedUpdate(task.UID, worker, now()).
			Set("units_granted = ?", prepared.Units)); err != nil {
			return nil, err
		}

		task.UnitsGranted = prepared.Units
	}

	return buildClaim(task, state, prepared)
}

// prepareKind runs the kind's PrepareClaim. It returns (nil, nil) when the
// task was canceled on the way.
func (s *Service) prepareKind(
	ctx context.Context, kind TaskKind, task *models.WorkerTask, identity *WorkerIdentity, state json.RawMessage,
) (*Prepared, error) {
	worker := *task.WorkerUID

	if kind.PrepareClaim == nil {
		return &Prepared{}, nil
	}

	prepared, err := kind.PrepareClaim(ctx, task, identity, state)
	if errors.Is(err, ErrCancelTask) {
		return nil, s.terminate(ctx, task, worker, models.WorkerTaskStatusCancelled, "", false)
	}

	if err != nil {
		// Released at once rather than left to expire: a held slot would
		// starve the worker for a whole lease. The attempt stays counted, so
		// a kind that always errors ends on max_attempts.
		if releaseErr := s.release(ctx, task, worker, task.NotBefore, false); releaseErr != nil {
			return nil, releaseErr
		}

		return nil, fmt.Errorf("prepare worker task %s: %w", task.Kind, err)
	}

	if prepared == nil {
		prepared = &Prepared{}
	}

	return prepared, nil
}

func buildClaim(task *models.WorkerTask, state json.RawMessage, prepared *Prepared) (*Claim, error) {
	input := prepared.Input
	if input == nil && len(task.Payload) > 0 {
		raw, err := json.Marshal(task.Payload)
		if err != nil {
			return nil, fmt.Errorf("encode worker task payload: %w", err)
		}

		input = raw
	}

	claim := &Claim{
		UID:             task.UID,
		Kind:            task.Kind,
		OrganizationUID: task.OrganizationUID,
		Region:          task.Region,
		Slice:           task.SliceCount,
		Attempt:         task.Attempts,
		Input:           input,
		State:           state,
		SliceBudgetMs:   prepared.SliceBudget.Milliseconds(),
		Units:           prepared.Units,
		Final:           prepared.Final,
	}

	if task.LeaseUntil != nil {
		claim.LeaseUntil = *task.LeaseUntil
	}

	if task.CheckUID != nil {
		claim.CheckUID = *task.CheckUID
	}

	if task.JobUID != nil {
		claim.JobUID = *task.JobUID
	}

	return claim, nil
}

// readState returns the decoded state payload, or nil on the first slice.
func (s *Service) readState(ctx context.Context, task *models.WorkerTask) (json.RawMessage, error) {
	if task.StateFileUID == nil {
		return nil, nil
	}

	store, err := s.stateStore()
	if err != nil {
		return nil, err
	}

	body, err := store.ReadTaskState(ctx, task.OrganizationUID, task.UID, *task.StateFileUID)
	if err != nil {
		return nil, err
	}

	envelope, err := attachments.DecodeStepState(body)
	if err != nil {
		return nil, err
	}

	if envelope.RunUID != task.UID || envelope.CheckType != task.Kind {
		return nil, ErrInvalidState
	}

	return envelope.Payload, nil
}

// fencedUpdate is an update that only applies while the worker holds a live
// lease on the task. Every write except Claim goes through it.
func (s *Service) fencedUpdate(uid, worker string, current time.Time) *bun.UpdateQuery {
	return s.db.NewUpdate().Model((*models.WorkerTask)(nil)).
		Where("uid = ?", uid).
		Where("worker_uid = ?", worker).
		Where("status = ?", models.WorkerTaskStatusClaimed).
		Where("lease_until >= ?", current).
		Set("updated_at = ?", current)
}

func execFenced(ctx context.Context, query *bun.UpdateQuery) error {
	res, err := query.Exec(ctx)
	if err != nil {
		return fmt.Errorf("update worker task: %w", err)
	}

	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTaskGone
	}

	return nil
}

// held loads the task when the worker holds a live lease on it.
func (s *Service) held(ctx context.Context, uid, worker string) (*models.WorkerTask, error) {
	task := new(models.WorkerTask)

	err := s.db.NewSelect().Model(task).
		Where("uid = ?", uid).
		Where("worker_uid = ?", worker).
		Where("status = ?", models.WorkerTaskStatusClaimed).
		Where("lease_until >= ?", now()).
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTaskGone
	}

	if err != nil {
		return nil, fmt.Errorf("load worker task: %w", err)
	}

	return task, nil
}

// release gives a claimed task back: pending, no lease, not before notBefore.
// A deferral also gives the attempt back.
func (s *Service) release(
	ctx context.Context, task *models.WorkerTask, worker string, notBefore time.Time, giveBackAttempt bool,
) error {
	query := s.fencedUpdate(task.UID, worker, now()).
		Set("status = ?", models.WorkerTaskStatusPending).
		Set("worker_uid = NULL").
		Set("lease_until = NULL").
		Set("not_before = ?", notBefore)

	if giveBackAttempt {
		query = query.Set("attempts = attempts - 1")

		// A deferred task that never ran a slice has not started its run:
		// its run clock starts at the claim that runs one.
		if task.SliceCount == 0 && task.StateFileUID == nil {
			query = query.Set("run_started_at = NULL")
		}
	}

	if err := execFenced(ctx, query); err != nil && !errors.Is(err, ErrTaskGone) {
		return err
	}

	return nil
}

// terminate ends a held task with status and code, purges its state, and,
// when callFail is set, runs the kind's Fail hook.
func (s *Service) terminate(
	ctx context.Context, task *models.WorkerTask, worker string,
	status models.WorkerTaskStatus, code string, callFail bool,
) error {
	query := s.fencedUpdate(task.UID, worker, now()).
		Set("status = ?", status).
		Set("worker_uid = NULL").
		Set("lease_until = NULL")

	if code != "" {
		query = query.Set("error_code = ?", code)
	}

	if err := execFenced(ctx, query); err != nil {
		return err
	}

	task.Status = status

	if code != "" {
		task.ErrorCode = &code
	}

	if kind, ok := s.kind(task.Kind); ok && callFail && kind.Fail != nil {
		if err := kind.Fail(ctx, task, code); err != nil {
			s.logger.WarnContext(ctx, "Worker task fail hook failed",
				"task", task.UID, "kind", task.Kind, "error", err)
		}
	}

	s.ended(ctx, task)

	return nil
}

// ended runs the kind's End hook and purges the state of a task that just
// reached a terminal status.
func (s *Service) ended(ctx context.Context, task *models.WorkerTask) {
	if kind, ok := s.kind(task.Kind); ok && kind.End != nil {
		kind.End(ctx, task)
	}

	s.purgeState(ctx, task)
}

func (s *Service) purgeState(ctx context.Context, task *models.WorkerTask) {
	if task.StateFileUID == nil {
		return
	}

	store, err := s.stateStore()
	if err != nil {
		return
	}

	if err := store.PurgeTaskState(ctx, task.OrganizationUID, task.UID); err != nil {
		s.logger.WarnContext(ctx, "Failed to purge worker task state", "task", task.UID, "error", err)
	}
}

// Heartbeat extends the worker's lease and returns its new end.
func (s *Service) Heartbeat(ctx context.Context, uid, worker string) (time.Time, error) {
	current := now()
	leaseUntil := current.Add(LeaseDuration)

	if err := execFenced(ctx, s.fencedUpdate(uid, worker, current).Set("lease_until = ?", leaseUntil)); err != nil {
		return time.Time{}, err
	}

	return leaseUntil, nil
}

// Step records a non-final slice: the next state (stored as an attachment,
// 1 MiB cap), the progress, and puts the task back to pending for its next
// slice, with the lease cleared.
func (s *Service) Step(ctx context.Context, uid, worker string, req StepRequest) error {
	if len(req.State) == 0 || !json.Valid(req.State) {
		return ErrInvalidState
	}

	if err := checkProgress(req.Progress); err != nil {
		return err
	}

	// Size is checked before anything is written: an oversized state leaves
	// the task exactly as it was.
	if len(req.State) > attachments.MaxStepStateBytes {
		return ErrTooLarge
	}

	task, err := s.held(ctx, uid, worker)
	if err != nil {
		return err
	}

	fileUID, err := s.putState(ctx, task, req)
	if err != nil {
		return err
	}

	current := now()

	query := s.fencedUpdate(uid, worker, current).
		Set("state_file_uid = ?", fileUID).
		Set("slice_count = slice_count + 1").
		Set("slice_failures = 0").
		Set("attempts = 0").
		Set("units_granted = 0").
		Set("status = ?", models.WorkerTaskStatusPending).
		Set("not_before = ?", current).
		Set("worker_uid = NULL").
		Set("lease_until = NULL")

	if req.Progress != nil {
		query = query.Set("progress = ?", req.Progress)
	}

	if err := execFenced(ctx, query); err != nil {
		return err
	}

	if kind, ok := s.kind(task.Kind); ok && kind.Step != nil {
		if err := kind.Step(ctx, task, req); err != nil {
			s.logger.WarnContext(ctx, "Worker task step hook failed", "task", uid, "kind", task.Kind, "error", err)
		}
	}

	return nil
}

// putState stores the next state of a held task and returns its file uid.
func (s *Service) putState(ctx context.Context, task *models.WorkerTask, req StepRequest) (string, error) {
	store, err := s.stateStore()
	if err != nil {
		return "", err
	}

	envelope, err := attachments.EncodeStepState(task.UID, task.Kind, req.State)
	if err != nil {
		return "", fmt.Errorf("encode worker task state: %w", err)
	}

	if len(envelope) > attachments.MaxStepStateBytes {
		return "", ErrTooLarge
	}

	fileUID, err := store.PutTaskState(ctx, task.OrganizationUID, task.UID, envelope,
		attachments.StepStateDetails(task.UID, task.SliceCount+1, req.Progress))
	if errors.Is(err, attachments.ErrAttachmentTooLarge) {
		return "", ErrTooLarge
	}

	if err != nil {
		return "", fmt.Errorf("store worker task state: %w", err)
	}

	return fileUID, nil
}

func checkProgress(progress models.JSONMap) error {
	if progress == nil {
		return nil
	}

	raw, err := json.Marshal(progress)
	if err != nil {
		return ErrInvalidState
	}

	if len(raw) > MaxProgressBytes {
		return ErrTooLarge
	}

	return nil
}

// Finish hands a finished task's body to its kind's Complete hook, then marks
// it done, or failed with the code the hook returns.
func (s *Service) Finish(ctx context.Context, uid, worker string, body json.RawMessage) error {
	task, err := s.held(ctx, uid, worker)
	if err != nil {
		return err
	}

	kind, ok := s.kind(task.Kind)
	if !ok {
		return ErrUnknownKind
	}

	code := ""

	if kind.Complete != nil {
		code, err = kind.Complete(ctx, task, body)
		if err != nil {
			return fmt.Errorf("complete worker task %s: %w", task.Kind, err)
		}
	}

	status := models.WorkerTaskStatusDone
	if code != "" {
		status = models.WorkerTaskStatusFailed
	}

	return s.terminate(ctx, task, worker, status, code, false)
}

// FailSlice records a failed slice. MaxSliceFailures in a row fail the task
// (and run the kind's Fail hook); otherwise it goes back to pending and waits
// SliceRetryDelay.
func (s *Service) FailSlice(ctx context.Context, uid, worker, errorCode string) error {
	task, err := s.held(ctx, uid, worker)
	if err != nil {
		return err
	}

	code := sanitizeErrorCode(errorCode)

	if task.SliceFailures+1 >= MaxSliceFailures {
		return s.terminate(ctx, task, worker, models.WorkerTaskStatusFailed, code, true)
	}

	current := now()

	return execFenced(ctx, s.fencedUpdate(uid, worker, current).
		Set("slice_failures = slice_failures + 1").
		Set("units_granted = 0").
		Set("status = ?", models.WorkerTaskStatusPending).
		Set("not_before = ?", current.Add(SliceRetryDelay)).
		Set("worker_uid = NULL").
		Set("lease_until = NULL"))
}

// sanitizeErrorCode keeps a worker-sent code to [a-z0-9_-], 64 chars.
func sanitizeErrorCode(code string) string {
	if code == "" || len(code) > maxErrorCodeLength {
		return ErrorSliceFailed
	}

	for _, char := range code {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' && char != '-' {
			return ErrorSliceFailed
		}
	}

	return code
}

// Cancel moves the pending and claimed tasks of a check or a job to
// WorkerTaskStatusCancelled, runs their kind's End hook and returns how many
// it changed. A worker still running one gets ErrTaskGone on its next call.
func (s *Service) Cancel(ctx context.Context, filter CancelFilter) (int, error) {
	if (filter.CheckUID == "") == (filter.JobUID == "") {
		return 0, ErrInvalidTask
	}

	var tasks []*models.WorkerTask

	query := s.db.NewUpdate().Model(&tasks).
		Set("status = ?", models.WorkerTaskStatusCancelled).
		Set("worker_uid = NULL").
		Set("lease_until = NULL").
		Set("updated_at = ?", now()).
		Where("status IN (?)", bun.List(activeStatuses())).
		Returning("*")

	if filter.CheckUID != "" {
		query = query.Where("check_uid = ?", filter.CheckUID)
	} else {
		query = query.Where("job_uid = ?", filter.JobUID)
	}

	if err := query.Scan(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("cancel worker tasks: %w", err)
	}

	for _, task := range tasks {
		s.ended(ctx, task)
	}

	return len(tasks), nil
}
