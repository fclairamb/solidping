package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// errBoom is an ordinary failure — NOT a unique violation — used to prove the
// loop does not retry (or swallow) anything else.
var errBoom = errors.New("boom")

// The two engines word a unique-constraint violation completely differently,
// and the retry classifier is a string match, so both wordings are pinned here
// verbatim as the driver emits them.
var (
	errPostgresDuplicate = errors.New(
		`ERROR: duplicate key value violates unique constraint "incidents_organization_number_idx" (SQLSTATE=23505)`)
	errSQLiteDuplicate = errors.New(
		"UNIQUE constraint failed: incidents.organization_uid, incidents.number")
)

// The OTHER unique index on `incidents`: uq_active_group_incident on
// (organization_uid, check_group_uid). The group-incident open path races on it
// on purpose and handles the loss itself, so retrying it is pure waste.
var (
	errPostgresGroupDuplicate = errors.New(
		`ERROR: duplicate key value violates unique constraint "uq_active_group_incident" (SQLSTATE=23505)`)
	errSQLiteGroupDuplicate = errors.New(
		"UNIQUE constraint failed: incidents.organization_uid, incidents.check_group_uid")
)

// TestCreateIncidentWithNumber_RetriesOnUniqueViolation is the deterministic
// positive control behind the parallel race test: whether two goroutines
// actually collide is up to the scheduler, so the retry is pinned here by
// forcing a unique violation on the first insert.
func TestCreateIncidentWithNumber_RetriesOnUniqueViolation(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// The stand-in for the table: MAX+1 grows only once the insert lands, which
	// is exactly what makes the first, stale guess collide.
	highest := int64(7)
	attempts := 0

	incident := models.NewIncident("org", "check", time.Now(), "collide")

	err := db.CreateIncidentWithNumber(t.Context(), incident,
		func(_ context.Context, _ string) (int64, error) { return highest + 1, nil },
		func(_ context.Context) error {
			attempts++

			if attempts == 1 {
				// Somebody else claimed #8 between our SELECT and our INSERT.
				highest = 8

				return errPostgresDuplicate
			}

			return nil
		})

	r.NoError(err)
	r.Equal(2, attempts, "the collision must be retried exactly once")
	r.EqualValues(9, incident.Number, "the retry must re-read MAX+1, not reuse the stale guess")
}

// TestCreateIncidentWithNumber_DoesNotRetryOtherErrors proves the loop is
// narrow: a real failure surfaces immediately instead of being hammered eight
// times and reported as "exhausted", which would bury the actual cause.
func TestCreateIncidentWithNumber_DoesNotRetryOtherErrors(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	attempts := 0
	incident := models.NewIncident("org", "check", time.Now(), "boom")

	err := db.CreateIncidentWithNumber(t.Context(), incident,
		func(_ context.Context, _ string) (int64, error) { return 1, nil },
		func(_ context.Context) error {
			attempts++

			return errBoom
		})

	r.ErrorIs(err, errBoom)
	r.Equal(1, attempts)
	r.Zero(incident.Number, "a failed create must not leave a number that was never persisted")
}

// TestCreateIncidentWithNumber_DoesNotRetryOtherUniqueViolations is the
// efficiency contract, and the subtlest one here.
//
// `incidents` carries a SECOND unique index, uq_active_group_incident on
// (organization_uid, check_group_uid), and the group-incident open path races on
// it deliberately: two workers see the same group fail, one wins the insert, the
// loser re-fetches the winner's incident. Retrying that can never succeed — the
// number changes on each attempt, the group key does not — so a retry loop that
// matched "any unique violation" would spend eight pointless round trips before
// handing the caller an outcome it was ready for on the first.
func TestCreateIncidentWithNumber_DoesNotRetryOtherUniqueViolations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		violation  error
		retryable  error
		wantRetry  bool
		wantErrIs  error
		wantTries  int
		wantNumber int64
	}{
		{
			name:      "postgres group index",
			violation: errPostgresGroupDuplicate,
			wantErrIs: errPostgresGroupDuplicate,
			wantTries: 1,
		},
		{
			name:      "sqlite group columns",
			violation: errSQLiteGroupDuplicate,
			wantErrIs: errSQLiteGroupDuplicate,
			wantTries: 1,
		},
		{
			// Positive control: the SAME loop, the SAME kind of error class,
			// but on the number index — this one MUST still be retried, or the
			// test above would also pass on a loop that retries nothing.
			name:       "postgres number index is still retried",
			violation:  errPostgresDuplicate,
			wantRetry:  true,
			wantTries:  2,
			wantNumber: 1,
		},
		{
			name:       "sqlite number columns are still retried",
			violation:  errSQLiteDuplicate,
			wantRetry:  true,
			wantTries:  2,
			wantNumber: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			attempts := 0
			incident := models.NewIncident("org", "check", time.Now(), "group race")

			err := db.CreateIncidentWithNumber(t.Context(), incident,
				func(_ context.Context, _ string) (int64, error) { return 1, nil },
				func(_ context.Context) error {
					attempts++

					if tt.wantRetry && attempts > 1 {
						return nil
					}

					return tt.violation
				})

			r.Equal(tt.wantTries, attempts)

			if tt.wantRetry {
				r.NoError(err)
				r.Equal(tt.wantNumber, incident.Number)

				return
			}

			r.ErrorIs(err, tt.wantErrIs)
			r.NotErrorIs(err, db.ErrIncidentNumberExhausted,
				"a non-number unique violation must surface as itself, not as exhaustion")
			r.Zero(incident.Number)
		})
	}
}

