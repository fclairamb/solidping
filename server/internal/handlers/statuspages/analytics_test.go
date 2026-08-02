package statuspages

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/analytics"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// eventRecorder is a fake analytics.Client that records what would have been
// sent, so the publish-transition rules can be asserted without any network.
type eventRecorder struct {
	mu     sync.Mutex
	events []analytics.Event
}

func (r *eventRecorder) Capture(_ context.Context, event analytics.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.events = append(r.events, event)
}

func (r *eventRecorder) Enabled() bool               { return true }
func (r *eventRecorder) Close(context.Context) error { return nil }

func (r *eventRecorder) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]string, 0, len(r.events))
	for _, e := range r.events {
		out = append(out, e.Name)
	}

	return out
}

// countPublished counts status_page_published events seen so far.
func (r *eventRecorder) countPublished() int {
	count := 0

	for _, name := range r.names() {
		if name == analytics.EventStatusPagePublished {
			count++
		}
	}

	return count
}

// attachRecorder gives THIS test's service its own analytics client.
//
// Deliberately per-service, not a swapped-out process-wide default: every test
// that creates or updates a status page runs through a capture path, so a
// shared global made any two of them collide under t.Parallel() — one test's
// events landing in another's assertions, intermittently, depending on
// goroutine scheduling. Injecting here means each test observes exactly its own
// events and every other test in the package keeps capturing into the global
// no-op, whatever it does.
func attachRecorder(t *testing.T, svc *Service) *eventRecorder {
	t.Helper()

	rec := &eventRecorder{}
	svc.analytics = rec

	return rec
}

func boolPtr(b bool) *bool { return &b }

// TestStatusPagePublishedNotFiredOnPrivateCreate: a page created private is not
// published, so nothing is emitted at create time.
func TestStatusPagePublishedNotFiredOnPrivateCreate(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupStatusPagesTest(t)
	rec := attachRecorder(t, svc)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name:       "Private page",
		Slug:       "private-page",
		Visibility: strPtr("private"),
	})
	r.NoError(err)
	r.Equal("private", page.Visibility)
	r.NotContains(rec.names(), analytics.EventStatusPagePublished,
		"a page created private must not count as published")
}

// TestStatusPagePublishedFiredOnPublicCreate: a page created already enabled +
// public (the default) is published on the spot.
//
// The capture lives in the SERVICE, not the handler, which is what makes
// MCP-created pages (internal/mcp/tools_statuspages.go calls the service
// directly) count identically to REST-created ones.
func TestStatusPagePublishedFiredOnPublicCreate(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupStatusPagesTest(t)
	rec := attachRecorder(t, svc)

	created, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Public page",
		Slug: "public-page",
	})
	r.NoError(err)
	r.True(created.Enabled)
	r.Equal(visibilityPublic, created.Visibility)

	r.Equal(1, rec.countPublished(),
		"creating an enabled+public page emits exactly one publish event")
}

// TestStatusPagePublishedFiresOnVisibilityTransition: flipping a private page
// public publishes it, and a later unrelated edit must not re-emit.
func TestStatusPagePublishedFiresOnVisibilityTransition(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupStatusPagesTest(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name:       "Private page",
		Slug:       "private-page",
		Visibility: strPtr("private"),
	})
	r.NoError(err)

	rec := attachRecorder(t, svc)

	_, err = svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		Visibility: strPtr(visibilityPublic),
	})
	r.NoError(err)
	r.Equal(1, rec.countPublished(),
		"making a private page public must emit exactly one publish event")

	// An unrelated edit while it stays public must emit nothing more — no
	// double-counting, no noise on every save.
	_, err = svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		Name: strPtr("Renamed"),
	})
	r.NoError(err)
	r.Equal(1, rec.countPublished(),
		"editing an already-public page must not re-emit the publish event")
}

// TestStatusPagePublishedFiresOnReEnable: the other half of the transition —
// disabling a public page is not a publish, re-enabling it is.
func TestStatusPagePublishedFiresOnReEnable(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupStatusPagesTest(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Public page",
		Slug: "public-page",
	})
	r.NoError(err)

	rec := attachRecorder(t, svc)

	_, err = svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		Enabled: boolPtr(false),
	})
	r.NoError(err)
	r.Equal(0, rec.countPublished(), "disabling a page is not a publish event")

	_, err = svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		Enabled: boolPtr(true),
	})
	r.NoError(err)
	r.Equal(1, rec.countPublished(), "re-enabling a public page is a publish transition")
}

// TestAnalyticsClientDefaultsToTheGlobalNoOp pins the fallback: a service built
// the normal way (no injection) captures through the process-wide client, which
// is the no-op unless PostHog is configured. This is what keeps every OTHER
// test in this package — and production — from needing to know analytics exists.
func TestAnalyticsClientDefaultsToTheGlobalNoOp(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, svc, _ := setupStatusPagesTest(t)

	r.Nil(svc.analytics, "a normally constructed service injects no client")
	r.NotNil(svc.analyticsClient(), "the fallback client must never be nil")
	r.False(svc.analyticsClient().Enabled(),
		"with PostHog unconfigured the fallback must be the no-op")

	rec := attachRecorder(t, svc)
	r.Same(analytics.Client(rec), svc.analyticsClient(),
		"an injected client must win over the global")
}

// TestIsPublishedRule pins the predicate both call sites share.
func TestIsPublishedRule(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.False(isPublished(nil))
	r.False(isPublished(&models.StatusPage{Enabled: false, Visibility: visibilityPublic}))
	r.False(isPublished(&models.StatusPage{Enabled: true, Visibility: "private"}))
	r.True(isPublished(&models.StatusPage{Enabled: true, Visibility: visibilityPublic}))
}
