package incidentpublications_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentpublications"
)

// Coverage for spec 2026-09-02-05: a publication could outlive its resolved
// incident forever, leaving the public page (and the TV wallboard) claiming
// trouble while every check was up.
//
// Two independent mechanisms produced it, and each is pinned separately below:
//
//  1. publishing an ALREADY-resolved incident minted a fresh `investigating`
//     entry that nothing would ever close — OnIncidentResolved is a one-shot
//     trigger and had already fired;
//  2. `PublishIncident` stamped `human_touched_at` on creation and
//     `OnIncidentResolved` skipped anything a machine had not created, so the
//     `if_untouched` default behaved exactly like `never` for every
//     hand-published entry.

// resolvedIncident drives one check down and back up, and returns the incident
// row in its final, RESOLVED shape. Nothing is published along the way: the
// setup's page has auto-publish off, which is what makes the publish in each
// test the first thing that ever reached the page.
func resolvedIncident(t *testing.T, s *pubSetup) *models.Incident {
	t.Helper()

	s.submit(models.ResultStatusDown)

	active := s.activeIncident()
	require.NotNil(t, active, "the outage must have opened an incident to resolve")

	uid := active.UID

	s.clk.Advance(10 * time.Minute)
	s.submit(models.ResultStatusUp)

	incident, err := s.dbSvc.GetIncident(t.Context(), s.org.UID, uid)
	require.NoError(t, err)
	require.NotNil(t, incident)
	require.Equal(t, models.IncidentStateResolved, incident.State,
		"the harness must hand the publish path a genuinely resolved incident")
	require.NotNil(t, incident.ResolvedAt)

	return incident
}

// liveIncident drives one check down and leaves it there.
func liveIncident(t *testing.T, s *pubSetup) *models.Incident {
	t.Helper()

	s.submit(models.ResultStatusDown)

	incident := s.activeIncident()
	require.NotNil(t, incident)
	require.Equal(t, models.IncidentStateActive, incident.State)

	return incident
}

func (s *pubSetup) publicationEventTypes(pubUID string) []models.EventType {
	s.t.Helper()

	events, err := s.dbSvc.ListEvents(s.t.Context(), &models.ListEventsFilter{
		OrganizationUID:   s.org.UID,
		EventTypePrefixes: []string{"statuspage"},
		Limit:             200,
	})
	require.NoError(s.t, err)

	out := make([]models.EventType, 0, len(events))

	for _, event := range events {
		if uid, ok := event.Payload["publication_uid"].(string); ok && uid == pubUID {
			out = append(out, event.EventType)
		}
	}

	return out
}

// TestPublishingAResolvedIncidentIsBornResolved is the reported bug, inverted.
func TestPublishingAResolvedIncidentIsBornResolved(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: false, delaySeconds: 0})

	incident := resolvedIncident(t, s)

	severity := string(models.PublicationSeverityMinor)
	resp, err := s.pubs.PublishIncident(t.Context(), s.org.Slug, incident.UID, s.userUID,
		&incidentpublications.PublishIncidentRequest{
			StatusPageUID: s.page.UID,
			Severity:      &severity,
		})
	r.NoError(err)

	// The response is what the dash0 publish dialog reads back.
	r.Equal(string(models.PublicationStateResolved), resp.State,
		"a retroactive publish must report itself as resolved")
	r.NotNil(resp.ResolvedAt)
	r.Equal(incident.ResolvedAt.UTC(), resp.ResolvedAt.UTC(),
		"the public duration is the OUTAGE's, not the paperwork's")
	r.False(resp.HumanTouched,
		"publishing is not taking over the narrative")

	// Severity is still accepted: it labels the past entry on the page.
	r.NotNil(resp.Severity)
	r.Equal(severity, *resp.Severity)

	pubs := s.publications()
	r.Len(pubs, 1)
	r.Equal(models.PublicationStateResolved, pubs[0].PublicState)
	r.NotNil(pubs[0].ResolvedAt)

	// published_at is when the ENTRY appeared, which is now — not when the
	// outage started.
	r.True(pubs[0].PublishedAt.After(*incident.ResolvedAt) ||
		pubs[0].PublishedAt.Equal(*incident.ResolvedAt))

	kinds := s.updateKinds(pubs[0])
	r.Contains(kinds, models.StatusUpdateKindInvestigating,
		"the timeline still opens with what was being investigated")
	r.Contains(kinds, models.StatusUpdateKindResolved,
		"…and closes in the same breath, so it reads as a post-mortem entry")

	r.Contains(s.publicationEventTypes(pubs[0].UID),
		models.EventTypeStatusPageIncidentResolved,
		"a webhook consumer must hear that the public entry closed")
}

