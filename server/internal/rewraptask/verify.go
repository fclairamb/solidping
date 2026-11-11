package rewraptask

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/sealing"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// Confirmation of a re-wrap by a second agent (spec 2026-11-11-02 §3).
//
// The re-wrap's result passed its checks: before the swap, a second online
// agent holding a current recipient key opens the new blob and answers. The
// confirmation is its own worker task (one active task per check, so it is
// created once the re-wrap ended, by the kind's End hook); the re-wrap's
// payload names it, and Status reports the pair as one outcome.

// confirmOrStore sends a passed re-wrap to a second agent, or stores it
// marked unconfirmed when no second agent can confirm it.
func (s *Server) confirmOrStore(
	ctx context.Context, task *models.WorkerTask, check *models.Check, envelope string,
) (string, error) {
	recipients, err := s.currentRecipients(ctx, check)
	if err != nil {
		return "", err
	}

	exclude := ""
	if task.TargetWorkerUID != nil {
		exclude = *task.TargetWorkerUID
	}

	pick, err := s.pickAgent(ctx, check.OrganizationUID, privateRegionsOf(check),
		sealing.SortedFingerprints(recipients), []string{VerifyCapability}, exclude)
	if err != nil {
		return "", err
	}

	if pick.agent == nil {
		return s.storeUnconfirmed(ctx, task, check, envelope)
	}

	challenge, challengeSum, err := credentials.NewSealedChallenge(pick.recipient)
	if err != nil {
		return "", err
	}

	task.Payload[payloadConfirm] = confirmPending
	task.Payload[payloadVerifyTaskUID] = uuid.New().String()
	task.Payload[payloadVerifierUID] = pick.agent.UID
	task.Payload[payloadVerifierRegion] = pick.agent.Region
	task.Payload[payloadPendingEnvelope] = envelope
	task.Payload[payloadVerifyChallenge] = challenge
	task.Payload[payloadVerifyChallengeSHA256] = challengeSum

	if err := s.savePayload(ctx, task); err != nil {
		return "", err
	}

	s.logger.InfoContext(ctx, "Re-wrapped secrets sent to a second agent to confirm",
		"check", check.UID, "task", task.UID, "verifier", pick.agent.UID)

	return "", nil
}

// storeUnconfirmed stores a passed re-wrap no second agent can confirm. The
// blob is marked unconfirmed, so the check stays flagged with that reason
// until a confirmed re-wrap or a new seal.
func (s *Server) storeUnconfirmed(
	ctx context.Context, task *models.WorkerTask, check *models.Check, envelope string,
) (string, error) {
	marked, err := credentials.MarkSealedUnconfirmed(envelope)
	if err != nil {
		return CodeInvalidResult, nil //nolint:nilerr // a blob that does not parse is refused
	}

	stored, err := s.store(ctx, check, *check.ConfigSealed, marked)
	if err != nil {
		return "", err
	}

	if !stored {
		return CodeCheckChanged, nil
	}

	task.Payload[payloadConfirm] = confirmNone

	if err := s.savePayload(ctx, task); err != nil {
		return "", err
	}

	s.logger.WarnContext(ctx, "Sealed secrets re-wrapped by a peer agent, not confirmed by a second agent",
		"check", check.UID, "task", task.UID)

	return "", nil
}

// savePayload writes a task's payload back.
func (s *Server) savePayload(ctx context.Context, task *models.WorkerTask) error {
	if _, err := s.db.DB().NewUpdate().
		Model((*models.WorkerTask)(nil)).
		Set("payload = ?", task.Payload).
		Where("uid = ?", task.UID).
		Exec(ctx); err != nil {
		return fmt.Errorf("save re-wrap payload: %w", err)
	}

	return nil
}

// sendConfirmation is the re-wrap kind's End hook: it creates the
// confirmation task of a re-wrap that asked for one. A failure here leaves
// the check as it was: Status reports the re-wrap as timed out once Timeout
// passed.
func (s *Server) sendConfirmation(ctx context.Context, task *models.WorkerTask) {
	if task.Status != models.WorkerTaskStatusDone || payloadString(task, payloadConfirm) != confirmPending ||
		task.CheckUID == nil {
		return
	}

	verifier := payloadString(task, payloadVerifierUID)

	verify := models.NewWorkerTask(task.OrganizationUID, VerifyKind, payloadString(task, payloadVerifierRegion))
	verify.UID = payloadString(task, payloadVerifyTaskUID)
	verify.CheckUID = task.CheckUID
	verify.TargetWorkerUID = &verifier
	verify.Payload = models.JSONMap{
		payloadRewrapTaskUID:   task.UID,
		payloadEnvelopeSHA256:  payloadString(task, payloadEnvelopeSHA256),
		payloadPendingEnvelope: payloadString(task, payloadPendingEnvelope),
		payloadChallenge:       payloadString(task, payloadVerifyChallenge),
		payloadChallengeSHA256: payloadString(task, payloadVerifyChallengeSHA256),
	}

	created, err := s.tasks.CreateIfNone(ctx, s.db.DB(), verify)
	if err != nil || !created {
		s.logger.WarnContext(ctx, "Re-wrap confirmation not sent", "check", *task.CheckUID, "task", task.UID,
			"created", created, "error", err)
	}
}

