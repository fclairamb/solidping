package statusupdates_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/statusupdates"
)

// presenceFixture wires a status page with a section+check pair (so
// sectionUid/checkUid have something real to validate against) and a baseline
// status update that references section, check and a link URL — the non-null
// starting point every positive control needs.
type presenceFixture struct {
	*testSetup
	sectionUID string
	checkUID   string
	baseline   statusupdates.StatusUpdateResponse
}

func newPresenceFixture(t *testing.T) *presenceFixture {
	t.Helper()

	r := require.New(t)
	s := newTestSetup(t)
	ctx := t.Context()

	section := models.NewStatusPageSection(s.pageID, "Core", "core", 0)
	r.NoError(s.dbSvc.CreateStatusPageSection(ctx, section))

	check := models.NewCheck(s.org.UID, "presence-check", "http")
	r.NoError(s.dbSvc.CreateCheck(ctx, check))

	resource := models.NewStatusPageResource(section.UID, check.UID, 0)
	r.NoError(s.dbSvc.CreateStatusPageResource(ctx, resource))

	linkURL := "https://status.example.com/incident/1"
	createReq := makeCreate(s.pageID, "investigating", "Baseline", "Baseline body")
	createReq.SectionUID = &section.UID
	createReq.CheckUID = &check.UID
	createReq.LinkURL = &linkURL

	baseline, err := s.svc.CreateStatusUpdate(ctx, s.org.Slug, s.user.UID, createReq)
	r.NoError(err)

	// Positive control: the baseline really is non-null before any clear is
	// attempted, otherwise "clear" tests below would trivially pass.
	r.NotNil(baseline.SectionUID)
	r.Equal(section.UID, *baseline.SectionUID)
	r.NotNil(baseline.CheckUID)
	r.Equal(check.UID, *baseline.CheckUID)
	r.NotNil(baseline.LinkURL)
	r.Equal(linkURL, *baseline.LinkURL)

	return &presenceFixture{
		testSetup:  s,
		sectionUID: section.UID,
		checkUID:   check.UID,
		baseline:   baseline,
	}
}

// TestUpdateSectionUID_Absent_LeavesUnchanged: a PATCH that never mentions
// sectionUid must leave it (and its sibling checkUid) exactly as they were.
func TestUpdateSectionUID_Absent_LeavesUnchanged(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newPresenceFixture(t)

	newTitle := "Still investigating"
	updated, err := f.svc.UpdateStatusUpdate(t.Context(), f.org.Slug, f.baseline.UID, f.user.UID,
		&statusupdates.UpdateStatusUpdateRequest{Title: &newTitle})
	r.NoError(err)

	r.NotNil(updated.SectionUID)
	r.Equal(f.sectionUID, *updated.SectionUID)
	// Sibling untouched too.
	r.NotNil(updated.CheckUID)
	r.Equal(f.checkUID, *updated.CheckUID)
}

// TestUpdateSectionUID_Null_Clears: an explicit null clears sectionUid without
// touching checkUid (no implicit cross-clearing).
func TestUpdateSectionUID_Null_Clears(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newPresenceFixture(t)

	req := &statusupdates.UpdateStatusUpdateRequest{SectionUIDSet: true, SectionUID: nil}
	updated, err := f.svc.UpdateStatusUpdate(t.Context(), f.org.Slug, f.baseline.UID, f.user.UID, req)
	r.NoError(err)

	r.Nil(updated.SectionUID)
	// Sibling field must survive the clear untouched.
	r.NotNil(updated.CheckUID)
	r.Equal(f.checkUID, *updated.CheckUID)
}

// TestUpdateSectionUID_Value_Set: a non-empty sectionUid is validated (must
// belong to the page) and set.
func TestUpdateSectionUID_Value_Set(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newPresenceFixture(t)
	ctx := t.Context()

	other := models.NewStatusPageSection(f.pageID, "Other", "other", 1)
	r.NoError(f.dbSvc.CreateStatusPageSection(ctx, other))

	req := &statusupdates.UpdateStatusUpdateRequest{SectionUIDSet: true, SectionUID: &other.UID}
	updated, err := f.svc.UpdateStatusUpdate(ctx, f.org.Slug, f.baseline.UID, f.user.UID, req)
	r.NoError(err)

	r.NotNil(updated.SectionUID)
	r.Equal(other.UID, *updated.SectionUID)
}

// TestUpdateSectionUID_Empty_ValidationError: "" is never a legal UID.
func TestUpdateSectionUID_Empty_ValidationError(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newPresenceFixture(t)

	empty := ""
	req := &statusupdates.UpdateStatusUpdateRequest{SectionUIDSet: true, SectionUID: &empty}
	_, err := f.svc.UpdateStatusUpdate(t.Context(), f.org.Slug, f.baseline.UID, f.user.UID, req)
	r.ErrorIs(err, statusupdates.ErrSectionUIDRequired)
}