// TestPublishingALiveIncidentIsUnchanged is the positive control for the test
// above: without it, a PublishIncident that resolved EVERYTHING would pass.
func TestPublishingALiveIncidentIsUnchanged(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: false, delaySeconds: 0})

	incident := liveIncident(t, s)

	resp, err := s.pubs.PublishIncident(t.Context(), s.org.Slug, incident.UID, s.userUID,
		&incidentpublications.PublishIncidentRequest{StatusPageUID: s.page.UID})
	r.NoError(err)

	r.Equal(string(models.PublicationStateInvestigating), resp.State)
	r.Nil(resp.ResolvedAt)

	pubs := s.publications()
	r.Len(pubs, 1)
	r.Equal(models.PublicationStateInvestigating, pubs[0].PublicState)
	r.NotContains(s.updateKinds(pubs[0]), models.StatusUpdateKindResolved)
	r.NotContains(s.publicationEventTypes(pubs[0].UID),
		models.EventTypeStatusPageIncidentResolved)
}

// TestCreateWithAResolvedIncidentIsBornResolved covers the generic create path,
// which can also bind a publication to an incident.
func TestCreateWithAResolvedIncidentIsBornResolved(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: false, delaySeconds: 0})

	incident := resolvedIncident(t, s)

	resp, err := s.pubs.CreatePublication(t.Context(), s.org.Slug, s.page.UID, s.userUID,
		&incidentpublications.CreatePublicationRequest{
			Title:       "Payments were briefly unavailable",
			IncidentUID: &incident.UID,
		})
	r.NoError(err)

	r.Equal(string(models.PublicationStateResolved), resp.State,
		"binding a resolved incident by hand must not open a live entry either")
	r.NotNil(resp.ResolvedAt)
	r.Equal(incident.ResolvedAt.UTC(), resp.ResolvedAt.UTC())

	pubs := s.publications()
	r.Len(pubs, 1)
	r.Contains(s.updateKinds(pubs[0]), models.StatusUpdateKindResolved)
}

// TestCreateWithALiveIncidentIsUnchanged is the matching positive control.
func TestCreateWithALiveIncidentIsUnchanged(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: false, delaySeconds: 0})

	incident := liveIncident(t, s)

	resp, err := s.pubs.CreatePublication(t.Context(), s.org.Slug, s.page.UID, s.userUID,
		&incidentpublications.CreatePublicationRequest{
			Title:       "Payments are unavailable",
			IncidentUID: &incident.UID,
		})
	r.NoError(err)

	r.Equal(string(models.PublicationStateInvestigating), resp.State)
	r.Nil(resp.ResolvedAt)
}

// TestHandPublishedEntryIsAutoResolvedWhenUntouched is the near-miss half of
// the reported scenario: the operator publishes at 12:17:00, the incident
// recovers at 12:17:22. Before this spec the entry stayed open forever, because
// PublishIncident had stamped human_touched_at and OnIncidentResolved only
// looked at machine-created rows.
func TestHandPublishedEntryIsAutoResolvedWhenUntouched(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{
		autoPublish: false, delaySeconds: 0, autoResolve: models.AutoResolveIfUntouched,
	})

	incident := liveIncident(t, s)

	resp, err := s.pubs.PublishIncident(t.Context(), s.org.Slug, incident.UID, s.userUID,
		&incidentpublications.PublishIncidentRequest{StatusPageUID: s.page.UID})
	r.NoError(err)
	r.False(resp.HumanTouched,
		"publishing must not pre-claim the narrative, or if_untouched is inert")

	s.clk.Advance(30 * time.Second)
	s.submit(models.ResultStatusUp)

	pubs := s.publications()
	r.Len(pubs, 1)
	r.Equal(models.PublicationStateResolved, pubs[0].PublicState,
		"a hand-published entry nobody edited is in scope of the page's policy")
	r.NotNil(pubs[0].ResolvedAt)
	r.Contains(s.updateKinds(pubs[0]), models.StatusUpdateKindResolved)
}

// TestHandPublishedEntryIsNotAutoResolvedOnceTouched pins the other half of
// `if_untouched`: the moment a person writes something, the machine reports the
// recovery and leaves the closing word to them.
func TestHandPublishedEntryIsNotAutoResolvedOnceTouched(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{
		autoPublish: false, delaySeconds: 0, autoResolve: models.AutoResolveIfUntouched,
	})

	incident := liveIncident(t, s)

	resp, err := s.pubs.PublishIncident(t.Context(), s.org.Slug, incident.UID, s.userUID,
		&incidentpublications.PublishIncidentRequest{StatusPageUID: s.page.UID})
	r.NoError(err)

	title := "We are working on payments"
	_, err = s.pubs.UpdatePublication(t.Context(), s.org.Slug, s.page.UID, resp.UID,
		s.userUID, &incidentpublications.UpdatePublicationRequest{Title: &title})
	r.NoError(err)

	s.clk.Advance(30 * time.Second)
	s.submit(models.ResultStatusUp)

	pubs := s.publications()
	r.Len(pubs, 1)
	r.NotEqual(models.PublicationStateResolved, pubs[0].PublicState)
	r.Nil(pubs[0].ResolvedAt)

	kinds := s.updateKinds(pubs[0])
	r.Contains(kinds, models.StatusUpdateKindMonitoring)
	r.NotContains(kinds, models.StatusUpdateKindResolved)
}

