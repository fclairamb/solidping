package statuspages

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/analytics"
	"github.com/fclairamb/solidping/server/internal/db/models"
	mw "github.com/fclairamb/solidping/server/internal/middleware"
)

// capturePagePublished records the status_page_published product event (spec
// 2026-08-02-08).
//
// SolidPing has no explicit "publish" action, so a page counts as published the
// moment it is both enabled and publicly visible. There are exactly two ways to
// reach that state and both funnel through here:
//
//   - creation, when the page is created already enabled + public (the default);
//   - update, when an existing private/disabled page transitions into it.
//
// The update path fires ONLY on the transition, so editing an already-public
// page is silent and a create is never double-counted.
//
// Privacy: only the org UID, the acting user UID and the low-cardinality
// visibility string travel — never the page name, slug, description, custom CSS
// or custom domain. No-op unless PostHog is configured.
func capturePagePublished(ctx context.Context, orgUID, visibility string) {
	var userUID string
	if claims, ok := mw.GetClaimsFromContext(ctx); ok && claims != nil {
		userUID = claims.UserUID
	}

	if orgUID == "" {
		if org, ok := mw.GetOrganizationFromContext(ctx); ok && org != nil {
			orgUID = org.UID
		}
	}

	analytics.Capture(ctx, analytics.Event{
		Name:       analytics.EventStatusPagePublished,
		OrgUID:     orgUID,
		UserUID:    userUID,
		Properties: map[string]any{"visibility": visibility},
	})
}

// capturePublishTransition fires status_page_published when an update moved the
// page INTO the published state (enabled + public).
//
// SolidPing has no explicit "publish" action, so publishing is a state
// transition. Firing only on the transition is what keeps the event honest: an
// unrelated edit to an already-public page emits nothing, and a page created
// public is counted once by the create path rather than twice.
func capturePublishTransition(ctx context.Context, orgUID string, before, after *models.StatusPage) {
	if isPublished(after) && !isPublished(before) {
		capturePagePublished(ctx, orgUID, after.Visibility)
	}
}
