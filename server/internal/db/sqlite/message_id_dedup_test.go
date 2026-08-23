package sqlite

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// seedMessageIDResult inserts one raw result carrying output.messageId, at the
// given period_start.
func seedMessageIDResult(
	t *testing.T, s *Service, orgUID, checkUID, messageID string, at time.Time,
) *models.Result {
	t.Helper()

	result := models.NewResult(orgUID, checkUID, models.ResultStatusUp, 0)
	result.PeriodStart = at
	result.Output = models.JSONMap{"messageId": messageID, "message": "Email received"}

	require.NoError(t, s.CreateResult(t.Context(), result))

	return result
}

// TestHasRawResultWithMessageID is the SQLite half of the dedup backstop that
// makes one inbound email mint exactly one result (spec 2026-08-22-01, layer
// 3). There is no unique index behind it — the query IS the constraint — so
// each of its dimensions is asserted rather than assumed: the messageId must
// match, the check must match, and the row must be inside the window.
func TestHasRawResultWithMessageID(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	org := models.NewOrganization("dedup-org", "Dedup Org")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "inbox-check", "email")
	other := models.NewCheck(org.UID, "other-check", "email")
	r.NoError(s.CreateCheck(ctx, check))
	r.NoError(s.CreateCheck(ctx, other))

	now := time.Now()
	window := now.Add(-7 * 24 * time.Hour)

	const known = "<known@mail.example.com>"

	seedMessageIDResult(t, s, org.UID, check.UID, known, now)

	found, err := s.HasRawResultWithMessageID(ctx, check.UID, known, window)
	r.NoError(err)
	r.True(found, "the seeded Message-ID must be found")

	found, err = s.HasRawResultWithMessageID(ctx, check.UID, "<never-seen@example.com>", window)
	r.NoError(err)
	r.False(found, "a different Message-ID must not match")

	// Same Message-ID, different check: a mail fanned out to two checks must
	// still produce a result for each.
	found, err = s.HasRawResultWithMessageID(ctx, other.UID, known, window)
	r.NoError(err)
	r.False(found, "the lookup must be scoped to the check")

	// Outside the window: an old row must not suppress a fresh delivery.
	seedMessageIDResult(t, s, org.UID, check.UID, "<ancient@example.com>", now.Add(-30*24*time.Hour))

	found, err = s.HasRawResultWithMessageID(ctx, check.UID, "<ancient@example.com>", window)
	r.NoError(err)
	r.False(found, "a row older than the window must not count as a duplicate")

	// A blank Message-ID never dedups: emails without the header (rare) must
	// keep being recorded rather than all collapsing onto one row.
	blank := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 0)
	blank.PeriodStart = now
	blank.Output = models.JSONMap{"messageId": ""}
	r.NoError(s.CreateResult(ctx, blank))

	found, err = s.HasRawResultWithMessageID(ctx, check.UID, "", window)
	r.NoError(err)
	r.False(found, "an empty Message-ID must never report a duplicate")
}

// TestHasRawResultWithMessageIDIgnoresRollups guards the period_type filter:
// an aggregated row can inherit an output blob, and matching one would
// suppress a genuinely new email.
func TestHasRawResultWithMessageIDIgnoresRollups(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	org := models.NewOrganization("dedup-rollup-org", "Dedup Rollup Org")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "rollup-check", "email")
	r.NoError(s.CreateCheck(ctx, check))

	now := time.Now()

	rollup := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 0)
	rollup.PeriodType = models.PeriodTypeHour
	rollup.PeriodStart = now
	rollup.Output = models.JSONMap{"messageId": "<rolled-up@example.com>"}
	r.NoError(s.CreateResult(ctx, rollup))

	found, err := s.HasRawResultWithMessageID(
		ctx, check.UID, "<rolled-up@example.com>", now.Add(-7*24*time.Hour),
	)
	r.NoError(err)
	r.False(found, "only raw rows may answer the dedup question")
}
