package incidentpublications_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentpublications"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/testsupport"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// portCheckDeletedPubsPG is unique among the embedded-Postgres ports of the
// repo, so the non-short suite can run it next to the others.
const portCheckDeletedPubsPG = 15877

// delSetup is one org with one status page, wired the way production is:
// the incident service carries the publication hook. Every scenario builds its
// own org, so the Postgres run can share one database.
type delSetup struct {
	t         *testing.T
	db        db.Service
	clk       *clock.Fake
	pubs      *incidentpublications.Service
	incidents *incidents.Service
	org       *models.Organization
	page      *models.StatusPage
	section   *models.StatusPageSection
	userUID   string
}

func newDelSetup(t *testing.T, dbSvc db.Service, policy models.AutoResolvePolicy) *delSetup {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	slug := "acme-" + uuid.NewString()[:8]

	org := models.NewOrganization(slug, "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser(slug + "@acme.com")
	r.NoError(dbSvc.CreateUser(ctx, user))

	page := models.NewStatusPage(org.UID, "Acme status", "public")
	page.AutoPublish = true
	page.AutoPublishDelaySeconds = 0
	page.AutoResolve = string(policy)
	r.NoError(dbSvc.CreateStatusPage(ctx, page))

	section := models.NewStatusPageSection(page.UID, "Services", "services", 1)
	r.NoError(dbSvc.CreateStatusPageSection(ctx, section))

	clk := clock.NewFake(time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC))
	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

	pubs := incidentpublications.NewService(dbSvc, clk, nil)
	incSvc := incidents.NewService(dbSvc, jobs, clk, nil)
	incSvc.SetPublicationHook(pubs)

	return &delSetup{
		t: t, db: dbSvc, clk: clk, pubs: pubs, incidents: incSvc,
		org: org, page: page, section: section, userUID: user.UID,
	}
}

// addCheck creates a check displayed on the page under a public name.
func (s *delSetup) addCheck(slug, publicName string, groupUID *string) *models.Check {
	s.t.Helper()

	ctx := s.t.Context()
	check := models.NewCheck(s.org.UID, slug, "http")
	check.CheckGroupUID = groupUID
	require.NoError(s.t, s.db.CreateCheck(ctx, check))

	resource := models.NewStatusPageResource(s.section.UID, check.UID, 1)
	resource.PublicName = &publicName
	require.NoError(s.t, s.db.CreateStatusPageResource(ctx, resource))

	return check
}

// open opens a per-check incident and publishes it, as the state machine does.
func (s *delSetup) open(check *models.Check) *models.Incident {
	s.t.Helper()

	incident := models.NewIncident(s.org.UID, check.UID, s.clk.Now(), check.UID+" is down")
	require.NoError(s.t, s.db.CreateIncident(s.t.Context(), incident))
	s.pubs.OnIncidentOpened(s.t.Context(), incident)
	s.clk.Advance(time.Minute)

	return incident
}

// resolveSilently resolves an incident without running any publication hook,
// as a recovery under `auto_resolve = never` leaves it.
func (s *delSetup) resolveSilently(incident *models.Incident) {
	s.t.Helper()

	now := s.clk.Now()
	state := models.IncidentStateResolved
	require.NoError(s.t, s.db.UpdateIncident(s.t.Context(), incident.UID, &models.IncidentUpdate{
		State: &state, ResolvedAt: &now,
	}))
}

// softDeleteOnly is the DB half of a check deletion, without the notification:
// what a delete did before the publication hook existed (or a crash between
// the two leaves). The DB layer still resolves the check's active incidents.
func (s *delSetup) softDeleteOnly(check *models.Check) {
	s.t.Helper()

	require.NoError(s.t, s.db.DeleteCheck(s.t.Context(), check.UID))
}

// deleteCheck is today's full path: the soft delete, then the notification of
// the incidents it resolved.
func (s *delSetup) deleteCheck(check *models.Check) {
	s.t.Helper()

	ctx := s.t.Context()

	var uids []string
	if active, err := s.db.FindActiveIncidentByCheckUID(ctx, check.UID); err == nil && active != nil {
		uids = append(uids, active.UID)
	}

	require.NoError(s.t, s.db.DeleteCheck(ctx, check.UID))
	s.incidents.NotifyCheckDeletedIncidents(ctx, s.org.UID, check, uids)
}

