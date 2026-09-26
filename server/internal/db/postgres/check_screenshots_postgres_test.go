package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Ports distinct from every other embedded-Postgres test in the repo.
const (
	portCheckScreenshotsPlan      = 15611
	portCheckScreenshotsSemantics = 15612
	portOrphanAttachments         = 15614
	portAdmitFixedWindows         = 15615
)

// TestCheckScreenshotListingUsesIndex_Postgres is the Postgres plan regression
// for spec 2026-09-25-34: the per-check listing rides files_org_check_uid_idx.
// The control respells the checkUid expression equivalently
// (`details->'checkUid' #>> '{}'`), which no expression index matches, and must
// not use it — so the main assertion cannot pass on a fixture the planner
// would have indexed anyway.
//
//nolint:paralleltest // embedded-postgres tests run sequentially in this package
func TestCheckScreenshotListingUsesIndex_Postgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)

	s := newTier1ServicePG(t, portCheckScreenshotsPlan)

	org := models.NewOrganization("shot-plan-org", "Shot Plan Org")
	r.NoError(s.CreateOrganization(ctx, org))

	// 400 checks × 50 screenshots: the target check is 0.25 % of the org's
	// attachments, which is what makes an index the planner's real choice.
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO files (uid, organization_uid, name, mime_type, size, file_uri, topic, details, created_at)
		SELECT gen_random_uuid(), ?, 'shot.png', 'image/png', 10, 'file://x',
		       'incidents/' || gen_random_uuid()::text || '/screenshot',
		       jsonb_build_object('checkUid', 'check-' || (i % 400)),
		       now() - (i * interval '1 minute')
		  FROM generate_series(1, 20000) AS i`, org.UID)
	r.NoError(err)

	_, err = s.db.ExecContext(ctx, "ANALYZE files")
	r.NoError(err)

	var files []*models.File

	sql := s.checkScreenshotFilesQuery(&files, org.UID, "check-7", 5).String()
	plan := explainSQL(ctx, t, s, sql)
	r.Contains(plan, "files_org_check_uid_idx", "the listing must ride the per-check index:\n%s", plan)
	r.NotContains(plan, "Seq Scan on files", plan)

	control := strings.Replace(sql, "details->>'checkUid'", "details->'checkUid' #>> '{}'", 1)
	r.NotEqual(sql, control, "the control must actually respell the expression")

	controlPlan := explainSQL(ctx, t, s, control)
	r.NotContains(controlPlan, "files_org_check_uid_idx",
		"control: a respelled expression cannot use the index:\n%s", controlPlan)

	got, err := s.ListCheckScreenshotFiles(ctx, org.UID, "check-7", 5)
	r.NoError(err)
	r.Len(got, 5)
}

// TestListCheckScreenshotFiles_Postgres pins what the listing returns on
// Postgres (the SQLite twin is exercised through the attachments service):
// incident and check-scoped captures of the check, newest first, `limit`
// honored, and nothing from another check, another org, another kind, or a
// soft-deleted row.
//
//nolint:paralleltest // embedded-postgres tests run sequentially in this package
func TestListCheckScreenshotFiles_Postgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)

	s := newTier1ServicePG(t, portCheckScreenshotsSemantics)

	org := models.NewOrganization("shot-sem-org", "Shot Semantics Org")
	r.NoError(s.CreateOrganization(ctx, org))

	otherOrg := models.NewOrganization("shot-sem-other", "Other Org")
	r.NoError(s.CreateOrganization(ctx, otherOrg))

	checkUID := uuid.New().String()
	base := time.Now().Add(-time.Hour)

	write := func(orgUID, topic, forCheck string, age int) *models.File {
		file := models.NewFile(orgUID, "shot.png", "image/png", "file://x", 10, nil)
		file.Topic = &topic
		file.Details = models.JSONMap{"checkUid": forCheck}
		file.CreatedAt = base.Add(time.Duration(age) * time.Minute)
		r.NoError(s.CreateFile(ctx, file))

		return file
	}

	oldest := write(org.UID, "incidents/"+uuid.New().String()+"/screenshot", checkUID, 1)
	checkScoped := write(org.UID, "checks/"+checkUID+"/screenshot", checkUID, 2)
	newest := write(org.UID, "incidents/"+uuid.New().String()+"/screenshot", checkUID, 3)

	// Noise that must never come back.
	write(org.UID, "incidents/"+uuid.New().String()+"/screenshot", uuid.New().String(), 4)
	write(org.UID, "incidents/"+uuid.New().String()+"/traceroute", checkUID, 5)
	write(otherOrg.UID, "incidents/"+uuid.New().String()+"/screenshot", checkUID, 6)
	deleted := write(org.UID, "checks/"+checkUID+"/screenshot", checkUID, 7)
	r.NoError(s.DeleteFile(ctx, org.UID, deleted.UID))

	got, err := s.ListCheckScreenshotFiles(ctx, org.UID, checkUID, 10)
	r.NoError(err)
	r.Len(got, 3)
	r.Equal([]string{newest.UID, checkScoped.UID, oldest.UID}, []string{got[0].UID, got[1].UID, got[2].UID})

	limited, err := s.ListCheckScreenshotFiles(ctx, org.UID, checkUID, 1)
	r.NoError(err)
	r.Len(limited, 1)
	r.Equal(newest.UID, limited[0].UID)

	none, err := s.ListCheckScreenshotFiles(ctx, org.UID, uuid.New().String(), 5)
	r.NoError(err)
	r.Empty(none)
}

// TestListOrphanAttachments_Postgres pins the orphan sweep's anti-join on
// Postgres (the SQLite twin runs through internal/jobs/jobtypes): only
// attachments whose entity is missing, soft-deleted or in another org come
// back — never a live entity's, never a malformed topic, never a fresh row.
//
//nolint:paralleltest // embedded-postgres tests run sequentially in this package
func TestListOrphanAttachments_Postgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)

	s := newTier1ServicePG(t, portOrphanAttachments)

	org := models.NewOrganization("orphan-org", "Orphan Org")
	r.NoError(s.CreateOrganization(ctx, org))

	other := models.NewOrganization("orphan-other", "Other Org")
	r.NoError(s.CreateOrganization(ctx, other))

	liveCheck := models.NewCheck(org.UID, "live", "browser")
	r.NoError(s.CreateCheck(ctx, liveCheck))

	deletedCheck := models.NewCheck(org.UID, "gone", "browser")
	r.NoError(s.CreateCheck(ctx, deletedCheck))
	r.NoError(s.DeleteCheck(ctx, deletedCheck.UID))

	liveIncident := models.NewIncident(org.UID, liveCheck.UID, time.Now(), "live is down")
	r.NoError(s.CreateIncident(ctx, liveIncident))

	old := time.Now().Add(-48 * time.Hour)
	write := func(orgUID, topic string, createdAt time.Time) *models.File {
		file := models.NewFile(orgUID, "shot.png", "image/png", "file://x", 1, nil)
		file.Topic = &topic
		file.CreatedAt = createdAt
		r.NoError(s.CreateFile(ctx, file))

		return file
	}

	write(org.UID, "checks/"+liveCheck.UID+"/screenshot", old)
	write(org.UID, "incidents/"+liveIncident.UID+"/screenshot", old)
	write(org.UID, "checks/not-a-uuid/screenshot", old)
	write(org.UID, "checks/"+uuid.New().String()+"/screenshot", time.Now())

	deletedOrphan := write(org.UID, "checks/"+deletedCheck.UID+"/screenshot", old)
	missingOrphan := write(org.UID, "checks/"+uuid.New().String()+"/screenshot", old)
	foreignOrphan := write(other.UID, "checks/"+liveCheck.UID+"/screenshot", old)
	incidentOrphan := write(org.UID, "incidents/"+uuid.New().String()+"/screenshot", old)

	before := time.Now().Add(-time.Hour)

	checkOrphans, err := s.ListOrphanAttachments(ctx, "checks", before, 100)
	r.NoError(err)

	got := make([]string, 0, len(checkOrphans))
	for _, file := range checkOrphans {
		got = append(got, file.UID)
	}

	r.ElementsMatch([]string{deletedOrphan.UID, missingOrphan.UID, foreignOrphan.UID}, got)

	incidentOrphans, err := s.ListOrphanAttachments(ctx, "incidents", before, 100)
	r.NoError(err)
	r.Len(incidentOrphans, 1)
	r.Equal(incidentOrphan.UID, incidentOrphans[0].UID)

	_, err = s.ListOrphanAttachments(ctx, "organizations", before, 100)
	r.Error(err, "an entity with no sweep is refused, never spliced into SQL")
}

// TestAdmitFixedWindows_Postgres pins the admission semantics on Postgres (the
// concurrency guarantee lives in internal/handlers/checkscreenshots): all
// windows count or none does, the first refusing window is reported with the
// time until it reopens, and an elapsed window starts over.
//
//nolint:paralleltest // embedded-postgres tests run sequentially in this package
func TestAdmitFixedWindows_Postgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)

	s := newTier1ServicePG(t, portAdmitFixedWindows)

	org := models.NewOrganization("admit-org", "Admit Org")
	r.NoError(s.CreateOrganization(ctx, org))

	now := time.Now()
	windows := []models.FixedWindow{
		{Key: "admit.small", Limit: 1, Window: time.Minute},
		{Key: "admit.big", Limit: 5, Window: time.Hour},
	}

	refused, _, err := s.AdmitFixedWindows(ctx, org.UID, windows, now)
	r.NoError(err)
	r.Equal(-1, refused)

	refused, retryAfter, err := s.AdmitFixedWindows(ctx, org.UID, windows, now.Add(10*time.Second))
	r.NoError(err)
	r.Equal(0, refused, "the small window refuses")
	r.Equal(50*time.Second, retryAfter.Round(time.Second))

	big, err := s.GetStateEntry(ctx, &org.UID, "admit.big")
	r.NoError(err)
	r.InDelta(1, (*big.Value)["count"], 0, "a refused admission counts against no window")

	refused, _, err = s.AdmitFixedWindows(ctx, org.UID, windows, now.Add(61*time.Second))
	r.NoError(err)
	r.Equal(-1, refused, "the small window reopened")

	big, err = s.GetStateEntry(ctx, &org.UID, "admit.big")
	r.NoError(err)
	r.InDelta(2, (*big.Value)["count"], 0)
}
