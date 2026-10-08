package db_test

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/dbctx"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/testsupport"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// portCheckVersionsPG is distinct from every other embedded-Postgres port in
// the repo.
const portCheckVersionsPG = 15652

// TestCheckVersions_SQLite runs the check version history contract (spec
// 2026-10-03-06) on SQLite.
func TestCheckVersions_SQLite(t *testing.T) {
	t.Parallel()

	tempDir, err := os.MkdirTemp("", "sqlite-check-versions-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })

	svc, err := sqlite.New(t.Context(), sqlite.Config{DataDir: tempDir})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	require.NoError(t, svc.Initialize(t.Context()))

	testCheckVersions(t, svc)
}

// TestCheckVersions_Postgres is the Postgres twin.
func TestCheckVersions_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	svc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portCheckVersionsPG,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	testCheckVersions(t, svc)
}

func testCheckVersions(t *testing.T, svc db.Service) {
	t.Helper()

	cases := map[string]func(t *testing.T, svc db.Service, org *models.Organization){
		"CreateThenRename":       testVersionCreateThenRename,
		"SecretOnlyWrite":        testVersionSecretOnlyWrite,
		"RuntimeOnlyWrite":       testVersionRuntimeOnlyWrite,
		"ChangeSource":           testVersionChangeSource,
		"OneVersionPerChange":    testVersionOneVersionPerChange,
		"Labels":                 testVersionLabels,
		"BaselineForOldCheck":    testVersionBaselineForOldCheck,
		"Retention":              testVersionRetention,
		"PurgeCascades":          testVersionPurgeCascades,
		"ProposalDecide":         testVersionProposalDecide,
		"ApprovalAmendsProposal": testVersionApprovalAmendsProposal,
	}

	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			slug := "cv-" + uuid.NewString()[:12]
			org := models.NewOrganization(slug, slug)
			require.NoError(t, svc.CreateOrganization(t.Context(), org))

			run(t, svc, org)
		})
	}
}

func newVersionedCheck(t *testing.T, svc db.Service, org *models.Organization, slug string) *models.Check {
	t.Helper()

	check := models.NewCheck(org.UID, slug, "http")
	check.Config = models.JSONMap{"url": "https://acme.com"}
	require.NoError(t, svc.CreateCheck(t.Context(), check))

	return check
}

func versionsOf(t *testing.T, svc db.Service, checkUID string) []*models.CheckVersion {
	t.Helper()

	versions, err := svc.ListCheckVersions(t.Context(), checkUID, 0)
	require.NoError(t, err)

	return versions
}

func testVersionCreateThenRename(t *testing.T, svc db.Service, org *models.Organization) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	check := newVersionedCheck(t, svc, org, "rename")

	versions := versionsOf(t, svc, check.UID)
	r.Len(versions, 1)
	r.Equal(1, versions[0].Version)
	r.Equal(models.CheckVersionStatusApplied, versions[0].Status)
	r.Equal(models.CheckVersionOriginSystem, versions[0].Origin, "a write without a change source is system")
	r.Nil(versions[0].ActorUserUID)

	name := "Renamed"
	r.NoError(svc.UpdateCheck(ctx, check.UID, &models.CheckUpdate{Name: &name}))

	versions = versionsOf(t, svc, check.UID)
	r.Len(versions, 2)
	r.Equal(2, versions[0].Version, "newest first")
	r.Equal("Renamed", versions[0].Snapshot["name"])

	// check.updated carries the version.
	events, err := svc.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: org.UID,
		CheckUID:        &check.UID,
		EventTypes:      []models.EventType{models.EventTypeCheckUpdated},
	})
	r.NoError(err)
	r.Len(events, 1)
	r.EqualValues(2, events[0].Payload["version"])

	// Writing the same name again records nothing.
	r.NoError(svc.UpdateCheck(ctx, check.UID, &models.CheckUpdate{Name: &name}))
	r.Len(versionsOf(t, svc, check.UID), 2)
}

func testVersionSecretOnlyWrite(t *testing.T, svc db.Service, org *models.Organization) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	check := models.NewCheck(org.UID, "pg-check", "postgresql")
	check.Config = models.JSONMap{"host": "db.acme.com", "password": "first"}
	r.NoError(svc.CreateCheck(ctx, check))
	r.Len(versionsOf(t, svc, check.UID), 1)

	// Encrypted side only (credmigrate, a re-key).
	envelope := `{"v":1}`
	keys := `["password"]`
	r.NoError(svc.UpdateCheck(ctx, check.UID, &models.CheckUpdate{
		ConfigPrivate: &envelope, ConfigPrivateKeys: &keys,
	}))

	// Plaintext config where only the secret key moved (no master key).
	config := models.JSONMap{"host": "db.acme.com", "password": "second"}
	r.NoError(svc.UpdateCheck(ctx, check.UID, &models.CheckUpdate{Config: &config}))

	versions := versionsOf(t, svc, check.UID)
	r.Len(versions, 1, "secret-only writes add no version")
	r.NotContains(versions[0].Snapshot["config"], "password")
}

