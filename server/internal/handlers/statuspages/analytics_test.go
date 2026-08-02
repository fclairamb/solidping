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

// installRecorder swaps in a recording analytics client for the duration of the
// test. This is the only test in the package that touches the process-wide
// analytics client, so there is nothing here for it to race with.
func installRecorder(t *testing.T) *eventRecorder {
	t.Helper()

	rec := &eventRecorder{}
	analytics.SetDefault(rec)
	t.Cleanup(func() { analytics.SetDefault(nil) })

	return rec
}

func boolPtr(b bool) *bool { return &b }

// TestStatusPagePublishedFiresOnlyOnTheTransition covers both publish paths
// (spec 2026-08-02-08). SolidPing has no explicit publish action, so
// "published" is the transition into enabled + public. A page created private
// must emit nothing at create time, exactly one event when it is later made
// public, and nothing on any subsequent edit while it stays public; a page
// created already public counts once, at create.
//
// Both captures live in the SERVICE, not the handler, so the MCP status-page
// tools (internal/mcp/tools_statuspages.go, which call the service directly)
// are covered by the same code path this test exercises.
func TestStatusPagePublishedFiresOnlyOnTheTransition(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupStatusPagesTest(t)
	rec := installRecorder(t)

	// Created private: no publish event.
	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name:       "Private page",
		Slug:       "private-page",
		Visibility: strPtr("private"),
	})
	r.NoError(err)
	r.Equal("private", page.Visibility)
	r.NotContains(rec.names(), analytics.EventStatusPagePublished,
		"a page created private must not count as published")

	// Flip to public → exactly one publish event.
	_, err = svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		Visibility: strPtr(visibilityPublic),
	})
	r.NoError(err)

	published := countPublished(rec)
	r.Equal(1, published, "making a private page public must emit exactly one publish event")

	// An unrelated edit while it stays public must emit nothing more — no
	// double-counting, no noise on every save.
	_, err = svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		Name: strPtr("Renamed"),
	})
	r.NoError(err)

	published = countPublished(rec)
	r.Equal(1, published, "editing an already-public page must not re-emit the publish event")

	// The other half of the transition: disabling a public page and then
	// re-enabling it is a publish too. Kept inside this same test function
	// because it mutates the same process-wide analytics client — two parallel
	// top-level tests installing recorders would steal each other's events.
	_, err = svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		Enabled: boolPtr(false),
	})
	r.NoError(err)

	published = countPublished(rec)
	r.Equal(1, published, "disabling a page is not a publish event")

	_, err = svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		Enabled: boolPtr(true),
	})
	r.NoError(err)

	r.Equal(2, countPublished(rec), "re-enabling a public page is a publish transition")

	// A page created already enabled + public (the default) is published on the
	// spot, by the service — which is what makes MCP-created pages count.
	created, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Public page",
		Slug: "public-page",
	})
	r.NoError(err)
	r.True(created.Enabled)
	r.Equal(visibilityPublic, created.Visibility)

	r.Equal(3, countPublished(rec), "creating an enabled+public page emits exactly one publish event")
}

// countPublished counts status_page_published events seen so far.
func countPublished(rec *eventRecorder) int {
	count := 0

	for _, name := range rec.names() {
		if name == analytics.EventStatusPagePublished {
			count++
		}
	}

	return count
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
