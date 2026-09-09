package db_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// testIncidentPublicationsActiveOnly is the cross-engine parity guard for the
// `ActiveOnly` filter of ListIncidentPublications.
//
// That one clause is the whole reason a resolved incident stops being shown in
// the public status page's active section: the public feed is
// `ListPublicIncidents(ctx, page, activeOnly=true)`, which sets `ActiveOnly` on
// this filter, and each engine spells the exclusion out for itself
// (postgres/incident_publication.go and sqlite/incident_publication.go both
// carry their own `public_state <> 'resolved'`). Two copies of one contract is
// exactly the shape that drifts silently, so both engines are exercised here.
//
// The e2e counterpart (web/status0/e2e/incident-publications.spec.ts) proves
// the same thing through the browser, but it has to defeat the HTTP cache to
// see it — this test sits below all of that and fails loudly and fast.
func testIncidentPublicationsActiveOnly(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	short := suffix[len(suffix)-6:]

	org := models.NewOrganization("ipao-"+short, "Active Only Org")
	r.NoError(svc.CreateOrganization(ctx, org))

	page := models.NewStatusPage(org.UID, "Active Only", "ipao-page-"+short)
	r.NoError(svc.CreateStatusPage(ctx, page))

	now := time.Now()

	// One publication per public state, so the exclusion is pinned as
	// "resolved and only resolved" rather than "the last one inserted".
	states := []models.PublicationState{
		models.PublicationStateInvestigating,
		models.PublicationStateIdentified,
		models.PublicationStateMonitoring,
		models.PublicationStateResolved,
	}

	uids := make(map[models.PublicationState]string, len(states))

	for i, state := range states {
		pub := models.NewIncidentPublication(org.UID, page.UID, "pub "+string(state), now.Add(time.Duration(i)*time.Second))
		pub.PublicState = state

		if state == models.PublicationStateResolved {
			resolvedAt := now.Add(time.Duration(i) * time.Second)
			pub.ResolvedAt = &resolvedAt
		}

		r.NoError(svc.CreateIncidentPublication(ctx, pub))
		uids[state] = pub.UID
	}

	// A soft-deleted row in an ACTIVE state (investigating): the stronger case,
	// because the ActiveOnly clause would happily let it through on its state
	// alone — only deleted_at keeps it out. It must not come back on either
	// branch, so a future "just drop the ActiveOnly clause" cannot be
	// rationalized as harmless because deleted_at already hides most of it.
	deleted := models.NewIncidentPublication(org.UID, page.UID, "pub deleted", now.Add(10*time.Second))
	deleted.PublicState = models.PublicationStateInvestigating
	r.NoError(svc.CreateIncidentPublication(ctx, deleted))
	r.NoError(svc.SoftDeleteIncidentPublication(ctx, deleted.UID))

	tests := []struct {
		name       string
		activeOnly bool
		wantStates []models.PublicationState
	}{
		{
			name:       "active only excludes the resolved publication",
			activeOnly: true,
			wantStates: []models.PublicationState{
				models.PublicationStateInvestigating,
				models.PublicationStateIdentified,
				models.PublicationStateMonitoring,
			},
		},
		{
			// The positive control. Without it, a query that returned nothing
			// at all — a broken join, a mis-scoped org — would satisfy the
			// exclusion case above and read as a pass.
			name:       "history keeps the resolved publication",
			activeOnly: false,
			wantStates: states,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rr := require.New(t)

			pubs, err := svc.ListIncidentPublications(ctx, &models.ListIncidentPublicationsFilter{
				OrganizationUID: org.UID,
				StatusPageUID:   page.UID,
				ActiveOnly:      tc.activeOnly,
				Limit:           50,
			})
			rr.NoError(err)

			got := make(map[string]models.PublicationState, len(pubs))
			for _, pub := range pubs {
				got[pub.UID] = pub.PublicState
			}

			rr.Len(got, len(tc.wantStates))

			for _, state := range tc.wantStates {
				rr.Equal(state, got[uids[state]],
					"publication in state %q must be listed with activeOnly=%v", state, tc.activeOnly)
			}

			rr.NotContains(got, deleted.UID, "a soft-deleted publication must never be listed")
		})
	}

	// The same filter, restricted to `State: resolved`, still finds the row —
	// so the exclusion above is the ActiveOnly clause doing its job, not the
	// resolved publication having failed to persist in the first place.
	resolved, err := svc.ListIncidentPublications(ctx, &models.ListIncidentPublicationsFilter{
		OrganizationUID: org.UID,
		StatusPageUID:   page.UID,
		State:           string(models.PublicationStateResolved),
		Limit:           50,
	})
	r.NoError(err)
	r.Len(resolved, 1)
	r.Equal(uids[models.PublicationStateResolved], resolved[0].UID)
}