func (s *delSetup) touch(pub *models.IncidentPublication) {
	s.t.Helper()

	title := "We are looking into payments"
	_, err := s.pubs.UpdatePublication(s.t.Context(), s.org.Slug, s.page.UID, pub.UID,
		s.userUID, &incidentpublications.UpdatePublicationRequest{Title: &title})
	require.NoError(s.t, err)
}

func (s *delSetup) publications() []*models.IncidentPublication {
	s.t.Helper()

	pubs, err := s.db.ListIncidentPublications(s.t.Context(), &models.ListIncidentPublicationsFilter{
		OrganizationUID: s.org.UID,
		StatusPageUID:   s.page.UID,
		Limit:           100,
	})
	require.NoError(s.t, err)

	return pubs
}

func (s *delSetup) onlyPublication() *models.IncidentPublication {
	s.t.Helper()

	pubs := s.publications()
	require.Len(s.t, pubs, 1)

	return pubs[0]
}

func (s *delSetup) updates(pub *models.IncidentPublication) []*models.StatusUpdate {
	s.t.Helper()

	updates, err := s.db.ListStatusUpdates(s.t.Context(), s.org.UID, models.StatusUpdatesFilter{
		StatusPageUID:          pub.StatusPageUID,
		IncidentPublicationUID: &pub.UID,
		Limit:                  200,
	})
	require.NoError(s.t, err)

	return updates
}

func (s *delSetup) kinds(pub *models.IncidentPublication) []models.StatusUpdateKind {
	s.t.Helper()

	out := []models.StatusUpdateKind{}
	for _, upd := range s.updates(pub) {
		out = append(out, upd.Kind)
	}

	return out
}

// hasRemovedUpdate reports whether a "the monitored resource was removed"
// resolved update was posted on the publication.
func (s *delSetup) hasRemovedUpdate(pub *models.IncidentPublication) bool {
	s.t.Helper()

	for _, upd := range s.updates(pub) {
		if upd.Kind == models.StatusUpdateKindResolved &&
			strings.Contains(upd.BodyMarkdown, "monitored resource was removed") {
			return true
		}
	}

	return false
}

