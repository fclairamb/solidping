package incidentpublications_test

// Degraded incidents do not auto-publish (spec 2026-09-22-03). "7 of the last 60
// probes failed, currently up" is an internal operations signal; a page whose
// operator opted in to announcing OUTAGES has not thereby agreed to announce
// intermittence. The opt-in is a second flag, and this is what pins that the two
// are really independent.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

// openDegraded opens a degraded incident on the setup's check.
func openDegraded(t *testing.T, s *pubSetup) *models.Incident {
	t.Helper()

	pct := 88.3

	incident, err := s.incidents.OpenDegradedIncident(t.Context(), &incidents.OpenDegradedIncidentRequest{
		Check:     s.check,
		StartedAt: s.clk.Now().Add(-30 * time.Minute),
		Title:     "payments-api is degraded: 7 failures in the last 60 probes (88.3%)",
		Snapshot: &incidents.DegradedSnapshot{
			Failures: 5, FailuresWindow: 60, FailureSlots: 60, FailureMatches: 7,
			FailuresFired: true, AvailabilityPct: &pct, ResolveWindow: 60, CurrentlyUp: true,
		},
	})
	require.NoError(t, err)

	return incident
}

func publicationCount(t *testing.T, s *pubSetup, orgUID string) int {
	t.Helper()

	pubs, err := s.dbSvc.ListIncidentPublications(t.Context(), &models.ListIncidentPublicationsFilter{
		OrganizationUID: orgUID,
		Limit:           100,
	})
	require.NoError(t, err)

	return len(pubs)
}

// TestDegradedIncidentDoesNotAutoPublish pins the default: auto-publish ON,
// publish_degraded off (which is every page, including new ones) means a degraded
// incident reaches nobody's customers.
func TestDegradedIncidentDoesNotAutoPublish(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newPubSetup(t, setupOptions{autoPublish: true})

	openDegraded(t, s)

	r.Zero(publicationCount(t, s, s.org.UID),
		"a degraded incident must not publish on a page that only opted in to outages")

	// Positive control: the same page, the same resource, a REAL outage — which
	// does publish. Without this a broken fixture (no resource wired, page
	// disabled) would make the zero above meaningless.
	s.submit(models.ResultStatusDown)

	r.Equal(1, publicationCount(t, s, s.org.UID),
		"auto-publish really is on for this page — the skip above is about the kind")
}

// TestDegradedIncidentPublishesWhenPageOptsIn pins the other half: a page that
// explicitly asked for degraded incidents gets them.
func TestDegradedIncidentPublishesWhenPageOptsIn(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newPubSetup(t, setupOptions{autoPublish: true})

	optIn := true
	r.NoError(s.dbSvc.UpdateStatusPage(t.Context(), s.page.UID, &models.StatusPageUpdate{
		PublishDegraded: &optIn,
	}))

	openDegraded(t, s)

	r.Equal(1, publicationCount(t, s, s.org.UID),
		"the opt-in is what makes a degraded incident public")
}

// TestDegradedOptInDoesNotOverrideAutoPublishOff pins the ordering: the degraded
// opt-in is an additional gate, not a bypass. Turning auto-publish off silences
// everything, whatever publish_degraded says.
func TestDegradedOptInDoesNotOverrideAutoPublishOff(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newPubSetup(t, setupOptions{autoPublish: false})

	optIn := true
	r.NoError(s.dbSvc.UpdateStatusPage(t.Context(), s.page.UID, &models.StatusPageUpdate{
		PublishDegraded: &optIn,
	}))

	openDegraded(t, s)

	r.Zero(publicationCount(t, s, s.org.UID),
		"auto-publish off means nothing publishes, degraded opt-in included")
}