func testVersionRuntimeOnlyWrite(t *testing.T, svc db.Service, org *models.Organization) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	check := newVersionedCheck(t, svc, org, "runtime")

	status := models.CheckStatusDown
	streak := 3
	now := time.Now()
	r.NoError(svc.UpdateCheck(ctx, check.UID, &models.CheckUpdate{
		Status: &status, StatusStreak: &streak, StatusChangedAt: &now,
		FirstFailureAt: &now, DegradedEvaluatedAt: &now,
	}))

	r.Len(versionsOf(t, svc, check.UID), 1, "runtime-only writes add no version")
}

func testVersionChangeSource(t *testing.T, svc db.Service, org *models.Organization) {
	t.Helper()

	r := require.New(t)

	user := models.NewUser(fmt.Sprintf("alice-%d@acme.com", time.Now().UnixNano()))
	r.NoError(svc.CreateUser(t.Context(), user))

	check := newVersionedCheck(t, svc, org, "source")

	ctx := dbctx.WithChangeSource(t.Context(), string(models.CheckVersionOriginUser), user.UID)
	dbctx.ChangeSourceFromContext(ctx).Reason = "because"

	period := timeutils.Duration(5 * time.Minute)
	r.NoError(svc.UpdateCheck(ctx, check.UID, &models.CheckUpdate{Period: &period}))

	latest, err := svc.GetLatestAppliedCheckVersion(t.Context(), check.UID)
	r.NoError(err)
	r.Equal(2, latest.Version)
	r.Equal(models.CheckVersionOriginUser, latest.Origin)
	r.NotNil(latest.ActorUserUID)
	r.Equal(user.UID, *latest.ActorUserUID)
	r.NotNil(latest.Reason)
	r.Equal("because", *latest.Reason)
	r.Equal("00:05:00", latest.Snapshot["period"])
}

func testVersionOneVersionPerChange(t *testing.T, svc db.Service, org *models.Organization) {
	t.Helper()

	r := require.New(t)

	check := newVersionedCheck(t, svc, org, "coalesce")

	label, err := svc.GetOrCreateLabel(t.Context(), org.UID, "env", "prod")
	r.NoError(err)

	ctx := dbctx.WithChangeSource(t.Context(), string(models.CheckVersionOriginAPI), "")

	name := "Coalesced"
	r.NoError(svc.UpdateCheck(ctx, check.UID, &models.CheckUpdate{Name: &name}))
	r.NoError(svc.SetCheckLabels(ctx, check.UID, []string{label.UID}))

	versions := versionsOf(t, svc, check.UID)
	r.Len(versions, 2, "one change, one version")
	r.Equal("Coalesced", versions[0].Snapshot["name"])
	r.Equal(map[string]any{"env": "prod"}, versions[0].Snapshot["labels"])
}

func testVersionLabels(t *testing.T, svc db.Service, org *models.Organization) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	check := newVersionedCheck(t, svc, org, "labels")

	label, err := svc.GetOrCreateLabel(ctx, org.UID, "team", "core")
	r.NoError(err)

	r.NoError(svc.SetCheckLabels(ctx, check.UID, []string{label.UID}))
	r.Len(versionsOf(t, svc, check.UID), 2)

	// Same labels again: nothing new.
	r.NoError(svc.SetCheckLabels(ctx, check.UID, []string{label.UID}))
	r.Len(versionsOf(t, svc, check.UID), 2)
}

func testVersionBaselineForOldCheck(t *testing.T, svc db.Service, org *models.Organization) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	check := newVersionedCheck(t, svc, org, "old")
	r.Nil(check.Name)

	// A check that predates the history has no version at all.
	_, err := svc.DB().NewDelete().Model((*models.CheckVersion)(nil)).
		Where("check_uid = ?", check.UID).Exec(ctx)
	r.NoError(err)

	name := "Edited"
	r.NoError(svc.UpdateCheck(ctx, check.UID, &models.CheckUpdate{Name: &name}))

	versions := versionsOf(t, svc, check.UID)
	r.Len(versions, 2)
	r.Empty(versions[1].Snapshot["name"], "v1 is the state before the first edit")
	r.Equal("old", versions[1].Snapshot["slug"])
	r.Equal("Edited", versions[0].Snapshot["name"])
}

