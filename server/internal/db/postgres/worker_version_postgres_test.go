package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portWorkerVersion is distinct from every other _postgres_test.go embedded
// port in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portWorkerVersion = 15480

func newVerPG(t *testing.T) *Service {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	svc, err := New(ctx, &Config{Embedded: true, Port: portWorkerVersion, RunMode: "test"})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	return svc
}

func seedVerWorkerPG(ctx context.Context, t *testing.T, svc *Service, slug string) *models.Worker {
	t.Helper()

	region := "eu-west-1"
	worker, err := svc.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID: uuid.New().String(), Slug: slug, Name: slug, Region: &region,
	})
	require.NoError(t, err)

	return worker
}

func reloadVerPG(ctx context.Context, t *testing.T, svc *Service, uid string) *models.Worker {
	t.Helper()

	var row models.Worker
	require.NoError(t, svc.DB().NewSelect().Model(&row).
		Where("uid = ?", uid).Scan(ctx))

	return &row
}

// TestWorkerVersion_Postgres is the Postgres half of the round-trip proof for
// spec 2026-08-19-07: NULL and a reported version must come back as two
// genuinely distinguishable answers, an unreported heartbeat must never wipe
// a known value, and the database itself must refuse an empty string —
// version is a two-state column (unlike the three-state `capabilities`), so
// there is no "reported and empty" state to protect, only "never sent an
// empty string in the first place".
func TestWorkerVersion_Postgres(t *testing.T) {
	t.Parallel()

	svc := newVerPG(t)

	t.Run("round-trip", func(t *testing.T) {
		t.Parallel()
		verRoundTripPG(t, svc)
	})
	t.Run("heartbeat-keeps-value", func(t *testing.T) {
		t.Parallel()
		verHeartbeatKeepsValuePG(t, svc)
	})
	t.Run("empty-string-rejected", func(t *testing.T) {
		t.Parallel()
		verEmptyStringRejectedPG(t, svc)
	})
}

func verRoundTripPG(t *testing.T, svc *Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	unknown := seedVerWorkerPG(ctx, t, svc, "ver-unknown")
	reported := seedVerWorkerPG(ctx, t, svc, "ver-reported")

	r.NoError(svc.UpdateWorkerHeartbeat(ctx, unknown.UID, nil, ""))
	r.NoError(svc.UpdateWorkerHeartbeat(ctx, reported.UID, nil, "0.17.0"))

	r.Nil(reloadVerPG(ctx, t, svc, unknown.UID).Version,
		"a worker that never reported must leave the column SQL NULL — never an empty string")
	r.NotNil(reloadVerPG(ctx, t, svc, reported.UID).Version)
	r.Equal("0.17.0", *reloadVerPG(ctx, t, svc, reported.UID).Version)
}

// verHeartbeatKeepsValuePG: a silent heartbeat (empty version, "not
// reported") never downgrades a known version back to NULL.
func verHeartbeatKeepsValuePG(t *testing.T, svc *Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	worker := seedVerWorkerPG(ctx, t, svc, "ver-keep")
	r.NoError(svc.UpdateWorkerHeartbeat(ctx, worker.UID, nil, "0.17.0"))

	for range 3 {
		r.NoError(svc.UpdateWorkerHeartbeat(ctx, worker.UID, nil, ""))
		r.Equal("0.17.0", *reloadVerPG(ctx, t, svc, worker.UID).Version)
	}

	again, err := svc.RegisterOrUpdateWorker(ctx, &models.Worker{Slug: "ver-keep", Name: "ver-keep"})
	r.NoError(err)
	r.NotNil(again.Version)
	r.Equal("0.17.0", *again.Version)
}

// verEmptyStringRejectedPG: the schema-level CHECK, not merely the writer's
// "empty means don't touch" convention, refuses an empty string outright.
func verEmptyStringRejectedPG(t *testing.T, svc *Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	worker := seedVerWorkerPG(ctx, t, svc, "ver-empty-check")

	_, err := svc.DB().ExecContext(ctx, "update workers set version = '' where uid = ?", worker.UID)
	r.Error(err, "an empty string must be rejected by the database, not merely by the writers")
}
