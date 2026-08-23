package postgres

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portMessageIDDedup is distinct from every other _postgres_test.go file's
// embedded-Postgres port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portMessageIDDedup = 15490

// TestHasRawResultWithMessageID_Postgres is the Postgres half of the
// inbound-email dedup backstop (spec 2026-08-22-01, layer 3). The two dialects
// answer the same question through different JSON operators — `output->>'…'`
// here, `json_extract` on SQLite — and nothing but a paired test keeps them
// agreeing, since no schema constraint backs either one.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction) with its siblings
func TestHasRawResultWithMessageID_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{
		Embedded: true,
		Port:     portMessageIDDedup,
		RunMode:  runModeTest,
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("dedup-pg-org", "Dedup PG Org")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "inbox-check", "email")
	other := models.NewCheck(org.UID, "other-check", "email")
	r.NoError(s.CreateCheck(ctx, check))
	r.NoError(s.CreateCheck(ctx, other))

	now := time.Now()
	window := now.Add(-7 * 24 * time.Hour)

	const known = "<known@mail.example.com>"

	seed := func(checkUID, messageID string, at time.Time, periodType string) {
		result := models.NewResult(org.UID, checkUID, models.ResultStatusUp, 0)
		result.PeriodType = periodType
		result.PeriodStart = at
		result.Output = models.JSONMap{"messageId": messageID, "message": "Email received"}
		r.NoError(s.CreateResult(ctx, result))
	}

	seed(check.UID, known, now, models.PeriodTypeRaw)

	found, err := s.HasRawResultWithMessageID(ctx, check.UID, known, window)
	r.NoError(err)
	r.True(found, "the seeded Message-ID must be found")

	found, err = s.HasRawResultWithMessageID(ctx, check.UID, "<never-seen@example.com>", window)
	r.NoError(err)
	r.False(found, "a different Message-ID must not match")

	found, err = s.HasRawResultWithMessageID(ctx, other.UID, known, window)
	r.NoError(err)
	r.False(found, "the lookup must be scoped to the check")

	seed(check.UID, "<ancient@example.com>", now.Add(-30*24*time.Hour), models.PeriodTypeRaw)

	found, err = s.HasRawResultWithMessageID(ctx, check.UID, "<ancient@example.com>", window)
	r.NoError(err)
	r.False(found, "a row older than the window must not count as a duplicate")

	seed(check.UID, "<rolled-up@example.com>", now, models.PeriodTypeHour)

	found, err = s.HasRawResultWithMessageID(ctx, check.UID, "<rolled-up@example.com>", window)
	r.NoError(err)
	r.False(found, "only raw rows may answer the dedup question")

	found, err = s.HasRawResultWithMessageID(ctx, check.UID, "", window)
	r.NoError(err)
	r.False(found, "an empty Message-ID must never report a duplicate")
}