// TestHandPublishedEntryIsAutoResolvedUnderAlways pins `always`: even an edited
// entry closes.
func TestHandPublishedEntryIsAutoResolvedUnderAlways(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{
		autoPublish: false, delaySeconds: 0, autoResolve: models.AutoResolveAlways,
	})

	incident := liveIncident(t, s)

	resp, err := s.pubs.PublishIncident(t.Context(), s.org.Slug, incident.UID, s.userUID,
		&incidentpublications.PublishIncidentRequest{StatusPageUID: s.page.UID})
	r.NoError(err)

	title := "We are working on payments"
	_, err = s.pubs.UpdatePublication(t.Context(), s.org.Slug, s.page.UID, resp.UID,
		s.userUID, &incidentpublications.UpdatePublicationRequest{Title: &title})
	r.NoError(err)

	s.clk.Advance(30 * time.Second)
	s.submit(models.ResultStatusUp)

	pubs := s.publications()
	r.Len(pubs, 1)
	r.Equal(models.PublicationStateResolved, pubs[0].PublicState)
}

// TestAutoResolveNeverLeavesAHandPublishedEntryAlone is the negative control
// the spec asks for by name. Widening auto-resolve to hand-published entries
// must not have widened it past the page's own opt-out.
func TestAutoResolveNeverLeavesAHandPublishedEntryAlone(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{
		autoPublish: false, delaySeconds: 0, autoResolve: models.AutoResolveNever,
	})

	incident := liveIncident(t, s)

	_, err := s.pubs.PublishIncident(t.Context(), s.org.Slug, incident.UID, s.userUID,
		&incidentpublications.PublishIncidentRequest{StatusPageUID: s.page.UID})
	r.NoError(err)

	pubs := s.publications()
	r.Len(pubs, 1)

	before := len(s.updateKinds(pubs[0]))

	s.clk.Advance(30 * time.Second)
	s.submit(models.ResultStatusUp)

	pubs = s.publications()
	r.Equal(models.PublicationStateInvestigating, pubs[0].PublicState)
	r.Nil(pubs[0].ResolvedAt)
	r.Len(s.updateKinds(pubs[0]), before, "never posts nothing at all")
}

// TestFreeFormPublicationSurvivesAnIncidentResolving is the scope guard.
//
// It sits a free-form entry ("we are migrating tonight") on the SAME page as an
// incident-linked one and resolves the incident. A change that made
// OnIncidentResolved close everything on the page — the obvious over-correction
// — would close the free-form one too and fail here, while the linked one
// closing proves the resolve really ran.
func TestFreeFormPublicationSurvivesAnIncidentResolving(t *testing.T) {
	t.Parallel()

	for _, policy := range []models.AutoResolvePolicy{
		models.AutoResolveIfUntouched, models.AutoResolveAlways,
	} {
		t.Run(string(policy), func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			s := newPubSetup(t, setupOptions{
				autoPublish: false, delaySeconds: 0, autoResolve: policy,
			})

			freeForm, err := s.pubs.CreatePublication(t.Context(), s.org.Slug, s.page.UID, s.userUID,
				&incidentpublications.CreatePublicationRequest{
					Title: "Planned database migration",
				})
			r.NoError(err)
			r.Nil(freeForm.IncidentUID, "the guard only means anything if it tracks nothing")

			incident := liveIncident(t, s)

			linked, err := s.pubs.PublishIncident(t.Context(), s.org.Slug, incident.UID, s.userUID,
				&incidentpublications.PublishIncidentRequest{StatusPageUID: s.page.UID})
			r.NoError(err)

			s.clk.Advance(30 * time.Second)
			s.submit(models.ResultStatusUp)

			byUID := map[string]*models.IncidentPublication{}
			for _, pub := range s.publications() {
				byUID[pub.UID] = pub
			}

			r.Len(byUID, 2)
			r.Equal(models.PublicationStateResolved, byUID[linked.UID].PublicState,
				"the linked entry closes — proof the resolve pass actually ran")
			r.NotEqual(models.PublicationStateResolved, byUID[freeForm.UID].PublicState,
				"a free-form entry tracks no incident, so no recovery may close it")
			r.Nil(byUID[freeForm.UID].ResolvedAt)
		})
	}
}

