package statusupdates_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/statusupdates"
)

// captureNotifier records fan-out events. Dispatch runs in a goroutine, so the
// channel lets the test wait for the event deterministically.
type captureNotifier struct {
	events chan statusupdates.SubscriberUpdateEvent
}

func newCaptureNotifier() *captureNotifier {
	return &captureNotifier{events: make(chan statusupdates.SubscriberUpdateEvent, 8)}
}

func (c *captureNotifier) NotifyStatusUpdate(_ context.Context, ev statusupdates.SubscriberUpdateEvent) {
	c.events <- ev
}

func (c *captureNotifier) wait(t *testing.T) statusupdates.SubscriberUpdateEvent {
	t.Helper()

	select {
	case ev := <-c.events:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fan-out event")

		return statusupdates.SubscriberUpdateEvent{}
	}
}

// TestFanOutDispatchesOnCreate verifies CreateStatusUpdate triggers the
// subscriber notifier with the page + update details.
func TestFanOutDispatchesOnCreate(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newTestSetup(t)
	notifier := newCaptureNotifier()
	s.svc.SetSubscriberNotifier(notifier)

	_, err := s.svc.CreateStatusUpdate(t.Context(), s.org.Slug, s.user.UID,
		makeCreate(s.pageID, "info", "Heads up", "Body"))
	r.NoError(err)

	ev := notifier.wait(t)
	r.Equal(s.pageID, ev.StatusPageUID)
	r.Equal("info", ev.Kind)
	r.Equal("Heads up", ev.Title)
	r.Equal("Default Page", ev.PageName)
}

// TestFanOutResolvedKind verifies the resolved kind is propagated so the
// notifier can pick the resolved template.
func TestFanOutResolvedKind(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newTestSetup(t)
	notifier := newCaptureNotifier()
	s.svc.SetSubscriberNotifier(notifier)

	_, err := s.svc.CreateStatusUpdate(t.Context(), s.org.Slug, s.user.UID,
		makeCreate(s.pageID, "resolved", "All clear", "Fixed"))
	r.NoError(err)

	ev := notifier.wait(t)
	r.Equal("resolved", ev.Kind)
}

// TestNoNotifierIsSafe verifies CreateStatusUpdate works without a notifier.
func TestNoNotifierIsSafe(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newTestSetup(t)

	_, err := s.svc.CreateStatusUpdate(t.Context(), s.org.Slug, s.user.UID,
		makeCreate(s.pageID, "info", "No notifier", "Body"))
	r.NoError(err)
}

// testSetup spins up an in-memory sqlite db and all required fixtures.
type testSetup struct {
	svc    *statusupdates.Service
	dbSvc  *sqlite.Service
	org    *models.Organization
	org2   *models.Organization
	page   *models.StatusPage
	user   *models.User
	pageID string // convenience shortcut
}

func newTestSetup(t *testing.T) *testSetup {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	svc := statusupdates.NewService(dbSvc)

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	org2 := models.NewOrganization("other", "Other")
	r.NoError(dbSvc.CreateOrganization(ctx, org2))

	user := models.NewUser("admin@acme.com")
	user.Name = "Admin"
	r.NoError(dbSvc.CreateUser(ctx, user))

	page := models.NewStatusPage(org.UID, "Default Page", "default")
	r.NoError(dbSvc.CreateStatusPage(ctx, page))

	return &testSetup{
		svc:    svc,
		dbSvc:  dbSvc,
		org:    org,
		org2:   org2,
		page:   page,
		user:   user,
		pageID: page.UID,
	}
}

func makeCreate(pageUID, kind, title, body string) *statusupdates.CreateStatusUpdateRequest {
	return &statusupdates.CreateStatusUpdateRequest{
		StatusPageUID: pageUID,
		Kind:          kind,
		Title:         title,
		BodyMarkdown:  body,
	}
}

// TestCRUDRoundTrip verifies create → get → update → delete.
func TestCRUDRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newTestSetup(t)
	ctx := t.Context()

	// Create
	resp, err := s.svc.CreateStatusUpdate(ctx, s.org.Slug, s.user.UID,
		makeCreate(s.pageID, "investigating", "We are looking at it", "Service is down"))
	r.NoError(err)
	r.Equal("investigating", resp.Kind)
	r.Equal("We are looking at it", resp.Title)
	r.Equal(s.pageID, resp.StatusPageUID)

	// Get
	got, err := s.svc.GetStatusUpdate(ctx, s.org.Slug, resp.UID)
	r.NoError(err)
	r.Equal(resp.UID, got.UID)
	r.Equal("We are looking at it", got.Title)

	// Update
	newTitle := "Fixed!"
	updated, err := s.svc.UpdateStatusUpdate(ctx, s.org.Slug, resp.UID, s.user.UID,
		&statusupdates.UpdateStatusUpdateRequest{Title: &newTitle})
	r.NoError(err)
	r.Equal("Fixed!", updated.Title)

	// Soft delete
	r.NoError(s.svc.DeleteStatusUpdate(ctx, s.org.Slug, resp.UID, s.user.UID))

	// Get after delete → not found
	_, err = s.svc.GetStatusUpdate(ctx, s.org.Slug, resp.UID)
	r.ErrorIs(err, statusupdates.ErrStatusUpdateNotFound)
}

// TestInvalidKindRejects checks that unknown kind values are rejected.
func TestInvalidKindRejects(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newTestSetup(t)

	_, err := s.svc.CreateStatusUpdate(t.Context(), s.org.Slug, s.user.UID,
		makeCreate(s.pageID, "unicorn", "title", "body"))
	r.ErrorIs(err, statusupdates.ErrInvalidKind)
}