// TestCreateIncidentWithNumber_SurvivesMoreLostRacesThanTheStallBudget is the
// deterministic guard for the bug the parallel race test only caught on a
// loaded runner.
//
// Losing the race is not a failure mode: it means a competing creation in the
// same organization committed, so MAX+1 has MOVED and the next attempt is a
// fresh number rather than the same guess again. N incidents opening at once
// make the unlucky one lose up to N-1 times, and N is set by the outage, not by
// this loop — so charging those losses to a small constant made a burst of
// simultaneous incidents fail to get numbered at all.
//
// The counterpart is TestCreateIncidentWithNumber_GivesUpAfterTooManyCollisions
// below: there MAX+1 never moves, and the loop must still give up. Together
// they pin the distinction — a loop that simply retried more would pass this
// test and fail that one.
func TestCreateIncidentWithNumber_SurvivesMoreLostRacesThanTheStallBudget(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Comfortably more lost races than the stall budget, every one of them
	// legitimate contention.
	const lostRaces = db.IncidentNumberAttempts * 4

	highest := int64(0)
	attempts := 0

	incident := models.NewIncident("org", "check", time.Now(), "burst")

	err := db.CreateIncidentWithNumber(t.Context(), incident,
		func(_ context.Context, _ string) (int64, error) { return highest + 1, nil },
		func(_ context.Context) error {
			attempts++

			if attempts <= lostRaces {
				// Somebody else claimed the number between our SELECT and our
				// INSERT, which is what makes the next read return a higher one.
				highest++

				return errSQLiteDuplicate
			}

			return nil
		})

	r.NoError(err, "a lost race is contention the loop must ride out, not a livelock")
	r.Equal(lostRaces+1, attempts)
	r.EqualValues(lostRaces+1, incident.Number)
}

// TestCreateIncidentWithNumber_GivesUpAfterTooManyCollisions proves the loop
// terminates. Retrying forever under a genuine stampede would livelock the
// worker that opened the incident.
func TestCreateIncidentWithNumber_GivesUpAfterTooManyCollisions(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	attempts := 0
	incident := models.NewIncident("org", "check", time.Now(), "stampede")

	err := db.CreateIncidentWithNumber(t.Context(), incident,
		func(_ context.Context, _ string) (int64, error) { return 1, nil },
		func(_ context.Context) error {
			attempts++

			return errSQLiteDuplicate
		})

	r.ErrorIs(err, db.ErrIncidentNumberExhausted)
	r.Equal(db.IncidentNumberAttempts, attempts)
}

// TestCreateIncidentWithNumber_KeepsAnExplicitNumber proves a pre-numbered
// incident (a fixture, a replay) is inserted untouched rather than renumbered.
func TestCreateIncidentWithNumber_KeepsAnExplicitNumber(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	incident := models.NewIncident("org", "check", time.Now(), "explicit")
	incident.Number = 42

	reads := 0

	err := db.CreateIncidentWithNumber(t.Context(), incident,
		func(_ context.Context, _ string) (int64, error) {
			reads++

			return 1, nil
		},
		func(_ context.Context) error { return nil })

	r.NoError(err)
	r.Zero(reads, "an explicit number must not trigger a MAX+1 read")
	r.EqualValues(42, incident.Number)
}
