package db_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portDeleteCheckIncidentsPG is distinct from every other embedded-Postgres
// port in the repo.
const portDeleteCheckIncidentsPG = 15671

// TestDeleteCheckResolvesIncidents_SQLite pins the DB-layer invariant of spec
// 2026-10-08-02 on SQLite: DeleteCheck resolves the check's active incidents.
func TestDeleteCheckResolvesIncidents_SQLite(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	svc, err := sqlite.New(t.Context(), sqlite.Config{DataDir: tempDir})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	require.NoError(t, svc.Initialize(t.Context()))

	testDeleteCheckResolvesIncidents(t, svc)
}

// TestDeleteCheckResolvesIncidents_Postgres is the Postgres twin. It also
// re-runs the 027 backfill on a seeded orphan, which a fresh database never
// exercises.
func TestDeleteCheckResolvesIncidents_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	svc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portDeleteCheckIncidentsPG,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	testDeleteCheckResolvesIncidents(t, svc)

	t.Run("Migration027Backfill", func(t *testing.T) {
		r := require.New(t)

		org := models.NewOrganization("acme-orphans", "Acme")
		r.NoError(svc.CreateOrganization(ctx, org))

		check := models.NewCheck(org.UID, "gone", "http")
		r.NoError(svc.CreateCheck(ctx, check))

		orphan := models.NewIncident(org.UID, check.UID, time.Now().Add(-time.Hour), "orphan")
		r.NoError(svc.CreateIncident(ctx, orphan))

		// Reproduce the pre-fix state: the check soft-deleted behind the
		// incidents' back, the way org deletion used to do it.
		deletedAt := time.Now().Add(-30 * time.Minute).UTC().Truncate(time.Second)
		_, err := svc.DB().ExecContext(ctx,
			`update checks set deleted_at = ? where uid = ?`, deletedAt, check.UID)
		r.NoError(err)

		content, err := os.ReadFile(filepath.Join("postgres", "migrations", "027_v0_39_0.up.sql"))
		r.NoError(err)
		_, err = svc.DB().ExecContext(ctx, string(content))
		r.NoError(err)

		got, err := svc.GetIncident(ctx, org.UID, orphan.UID)
		r.NoError(err)
		r.Equal(models.IncidentStateResolved, got.State)
		r.NotNil(got.ResolutionType)
		r.Equal(models.ResolutionTypeCheckDeleted, *got.ResolutionType)
		r.NotNil(got.ResolvedAt)
		r.True(got.ResolvedAt.Equal(deletedAt), "resolved_at must be the check's deleted_at, got %s", got.ResolvedAt)
	})
}

func testDeleteCheckResolvesIncidents(t *testing.T, svc db.Service) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	org := models.NewOrganization("acme-delete-check", "Acme")
	r.NoError(svc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "doomed", "http")
	r.NoError(svc.CreateCheck(ctx, check))

	other := models.NewCheck(org.UID, "survivor", "http")
	r.NoError(svc.CreateCheck(ctx, other))

	active := models.NewIncident(org.UID, check.UID, time.Now().Add(-time.Hour), "active")
	r.NoError(svc.CreateIncident(ctx, active))

	// An already-resolved incident on the same check keeps its own resolution.
	earlier := time.Now().Add(-3 * time.Hour).UTC().Truncate(time.Second)
	resolvedAt := earlier.Add(time.Hour)
	resolved := models.NewIncident(org.UID, check.UID, earlier, "resolved")
	r.NoError(svc.CreateIncident(ctx, resolved))

	resolvedState := models.IncidentStateResolved
	autoType := models.ResolutionTypeAuto
	r.NoError(svc.UpdateIncident(ctx, resolved.UID, &models.IncidentUpdate{
		State: &resolvedState, ResolvedAt: &resolvedAt, ResolutionType: &autoType,
	}))

	// An active incident on another check is not touched.
	bystander := models.NewIncident(org.UID, other.UID, time.Now().Add(-time.Hour), "bystander")
	r.NoError(svc.CreateIncident(ctx, bystander))

	r.NoError(svc.DeleteCheck(ctx, check.UID))

	got, err := svc.GetIncident(ctx, org.UID, active.UID)
	r.NoError(err)
	r.Equal(models.IncidentStateResolved, got.State, "deleting a check resolves its active incident")
	r.NotNil(got.ResolvedAt)
	r.NotNil(got.ResolutionType)
	r.Equal(models.ResolutionTypeCheckDeleted, *got.ResolutionType)

	got, err = svc.GetIncident(ctx, org.UID, resolved.UID)
	r.NoError(err)
	r.NotNil(got.ResolutionType)
	r.Equal(models.ResolutionTypeAuto, *got.ResolutionType, "an already-resolved incident is left untouched")
	r.True(got.ResolvedAt.Equal(resolvedAt), "its resolved_at is left untouched")

	got, err = svc.GetIncident(ctx, org.UID, bystander.UID)
	r.NoError(err)
	r.Equal(models.IncidentStateActive, got.State, "another check's incident is not touched")

	// The check itself is soft-deleted: GetCheck no longer sees it, the
	// including-deleted reader still does.
	_, err = svc.GetCheck(ctx, org.UID, check.UID)
	r.Error(err)

	deleted, err := svc.GetCheckIncludingDeleted(ctx, org.UID, check.UID)
	r.NoError(err)
	r.NotNil(deleted.DeletedAt)
}
