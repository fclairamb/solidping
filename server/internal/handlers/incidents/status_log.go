package incidents

import (
	"context"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// statusChangeLog builds the log line a check's status transition earns: the
// level, the message ("Check down", "Check up", ...) and its attributes. ok is
// false when the status did not change. Pure, so the rule is table-tested.
//
// INFO is kept for the transitions an operator acts on, the same ones that
// notify: down, back up, warning, stale and back. Three are DEBUG:
//   - a brand-new check whose first result is up: the expected outcome, one
//     line per check on every fresh install otherwise;
//   - entering validating: the confirmation window before down, which notifies
//     nobody (the "Check down" line follows once it is confirmed);
//   - validating back to up: a blip that recovered inside that window.
//
// A brand-new check that starts failing is INFO once it is down: exactly what
// an operator wants to see.
//
// The line names the check (name and slug, never a lookup: both are on the row
// the result pipeline already holds), the previous status, and, when the check
// is failing, the reason taken from the result.
func statusChangeLog(
	check *models.Check, previous, current models.CheckStatus, result *models.Result,
) (slog.Level, string, []any, bool) {
	if previous == current {
		return 0, "", nil, false
	}

	level := slog.LevelInfo
	if current == models.CheckStatusValidating ||
		(current == models.CheckStatusUp &&
			(previous == models.CheckStatusCreated || previous == models.CheckStatusValidating)) {
		level = slog.LevelDebug
	}

	attrs := []any{
		"name", derefOr(check.Name, ""),
		"slug", derefOr(check.Slug, check.UID),
		"previous", previous.String(),
	}

	if reason := statusChangeReason(current, result); reason != "" {
		attrs = append(attrs, "error", reason)
	}

	return level, "Check " + current.String(), attrs, true
}

// statusChangeReason is the `error` attribute of a status-change line: the
// failure reason for a failing check (down or validating, with the status-
// derived fallback), the checker's own message for a warning when it gave one,
// nothing otherwise.
func statusChangeReason(current models.CheckStatus, result *models.Result) string {
	switch current {
	case models.CheckStatusDown, models.CheckStatusValidating:
		return failureReasonFromResult(result)
	case models.CheckStatusWarning:
		if result != nil && result.Output != nil {
			if msg, ok := result.Output[checkerdef.OutputKeyError].(string); ok {
				return msg
			}
		}

		return ""
	case models.CheckStatusCreated, models.CheckStatusUp, models.CheckStatusDegraded, models.CheckStatusStale:
		return ""
	default:
		return ""
	}
}

// logStatusChange writes statusChangeLog's line, if any.
func logStatusChange(
	ctx context.Context, check *models.Check, previous, current models.CheckStatus, result *models.Result,
) {
	level, msg, attrs, ok := statusChangeLog(check, previous, current, result)
	if !ok {
		return
	}

	slog.Log(ctx, level, msg, attrs...)
}

func derefOr(value *string, fallback string) string {
	if value == nil || *value == "" {
		return fallback
	}

	return *value
}