func testVersionRetention(t *testing.T, svc db.Service, org *models.Organization) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	check := newVersionedCheck(t, svc, org, "retention")

	for i := range models.CheckVersionRetention {
		name := fmt.Sprintf("name-%d", i)
		r.NoError(svc.UpdateCheck(ctx, check.UID, &models.CheckUpdate{Name: &name}))
	}

	versions := versionsOf(t, svc, check.UID)
	r.Len(versions, models.CheckVersionRetention, "the 101st applied version pruned the oldest")
	r.Equal(models.CheckVersionRetention+1, versions[0].Version)
	r.Equal(2, versions[len(versions)-1].Version)
}

func testVersionPurgeCascades(t *testing.T, svc db.Service, org *models.Organization) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	check := newVersionedCheck(t, svc, org, "purge")
	r.Len(versionsOf(t, svc, check.UID), 1)

	r.NoError(svc.PurgeCheck(ctx, check.UID))
	r.Empty(versionsOf(t, svc, check.UID))
}

func proposeName(t *testing.T, svc db.Service, check *models.Check, name string, base int) *models.CheckVersion {
	t.Helper()

	latest, err := svc.GetLatestAppliedCheckVersion(t.Context(), check.UID)
	require.NoError(t, err)

	snapshot := models.JSONMap{}
	for key, value := range latest.Snapshot {
		snapshot[key] = value
	}

	snapshot["name"] = name

	row := models.NewCheckVersion(check.OrganizationUID, check.UID, snapshot, "")
	row.BaseVersion = &base
	row.Origin = models.CheckVersionOriginAIRepair
	require.NoError(t, svc.CreateCheckVersionProposal(t.Context(), row))

	return row
}

func testVersionProposalDecide(t *testing.T, svc db.Service, org *models.Organization) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	check := newVersionedCheck(t, svc, org, "proposal")

	proposal := proposeName(t, svc, check, "Proposed", 1)
	r.Equal(2, proposal.Version)
	r.Equal(models.CheckVersionStatusProposed, proposal.Status)
	r.NotEmpty(proposal.SnapshotHash)

	// A proposal is not the latest applied version.
	latest, err := svc.GetLatestAppliedCheckVersion(ctx, check.UID)
	r.NoError(err)
	r.Equal(1, latest.Version)

	r.NoError(svc.DecideCheckVersion(ctx, check.UID, 2, models.CheckVersionStatusRejected, ""))

	rejected, err := svc.GetCheckVersion(ctx, check.UID, 2)
	r.NoError(err)
	r.Equal(models.CheckVersionStatusRejected, rejected.Status)
	r.NotNil(rejected.DecidedAt)

	r.ErrorIs(svc.DecideCheckVersion(ctx, check.UID, 2, models.CheckVersionStatusRejected, ""),
		db.ErrCheckVersionNotProposed)

	_, err = svc.GetCheckVersion(ctx, check.UID, 42)
	r.ErrorIs(err, sql.ErrNoRows)

	// The next applied write numbers after the proposal.
	name := "Edited"
	r.NoError(svc.UpdateCheck(ctx, check.UID, &models.CheckUpdate{Name: &name}))

	latest, err = svc.GetLatestAppliedCheckVersion(ctx, check.UID)
	r.NoError(err)
	r.Equal(3, latest.Version)
}

func testVersionApprovalAmendsProposal(t *testing.T, svc db.Service, org *models.Organization) {
	t.Helper()

	r := require.New(t)

	check := newVersionedCheck(t, svc, org, "approve")
	proposal := proposeName(t, svc, check, "Approved", 1)

	// What the approve endpoint does: the write lands under a change source
	// pointing at the proposal row, which becomes the applied version.
	source := &dbctx.ChangeSource{Origin: string(models.CheckVersionOriginAIRepair)}
	source.SetRecordedVersion(check.UID, proposal.Version)
	ctx := dbctx.WithChange(t.Context(), source)

	name := "Approved"
	r.NoError(svc.UpdateCheck(ctx, check.UID, &models.CheckUpdate{Name: &name}))

	versions := versionsOf(t, svc, check.UID)
	r.Len(versions, 2)
	r.Equal(models.CheckVersionStatusApplied, versions[0].Status)
	r.Equal(proposal.Version, versions[0].Version)
}
