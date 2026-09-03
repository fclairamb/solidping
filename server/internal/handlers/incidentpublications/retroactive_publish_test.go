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

// TestHandPublishedEntryIsReopenedOnRelapse is the counterweight to widening
// auto-resolve.
//
// Step 2 of the spec made a hand-published, incident-linked entry closeable by
// a recovery. That, on its own, opened a hole in the OTHER direction: the
// relapse path still asked `auto_created`, so the entry it had just closed
// would stay `resolved` through the next outage. `ListPublicIncidents(active)`
// excludes a resolved entry, so the public page — and the wallboard — would
// have gone GREEN during a live outage. That is strictly worse than the
// amber-while-healthy state this spec set out to fix, and the `stale` flag
// cannot see it (it only looks for open entry + resolved incident).
func TestHandPublishedEntryIsReopenedOnRelapse(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{
		autoPublish: false, delaySeconds: 0, autoResolve: models.AutoResolveIfUntouched,
	})

	incident := liveIncident(t, s)

	resp, err := s.pubs.PublishIncident(t.Context(), s.org.Slug, incident.UID, s.userUID,
		&incidentpublications.PublishIncidentRequest{StatusPageUID: s.page.UID})
	r.NoError(err)

	// Recovery closes it — that is the behavior this spec introduced.
	s.clk.Advance(30 * time.Second)
	s.submit(models.ResultStatusUp)

	pubs := s.publications()
	r.Len(pubs, 1)
	r.Equal(models.PublicationStateResolved, pubs[0].PublicState)

	// Relapse inside the reopen cooldown.
	s.clk.Advance(30 * time.Second)
	s.submit(models.ResultStatusDown)

	pubs = s.publications()
	r.Len(pubs, 1, "a relapse must not mint a second public entry")
	r.Equal(resp.UID, pubs[0].UID)
	r.Equal(models.PublicationStateInvestigating, pubs[0].PublicState,
		"the entry auto-resolve closed is the entry a relapse must reopen")
	r.Nil(pubs[0].ResolvedAt)

	// The customer-visible end: the page says something is wrong again.
	active, err := s.pubs.ListPublicIncidents(t.Context(), s.page, true)
	r.NoError(err)
	r.Len(active, 1,
		"the public page must never read green during a live outage")
}

// TestFreeFormPublicationIsNeverReopened is the reopen path's scope guard, and
// the mirror of TestFreeFormPublicationSurvivesAnIncidentResolving.
func TestFreeFormPublicationIsNeverReopened(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{
		autoPublish: false, delaySeconds: 0, autoResolve: models.AutoResolveIfUntouched,
	})

	resolvedState := string(models.PublicationStateResolved)
	freeForm, err := s.pubs.CreatePublication(t.Context(), s.org.Slug, s.page.UID, s.userUID,
		&incidentpublications.CreatePublicationRequest{
			Title: "Last week's planned migration",
			State: &resolvedState,
		})
	r.NoError(err)
	r.Nil(freeForm.IncidentUID)

	incident := liveIncident(t, s)

	linked, err := s.pubs.PublishIncident(t.Context(), s.org.Slug, incident.UID, s.userUID,
		&incidentpublications.PublishIncidentRequest{StatusPageUID: s.page.UID})
	r.NoError(err)

	s.clk.Advance(30 * time.Second)
	s.submit(models.ResultStatusUp)

	s.clk.Advance(30 * time.Second)
	s.submit(models.ResultStatusDown)

	byUID := map[string]*models.IncidentPublication{}
	for _, pub := range s.publications() {
		byUID[pub.UID] = pub
	}

	r.Len(byUID, 2)
	r.Equal(models.PublicationStateInvestigating, byUID[linked.UID].PublicState,
		"the linked entry reopens — proof the relapse pass actually ran")
	r.Equal(models.PublicationStateResolved, byUID[freeForm.UID].PublicState,
		"a closed free-form entry is not resurrected by an unrelated relapse")
}

// TestRetroactiveCreateWithABodyPostsOneNarrative pins the create path's
// narrative shape: the operator's own words, posted once, as the resolved
// entry — not their words followed by a machine saying the same thing.
func TestRetroactiveCreateWithABodyPostsOneNarrative(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: false, delaySeconds: 0})

	incident := resolvedIncident(t, s)

	body := "A configuration change took payments offline for 36 minutes."
	resp, err := s.pubs.CreatePublication(t.Context(), s.org.Slug, s.page.UID, s.userUID,
		&incidentpublications.CreatePublicationRequest{
			Title:        "Payments were briefly unavailable",
			IncidentUID:  &incident.UID,
			BodyMarkdown: &body,
		})
	r.NoError(err)
	r.Equal(string(models.PublicationStateResolved), resp.State)

	full, err := s.pubs.GetPublication(t.Context(), s.org.Slug, s.page.UID, resp.UID)
	r.NoError(err)
	r.Len(full.Updates, 1,
		"the operator wrote the narrative; a templated close on top would repeat them")
	r.Equal(body, full.Updates[0].BodyMarkdown)
	r.Equal(string(models.StatusUpdateKindResolved), full.Updates[0].Kind,
		"the timeline and the header must not disagree")

	// The event still fires — a webhook consumer hears about the closed entry
	// whoever phrased it.
	r.Contains(s.publicationEventTypes(resp.UID),
		models.EventTypeStatusPageIncidentResolved)
}

