package sqlite

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestWorkerSlugLeadingDigit is the SQLite half of spec 2026-09-05-04. SQLite's
// workers.slug CHECK was always length-only, so the digit-leading slug itself
// was never the problem here — what this test pins is that the statement-free
// 018_v0_24_0 mirror is ACCEPTED by the migrator and recorded as applied,
// rather than choking bun or being silently skipped, so both dialects stay on
// the same NNN sequence. Bun keys the ledger on the numeric prefix alone and
// ignores the rest of the file name.
func TestWorkerSlugLeadingDigit(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc := newCapDB(t)
	ctx := t.Context()

	var applied int
	r.NoError(svc.db.NewRaw(
		"SELECT count(*) FROM bun_migrations WHERE name = '018'",
	).Scan(ctx, &applied))
	r.Equal(1, applied, "the statement-free 018_v0_24_0 mirror must be recorded as applied")

	// Docker's default hostname: the 12-char hex container ID.
	dockerID := "063429309bca"
	region := "eu-west-1"
	worker, err := svc.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID: uuid.New().String(), Slug: dockerID, Name: dockerID, Region: &region,
	})
	r.NoError(err)

	var stored string
	r.NoError(svc.DB().NewSelect().Table("workers").Column("slug").
		Where("uid = ?", worker.UID).Scan(ctx, &stored))
	r.Equal(dockerID, stored, "the slug must be stored verbatim, not rewritten")
}