// TestUpdateSectionUID_UnknownSection_NotFound: a sectionUid that doesn't
// belong to the page is rejected, mirroring create-time validation.
func TestUpdateSectionUID_UnknownSection_NotFound(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newPresenceFixture(t)

	bogus := "00000000-0000-0000-0000-000000000000"
	req := &statusupdates.UpdateStatusUpdateRequest{SectionUIDSet: true, SectionUID: &bogus}
	_, err := f.svc.UpdateStatusUpdate(t.Context(), f.org.Slug, f.baseline.UID, f.user.UID, req)
	r.ErrorIs(err, statusupdates.ErrSectionNotFound)
}

// TestUpdateCheckUID_Absent_LeavesUnchanged mirrors the section case for checkUid.
func TestUpdateCheckUID_Absent_LeavesUnchanged(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newPresenceFixture(t)

	newTitle := "Still investigating"
	updated, err := f.svc.UpdateStatusUpdate(t.Context(), f.org.Slug, f.baseline.UID, f.user.UID,
		&statusupdates.UpdateStatusUpdateRequest{Title: &newTitle})
	r.NoError(err)

	r.NotNil(updated.CheckUID)
	r.Equal(f.checkUID, *updated.CheckUID)
}

// TestUpdateCheckUID_Null_Clears clears checkUid without touching sectionUid.
func TestUpdateCheckUID_Null_Clears(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newPresenceFixture(t)

	req := &statusupdates.UpdateStatusUpdateRequest{CheckUIDSet: true, CheckUID: nil}
	updated, err := f.svc.UpdateStatusUpdate(t.Context(), f.org.Slug, f.baseline.UID, f.user.UID, req)
	r.NoError(err)

	r.Nil(updated.CheckUID)
	// Sibling field must survive the clear untouched.
	r.NotNil(updated.SectionUID)
	r.Equal(f.sectionUID, *updated.SectionUID)
}

// TestUpdateCheckUID_Value_Set validates and sets a new checkUid.
func TestUpdateCheckUID_Value_Set(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newPresenceFixture(t)
	ctx := t.Context()

	otherCheck := models.NewCheck(f.org.UID, "presence-check-2", "http")
	r.NoError(f.dbSvc.CreateCheck(ctx, otherCheck))
	resource := models.NewStatusPageResource(f.sectionUID, otherCheck.UID, 1)
	r.NoError(f.dbSvc.CreateStatusPageResource(ctx, resource))

	req := &statusupdates.UpdateStatusUpdateRequest{CheckUIDSet: true, CheckUID: &otherCheck.UID}
	updated, err := f.svc.UpdateStatusUpdate(ctx, f.org.Slug, f.baseline.UID, f.user.UID, req)
	r.NoError(err)

	r.NotNil(updated.CheckUID)
	r.Equal(otherCheck.UID, *updated.CheckUID)
}

// TestUpdateCheckUID_Empty_ValidationError: "" is never a legal UID.
func TestUpdateCheckUID_Empty_ValidationError(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newPresenceFixture(t)

	empty := ""
	req := &statusupdates.UpdateStatusUpdateRequest{CheckUIDSet: true, CheckUID: &empty}
	_, err := f.svc.UpdateStatusUpdate(t.Context(), f.org.Slug, f.baseline.UID, f.user.UID, req)
	r.ErrorIs(err, statusupdates.ErrCheckUIDRequired)
}

// TestUpdateCheckUID_NotOnPage_NotFound: a check that isn't a resource of the
// page is rejected.
func TestUpdateCheckUID_NotOnPage_NotFound(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newPresenceFixture(t)
	ctx := t.Context()

	unlisted := models.NewCheck(f.org.UID, "unlisted-check", "http")
	r.NoError(f.dbSvc.CreateCheck(ctx, unlisted))

	req := &statusupdates.UpdateStatusUpdateRequest{CheckUIDSet: true, CheckUID: &unlisted.UID}
	_, err := f.svc.UpdateStatusUpdate(ctx, f.org.Slug, f.baseline.UID, f.user.UID, req)
	r.ErrorIs(err, statusupdates.ErrCheckNotFound)
}

// TestUpdateIncidentUID_Absent_LeavesUnchanged mirrors the section/check cases
// for incidentUid.
func TestUpdateIncidentUID_Absent_LeavesUnchanged(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newTestSetup(t)
	ctx := t.Context()

	check := models.NewCheck(s.org.UID, "incident-check-1", "http")
	r.NoError(s.dbSvc.CreateCheck(ctx, check))
	incident := models.NewIncident(s.org.UID, check.UID, time.Now(), "Outage")
	r.NoError(s.dbSvc.CreateIncident(ctx, incident))

	createReq := makeCreate(s.pageID, "investigating", "Baseline", "Baseline body")
	createReq.IncidentUID = &incident.UID
	baseline, err := s.svc.CreateStatusUpdate(ctx, s.org.Slug, s.user.UID, createReq)
	r.NoError(err)
	r.NotNil(baseline.IncidentUID)
	r.Equal(incident.UID, *baseline.IncidentUID)

	newTitle := "Still investigating"
	updated, err := s.svc.UpdateStatusUpdate(ctx, s.org.Slug, baseline.UID, s.user.UID,
		&statusupdates.UpdateStatusUpdateRequest{Title: &newTitle})
	r.NoError(err)

	r.NotNil(updated.IncidentUID)
	r.Equal(incident.UID, *updated.IncidentUID)
}