// TestStaleFilterPaginatesTheFilteredSet pins that `stale` is applied BEFORE
// the caller's limit/offset. Filtering a page of rows that was already cut to
// `limit` would under-report: `?stale=true&limit=2` could answer with one row
// while a second waited on a page nobody asks for.
func TestStaleFilterPaginatesTheFilteredSet(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{
		autoPublish: false, delaySeconds: 0, autoResolve: models.AutoResolveNever,
	})

	// Two stale entries, and three free-form ones interleaved so that a naive
	// "take the newest 2 rows, then filter" would find no stale row at all.
	staleUIDs := make([]string, 0, 2)

	for round := range 2 {
		_, err := s.pubs.CreatePublication(t.Context(), s.org.Slug, s.page.UID, s.userUID,
			&incidentpublications.CreatePublicationRequest{
				Title: "Planned work " + string(rune('A'+round)),
			})
		r.NoError(err)

		incident := liveIncident(t, s)

		pub, err := s.pubs.PublishIncident(t.Context(), s.org.Slug, incident.UID, s.userUID,
			&incidentpublications.PublishIncidentRequest{StatusPageUID: s.page.UID})
		r.NoError(err)

		staleUIDs = append(staleUIDs, pub.UID)

		// `never` keeps the entry open past the recovery — the stale state.
		s.clk.Advance(time.Minute)
		s.submit(models.ResultStatusUp)
		s.clk.Advance(time.Hour) // past the reopen cooldown, so the next round opens a NEW incident
	}

	_, err := s.pubs.CreatePublication(t.Context(), s.org.Slug, s.page.UID, s.userUID,
		&incidentpublications.CreatePublicationRequest{Title: "Planned work C"})
	r.NoError(err)

	all, err := s.pubs.ListPublications(t.Context(), s.org.Slug, s.page.UID,
		incidentpublications.ListOptions{ActiveOnly: true, StaleOnly: true})
	r.NoError(err)
	r.Len(all, 2)
	r.ElementsMatch(staleUIDs, []string{all[0].UID, all[1].UID})

	// The newest publication on the page is free-form, so a limit applied by
	// the DB before filtering would have returned nothing here.
	limited, err := s.pubs.ListPublications(t.Context(), s.org.Slug, s.page.UID,
		incidentpublications.ListOptions{ActiveOnly: true, StaleOnly: true, Limit: 1})
	r.NoError(err)
	r.Len(limited, 1, "limit must cut the FILTERED set, not the set it was drawn from")
	r.Equal(all[0].UID, limited[0].UID)

	paged, err := s.pubs.ListPublications(t.Context(), s.org.Slug, s.page.UID,
		incidentpublications.ListOptions{ActiveOnly: true, StaleOnly: true, Limit: 1, Offset: 1})
	r.NoError(err)
	r.Len(paged, 1)
	r.Equal(all[1].UID, paged[0].UID, "offset walks the filtered set too")

	past, err := s.pubs.ListPublications(t.Context(), s.org.Slug, s.page.UID,
		incidentpublications.ListOptions{ActiveOnly: true, StaleOnly: true, Offset: 9})
	r.NoError(err)
	r.Empty(past, "an offset past the end is empty, not an error")
}

// TestHandPublishedGroupEntryClosesWithTheLastSibling covers the consolidated
// case (spec 2026-08-24-14 crossed with this one).
//
// A group's public entry hangs off whichever member's incident owns it, so when
// a DIFFERENT member is the last to recover, auto-resolve has to reach the
// entry through groupSiblingPublications. That helper filtered on
// `auto_created`, which re-created — for this one path — exactly the hole
// widening OnIncidentResolved closed everywhere else: a hand-published entry
// owning a group outage would never close.
func TestHandPublishedGroupEntryClosesWithTheLastSibling(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	probe, group, members := groupProbe(t, "Payments worker")

	// Auto-publish off: the ONLY entry on this page must be the hand-published
	// one, or the test would be watching a machine-minted twin instead.
	autoPublish := false
	r.NoError(probe.dbSvc.UpdateStatusPage(ctx, probe.page.UID,
		&models.StatusPageUpdate{AutoPublish: &autoPublish}))

	owner := openIncident(t, probe, probe.check, "payments-api is down")
	r.Empty(probe.publications(), "auto-publish is off; nothing publishes itself")

	pub, err := probe.pubs.PublishIncident(ctx, probe.org.Slug, owner.UID, probe.userUID,
		&incidentpublications.PublishIncidentRequest{StatusPageUID: probe.page.UID})
	r.NoError(err)
	r.False(pub.AutoCreated)

	probe.clk.Advance(20 * time.Second)

	sibling := openIncident(t, probe, members[0], "payments-member-a is down")

	// The owner recovers FIRST. The entry must stay open — a sibling is still
	// down, and announcing "resolved" over a live outage is the worse error.
	probe.clk.Advance(time.Minute)
	resolveIncident(t, probe, owner)

	pubs := probe.publications()
	r.Len(pubs, 1)
	r.NotEqual(models.PublicationStateResolved, pubs[0].PublicState,
		"a consolidated entry closes with the LAST member, not the first")

	// Now the last member recovers. Its own incident has no publication, so the
	// entry can only be reached through the group sibling walk.
	probe.clk.Advance(time.Minute)
	resolveIncident(t, probe, sibling)

	pubs = probe.publications()
	r.Len(pubs, 1)
	r.Equal(models.PublicationStateResolved, pubs[0].PublicState,
		"the last member's recovery closes the hand-published group entry")
	r.NotNil(pubs[0].ResolvedAt)

	_ = group
}
