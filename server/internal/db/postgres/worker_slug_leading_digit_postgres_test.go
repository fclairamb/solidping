package postgres

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portWorkerSlugLeadingDigit is distinct from every other _postgres_test.go
// embedded port in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portWorkerSlugLeadingDigit = 15522

// TestWorkerSlugLeadingDigit_Postgres pins the database half of spec
// 2026-09-05-04: after 018_v0_24_0 the workers.slug CHECK admits a leading
// digit, so Docker's default hex container-ID hostname registers. The
// leading-dash case is the positive control — it proves the constraint is
// still there and still rejecting, rather than having been dropped outright.
func TestWorkerSlugLeadingDigit_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portWorkerSlugLeadingDigit, RunMode: "test"})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	var constraintDef string
	r.NoError(svc.DB().NewRaw(
		`select pg_get_constraintdef(oid) from pg_constraint where conname = 'workers_slug_check'`,
	).Scan(ctx, &constraintDef))
	r.Contains(constraintDef, `'^[a-z0-9][a-z0-9-]{2,20}$'`,
		"018_v0_24_0 must have rewritten workers_slug_check to the relaxed pattern")

	region := "eu-west-1"

	// Docker's default hostname: the 12-char hex container ID.
	dockerID := "063429309bca"
	worker, err := svc.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID: uuid.New().String(), Slug: dockerID, Name: dockerID, Region: &region,
	})
	r.NoError(err, "a digit-leading slug must register after 018_v0_24_0")

	var stored string
	r.NoError(svc.DB().NewSelect().Table("workers").Column("slug").
		Where("uid = ?", worker.UID).Scan(ctx, &stored))
	r.Equal(dockerID, stored, "the slug must be stored verbatim, not rewritten")

	// Positive control: the constraint still exists and still rejects.
	_, err = svc.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID: uuid.New().String(), Slug: "-solidping", Name: "-solidping", Region: &region,
	})
	r.Error(err, "a leading dash must still violate workers_slug_check")
	r.Contains(err.Error(), "workers_slug_check")
}