// prepareVerifyClaim hands the confirming agent the re-sealed blob, the blob
// it replaces, its challenge and the check's type and public config.
func (s *Server) prepareVerifyClaim(
	ctx context.Context, task *models.WorkerTask, _ *workertasks.WorkerIdentity, _ json.RawMessage,
) (*workertasks.Prepared, error) {
	check, same := s.taskCheck(ctx, task)
	if !same {
		return nil, workertasks.ErrCancelTask
	}

	input, err := json.Marshal(VerifyInput{
		Envelope:  payloadString(task, payloadPendingEnvelope),
		Previous:  *check.ConfigSealed,
		Challenge: payloadString(task, payloadChallenge),
		CheckType: check.Type,
		Config:    check.Config,
	})
	if err != nil {
		return nil, fmt.Errorf("encode re-wrap confirmation input: %w", err)
	}

	return &workertasks.Prepared{Input: input, Final: true}, nil
}

// completeVerify stores the re-sealed blob once the second agent opened it
// and answered its challenge, if the check still has the old blob and the
// same recipients. A refusal leaves the check as it was, flagged.
func (s *Server) completeVerify(ctx context.Context, task *models.WorkerTask, body json.RawMessage) (string, error) {
	var result VerifyResult
	if err := json.Unmarshal(body, &result); err != nil {
		return CodeInvalidResult, nil //nolint:nilerr // a malformed body is a task outcome, not an error
	}

	if result.ErrorCode != "" {
		s.logger.WarnContext(ctx, "Re-wrapped secrets not confirmed by the second agent",
			"check", task.CheckUID, "task", task.UID, "code", result.ErrorCode)

		if isVerifyRefusal(result.ErrorCode) {
			return result.ErrorCode, nil
		}

		return CodeInvalidResult, nil
	}

	if !credentials.ChallengeAnswered(result.Challenge, payloadString(task, payloadChallengeSHA256)) {
		return CodeInvalidResult, nil
	}

	check, same := s.taskCheck(ctx, task)
	if !same {
		return CodeCheckChanged, nil
	}

	pending := payloadString(task, payloadPendingEnvelope)

	recipients, err := s.currentRecipients(ctx, check)
	if err != nil {
		return "", err
	}

	sealedTo, err := credentials.SealedRecipientFingerprints(pending)
	if err != nil {
		return CodeInvalidResult, nil //nolint:nilerr // an unreadable blob is refused
	}

	sorted := append([]string(nil), sealedTo...)
	slices.Sort(sorted)

	if !slices.Equal(sorted, sealing.SortedFingerprints(recipients)) {
		return CodeRecipientsChanged, nil
	}

	stored, err := s.store(ctx, check, *check.ConfigSealed, pending)
	if err != nil {
		return "", err
	}

	if !stored {
		return CodeCheckChanged, nil
	}

	s.logger.InfoContext(ctx, "Sealed secrets re-wrapped by a peer agent and confirmed by a second one",
		"check", check.UID, "task", task.UID)

	return "", nil
}

// confirmationOutcome folds the confirmation of a done re-wrap into its
// outcome: still pending while the confirmation runs, failed with its code
// when refused, done with CodeNotConfirmed when stored without one.
func (s *Server) confirmationOutcome(ctx context.Context, row *models.WorkerTask, outcome *Outcome) {
	switch payloadString(row, payloadConfirm) {
	case confirmNone:
		outcome.ErrorCode = CodeNotConfirmed
	case confirmPending:
		verify, err := s.tasks.Get(ctx, payloadString(row, payloadVerifyTaskUID))
		if err != nil || verify == nil {
			// Not created (yet): pending until the re-wrap times out.
			outcome.Status = string(models.WorkerTaskStatusPending)
			if row.CreatedAt.Before(s.now().Add(-Timeout)) {
				outcome.Status = string(models.WorkerTaskStatusFailed)
				outcome.ErrorCode = CodeTimedOut
			}

			return
		}

		outcome.Status = string(verify.Status)
		if verify.ErrorCode != nil {
			outcome.ErrorCode = *verify.ErrorCode
		}
	}
}
