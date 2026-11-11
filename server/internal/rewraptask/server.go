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

	deps.Tasks.RegisterKind(workertasks.TaskKind{
		Name: Kind, PrepareClaim: srv.prepareClaim, Complete: srv.complete, End: srv.sendConfirmation,
	})
	deps.Tasks.RegisterKind(workertasks.TaskKind{
		Name: VerifyKind, PrepareClaim: srv.prepareVerifyClaim, Complete: srv.completeVerify,
	})

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

	want := payloadString(task, payloadEnvelopeSHA256)

	return check, want != "" && envelopeHash(*check.ConfigSealed) == want
}

// payloadString reads a string of a task payload.
func payloadString(task *models.WorkerTask, key string) string {
	value, _ := task.Payload[key].(string)

	return value
}

// prepareClaim hands the claiming agent the current blob, the current
// recipient list, its possession challenge and the check's type and public
// config. A check whose blob changed, or that has nothing to seal to any
// more, cancels the task.
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

	input, err := json.Marshal(Input{
		Envelope:   *check.ConfigSealed,
		Recipients: sealing.RecipientKeys(recipients),
		Challenge:  payloadString(task, payloadChallenge),
		CheckType:  check.Type,
		Config:     check.Config,
	})
	if err != nil {
		return nil, fmt.Errorf("encode re-wrap input: %w", err)
	}

	return &workertasks.Prepared{Input: input, Final: true}, nil
}

