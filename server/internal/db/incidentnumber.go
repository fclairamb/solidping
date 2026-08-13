package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// IncidentNumberAttempts bounds the "take MAX+1, insert, retry on collision"
// loop. Every retry means a real concurrent creation in the SAME organization
// won the race; more than a handful of those in the microseconds one insert
// takes would be a stampede, not contention, and looping forever would turn it
// into a livelock.
const IncidentNumberAttempts = 8

// ErrIncidentNumberExhausted means the per-org number could not be claimed
// within IncidentNumberAttempts. Deliberately an error rather than a silent
// number-less incident: an incident with no reference cannot be acked from
// Telegram or named in Slack, which is worse than a failed create the caller
// can retry.
var ErrIncidentNumberExhausted = errors.New("could not assign a per-organization incident number")

// IsUniqueViolation reports whether err looks like a unique-constraint
// violation from either engine.
//
// It is a string match because the two drivers wrap their errors differently
// and the only distinction that matters here is "this exact row already
// exists" versus anything else. Over-matching is safe: the single caller
// simply recomputes the next number and tries again.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()

	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "duplicate key value") ||
		strings.Contains(msg, "SQLSTATE 23505") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}

// CreateIncidentWithNumber is the engine-agnostic half of incident creation:
// it claims the next per-org number and inserts, retrying the pair whenever a
// concurrent creation took the same number first.
//
// Why not a sequence or a counter table: the number must be per organization
// and gap-free-ish in practice, which a global sequence cannot give, and a
// counter row would need `UPDATE ... RETURNING` — supported on PostgreSQL, not
// portably on the SQLite driver this repo ships. MAX+1 plus the unique index on
// (organization_uid, number) is the one scheme that behaves identically on both
// engines: the index, not the read, is what actually guarantees uniqueness. The
// SELECT is only an optimistic guess, so a stale read costs one retry and never
// a duplicate.
//
// Numbers are never reused: nextNumber deliberately counts soft-deleted rows
// too, so `#42` identifies one incident forever.
//
// An incident that already carries a number (a test fixture, a re-insert) is
// inserted untouched.
func CreateIncidentWithNumber(
	ctx context.Context,
	incident *models.Incident,
	nextNumber func(ctx context.Context, orgUID string) (int64, error),
	insert func(ctx context.Context) error,
) error {
	if incident.Number > 0 {
		return insert(ctx)
	}

	var lastErr error

	for attempt := 0; attempt < IncidentNumberAttempts; attempt++ {
		next, err := nextNumber(ctx, incident.OrganizationUID)
		if err != nil {
			return fmt.Errorf("reading the next incident number: %w", err)
		}

		incident.Number = next

		insertErr := insert(ctx)
		if insertErr == nil {
			return nil
		}

		if !IsUniqueViolation(insertErr) {
			// Reset so a caller that inspects the model after a hard failure
			// does not see a number that was never persisted.
			incident.Number = 0

			return insertErr
		}

		lastErr = insertErr
	}

	incident.Number = 0

	return fmt.Errorf("%w after %d attempts: %w", ErrIncidentNumberExhausted, IncidentNumberAttempts, lastErr)
}
