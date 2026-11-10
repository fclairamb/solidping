package rewraptask

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"time"

	"github.com/uptrace/bun"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/sealing"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// statusRefused is the outcome status of a check no re-wrap was started for.
const statusRefused = "refused"

// statusWindow is how far back Status looks for a check's latest re-wrap.
const statusWindow = time.Hour

// Deps are what the server side of the kind needs.
type Deps struct {
	DB    db.Service
	Tasks *workertasks.Service
}

// Server is the server side of the re-wrap kind.
type Server struct {
	db     db.Service
	tasks  *workertasks.Service
	logger *slog.Logger
	now    func() time.Time
}

// Outcome is the state of one check's re-wrap, as the API reports it.
type Outcome struct {
	CheckUID  string `json:"checkUid"`
	TaskUID   string `json:"taskUid,omitempty"`
	AgentUID  string `json:"agentUid,omitempty"`
	AgentName string `json:"agentName,omitempty"`
	// Status is the worker task's status (models.WorkerTaskStatus), or
	// "refused" when no re-wrap could be started.
	Status    string `json:"status"`
	ErrorCode string `json:"errorCode,omitempty"`
}

// Register registers the re-wrap kind on deps.Tasks and returns its server side.
func Register(deps Deps) *Server {
	srv := &Server{
		db:     deps.DB,
		tasks:  deps.Tasks,
		logger: slog.Default().With("component", "rewrap_task"),
		now:    func() time.Time { return time.Now().UTC() },
	}

	deps.Tasks.RegisterKind(workertasks.TaskKind{Name: Kind, PrepareClaim: srv.prepareClaim, Complete: srv.complete})

	return srv
}

// privateRegionsOf returns the private regions of a check.
func privateRegionsOf(check *models.Check) []string {
	out := make([]string, 0, len(check.Regions))

	for _, region := range check.Regions {
		if regions.IsPrivateRegion(region) {
			out = append(out, region)
		}
	}

	return out
}

// currentRecipients returns what the check's blob must be sealed to now.
func (s *Server) currentRecipients(ctx context.Context, check *models.Check) ([]sealing.Recipient, error) {
	private := privateRegionsOf(check)
	if len(private) == 0 {
		return nil, nil
	}

	recipients, err := sealing.RecipientsForRegions(ctx, s.db, check.OrganizationUID, private)
	if err != nil {
		return nil, fmt.Errorf("recipients of check %s: %w", check.UID, err)
	}

	return recipients, nil
}

// taskCheck loads the check of a task and reports whether its blob is still
// the one the task was created for.
func (s *Server) taskCheck(ctx context.Context, task *models.WorkerTask) (*models.Check, bool) {
	if task.CheckUID == nil {
		return nil, false
	}

	check, err := s.db.GetCheck(ctx, task.OrganizationUID, *task.CheckUID)
	if err != nil || check == nil || check.ConfigSealed == nil {
		return nil, false
	}

	want, _ := task.Payload["envelopeSha256"].(string)

	return check, want != "" && envelopeHash(*check.ConfigSealed) == want
}

// prepareClaim hands the claiming agent the current blob and the current
// recipient list. A check whose blob changed, or that has nothing to seal to
// any more, cancels the task.
func (s *Server) prepareClaim(
	ctx context.Context, task *models.WorkerTask, _ *workertasks.WorkerIdentity, _ json.RawMessage,
) (*workertasks.Prepared, error) {
	check, same := s.taskCheck(ctx, task)
	if !same {
		return nil, workertasks.ErrCancelTask
	}

	recipients, err := s.currentRecipients(ctx, check)
	if err != nil {
		return nil, err
	}

	if len(recipients) == 0 {
		return nil, workertasks.ErrCancelTask
	}

	input, err := json.Marshal(Input{Envelope: *check.ConfigSealed, Recipients: sealing.RecipientKeys(recipients)})
	if err != nil {
		return nil, fmt.Errorf("encode re-wrap input: %w", err)
	}

	return &workertasks.Prepared{Input: input, Final: true}, nil
}

// complete validates the returned blob like a sealedSecrets write (stanza
// count, size, fingerprints equal to the current recipients) and stores it on
// the check and its jobs, keeping the blob's origin. A non-empty code fails
// the task and leaves the check as it was.
func (s *Server) complete(ctx context.Context, task *models.WorkerTask, body json.RawMessage) (string, error) {
	var result Result
	if err := json.Unmarshal(body, &result); err != nil {
		return CodeInvalidResult, nil //nolint:nilerr // a malformed body is a task outcome, not an error
	}

	if result.ErrorCode != "" {
		if isAgentRefusal(result.ErrorCode) {
			return result.ErrorCode, nil
		}

		return CodeInvalidResult, nil
	}

	check, same := s.taskCheck(ctx, task)
	if !same {
		return CodeCheckChanged, nil
	}

	code, envelope, err := s.validateResult(ctx, check, &result)
	if err != nil || code != "" {
		return code, err
	}

	stored, err := s.store(ctx, check, *check.ConfigSealed, envelope)
	if err != nil {
		return "", err
	}

	if !stored {
		return CodeCheckChanged, nil
	}

	s.logger.InfoContext(ctx, "Sealed secrets re-wrapped by a peer agent", "check", check.UID, "task", task.UID)

	return "", nil
}