// runCheckDeletedScenarios is the spec's test list (2026-11-05-05), run on
// whichever database newDB builds.
func runCheckDeletedScenarios(t *testing.T, newDB func(t *testing.T) db.Service) {
	t.Helper()

	t.Run("sweep_closes_entry_of_deleted_check", func(t *testing.T) {
		r := require.New(t)
		s := newDelSetup(t, newDB(t), models.AutoResolveIfUntouched)

		check := s.addCheck("payments-api", "Payments API", nil)
		s.open(check)
		s.softDeleteOnly(check)

		pub := s.onlyPublication()
		r.False(pub.IsResolved(), "precondition: the pre-hook delete left the entry open")

		closed, err := s.pubs.CloseOrphanedPublications(t.Context())
		r.NoError(err)
		r.GreaterOrEqual(closed, 1)

		pub = s.onlyPublication()
		r.True(pub.IsResolved())
		r.NotNil(pub.ResolvedAt)
		r.True(s.hasRemovedUpdate(pub))
		r.NotContains(s.kinds(pub), models.StatusUpdateKindMonitoring,
			"a deleted check never recovers: no monitoring note")

		before := len(s.updates(pub))
		_, err = s.pubs.CloseOrphanedPublications(t.Context())
		r.NoError(err)
		r.Len(s.updates(s.onlyPublication()), before, "the sweep is idempotent")
	})

	t.Run("sweep_closes_touched_entry_of_deleted_check", func(t *testing.T) {
		r := require.New(t)
		s := newDelSetup(t, newDB(t), models.AutoResolveNever)

		check := s.addCheck("payments-api", "Payments API", nil)
		incident := s.open(check)
		s.touch(s.onlyPublication())
		s.resolveSilently(incident)
		s.softDeleteOnly(check)

		_, err := s.pubs.CloseOrphanedPublications(t.Context())
		r.NoError(err)
		r.True(s.onlyPublication().IsResolved())
	})

	t.Run("sweep_keeps_entry_of_existing_check", func(t *testing.T) {
		r := require.New(t)
		s := newDelSetup(t, newDB(t), models.AutoResolveNever)

		check := s.addCheck("payments-api", "Payments API", nil)
		incident := s.open(check)
		s.resolveSilently(incident)

		pub := s.onlyPublication()
		before := len(s.updates(pub))

		_, err := s.pubs.CloseOrphanedPublications(t.Context())
		r.NoError(err)

		pub = s.onlyPublication()
		r.False(pub.IsResolved(), "the check still exists: closing the entry is a human's call")
		r.Len(s.updates(pub), before)
	})

	t.Run("deletion_resolves_touched_entry", func(t *testing.T) {
		r := require.New(t)
		s := newDelSetup(t, newDB(t), models.AutoResolveIfUntouched)

		check := s.addCheck("payments-api", "Payments API", nil)
		s.open(check)
		s.touch(s.onlyPublication())

		s.deleteCheck(check)

		pub := s.onlyPublication()
		r.True(pub.IsResolved(), "a deleted check's touched entry is resolved anyway")
		r.True(s.hasRemovedUpdate(pub))
		r.NotContains(s.kinds(pub), models.StatusUpdateKindMonitoring)
	})

	t.Run("deletion_overrides_auto_resolve_never", func(t *testing.T) {
		r := require.New(t)
		s := newDelSetup(t, newDB(t), models.AutoResolveNever)

		check := s.addCheck("payments-api", "Payments API", nil)
		s.open(check)

		s.deleteCheck(check)

		pub := s.onlyPublication()
		r.True(pub.IsResolved())
		r.True(s.hasRemovedUpdate(pub))
	})

	t.Run("group_entry_stays_open_while_a_member_is_down", func(t *testing.T) {
		r := require.New(t)
		dbSvc := newDB(t)
		s := newDelSetup(t, dbSvc, models.AutoResolveIfUntouched)

		group := models.NewCheckGroup(s.org.UID, "Payments platform", "payments-platform")
		r.NoError(dbSvc.CreateCheckGroup(t.Context(), group))

		first := s.addCheck("payments-api", "Payments API", &group.UID)
		second := s.addCheck("payments-worker", "Payments worker", &group.UID)

		s.open(first)
		s.open(second)
		r.Len(s.publications(), 1, "precondition: the members share one consolidated entry")

		// The owner is deleted while its sibling is still down.
		s.deleteCheck(first)
		r.False(s.onlyPublication().IsResolved(), "a covered member is still down")

		_, err := s.pubs.CloseOrphanedPublications(t.Context())
		r.NoError(err)
		r.False(s.onlyPublication().IsResolved(), "the sweep keeps it open too")

		// The last member goes the pre-hook way: only the sweep can close it.
		s.softDeleteOnly(second)

		_, err = s.pubs.CloseOrphanedPublications(t.Context())
		r.NoError(err)

		pub := s.onlyPublication()
		r.True(pub.IsResolved(), "every member is gone: the group entry closes")
		r.True(s.hasRemovedUpdate(pub))
	})

	t.Run("group_entry_closes_when_last_member_is_deleted", func(t *testing.T) {
		r := require.New(t)
		dbSvc := newDB(t)
		s := newDelSetup(t, dbSvc, models.AutoResolveNever)

		group := models.NewCheckGroup(s.org.UID, "Payments platform", "payments-platform")
		r.NoError(dbSvc.CreateCheckGroup(t.Context(), group))

		first := s.addCheck("payments-api", "Payments API", &group.UID)
		second := s.addCheck("payments-worker", "Payments worker", &group.UID)

		firstIncident := s.open(first)
		s.open(second)

		// The owner recovered (never policy: the entry stays open), then the
		// remaining member is deleted.
		s.resolveSilently(firstIncident)
		s.pubs.OnIncidentResolved(t.Context(), firstIncident)
		r.False(s.onlyPublication().IsResolved())

		s.deleteCheck(second)
		r.True(s.onlyPublication().IsResolved())
	})
}

func TestStatusPageEntriesOfDeletedChecks(t *testing.T) {
	t.Parallel()

	runCheckDeletedScenarios(t, func(t *testing.T) db.Service {
		t.Helper()

		dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
		require.NoError(t, err)
		require.NoError(t, dbSvc.Initialize(t.Context()))
		t.Cleanup(func() { _ = dbSvc.Close() })

		return dbSvc
	})
}

func TestStatusPageEntriesOfDeletedChecks_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portCheckDeletedPubsPG,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}
	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	runCheckDeletedScenarios(t, func(*testing.T) db.Service { return dbSvc })
}