// TestUpdateIncidentUID_Null_Clears clears incidentUid via an explicit null.
func TestUpdateIncidentUID_Null_Clears(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newTestSetup(t)
	ctx := t.Context()

	check := models.NewCheck(s.org.UID, "incident-check-2", "http")
	r.NoError(s.dbSvc.CreateCheck(ctx, check))
	incident := models.NewIncident(s.org.UID, check.UID, time.Now(), "Outage")
	r.NoError(s.dbSvc.CreateIncident(ctx, incident))

	createReq := makeCreate(s.pageID, "investigating", "Baseline", "Baseline body")
	createReq.IncidentUID = &incident.UID
	baseline, err := s.svc.CreateStatusUpdate(ctx, s.org.Slug, s.user.UID, createReq)
	r.NoError(err)
	r.NotNil(baseline.IncidentUID) // positive control

	req := &statusupdates.UpdateStatusUpdateRequest{IncidentUIDSet: true, IncidentUID: nil}
	updated, err := s.svc.UpdateStatusUpdate(ctx, s.org.Slug, baseline.UID, s.user.UID, req)
	r.NoError(err)
	r.Nil(updated.IncidentUID)
	// Sibling: title survives untouched.
	r.Equal("Baseline", updated.Title)
}

// TestUpdateIncidentUID_Empty_ValidationError: "" is never a legal UID.
func TestUpdateIncidentUID_Empty_ValidationError(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newPresenceFixture(t)

	empty := ""
	req := &statusupdates.UpdateStatusUpdateRequest{IncidentUIDSet: true, IncidentUID: &empty}
	_, err := f.svc.UpdateStatusUpdate(t.Context(), f.org.Slug, f.baseline.UID, f.user.UID, req)
	r.ErrorIs(err, statusupdates.ErrIncidentUIDRequired)
}

// TestUpdateLinkURL_Absent_LeavesUnchanged: an omitted linkUrl key leaves the
// stored URL untouched.
func TestUpdateLinkURL_Absent_LeavesUnchanged(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newPresenceFixture(t)

	newTitle := "Still investigating"
	updated, err := f.svc.UpdateStatusUpdate(t.Context(), f.org.Slug, f.baseline.UID, f.user.UID,
		&statusupdates.UpdateStatusUpdateRequest{Title: &newTitle})
	r.NoError(err)

	r.NotNil(updated.LinkURL)
	r.Equal(*f.baseline.LinkURL, *updated.LinkURL)
}

// TestUpdateLinkURL_Null_Clears clears linkUrl via an explicit null.
func TestUpdateLinkURL_Null_Clears(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newPresenceFixture(t)

	req := &statusupdates.UpdateStatusUpdateRequest{LinkURLSet: true, LinkURL: nil}
	updated, err := f.svc.UpdateStatusUpdate(t.Context(), f.org.Slug, f.baseline.UID, f.user.UID, req)
	r.NoError(err)

	r.Nil(updated.LinkURL)
	// Sibling untouched.
	r.NotNil(updated.SectionUID)
	r.Equal(f.sectionUID, *updated.SectionUID)
}

// TestUpdateLinkURL_Empty_Clears: unlike the UID fields, "" clears linkUrl
// rather than erroring — a browser input yields "", not null.
func TestUpdateLinkURL_Empty_Clears(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newPresenceFixture(t)

	empty := ""
	req := &statusupdates.UpdateStatusUpdateRequest{LinkURLSet: true, LinkURL: &empty}
	updated, err := f.svc.UpdateStatusUpdate(t.Context(), f.org.Slug, f.baseline.UID, f.user.UID, req)
	r.NoError(err)
	r.Nil(updated.LinkURL)
}

// TestUpdateLinkURL_Value_Set validates and sets a new linkUrl.
func TestUpdateLinkURL_Value_Set(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newPresenceFixture(t)

	newURL := "https://status.example.com/incident/2"
	req := &statusupdates.UpdateStatusUpdateRequest{LinkURLSet: true, LinkURL: &newURL}
	updated, err := f.svc.UpdateStatusUpdate(t.Context(), f.org.Slug, f.baseline.UID, f.user.UID, req)
	r.NoError(err)

	r.NotNil(updated.LinkURL)
	r.Equal(newURL, *updated.LinkURL)
}

// TestUpdateLinkURL_InvalidValue_Rejected: a set-a-value linkUrl is still
// validated as http/https.
func TestUpdateLinkURL_InvalidValue_Rejected(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newPresenceFixture(t)

	bad := "ftp://not-valid.example.com"
	req := &statusupdates.UpdateStatusUpdateRequest{LinkURLSet: true, LinkURL: &bad}
	_, err := f.svc.UpdateStatusUpdate(t.Context(), f.org.Slug, f.baseline.UID, f.user.UID, req)
	r.ErrorIs(err, statusupdates.ErrInvalidLinkURL)
}
