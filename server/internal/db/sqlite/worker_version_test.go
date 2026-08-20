package sqlite

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func seedVerWorker(ctx context.Context, t *testing.T, svc *Service, slug string) *models.Worker {
	t.Helper()

	region := "eu-west-1"
	worker, err := svc.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID: uuid.New().String(), Slug: slug, Name: slug, Region: &region,
	})
	require.NoError(t, err)

	return worker
}

func reloadVer(ctx context.Context, t *testing.T, svc *Service, uid string) *models.Worker {
	t.Helper()

	var row models.Worker
	require.NoError(t, svc.DB().NewSelect().Model(&row).
		Where("uid = ?", uid).Scan(ctx))

	return &row
}

// TestWorkerVersionRoundTrip is the SQLite half of the round-trip proof for
// spec 2026-08-19-07: NULL (never reported) and a reported value must survive
// a write and a read as two genuinely different answers — a scalar, so only
// two states, unlike the three-state `capabilities` column.
func TestWorkerVersionRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc := newCapDB(t)
	ctx := t.Context()

	unknown := seedVerWorker(ctx, t, svc, "ver-wrk-unknown")
	reported := seedVerWorker(ctx, t, svc, "ver-wrk-reported")

	r.NoError(svc.UpdateWorkerHeartbeat(ctx, unknown.UID, nil, ""))
	r.NoError(svc.UpdateWorkerHeartbeat(ctx, reported.UID, nil, "0.17.0"))

	var rawUnknown, rawReported *string
	r.NoError(svc.DB().NewSelect().Table("workers").Column("version").
		Where("uid = ?", unknown.UID).Scan(ctx, &rawUnknown))
	r.NoError(svc.DB().NewSelect().Table("workers").Column("version").
		Where("uid = ?", reported.UID).Scan(ctx, &rawReported))

	r.Nil(rawUnknown, "a worker that never reported must leave the column SQL NULL")
	r.NotNil(rawReported)
	r.Equal("0.17.0", *rawReported)

	r.Nil(reloadVer(ctx, t, svc, unknown.UID).Version)
	r.Equal("0.17.0", *reloadVer(ctx, t, svc, reported.UID).Version)
}

// TestWorkerVersionHeartbeatWithoutReportKeepsValue: an unreported heartbeat
// (empty version string) must never downgrade a known version back to NULL —
// same "do not overwrite a known answer with a guess" rule as capabilities.
func TestWorkerVersionHeartbeatWithoutReportKeepsValue(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc := newCapDB(t)
	ctx := t.Context()

	worker := seedVerWorker(ctx, t, svc, "ver-wrk-keep")
	r.NoError(svc.UpdateWorkerHeartbeat(ctx, worker.UID, nil, "0.17.0"))

	for range 3 {
		r.NoError(svc.UpdateWorkerHeartbeat(ctx, worker.UID, nil, ""))
		r.Equal("0.17.0", *reloadVer(ctx, t, svc, worker.UID).Version)
	}

	// The registration path carries the same guarantee — an agent hits it on
	// every reconnect, before its first version-bearing frame.
	again, err := svc.RegisterOrUpdateWorker(ctx, &models.Worker{Slug: "ver-wrk-keep", Name: "ver-wrk-keep"})
	r.NoError(err)
	r.NotNil(again.Version)
	r.Equal("0.17.0", *again.Version)
}

// TestWorkerVersionEmptyStringRejected: the schema-level CHECK refuses an
// empty string outright, not merely the writer's "empty means don't touch"
// convention — an empty string must never masquerade as a version.
func TestWorkerVersionEmptyStringRejected(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc := newCapDB(t)
	ctx := t.Context()

	worker := seedVerWorker(ctx, t, svc, "ver-wrk-empty-check")

	_, err := svc.DB().ExecContext(ctx, "update workers set version = '' where uid = ?", worker.UID)
	r.Error(err, "an empty string must be rejected by the database, not merely by the writers")

	_, err = svc.DB().ExecContext(ctx,
		"insert into workers (uid, slug, name, version) values (?, 'ver-wrk-empty-ins', 'ins', '')",
		uuid.New().String())
	r.Error(err, "the insert path must be rejected too")
}