// complete checks the possession challenge, validates the returned blob like
// a sealedSecrets write (stanza count, size, fingerprints equal to the current
// recipients), then sends it to a second agent to confirm, or stores it marked
// unconfirmed when no second agent can (spec 2026-11-11-02). A non-empty code
// fails the task and leaves the check as it was.
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

	// Possession (spec 2026-11-11-02): only the holder of the key the agent
	// was picked for can open the challenge. A task without one (created
	// before the challenge) is refused too.
	if !credentials.ChallengeAnswered(result.Challenge, payloadString(task, payloadChallengeSHA256)) {
		s.logger.WarnContext(ctx, "Re-wrap refused: possession challenge not answered",
			"check", task.CheckUID, "task", task.UID, "agent", task.TargetWorkerUID)

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

	return s.confirmOrStore(ctx, task, check, envelope)
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

	// An agent that runs the confirmation also answers the possession
	// challenge (0.48 and later); an older one would always be refused.
	pick, err := s.pickAgent(ctx, orgUID, privateRegionsOf(check), sealedTo,
		[]string{Capability, VerifyCapability}, "")
	if err != nil {
		return Outcome{}, err
	}

	agent := pick.agent

	switch {
	case agent == nil && pick.keyHolderWithoutRewrap:
		return refused(CodeRewrapNotOffered), nil
	case agent == nil && pick.keyHolderUnprovable:
		return refused(CodeNoProvableKey), nil
	case agent == nil:
		return refused(CodeNoOnlineRecipient), nil
	}

	// The possession challenge, sealed to the key the agent was picked for
	// and to nothing else (spec 2026-11-11-02).
	challenge, challengeSum, err := credentials.NewSealedChallenge(pick.recipient)
	if err != nil {
		return Outcome{}, err
	}

	task := models.NewWorkerTask(orgUID, Kind, agent.Region)
	task.CheckUID = &check.UID
	task.TargetWorkerUID = &agent.UID
	task.Payload = models.JSONMap{
		payloadEnvelopeSHA256:  envelopeHash(*check.ConfigSealed),
		payloadChallenge:       challenge,
		payloadChallengeSHA256: challengeSum,
	}

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

// agentPick is what pickAgent found: the agent to send the task to (nil
// when none fits) with the key it was picked for, and why an online key
// holder was passed over.
type agentPick struct {
	agent *models.Agent
	// recipient is the public key the agent was picked for: its own, or a
	// region key it advertises. The possession challenge is sealed to it.
	recipient string
	// keyHolderWithoutRewrap: an online agent holds a key but does not run
	// the kind (older, or SP_AGENT_REWRAP_ENABLED=false, spec 2026-11-10-04).
	keyHolderWithoutRewrap bool
	// keyHolderUnprovable: an online agent advertises a region key the
	// server has no public key for, so it cannot be challenged.
	keyHolderUnprovable bool
}

// pickAgent returns the online agent of the regions, most recently seen
// first, that reports every capability of wantCaps and holds one of the keys
// keyFingerprints (its own, or a region key it advertises) the server can
// seal a challenge to. exclude is an agent never picked.
func (s *Server) pickAgent(
	ctx context.Context, orgUID string, privateRegions, keyFingerprints, wantCaps []string, exclude string,
) (agentPick, error) {
	cutoff := regions.LivenessCutoff(s.now())

	known, err := sealing.KnownRegionRecipients(ctx, s.db, orgUID)
	if err != nil {
		return agentPick{}, err
	}

	var pick agentPick

	for _, region := range privateRegions {
		agents, listErr := s.db.ListActiveAgentsByRegion(ctx, orgUID, region)
		if listErr != nil {
			return agentPick{}, fmt.Errorf("list agents of %s: %w", region, listErr)
		}

		for _, agent := range agents {
			if agent.UID == exclude || !regions.IsAgentLive(agent.Status, agent.LastSeenAt, cutoff) {
				continue
			}

			access := s.agentKey(ctx, agent, keyFingerprints, known, wantCaps)

			switch {
			case !access.holdsKey:
				continue
			case !access.runsKind:
				pick.keyHolderWithoutRewrap = true

				continue
			case access.recipient == "":
				pick.keyHolderUnprovable = true

				continue
			}

			if pick.agent == nil || agent.LastSeenAt.After(*pick.agent.LastSeenAt) {
				pick.agent = agent
				pick.recipient = access.recipient
			}
		}
	}

	return pick, nil
}

// keyAccess says whether an agent holds one of a set of keys, the public key
// to challenge it on (empty when the server knows none), and whether it runs
// the kind.
type keyAccess struct {
	holdsKey  bool
	recipient string
	runsKind  bool
}

// agentKey reports whether an agent holds one of the keys keyFingerprints,
// which public key to challenge it on, and whether it reports every
// capability of wantCaps. Its own enrolled key comes first; a region key it
// advertises is challenged on the stored public key (current or retired).
func (s *Server) agentKey(
	ctx context.Context, agent *models.Agent, keyFingerprints []string, known map[string]string, wantCaps []string,
) keyAccess {
	var access keyAccess

	if agent.X25519PublicKey != "" &&
		slices.Contains(keyFingerprints, credentials.RecipientFingerprint(agent.X25519PublicKey)) {
		access.holdsKey = true
		access.recipient = agent.X25519PublicKey
	}

	worker, err := s.db.GetWorkerBySlug(ctx, agentcrypto.WorkerSlug(agent.UID))
	if err != nil || worker == nil {
		return access
	}

	for _, fp := range sealing.RegionKeyFingerprints(worker.Capabilities) {
		if !slices.Contains(keyFingerprints, fp) {
			continue
		}

		access.holdsKey = true

		if access.recipient == "" {
			access.recipient = known[fp]
		}
	}

	access.runsKind = true

	for _, capability := range wantCaps {
		access.runsKind = access.runsKind && slices.Contains(worker.Capabilities, capability)
	}

	return access
}

// expire fails the org's re-wraps (and confirmations) that no agent finished
// within Timeout.
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
		Where("kind IN (?)", bun.List([]string{Kind, VerifyKind})).
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

		if row.Status == models.WorkerTaskStatusDone {
			s.confirmationOutcome(ctx, row, &outcome)
		}

		out = append(out, outcome)
	}

	return out, nil
}
