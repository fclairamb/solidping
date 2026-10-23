package dbcontract

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func runIncidentMembers(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	check := models.NewCheck(fix.org.UID, "incident-check", "http")
	member := models.NewCheck(fix.org.UID, "incident-member", "http")
	must.NoError(svc.CreateCheck(ctx, check))
	must.NoError(svc.CreateCheck(ctx, member))

	incident := models.NewIncident(fix.org.UID, check.UID, time.Now(), "contract incident")
	must.NoError(svc.CreateIncident(ctx, incident))

	got, err := svc.GetIncidentAny(ctx, incident.UID)
	must.NoError(err)
	must.Equal(incident.UID, got.UID)

	active, err := svc.CountActiveIncidentsByCheckUID(ctx, check.UID)
	must.NoError(err)
	must.Equal(1, active)

	now := time.Now()
	row := &models.IncidentMemberCheck{
		IncidentUID: incident.UID, CheckUID: member.UID,
		JoinedAt: now, FirstFailureAt: now, LastFailureAt: now,
		FailureCount: 1, CurrentlyFailing: true,
	}
	must.NoError(svc.UpsertIncidentMemberCheck(ctx, row))

	failing, err := svc.CountFailingIncidentMembers(ctx, incident.UID)
	must.NoError(err)
	must.Equal(1, failing)

	one, err := svc.GetIncidentMemberCheck(ctx, incident.UID, member.UID)
	must.NoError(err)
	must.Equal(1, one.FailureCount)

	count := 4
	notFailing := false
	recovered := time.Now()
	must.NoError(svc.UpdateIncidentMemberCheck(ctx, incident.UID, member.UID, &models.IncidentMemberUpdate{
		FailureCount: &count, CurrentlyFailing: &notFailing, LastRecoveryAt: &recovered,
	}))

	failing, err = svc.CountFailingIncidentMembers(ctx, incident.UID)
	must.NoError(err)
	must.Zero(failing)

	byUIDs, err := svc.ListIncidentMemberChecksByIncidentUIDs(ctx, []string{incident.UID})
	must.NoError(err)
	must.Len(byUIDs[incident.UID], 1)
	must.Equal(4, byUIDs[incident.UID][0].FailureCount)

	incidentSnooze(t, fix, incident)
}

func incidentSnooze(t *testing.T, fix *fixture, incident *models.Incident) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	// A snooze in the past is reported as expired.
	snoozedUntil := time.Now().Add(-time.Minute)
	must.NoError(svc.UpdateIncident(ctx, incident.UID, &models.IncidentUpdate{SnoozedUntil: &snoozedUntil}))

	expired, err := svc.ListExpiredSnoozedIncidents(ctx, time.Now())
	must.NoError(err)

	found := false

	for _, inc := range expired {
		if inc.UID == incident.UID {
			found = true
		}
	}

	must.True(found, "the snoozed incident is listed once its snooze has passed")
}

func runEventsAndLabels(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	old := models.NewEvent(fix.org.UID, models.EventTypeCheckCreated, models.ActorTypeSystem)
	old.CreatedAt = time.Now().Add(-48 * time.Hour)
	recent := models.NewEvent(fix.org.UID, models.EventTypeCheckUpdated, models.ActorTypeSystem)
	must.NoError(svc.CreateEvent(ctx, old))
	must.NoError(svc.CreateEvent(ctx, recent))

	must.NoError(svc.UpdateEventPayload(ctx, recent.UID, models.JSONMap{"target_name": "renamed"}))

	deleted, err := svc.DeleteEventsBefore(ctx, time.Now().Add(-24*time.Hour), 100)
	must.NoError(err)
	must.Equal(int64(1), deleted)

	events, err := svc.ListEvents(ctx, &models.ListEventsFilter{OrganizationUID: fix.org.UID})
	must.NoError(err)
	must.Len(events, 1)
	must.Equal(recent.UID, events[0].UID)

	// Labels: suggestions over keys and values.
	env, err := svc.GetOrCreateLabel(ctx, fix.org.UID, "env", "prod")
	must.NoError(err)

	envStaging, err := svc.GetOrCreateLabel(ctx, fix.org.UID, "env", "staging")
	must.NoError(err)

	must.NoError(svc.SetCheckLabels(ctx, fix.check.UID, []string{env.UID, envStaging.UID}))

	keys, err := svc.ListDistinctLabelKeys(ctx, fix.org.UID, "en", 10)
	must.NoError(err)
	must.NotEmpty(keys)

	values, err := svc.ListDistinctLabelValues(ctx, fix.org.UID, "env", "st", 10)
	must.NoError(err)
	must.Len(values, 1)

	noMatch, err := svc.ListDistinctLabelKeys(ctx, fix.org.UID, "zzz", 10)
	must.NoError(err)
	must.Empty(noMatch)
}

func runBulkResults(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	check := models.NewCheck(fix.org.UID, "bulk-results", "http")
	must.NoError(svc.CreateCheck(ctx, check))

	results := make([]*models.Result, 0, 5)
	for i := range 5 {
		res := models.NewResult(fix.org.UID, check.UID, models.ResultStatusUp, float32(i+1))
		res.PeriodStart = time.Now().Add(-time.Duration(i) * time.Minute)
		results = append(results, res)
	}

	must.NoError(svc.CreateResults(ctx, results))

	listed, err := svc.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: fix.org.UID, CheckUIDs: []string{check.UID},
		Statuses: []int{int(models.ResultStatusUp)},
	})
	must.NoError(err)
	must.Len(listed.Results, 5)
}
