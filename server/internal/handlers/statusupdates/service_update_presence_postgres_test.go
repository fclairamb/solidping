package statusupdates_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/handlers/statusupdates"
)

// portPresencePG is distinct from every other embedded-Postgres port claimed
// in the repo (5435, 15434, 15436-15448, 15451-15485) so the suites can never
// collide if they ever run concurrently.
const portPresencePG = 15486

// pgPresenceFixture mirrors presenceFixture from
// service_update_presence_test.go but against a REAL Postgres backend —
// UpdateStatusUpdate's presence-aware clear/set logic runs the same bun
// full-model update on both dialects, but only a real Postgres run catches a
// dialect-specific regression (e.g. NULL-write behavior differing from
// SQLite). Self-skips under `-short` and on any embedded-startup error,
// mirroring the other embedded-postgres tests (see tunnel_postgres_test.go in
// the checks package).
type pgPresenceFixture struct {
	svc        *statusupdates.Service
	dbSvc      *postgres.Service
	org        *models.Organization
	userUID    string
	pageID     string
	sectionUID string
	checkUID   string
	baseline   statusupdates.StatusUpdateResponse
}

func newPGPresenceFixture(t *testing.T) *pgPresenceFixture {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portPresencePG,
		RunMode:  "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	svc := statusupdates.NewService(dbSvc)

	org := models.NewOrganization("presence-pg-org", "Presence PG Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("admin@presence-pg.example.com")
	user.Name = "Admin"
	r.NoError(dbSvc.CreateUser(ctx, user))

	page := models.NewStatusPage(org.UID, "Default Page", "default")
	r.NoError(dbSvc.CreateStatusPage(ctx, page))

	section := models.NewStatusPageSection(page.UID, "Core", "core", 0)
	r.NoError(dbSvc.CreateStatusPageSection(ctx, section))

	check := models.NewCheck(org.UID, "presence-check-pg", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	resource := models.NewStatusPageResource(section.UID, check.UID, 0)
	r.NoError(dbSvc.CreateStatusPageResource(ctx, resource))

	linkURL := "https://status.example.com/incident/1"
	createReq := &statusupdates.CreateStatusUpdateRequest{
		StatusPageUID: page.UID,
		SectionUID:    &section.UID,
		CheckUID:      &check.UID,
		LinkURL:       &linkURL,
		Kind:          "investigating",
		Title:         "Baseline",
		BodyMarkdown:  "Baseline body",
	}

	baseline, err := svc.CreateStatusUpdate(ctx, org.Slug, user.UID, createReq)
	r.NoError(err)

	// Positive control: the baseline really is non-null before any clear is
	// attempted.
	r.NotNil(baseline.SectionUID)
	r.NotNil(baseline.CheckUID)
	r.NotNil(baseline.LinkURL)

	return &pgPresenceFixture{
		svc:        svc,
		dbSvc:      dbSvc,
		org:        org,
		userUID:    user.UID,
		pageID:     page.UID,
		sectionUID: section.UID,
		checkUID:   check.UID,
		baseline:   baseline,
	}
}

func (f *pgPresenceFixture) update(
	ctx context.Context, t *testing.T, req *statusupdates.UpdateStatusUpdateRequest,
) statusupdates.StatusUpdateResponse {
	t.Helper()

	updated, err := f.svc.UpdateStatusUpdate(ctx, f.org.Slug, f.baseline.UID, f.userUID, req)
	require.NoError(t, err)

	return updated
}

// TestUpdateStatusUpdate_Postgres_PresenceSemantics covers the
// absent/null/value/"" matrix for all four nullable fields against a real
// Postgres backend in one pass — a single embedded instance, one test
// function so `t.Parallel()` never races two Start() calls onto the same
// fixed port. SQLite carries the exhaustive per-field/per-case matrix
// already (service_update_presence_test.go); this is the dialect check.
func TestUpdateStatusUpdate_Postgres_PresenceSemantics(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newPGPresenceFixture(t)
	ctx := t.Context()

	// Absent: a PATCH that only touches title leaves every nullable field
	// (and its siblings) untouched.
	newTitle := "Still investigating"
	afterAbsent := f.update(ctx, t, &statusupdates.UpdateStatusUpdateRequest{Title: &newTitle})
	r.NotNil(afterAbsent.SectionUID)
	r.Equal(f.sectionUID, *afterAbsent.SectionUID)
	r.NotNil(afterAbsent.CheckUID)
	r.Equal(f.checkUID, *afterAbsent.CheckUID)
	r.NotNil(afterAbsent.LinkURL)
	r.Equal(*f.baseline.LinkURL, *afterAbsent.LinkURL)

	// Null: clearing sectionUid must not clear checkUid (no implicit
	// cross-clear), and must not touch linkUrl either.
	afterSectionClear := f.update(ctx, t,
		&statusupdates.UpdateStatusUpdateRequest{SectionUIDSet: true, SectionUID: nil})
	r.Nil(afterSectionClear.SectionUID)
	r.NotNil(afterSectionClear.CheckUID)
	r.Equal(f.checkUID, *afterSectionClear.CheckUID)
	r.NotNil(afterSectionClear.LinkURL)

	// Null: clearing checkUid and linkUrl together.
	afterCheckAndLinkClear := f.update(ctx, t, &statusupdates.UpdateStatusUpdateRequest{
		CheckUIDSet: true, CheckUID: nil,
		LinkURLSet: true, LinkURL: nil,
	})
	r.Nil(afterCheckAndLinkClear.CheckUID)
	r.Nil(afterCheckAndLinkClear.LinkURL)
	// sectionUid was already cleared above and must stay cleared (not
	// resurrected by an unrelated PATCH).
	r.Nil(afterCheckAndLinkClear.SectionUID)

	// Value: setting sectionUid/checkUid/linkUrl back validates and persists.
	newLinkURL := "https://status.example.com/incident/2"
	afterSet := f.update(ctx, t, &statusupdates.UpdateStatusUpdateRequest{
		SectionUIDSet: true, SectionUID: &f.sectionUID,
		CheckUIDSet: true, CheckUID: &f.checkUID,
		LinkURLSet: true, LinkURL: &newLinkURL,
	})
	r.NotNil(afterSet.SectionUID)
	r.Equal(f.sectionUID, *afterSet.SectionUID)
	r.NotNil(afterSet.CheckUID)
	r.Equal(f.checkUID, *afterSet.CheckUID)
	r.NotNil(afterSet.LinkURL)
	r.Equal(newLinkURL, *afterSet.LinkURL)

	// "" is never a legal UID: rejected for the three UID fields, but clears
	// linkUrl (a browser input yields "", not null).
	empty := ""
	_, err := f.svc.UpdateStatusUpdate(ctx, f.org.Slug, f.baseline.UID, f.userUID,
		&statusupdates.UpdateStatusUpdateRequest{SectionUIDSet: true, SectionUID: &empty})
	r.ErrorIs(err, statusupdates.ErrSectionUIDRequired)

	_, err = f.svc.UpdateStatusUpdate(ctx, f.org.Slug, f.baseline.UID, f.userUID,
		&statusupdates.UpdateStatusUpdateRequest{CheckUIDSet: true, CheckUID: &empty})
	r.ErrorIs(err, statusupdates.ErrCheckUIDRequired)

	_, err = f.svc.UpdateStatusUpdate(ctx, f.org.Slug, f.baseline.UID, f.userUID,
		&statusupdates.UpdateStatusUpdateRequest{IncidentUIDSet: true, IncidentUID: &empty})
	r.ErrorIs(err, statusupdates.ErrIncidentUIDRequired)

	afterEmptyLinkURL, err := f.svc.UpdateStatusUpdate(ctx, f.org.Slug, f.baseline.UID, f.userUID,
		&statusupdates.UpdateStatusUpdateRequest{LinkURLSet: true, LinkURL: &empty})
	r.NoError(err)
	r.Nil(afterEmptyLinkURL.LinkURL)
}
