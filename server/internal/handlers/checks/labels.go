package checks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ErrCheckCreatedIncomplete is returned when a check was inserted, something
// after the insert failed, AND the compensating hard delete also failed — so a
// half-configured row really is on disk. It is the one outcome an import must
// never report as `created: 0`: the caller has to be told exactly which slugs
// exist, or it will retry into duplicates (spec 2026-09-10-01).
var ErrCheckCreatedIncomplete = errors.New("check was created but could not be completed or removed")

// attachLabels resolves a label map to label UIDs and binds them to a check.
//
// The keys and values are expected to have been validated already (both write
// paths call models.ValidateLabels before their first write); a residual
// database CHECK violation is nonetheless translated back into the canonical
// validation error rather than surfaced as the driver string it is. That
// string used to reach import reports verbatim — double-prefixed and carrying
// a SQLSTATE — which is both unreadable and a schema detail no API should
// leak.
func (s *Service) attachLabels(ctx context.Context, orgUID, checkUID string, labels map[string]string) error {
	labelUIDs := make([]string, 0, len(labels))

	// Sorted so a multi-label failure always reports the same key first.
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		label, err := s.db.GetOrCreateLabel(ctx, orgUID, key, labels[key])
		if err != nil {
			return translateLabelWriteError(ctx, key, labels[key], err)
		}

		labelUIDs = append(labelUIDs, label.UID)
	}

	if err := s.db.SetCheckLabels(ctx, checkUID, labelUIDs); err != nil {
		return fmt.Errorf("set check labels: %w", err)
	}

	return nil
}

// translateLabelWriteError maps a database-level label CHECK violation onto
// the canonical validation error, and otherwise passes the error through with
// a single (not doubled) prefix.
//
// The driver error NEVER travels in the returned message: a SQLSTATE and a
// constraint name are schema detail, and leaking them into an import report is
// precisely the symptom spec 2026-09-10-01 exists to remove. On the one branch
// where the driver error carries information nobody else has — the Go rule and
// the database CHECK disagreeing — it is logged at ERROR (an operator has to
// know the two have drifted) and dropped from the response.
func translateLabelWriteError(ctx context.Context, key, value string, err error) error {
	if !db.IsLabelConstraintViolation(err) {
		return fmt.Errorf("label %q: %w", key, err)
	}

	// Decide which of the two rules it was by re-running the Go checks: the
	// database tells us a CHECK failed, it does not reliably tell us which.
	if keyErr := models.ValidateLabelKey(key); keyErr != nil {
		return keyErr
	}

	if valueErr := models.ValidateLabelValue(key, value); valueErr != nil {
		return valueErr
	}

	// The database refused something the Go rule accepts — a genuine drift
	// between the two, and a bug in this repo rather than in the request.
	slog.ErrorContext(ctx, "label rejected by the database but accepted by the Go rule",
		"label_key", key, "error", err)

	return fmt.Errorf("%w: %q: rejected by the database", models.ErrLabelKeyInvalid, key)
}

// compensateFailedCreate removes a check that was inserted but could not be
// finished, and returns the error to report for it.
//
// Why a compensating delete rather than a transaction: the DB layer reaches
// the database through ~260 direct s.db.New* call sites per backend and never
// consults dbctx.GetDB, so dbctx.RunInTx has no production callers and
// wrapping this path in it would compile, run, and roll back nothing. With
// label validation moved ahead of the insert, the only failures that can still
// land here are infrastructure ones, which makes a compensating delete both
// sufficient and far smaller in blast radius than teaching both backends to
// honor a context transaction.
//
// PurgeCheck, not DeleteCheck: DeleteCheck is a SOFT delete, so the slug would
// stay claimed and the very next import of the corrected document would fail
// on a slug conflict instead of creating the check.
func (s *Service) compensateFailedCreate(ctx context.Context, check *models.Check, cause error) error {
	if purgeErr := s.db.PurgeCheck(ctx, check.UID); purgeErr != nil {
		slog.ErrorContext(ctx, "check created but neither completed nor removed",
			"check_uid", check.UID, "cause", cause, "purge_error", purgeErr)

		return fmt.Errorf("%w: %w", ErrCheckCreatedIncomplete, cause)
	}

	s.checkStats.invalidate(check.OrganizationUID)

	return cause
}
