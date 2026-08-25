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
func (s *Service) capturePagePublished(ctx context.Context, orgUID, visibility string) {
	var userUID string
	if claims, ok := mw.GetClaimsFromContext(ctx); ok && claims != nil {
		userUID = claims.UserUID
	}

	if orgUID == "" {
		if org, ok := mw.GetOrganizationFromContext(ctx); ok && org != nil {
			orgUID = org.UID
		}
	}

	s.analyticsClient().Capture(ctx, analytics.Event{
		Name:       analytics.EventStatusPagePublished,
		OrgUID:     orgUID,
		UserUID:    userUID,
		Properties: map[string]any{fieldVisibility: visibility},
	})
}

// analyticsClient returns the client this service captures through: the
// injected one when a test attached a recorder, otherwise the process-wide
// client (a no-op unless PostHog is configured).
func (s *Service) analyticsClient() analytics.Client {
	if s.analytics != nil {
		return s.analytics
	}

	return analytics.Default()
}

// capturePublishTransition fires status_page_published when a write moved the
// page INTO the published state (enabled + public).
//
// SolidPing has no explicit "publish" action, so publishing is a state
// transition. Firing only on the transition is what keeps the event honest: an
// unrelated edit to an already-public page emits nothing.
//
// Creation passes before=nil, which is never published, so a page created
// already enabled + public counts exactly once and one created private counts
// later, on the update that publishes it.
func (s *Service) capturePublishTransition(
	ctx context.Context, orgUID string, before, after *models.StatusPage,
) {
	if isPublished(after) && !isPublished(before) {
		s.capturePagePublished(ctx, orgUID, after.Visibility)
	}
}