// TestPublicPageDropsTheResolvedPublication is the customer-visible end of the
// fix: the banner the reporter saw is gone.
func TestPublicPageDropsTheResolvedPublication(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: false, delaySeconds: 0})

	incident := resolvedIncident(t, s)

	_, err := s.pubs.PublishIncident(t.Context(), s.org.Slug, incident.UID, s.userUID,
		&incidentpublications.PublishIncidentRequest{StatusPageUID: s.page.UID})
	r.NoError(err)

	active, err := s.pubs.ListPublicIncidents(t.Context(), s.page, true)
	r.NoError(err)
	r.Empty(active,
		"a page whose only publication is resolved has no active incidents")

	// …and the entry is still THERE, in the history. Dropping it from both
	// feeds would be a different bug wearing this one's clothes.
	history, err := s.pubs.ListPublicIncidents(t.Context(), s.page, false)
	r.NoError(err)
	r.Len(history, 1)
	r.Equal(string(models.PublicationStateResolved), history[0].State)
}

// TestStaleFlagAndFilter covers the signal dash0's warning banner reads.
func TestStaleFlagAndFilter(t *testing.T) {
	t.Parallel()

	t.Run("an entry left open past its incident's recovery is stale", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		s := newPubSetup(t, setupOptions{
			autoPublish: false, delaySeconds: 0, autoResolve: models.AutoResolveNever,
		})

		incident := liveIncident(t, s)

		resp, err := s.pubs.PublishIncident(t.Context(), s.org.Slug, incident.UID, s.userUID,
			&incidentpublications.PublishIncidentRequest{StatusPageUID: s.page.UID})
		r.NoError(err)

		// While the incident is live the page is telling the truth.
		listed, err := s.pubs.ListPublications(t.Context(), s.org.Slug, s.page.UID,
			incidentpublications.ListOptions{ActiveOnly: true})
		r.NoError(err)
		r.Len(listed, 1)
		r.False(listed[0].Stale, "an open entry over a LIVE incident is not stale")

		staleOnly, err := s.pubs.ListPublications(t.Context(), s.org.Slug, s.page.UID,
			incidentpublications.ListOptions{ActiveOnly: true, StaleOnly: true})
		r.NoError(err)
		r.Empty(staleOnly)

		// The check recovers. `never` keeps the entry open, which is exactly
		// the state an operator has to be told about.
		s.clk.Advance(time.Minute)
		s.submit(models.ResultStatusUp)

		listed, err = s.pubs.ListPublications(t.Context(), s.org.Slug, s.page.UID,
			incidentpublications.ListOptions{ActiveOnly: true})
		r.NoError(err)
		r.Len(listed, 1)
		r.True(listed[0].Stale)

		staleOnly, err = s.pubs.ListPublications(t.Context(), s.org.Slug, s.page.UID,
			incidentpublications.ListOptions{ActiveOnly: true, StaleOnly: true})
		r.NoError(err)
		r.Len(staleOnly, 1)
		r.Equal(resp.UID, staleOnly[0].UID)

		// The single read carries it too, so the editor route can say so.
		single, err := s.pubs.GetPublication(t.Context(), s.org.Slug, s.page.UID, resp.UID)
		r.NoError(err)
		r.True(single.Stale)
	})

	t.Run("a resolved entry is never stale", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		s := newPubSetup(t, setupOptions{autoPublish: false, delaySeconds: 0})

		incident := resolvedIncident(t, s)

		resp, err := s.pubs.PublishIncident(t.Context(), s.org.Slug, incident.UID, s.userUID,
			&incidentpublications.PublishIncidentRequest{StatusPageUID: s.page.UID})
		r.NoError(err)
		r.Equal(string(models.PublicationStateResolved), resp.State)

		listed, err := s.pubs.ListPublications(t.Context(), s.org.Slug, s.page.UID,
			incidentpublications.ListOptions{})
		r.NoError(err)
		r.Len(listed, 1)
		r.False(listed[0].Stale, "nothing left to close")
	})

	t.Run("a free-form entry is never stale", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		s := newPubSetup(t, setupOptions{autoPublish: false, delaySeconds: 0})

		_, err := s.pubs.CreatePublication(t.Context(), s.org.Slug, s.page.UID, s.userUID,
			&incidentpublications.CreatePublicationRequest{Title: "Planned database migration"})
		r.NoError(err)

		listed, err := s.pubs.ListPublications(t.Context(), s.org.Slug, s.page.UID,
			incidentpublications.ListOptions{ActiveOnly: true})
		r.NoError(err)
		r.Len(listed, 1)
		r.False(listed[0].Stale,
			"it tracks no incident, so no recovery can contradict it — warning "+
				"about it would be noise until its author closes it")
	})
}
