package rewraptask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"

	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
	"github.com/fclairamb/solidping/server/internal/checkworker/tasks"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// Executor is the agent side of the re-wrap kind.
type Executor struct {
	identities func() []string
	logger     *slog.Logger
}

var _ tasks.Executor = (*Executor)(nil)

// NewExecutor builds the executor over the agent's age identities: its own
// key, then the shared region keys it holds.
func NewExecutor(identities func() []string) *Executor {
	return &Executor{identities: identities, logger: slog.Default().With("component", "rewrap_task")}
}

// Kind implements tasks.Executor.
func (*Executor) Kind() string { return Kind }

// Capability implements tasks.Executor.
func (*Executor) Capability() string { return Capability }

// Run implements tasks.Executor: prove possession, open the blob, check its
// plaintext, seal it to the new list, return the new ciphertext and the
// opened challenge only. A blob the agent cannot open finishes with
// CodeCannotOpen: the server reports it, retrying cannot help.
func (e *Executor) Run(ctx context.Context, claim *workertasks.Claim, rep tasks.Reporter) error {
	var input Input
	if err := json.Unmarshal(claim.Input, &input); err != nil || input.Envelope == "" || len(input.Recipients) == 0 {
		return refuse(ctx, e.logger, claim, rep, CodeInvalidInput, fmt.Errorf("decode re-wrap input: %w", err))
	}

	// The possession challenge (spec 2026-11-11-02). A server older than the
	// challenge sends none, and ignores the answer.
	answer := ""

	if input.Challenge != "" {
		opened, err := credentials.OpenSealedChallenge(input.Challenge, e.identities()...)
		if err != nil {
			return refuse(ctx, e.logger, claim, rep, CodeCannotOpen, err)
		}

		answer = opened
	}

	secrets, err := credentials.UnsealWithIdentities(input.Envelope, e.identities()...)
	if err != nil {
		return refuse(ctx, e.logger, claim, rep, CodeCannotOpen, err)
	}

	if plainErr := checkPlaintext(input.Envelope, secrets, input.CheckType, input.Config); plainErr != nil {
		return refuse(ctx, e.logger, claim, rep, CodeNotSecret, plainErr)
	}

	fingerprints, ct, err := credentials.SealForClient(input.Recipients, secrets)
	if err != nil {
		return refuse(ctx, e.logger, claim, rep, CodeSealFailed, err)
	}

	body, err := json.Marshal(Result{CT: ct, Fingerprints: fingerprints, Challenge: answer})
	if err != nil {
		return fmt.Errorf("encode re-wrap result: %w", err)
	}

	e.logger.InfoContext(ctx, "Sealed secrets re-wrapped", "task", claim.UID, "check", claim.CheckUID,
		"recipient_count", len(fingerprints), "recipients", fingerprints)

	return rep.Finish(ctx, body)
}

// checkPlaintext refuses a plaintext that carries a key that is not a secret
// field of the check type, or that is bound to another target (spec
// 2026-11-11-02 §2). The secret-fields rule holds whatever the blob's origin
// (spec 2026-11-12-03): a stored server blob is sealed from the private half
// of the config, so it only ever holds secret fields, and a server blob of a
// private-only region goes through a peer re-wrap too. A server older than the
// check sends no type: nothing to check against.
func checkPlaintext(envelope string, plain map[string]any, checkType string, config map[string]any) error {
	if checkType == "" {
		return nil
	}

	check := configregistry.BindingCheckFor(checkType, config)

	secrets, _, err := credentials.OpenSealedPlaintext(envelope, plain, check)
	if err != nil {
		return err
	}

	return credentials.StrictSecretKeys(secrets, check)
}

// VerifyExecutor is the agent side of the confirmation of a re-wrap: it opens
// the re-sealed blob with its current key and answers whether it passes.
type VerifyExecutor struct {
	identities func() []string
	logger     *slog.Logger
}

var _ tasks.Executor = (*VerifyExecutor)(nil)

// NewVerifyExecutor builds the confirmation executor over the agent's age
// identities.
func NewVerifyExecutor(identities func() []string) *VerifyExecutor {
	return &VerifyExecutor{identities: identities, logger: slog.Default().With("component", "rewrap_verify_task")}
}

// Kind implements tasks.Executor.
func (*VerifyExecutor) Kind() string { return VerifyKind }

// Capability implements tasks.Executor.
func (*VerifyExecutor) Capability() string { return VerifyCapability }

// Run implements tasks.Executor: prove possession, open the re-sealed blob,
// check it carries only secret fields of the type, and, when this agent can
// open the old blob too, the same plaintext. Answers with the opened
// challenge only: the plaintext never leaves the agent.
func (e *VerifyExecutor) Run(ctx context.Context, claim *workertasks.Claim, rep tasks.Reporter) error {
	var input VerifyInput
	if err := json.Unmarshal(claim.Input, &input); err != nil || input.Envelope == "" || input.Challenge == "" {
		return refuse(ctx, e.logger, claim, rep, CodeInvalidInput, fmt.Errorf("decode re-wrap confirmation: %w", err))
	}

	answer, err := credentials.OpenSealedChallenge(input.Challenge, e.identities()...)
	if err != nil {
		return refuse(ctx, e.logger, claim, rep, CodeCannotOpen, err)
	}

	pending, err := credentials.UnsealWithIdentities(input.Envelope, e.identities()...)
	if err != nil {
		return refuse(ctx, e.logger, claim, rep, CodeVerifyCannotOpen, err)
	}

	if plainErr := checkPlaintext(input.Envelope, pending, input.CheckType, input.Config); plainErr != nil {
		return refuse(ctx, e.logger, claim, rep, CodeNotSecret, plainErr)
	}

	if input.Previous != "" {
		// Only a holder of an old key can compare: a new agent cannot.
		if previous, prevErr := credentials.UnsealWithIdentities(input.Previous, e.identities()...); prevErr == nil &&
			!reflect.DeepEqual(previous, pending) {
			return refuse(ctx, e.logger, claim, rep, CodeVerifyMismatch,
				errors.New("the re-sealed secrets differ from the old ones")) //nolint:err113 // log only
		}
	}

	body, err := json.Marshal(VerifyResult{Challenge: answer})
	if err != nil {
		return fmt.Errorf("encode re-wrap confirmation: %w", err)
	}

	e.logger.InfoContext(ctx, "Re-wrapped secrets confirmed", "task", claim.UID, "check", claim.CheckUID)

	return rep.Finish(ctx, body)
}

// refuse finishes the task with a terminal code. Never logs key material:
// the error comes from age or JSON decoding.
func refuse(
	ctx context.Context, logger *slog.Logger, claim *workertasks.Claim, rep tasks.Reporter, code string, cause error,
) error {
	logger.WarnContext(ctx, "Re-wrap refused", "task", claim.UID, "check", claim.CheckUID,
		"code", code, "error", cause)

	body, err := json.Marshal(Result{ErrorCode: code})
	if err != nil {
		return fmt.Errorf("encode re-wrap refusal: %w", err)
	}

	return rep.Finish(ctx, body)
}
