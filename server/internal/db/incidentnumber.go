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

// IncidentNumberIndex is the unique index on (organization_uid, number). Named
// here rather than only in the migrations because the retry loop has to
// recognize a violation OF THIS INDEX in a driver error string.
const IncidentNumberIndex = "incidents_organization_number_idx"

// sqliteIncidentNumberColumns is how SQLite words the same violation. It names
// the COLUMNS, not the index ("UNIQUE constraint failed:
// incidents.organization_uid, incidents.number"), so the index name alone would
// never match there.
const sqliteIncidentNumberColumns = "incidents.number"

// IsUniqueViolation reports whether err looks like a unique-constraint
// violation from either engine.
//
// It is a string match because the two drivers wrap their errors differently
// and expose no portable typed error. It says nothing about WHICH constraint —
// use IsIncidentNumberCollision for that.
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

// IsIncidentNumberCollision narrows IsUniqueViolation to the ONE constraint the
// retry loop can actually resolve by trying again.
//
// Exported so a test can feed it a violation produced by a REAL database on
// each engine: it matches on driver error text, and driver error text is not
// something a hand-written fake can be trusted to reproduce.
//
// This distinction is not pedantry. `incidents` also carries
// `uq_active_group_incident` on (organization_uid, check_group_uid), and the
// group-incident open path at handlers/incidents/service.go deliberately RACES
// on it: two workers seeing the same group fail simultaneously, one losing, and
// the loser re-fetching the winner's incident. Retrying that violation cannot
// ever succeed — the number changes, the group key does not — so a broad match
// would burn all IncidentNumberAttempts round trips before surfacing an outcome
// the caller was already prepared to handle on the first one.
func IsIncidentNumberCollision(err error) bool {
	if !IsUniqueViolation(err) {
		return false
	}

	msg := err.Error()

	// Postgres names the index; SQLite names the columns.
	return strings.Contains(msg, IncidentNumberIndex) ||
		strings.Contains(msg, sqliteIncidentNumberColumns)
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

		// Only a collision on the NUMBER index is worth another go. Anything
		// else — including a violation of the group-incident unique index the
		// caller races on deliberately — is surfaced on the first attempt.
		if !IsIncidentNumberCollision(insertErr) {
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
