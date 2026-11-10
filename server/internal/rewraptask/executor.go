package rewraptask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/checkworker/tasks"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// Executor is the agent side of the re-wrap kind.
type Executor struct {
	identities func() []string
	logger     *slog.Logger
}

// errEmptyInput is an input without a blob or without recipients.
var errEmptyInput = errors.New("no blob or no recipients")

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

// Run implements tasks.Executor: open the blob, seal it to the new list,
// return the new ciphertext only. A blob the agent cannot open finishes with
// CodeCannotOpen: the server reports it, retrying cannot help.
func (e *Executor) Run(ctx context.Context, claim *workertasks.Claim, rep tasks.Reporter) error {
	var input Input

	err := json.Unmarshal(claim.Input, &input)
	if err == nil && (input.Envelope == "" || len(input.Recipients) == 0) {
		err = errEmptyInput
	}

	if err != nil {
		return e.refuse(ctx, claim, rep, CodeInvalidInput, fmt.Errorf("decode re-wrap input: %w", err))
	}

	secrets, err := credentials.UnsealWithIdentities(input.Envelope, e.identities()...)
	if err != nil {
		return e.refuse(ctx, claim, rep, CodeCannotOpen, err)
	}

	fingerprints, ct, err := credentials.SealForClient(input.Recipients, secrets)
	if err != nil {
		return e.refuse(ctx, claim, rep, CodeSealFailed, err)
	}

	body, _ := json.Marshal(Result{CT: ct, Fingerprints: fingerprints}) //nolint:errchkjson // plain strings: cannot fail

	e.logger.InfoContext(ctx, "Sealed secrets re-wrapped", "task", claim.UID, "check", claim.CheckUID,
		"recipient_count", len(fingerprints), "recipients", fingerprints)

	return rep.Finish(ctx, body)
}

// refuse finishes the task with a terminal code. Never logs key material:
// the error comes from age or JSON decoding.
func (e *Executor) refuse(
	ctx context.Context, claim *workertasks.Claim, rep tasks.Reporter, code string, cause error,
) error {
	e.logger.WarnContext(ctx, "Re-wrap refused", "task", claim.UID, "check", claim.CheckUID,
		"code", code, "error", cause)

	body, _ := json.Marshal(Result{ErrorCode: code}) //nolint:errchkjson // plain strings: cannot fail

	return rep.Finish(ctx, body)
}