// TestTitleValidation checks empty and too-long title validation.
func TestTitleValidation(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newTestSetup(t)
	ctx := t.Context()

	// Empty title
	_, err := s.svc.CreateStatusUpdate(ctx, s.org.Slug, s.user.UID,
		makeCreate(s.pageID, "info", "", "body"))
	r.ErrorIs(err, statusupdates.ErrTitleRequired)

	// Too long title (201 chars)
	longTitle := strings.Repeat("x", 201)

	_, err = s.svc.CreateStatusUpdate(ctx, s.org.Slug, s.user.UID,
		makeCreate(s.pageID, "info", longTitle, "body"))
	r.ErrorIs(err, statusupdates.ErrTitleTooLong)
}

// TestBodyValidation checks empty and too-long body validation.
func TestBodyValidation(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newTestSetup(t)
	ctx := t.Context()

	// Empty body
	_, err := s.svc.CreateStatusUpdate(ctx, s.org.Slug, s.user.UID,
		makeCreate(s.pageID, "info", "title", ""))
	r.ErrorIs(err, statusupdates.ErrBodyRequired)

	// Too long body (16385 chars)
	longBody := strings.Repeat("x", 16385)

	_, err = s.svc.CreateStatusUpdate(ctx, s.org.Slug, s.user.UID,
		makeCreate(s.pageID, "info", "title", longBody))
	r.ErrorIs(err, statusupdates.ErrBodyTooLong)
}

// TestLinkURLValidation checks that non-http/https URLs are rejected.
func TestLinkURLValidation(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newTestSetup(t)
	ctx := t.Context()

	badURL := "ftp://bad.example.com"
	req := makeCreate(s.pageID, "info", "title", "body")
	req.LinkURL = &badURL
	_, err := s.svc.CreateStatusUpdate(ctx, s.org.Slug, s.user.UID, req)
	r.ErrorIs(err, statusupdates.ErrInvalidLinkURL)

	// Good URL should work
	goodURL := "https://status.example.com"
	req2 := makeCreate(s.pageID, "info", "title", "body")
	req2.LinkURL = &goodURL
	resp, err := s.svc.CreateStatusUpdate(ctx, s.org.Slug, s.user.UID, req2)
	r.NoError(err)
	r.NotNil(resp.LinkURL)
	r.Equal(goodURL, *resp.LinkURL)
}

// TestStatusPageFKValidation checks that unknown status page UIDs are rejected.
func TestStatusPageFKValidation(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newTestSetup(t)

	_, err := s.svc.CreateStatusUpdate(t.Context(), s.org.Slug, s.user.UID,
		makeCreate("non-existent-uid", "info", "title", "body"))
	r.ErrorIs(err, statusupdates.ErrStatusPageNotFound)
}

// TestCrossOrgAccess verifies that a status update from org A is not accessible by org B (403→404).
func TestCrossOrgAccess(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newTestSetup(t)
	ctx := t.Context()

	// Create update in org1
	resp, err := s.svc.CreateStatusUpdate(ctx, s.org.Slug, s.user.UID,
		makeCreate(s.pageID, "info", "title", "body"))
	r.NoError(err)

	// org2 cannot see it
	_, err = s.svc.GetStatusUpdate(ctx, s.org2.Slug, resp.UID)
	r.ErrorIs(err, statusupdates.ErrStatusUpdateNotFound)

	// org2 cannot delete it
	err = s.svc.DeleteStatusUpdate(ctx, s.org2.Slug, resp.UID, s.user.UID)
	r.ErrorIs(err, statusupdates.ErrStatusUpdateNotFound)
}

// TestSoftDeleteMakesInvisible verifies deleted items do not appear in list.
func TestSoftDeleteMakesInvisible(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newTestSetup(t)
	ctx := t.Context()

	resp, err := s.svc.CreateStatusUpdate(ctx, s.org.Slug, s.user.UID,
		makeCreate(s.pageID, "resolved", "All good", "Services restored"))
	r.NoError(err)

	// Appears in list
	list, err := s.svc.ListStatusUpdates(ctx, s.org.Slug,
		&statusupdates.ListStatusUpdatesOptions{StatusPage: s.pageID})
	r.NoError(err)
	r.Len(list, 1)

	// Delete it
	r.NoError(s.svc.DeleteStatusUpdate(ctx, s.org.Slug, resp.UID, s.user.UID))

	// No longer in list
	list, err = s.svc.ListStatusUpdates(ctx, s.org.Slug,
		&statusupdates.ListStatusUpdatesOptions{StatusPage: s.pageID})
	r.NoError(err)
	r.Empty(list)
}

// TestListFilter verifies filtering by incident UID.
func TestListFilter(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newTestSetup(t)
	ctx := t.Context()

	// Create two updates
	req1 := makeCreate(s.pageID, "info", "General notice", "Nothing wrong")
	_, err := s.svc.CreateStatusUpdate(ctx, s.org.Slug, s.user.UID, req1)
	r.NoError(err)

	req2 := makeCreate(s.pageID, "maintenance", "Maintenance tonight", "DB upgrade at 2 AM")
	_, err = s.svc.CreateStatusUpdate(ctx, s.org.Slug, s.user.UID, req2)
	r.NoError(err)

	// List all
	list, err := s.svc.ListStatusUpdates(ctx, s.org.Slug,
		&statusupdates.ListStatusUpdatesOptions{StatusPage: s.pageID})
	r.NoError(err)
	r.Len(list, 2)
}