// validateResult checks a returned blob against the check's current
// recipients and builds the envelope to store. A non-empty code refuses it.
func (s *Server) validateResult(
	ctx context.Context, check *models.Check, result *Result,
) (string, string, error) {
	recipients, err := s.currentRecipients(ctx, check)
	if err != nil {
		return "", "", err
	}

	want := sealing.SortedFingerprints(recipients)
	got := append([]string(nil), result.Fingerprints...)
	sort.Strings(got)

	if len(want) == 0 || !slices.Equal(want, got) {
		return CodeRecipientsChanged, "", nil
	}

	if ctErr := credentials.ValidateSealedCiphertext(result.CT, len(want)); ctErr != nil {
		return CodeInvalidResult, "", nil //nolint:nilerr // a refused blob is a task outcome, not an error
	}

	envelope, err := credentials.RewrapSealedEnvelope(*check.ConfigSealed, got, result.CT)
	if err != nil {
		return CodeInvalidResult, "", nil //nolint:nilerr // a blob that does not parse is refused, not an error
	}

	return "", envelope, nil
}

// store swaps the check's blob for the new one, only if it is still the old
// one, and copies it to the check's jobs. It reports whether it swapped.
func (s *Server) store(ctx context.Context, check *models.Check, oldEnvelope, newEnvelope string) (bool, error) {
	stored := false

	err := s.db.DB().RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		res, err := tx.NewUpdate().
			Model((*models.Check)(nil)).
			Set("config_sealed = ?", newEnvelope).
			Set("updated_at = ?", s.now()).
			Where("uid = ?", check.UID).
			Where("config_sealed = ?", oldEnvelope).
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("store re-wrapped blob: %w", err)
		}

		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}

		if _, err := tx.NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("config_sealed = ?", newEnvelope).
			Set("updated_at = ?", s.now()).
			Where("check_uid = ?", check.UID).
			Exec(ctx); err != nil {
			return fmt.Errorf("store re-wrapped blob on jobs: %w", err)
		}

		stored = true

		return nil
	})

	return stored, err
}

// Start sends one re-wrap per check to an online agent that can open its
// blob. A check that cannot get one is reported with its reason, and keeps
// its blob and its flag.
func (s *Server) Start(ctx context.Context, orgUID string, checks []*models.Check) ([]Outcome, error) {
	if err := s.expire(ctx, orgUID); err != nil {
		return nil, err
	}

	out := make([]Outcome, 0, len(checks))

	for _, check := range checks {
		outcome, err := s.startOne(ctx, orgUID, check)
		if err != nil {
			return nil, err
		}

		out = append(out, outcome)
	}

	return out, nil
}

func (s *Server) startOne(ctx context.Context, orgUID string, check *models.Check) (Outcome, error) {
	refused := func(code string) Outcome {
		return Outcome{CheckUID: check.UID, Status: statusRefused, ErrorCode: code}
	}

	if check.ConfigSealed == nil || !credentials.IsSealedEnvelope(*check.ConfigSealed) {
		return refused(CodeNotSealed), nil
	}

	recipients, err := s.currentRecipients(ctx, check)
	if err != nil {
		return Outcome{}, err
	}

	if len(recipients) == 0 {
		return refused(CodeNoRecipients), nil
	}

	need, err := credentials.NeedsReseal(*check.ConfigSealed, sealing.SortedFingerprints(recipients))
	if err != nil {
		return refused(CodeNotSealed), nil //nolint:nilerr // an unreadable blob is reported, not an error
	}

	if !need {
		return refused(CodeUpToDate), nil
	}

	sealedTo, err := credentials.SealedRecipientFingerprints(*check.ConfigSealed)
	if err != nil {
		return refused(CodeNotSealed), nil //nolint:nilerr // as above
	}

	agent, err := s.pickAgent(ctx, orgUID, privateRegionsOf(check), sealedTo)
	if err != nil {
		return Outcome{}, err
	}

	if agent == nil {
		return refused(CodeNoOnlineRecipient), nil
	}

	task := models.NewWorkerTask(orgUID, Kind, agent.Region)
	task.CheckUID = &check.UID
	task.TargetWorkerUID = &agent.UID
	task.Payload = models.JSONMap{"envelopeSha256": envelopeHash(*check.ConfigSealed)}

	created, err := s.tasks.CreateIfNone(ctx, s.db.DB(), task)
	if err != nil {
		return Outcome{}, err
	}

	if !created {
		return refused(CodeAlreadyRunning), nil
	}

	return Outcome{
		CheckUID: check.UID, TaskUID: task.UID, AgentUID: agent.UID, AgentName: agent.Name,
		Status: string(models.WorkerTaskStatusPending),
	}, nil
}

// pickAgent returns the online agent of the regions, most recently seen
// first, that runs the re-wrap executor and holds a key the blob was sealed
// to (its own, or a region key it advertises). Nil when there is none.
func (s *Server) pickAgent(
	ctx context.Context, orgUID string, privateRegions, sealedTo []string,
) (*models.Agent, error) {
	cutoff := regions.LivenessCutoff(s.now())

	var best *models.Agent

	for _, region := range privateRegions {
		agents, err := s.db.ListActiveAgentsByRegion(ctx, orgUID, region)
		if err != nil {
			return nil, fmt.Errorf("list agents of %s: %w", region, err)
		}

		for _, agent := range agents {
			if !regions.IsAgentLive(agent.Status, agent.LastSeenAt, cutoff) {
				continue
			}

			if !s.agentCanOpen(ctx, agent, sealedTo) {
				continue
			}

			if best == nil || agent.LastSeenAt.After(*best.LastSeenAt) {
				best = agent
			}
		}
	}

	return best, nil
}

// agentCanOpen reports whether an agent runs the executor and holds one of
// the keys a blob was sealed to.
func (s *Server) agentCanOpen(ctx context.Context, agent *models.Agent, sealedTo []string) bool {
	worker, err := s.db.GetWorkerBySlug(ctx, agentcrypto.WorkerSlug(agent.UID))
	if err != nil || worker == nil || !slices.Contains(worker.Capabilities, Capability) {
		return false
	}

	if agent.X25519PublicKey != "" && slices.Contains(sealedTo, credentials.RecipientFingerprint(agent.X25519PublicKey)) {
		return true
	}

	for _, fp := range sealing.RegionKeyFingerprints(worker.Capabilities) {
		if slices.Contains(sealedTo, fp) {
			return true
		}
	}

	return false
}

// expire fails the org's re-wraps that no agent finished within Timeout.
// A late finish is then fenced off (the task is no longer claimed).
func (s *Server) expire(ctx context.Context, orgUID string) error {
	current := s.now()

	if _, err := s.db.DB().NewUpdate().
		Model((*models.WorkerTask)(nil)).
		Set("status = ?", models.WorkerTaskStatusFailed).
		Set("error_code = ?", CodeTimedOut).
		Set("worker_uid = NULL").
		Set("lease_until = NULL").
		Set("updated_at = ?", current).
		Where("organization_uid = ?", orgUID).
		Where("kind = ?", Kind).
		Where("status IN (?)", bun.List([]models.WorkerTaskStatus{
			models.WorkerTaskStatusPending, models.WorkerTaskStatusClaimed,
		})).
		Where("created_at < ?", current.Add(-Timeout)).
		Exec(ctx); err != nil {
		return fmt.Errorf("expire re-wrap tasks: %w", err)
	}

	return nil
}

// Status returns the latest re-wrap of each of the checks started within the
// last hour (every check of the org with one when checkUIDs is empty).
func (s *Server) Status(ctx context.Context, orgUID string, checkUIDs []string) ([]Outcome, error) {
	if err := s.expire(ctx, orgUID); err != nil {
		return nil, err
	}

	var rows []*models.WorkerTask

	query := s.db.DB().NewSelect().
		Model(&rows).
		Where("organization_uid = ?", orgUID).
		Where("kind = ?", Kind).
		Where("created_at >= ?", s.now().Add(-statusWindow)).
		Order("created_at DESC")

	if len(checkUIDs) > 0 {
		query = query.Where("check_uid IN (?)", bun.List(checkUIDs))
	}

	if err := query.Scan(ctx); err != nil {
		return nil, fmt.Errorf("list re-wrap tasks: %w", err)
	}

	out := make([]Outcome, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))

	for _, row := range rows {
		if row.CheckUID == nil {
			continue
		}

		if _, dup := seen[*row.CheckUID]; dup {
			continue
		}

		seen[*row.CheckUID] = struct{}{}

		outcome := Outcome{CheckUID: *row.CheckUID, TaskUID: row.UID, Status: string(row.Status)}
		if row.TargetWorkerUID != nil {
			outcome.AgentUID = *row.TargetWorkerUID
		}

		if row.ErrorCode != nil {
			outcome.ErrorCode = *row.ErrorCode
		}

		out = append(out, outcome)
	}

	return out, nil
}
