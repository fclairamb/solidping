package incidentpublications_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentpublications"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/handlers/statusupdates"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// sentinelHostname is planted in the probe output of every failing result these
// tests submit. It stands in for the internal hostnames, IPs and stack
// fragments real probe output carries.
//
// The security invariant of this whole feature is that it NEVER appears in a
// public field. Every negative assertion below greps for exactly this string,
// and the positive control (TestSentinelIsVisibleOnTheInternalIncident) proves
// the sentinel really did reach the incident — so a negative that passes
// because nothing was ever recorded would fail loudly instead of quietly.
const sentinelHostname = "db-primary-07.internal.acme.corp"

// --- Harness -----------------------------------------------------------------

type pubSetup struct {
	t          *testing.T
	dbSvc      *sqlite.Service
	clk        *clock.Fake
	incidents  *incidents.Service
	pubs       *incidentpublications.Service
	org        *models.Organization
	page       *models.StatusPage
	section    *models.StatusPageSection
	check      *models.Check
	resource   *models.StatusPageResource
	notifier   *countingNotifier
	scheduler  *recordingScheduler
	publicName string
	// userUID is a REAL user row. status_updates.author_uid and events.actor_uid
	// both carry a foreign key to users(uid), so a made-up s.userUID would fail
	// the insert and silently turn every human-authored assertion into a
	// no-op.
	userUID string
}

// countingNotifier records fan-out waves instead of sending mail, and keeps the
// rendered payload so tests can grep it for the sentinel.
type countingNotifier struct {
	events chan *statusupdates.SubscriberUpdateEvent
}

func newCountingNotifier() *countingNotifier {
	return &countingNotifier{events: make(chan *statusupdates.SubscriberUpdateEvent, 64)}
}

func (n *countingNotifier) NotifyStatusUpdate(_ context.Context, ev *statusupdates.SubscriberUpdateEvent) {
	n.events <- ev
}

// drain collects every event delivered within a short grace window. Fan-out is
// intentionally detached (a goroutine), so a bounded wait is the honest way to
// observe it; the window is generous because CI is slow, and the assertions are
// on counts, not on timing.
func (n *countingNotifier) drain() []*statusupdates.SubscriberUpdateEvent {
	var out []*statusupdates.SubscriberUpdateEvent

	deadline := time.After(2 * time.Second)

	for {
		select {
		case ev := <-n.events:
			out = append(out, ev)
		case <-time.After(150 * time.Millisecond):
			return out
		case <-deadline:
			return out
		}
	}
}

// recordingScheduler stands in for the job queue: it records what the policy
// asked to be scheduled and when, without running it. Tests then call
// AutoPublish themselves at whatever simulated time they want, which is exactly
// what the real job does at fire time.
type recordingScheduler struct {
	calls []schedulerCall
}

type schedulerCall struct {
	incidentUID   string
	statusPageUID string
	fireAt        time.Time
}

func (s *recordingScheduler) SchedulePublish(
	_ context.Context, _, incidentUID, statusPageUID string, fireAt time.Time,
) error {
	s.calls = append(s.calls, schedulerCall{incidentUID, statusPageUID, fireAt})

	return nil
}

type setupOptions struct {
	autoPublish  bool
	delaySeconds int
	autoResolve  models.AutoResolvePolicy
	// withScheduler leaves the debounce in place. Without it, the policy
	// publishes inline, which is what a delay-0 page does in production too.
	withScheduler bool
	// noResource omits the status-page resource, so the check is displayed
	// nowhere.
	noResource bool
	// groupResource points the page resource at the check's GROUP rather than
	// at the check.
	groupResource bool
	groupUID      string
}

func newPubSetup(t *testing.T, opts setupOptions) *pubSetup {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	clk := clock.NewFake(time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC))
	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("alice@acme.com")
	r.NoError(dbSvc.CreateUser(ctx, user))

	page := models.NewStatusPage(org.UID, "Acme status", "public")
	page.AutoPublish = opts.autoPublish
	page.AutoPublishDelaySeconds = opts.delaySeconds

	if opts.autoResolve == "" {
		opts.autoResolve = models.AutoResolveIfUntouched
	}

	page.AutoResolve = string(opts.autoResolve)
	r.NoError(dbSvc.CreateStatusPage(ctx, page))

	section := models.NewStatusPageSection(page.UID, "Services", "services", 1)
	r.NoError(dbSvc.CreateStatusPageSection(ctx, section))

	check := models.NewCheck(org.UID, "payments-api", "http")
	check.Status = models.CheckStatusUp
	check.ConfirmationPeriodSeconds = 0
	check.RecoveryPeriodSeconds = 0

	if opts.groupUID != "" {
		check.CheckGroupUID = &opts.groupUID
	}

	r.NoError(dbSvc.CreateCheck(ctx, check))

	publicName := "Payments API"

	var resource *models.StatusPageResource

	if !opts.noResource {
		if opts.groupResource {
			resource = models.NewStatusPageGroupResource(section.UID, opts.groupUID, 1)
		} else {
			resource = models.NewStatusPageResource(section.UID, check.UID, 1)
		}

		resource.PublicName = &publicName
		r.NoError(dbSvc.CreateStatusPageResource(ctx, resource))
	}

	notif := newCountingNotifier()
	pubs := incidentpublications.NewService(dbSvc, clk, nil)
	pubs.SetSubscriberNotifier(notif)

	sched := &recordingScheduler{}
	if opts.withScheduler {
		pubs.SetScheduler(sched)
	}

	incSvc := incidents.NewService(dbSvc, jobs, clk, nil)
	incSvc.SetPublicationHook(pubs)

	return &pubSetup{
		t: t, dbSvc: dbSvc, clk: clk, incidents: incSvc, pubs: pubs,
		org: org, page: page, section: section, check: check, resource: resource,
		notifier: notif, scheduler: sched, publicName: publicName, userUID: user.UID,
	}
}

// submit pushes one probe result through the incident state machine. Every
// failing result carries the sentinel hostname in its probe output.
func (s *pubSetup) submit(status models.ResultStatus) {
	s.t.Helper()

	result := models.NewResult(s.org.UID, s.check.UID, status, 12)
	result.PeriodStart = s.clk.Now()

	if status != models.ResultStatusUp {
		result.Output["error"] = "dial tcp " + sentinelHostname + ":5432: connection refused"
		result.Output["body"] = "upstream " + sentinelHostname + " unreachable"
	}

	require.NoError(s.t, s.dbSvc.CreateResult(s.t.Context(), result))
	require.NoError(s.t, s.incidents.ProcessCheckResult(context.Background(), s.check, result))

	// Keep the in-memory check in step with the persisted row, exactly as the
	// executor does between results.
	reloaded, err := s.dbSvc.GetCheck(s.t.Context(), s.org.UID, s.check.UID)
	require.NoError(s.t, err)
	s.check = reloaded
}

func (s *pubSetup) publications() []*models.IncidentPublication {
	s.t.Helper()

	pubs, err := s.dbSvc.ListIncidentPublications(s.t.Context(), &models.ListIncidentPublicationsFilter{
		OrganizationUID: s.org.UID,
		StatusPageUID:   s.page.UID,
		Limit:           100,
	})
	require.NoError(s.t, err)

	return pubs
}

func (s *pubSetup) activeIncident() *models.Incident {
	s.t.Helper()

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(s.t.Context(), s.check.UID)
	if err != nil {
		return nil
	}

	return inc
}

// publicJSON renders the public projection of the page and returns it as one
// searchable blob. Anything the customer can see is in here.
func (s *pubSetup) publicJSON() string {
	s.t.Helper()

	incidents, err := s.pubs.ListPublicIncidents(s.t.Context(), s.page, false)
	require.NoError(s.t, err)

	raw, err := json.Marshal(incidents)
	require.NoError(s.t, err)

	return string(raw)
}

// --- The security invariant --------------------------------------------------

// TestProbeOutputNeverReachesPublicFields is the security test the spec calls
// out: an incident opens, publishes, resolves and publishes its resolution —
// and at no point does the sentinel hostname planted in the probe output appear
// in the public JSON or in anything handed to the subscriber notifier.
func TestProbeOutputNeverReachesPublicFields(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0})

	s.submit(models.ResultStatusDown)

	pubs := s.publications()
	r.Len(pubs, 1, "an incident on an auto-publish page publishes")
	r.True(pubs[0].AutoCreated)
	r.Equal(models.PublicationStateInvestigating, pubs[0].PublicState)
	r.Contains(pubs[0].PublicTitle, s.publicName,
		"the title is templated from the resource's PUBLIC name")

	// Resolve, which posts a second templated update.
	s.clk.Advance(time.Minute)
	s.submit(models.ResultStatusUp)

	public := s.publicJSON()
	r.NotContains(public, sentinelHostname,
		"probe output must never reach the public status-page payload")
	r.NotContains(public, "connection refused")
	r.NotContains(public, *s.check.Slug,
		"the check slug is internal vocabulary and must not leak either")

	// The Atom feed reads the same status_updates rows the auto-publisher
	// writes, so it is a second public surface that has to be checked
	// independently — a leak that only shows up in the feed is still a leak.
	feed, err := s.dbSvc.ListPublicStatusUpdates(t.Context(), s.page.UID, s.page.HistoryDays)
	r.NoError(err)
	r.NotEmpty(feed, "the auto-generated updates DO reach the feed…")

	var feedText strings.Builder
	for _, entry := range feed {
		feedText.WriteString(entry.Title)
		feedText.WriteString("\n")
		feedText.WriteString(entry.BodyMarkdown)
		feedText.WriteString("\n")
	}

	r.NotContains(feedText.String(), sentinelHostname, "…without the probe output")
	r.NotContains(feedText.String(), "connection refused")
	r.NotContains(feedText.String(), *s.check.Slug)

	// Subscriber mail is the third public surface. Guarding on non-emptiness
	// first is what stops this leg from passing vacuously: a fan-out that never
	// fired would satisfy every NotContains below without proving anything.
	waves := s.notifier.drain()
	r.NotEmpty(waves, "the auto-generated updates DO reach the subscriber fan-out…")

	for _, ev := range waves {
		r.NotContains(ev.Title, sentinelHostname, "…without the probe output")
		r.NotContains(ev.BodyMarkdown, sentinelHostname)
		r.NotContains(ev.BodyMarkdown, "connection refused")
		r.NotContains(ev.Title, *s.check.Slug)
		r.NotContains(ev.BodyMarkdown, *s.check.Slug)
	}
}

// TestSentinelIsVisibleOnTheInternalIncident is the POSITIVE CONTROL for the
// test above. Without it, a leak test could pass simply because the sentinel
// was never recorded anywhere — which would make the whole guarantee vacuous.
func TestSentinelIsVisibleOnTheInternalIncident(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0})

	s.submit(models.ResultStatusDown)

	incident := s.activeIncident()
	r.NotNil(incident)

	raw, err := json.Marshal(incident.Details)
	r.NoError(err)
	r.Contains(string(raw), sentinelHostname,
		"positive control: the sentinel DID reach the internal incident, so the "+
			"public-side negatives above are meaningful")
}

// --- Debounce ----------------------------------------------------------------

// TestBlipInsideDelayNeverPublishes proves the debounce swallows a short
// outage: the job is scheduled, the incident resolves before it fires, and the
// job then publishes nothing.
func TestBlipInsideDelayNeverPublishes(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{
		autoPublish: true, delaySeconds: 60, withScheduler: true,
	})

	s.submit(models.ResultStatusDown)
	r.Len(s.scheduler.calls, 1, "a delayed page schedules the debounce job")
	r.Empty(s.publications(), "nothing is published before the job fires")

	// The blip: recovery 40 s later, inside the 60 s delay.
	s.clk.Advance(40 * time.Second)
	s.submit(models.ResultStatusUp)

	// Now the job fires.
	s.clk.Advance(30 * time.Second)
	call := s.scheduler.calls[0]
	r.NoError(s.pubs.AutoPublish(t.Context(), s.org.UID, call.incidentUID, call.statusPageUID))

	r.Empty(s.publications(),
		"an incident that resolved inside the delay must never reach the public page")
}

// TestDelayZeroPublishesImmediately is the POSITIVE CONTROL for the blip test:
// the exact same scenario with delay 0 does produce a publication, so the empty
// result above is the debounce working, not the pipeline being broken.
func TestDelayZeroPublishesImmediately(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0})

	s.submit(models.ResultStatusDown)

	r.Len(s.publications(), 1, "positive control: delay 0 publishes immediately")
}

// TestAutoPublishIsIdempotent proves the unique index and the read guard hold:
// firing the debounce job twice — and racing a manual publish against it —
// yields exactly one publication.
func TestAutoPublishIsIdempotent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{
		autoPublish: true, delaySeconds: 60, withScheduler: true,
	})

	s.submit(models.ResultStatusDown)
	r.Len(s.scheduler.calls, 1)

	call := s.scheduler.calls[0]
	s.clk.Advance(90 * time.Second)

	r.NoError(s.pubs.AutoPublish(t.Context(), s.org.UID, call.incidentUID, call.statusPageUID))
	r.NoError(s.pubs.AutoPublish(t.Context(), s.org.UID, call.incidentUID, call.statusPageUID))
	r.Len(s.publications(), 1, "a second job fire must not mint a second public incident")

	// A manual publish of the same incident onto the same page is a CONFLICT,
	// not a silent duplicate.
	_, err := s.pubs.PublishIncident(t.Context(), s.org.Slug, call.incidentUID, s.userUID,
		&incidentpublications.PublishIncidentRequest{StatusPageUID: s.page.UID})
	r.ErrorIs(err, incidentpublications.ErrAlreadyPublished)
	r.Len(s.publications(), 1)
}

// --- Policy skips ------------------------------------------------------------

// TestMaintenanceWindowSuppressesPublication proves planned work is never
// announced as an incident.
func TestMaintenanceWindowSuppressesPublication(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0})

	window := models.NewMaintenanceWindow(s.org.UID, "Planned upgrade",
		s.clk.Now().Add(-time.Hour), s.clk.Now().Add(time.Hour))
	r.NoError(s.dbSvc.CreateMaintenanceWindow(t.Context(), window))
	r.NoError(s.dbSvc.SetMaintenanceWindowChecks(t.Context(), window.UID, []string{s.check.UID}, nil))

	// The incident state machine itself skips a check in maintenance, so drive
	// the publication policy directly with an incident that exists.
	incident := models.NewIncident(s.org.UID, s.check.UID, s.clk.Now(), "payments-api is down")
	r.NoError(s.dbSvc.CreateIncident(t.Context(), incident))

	s.pubs.OnIncidentOpened(t.Context(), incident)
	r.Empty(s.publications(), "a check inside a maintenance window must not publish")

	r.NoError(s.pubs.AutoPublish(t.Context(), s.org.UID, incident.UID, s.page.UID))
	r.Empty(s.publications(), "…and the job re-checks it at fire time too")
}

// TestPagingSuppressedMemberDoesNotPublish proves a rolled-up member incident
// stays private: the parent is what customers hear about, or one outage would
// produce a wall of public incidents.
func TestPagingSuppressedMemberDoesNotPublish(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0})

	incident := models.NewIncident(s.org.UID, s.check.UID, s.clk.Now(), "payments-api is down")
	incident.PagingSuppressed = true
	r.NoError(s.dbSvc.CreateIncident(t.Context(), incident))

	s.pubs.OnIncidentOpened(t.Context(), incident)
	r.Empty(s.publications(), "a paging-suppressed member incident must not publish")

	// Positive control: the same incident, not suppressed, does publish. The
	// flag has to be cleared on the ROW as well as in memory — AutoPublish
	// re-reads the incident at fire time, which is the whole point of the
	// fire-time re-check.
	incident.PagingSuppressed = false
	notSuppressed := false
	r.NoError(s.dbSvc.UpdateIncident(t.Context(), incident.UID,
		&models.IncidentUpdate{PagingSuppressed: &notSuppressed}))
	s.pubs.OnIncidentOpened(t.Context(), incident)
	r.Len(s.publications(), 1,
		"positive control: without paging_suppressed the same incident publishes")
}

// TestAutoPublishOffPublishesNothing covers the page switch and its
// three-state per-resource override.
func TestAutoPublishOffPublishesNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: false, delaySeconds: 0})

	s.submit(models.ResultStatusDown)
	r.Empty(s.publications(), "a page with auto-publish off publishes nothing")
}

// TestResourceOverrideBeatsThePage proves the override is three-state: an
// explicit true opts a component in on an off page, and an explicit false opts
// one out of an on page.
func TestResourceOverrideBeatsThePage(t *testing.T) {
	t.Parallel()

	t.Run("explicit true on an off page publishes", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		s := newPubSetup(t, setupOptions{autoPublish: false, delaySeconds: 0})

		enabled := true
		r.NoError(s.dbSvc.UpdateStatusPageResource(t.Context(), s.resource.UID,
			&models.StatusPageResourceUpdate{SetAutoPublish: true, AutoPublish: &enabled}))

		s.submit(models.ResultStatusDown)
		r.Len(s.publications(), 1)
	})

	t.Run("explicit false on an on page publishes nothing", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		s := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0})

		disabled := false
		r.NoError(s.dbSvc.UpdateStatusPageResource(t.Context(), s.resource.UID,
			&models.StatusPageResourceUpdate{SetAutoPublish: true, AutoPublish: &disabled}))

		s.submit(models.ResultStatusDown)
		r.Empty(s.publications())
	})
}

// TestCheckNotOnAnyPagePublishesNothing pins the obvious but load-bearing case:
// a check nobody displays produces no public incident anywhere.
func TestCheckNotOnAnyPagePublishesNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0, noResource: true})

	s.submit(models.ResultStatusDown)
	r.Empty(s.publications())
}

// --- Resolve policy ----------------------------------------------------------

// TestAutoResolveIfUntouched covers both halves of the default policy: an
// untouched publication resolves itself, and a human-edited twin does not.
func TestAutoResolveIfUntouched(t *testing.T) {
	t.Parallel()

	t.Run("untouched resolves with a resolved update", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		s := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0})

		s.submit(models.ResultStatusDown)
		r.Len(s.publications(), 1)

		s.clk.Advance(5 * time.Minute)
		s.submit(models.ResultStatusUp)

		pubs := s.publications()
		r.Len(pubs, 1)
		r.Equal(models.PublicationStateResolved, pubs[0].PublicState)
		r.NotNil(pubs[0].ResolvedAt)

		updates := s.updateKinds(pubs[0])
		r.Contains(updates, models.StatusUpdateKindResolved)
	})

	t.Run("human-edited twin is NOT auto-resolved", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		s := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0})

		s.submit(models.ResultStatusDown)

		pubs := s.publications()
		r.Len(pubs, 1)

		// A human edits the title — the only thing that stamps human_touched_at.
		newTitle := "We are working on payments"
		_, err := s.pubs.UpdatePublication(t.Context(), s.org.Slug, s.page.UID, pubs[0].UID,
			s.userUID, &incidentpublications.UpdatePublicationRequest{Title: &newTitle})
		r.NoError(err)

		s.clk.Advance(5 * time.Minute)
		s.submit(models.ResultStatusUp)

		pubs = s.publications()
		r.Len(pubs, 1)
		r.NotEqual(models.PublicationStateResolved, pubs[0].PublicState,
			"a publication a human took over must not be auto-resolved")
		r.Nil(pubs[0].ResolvedAt)

		kinds := s.updateKinds(pubs[0])
		r.Contains(kinds, models.StatusUpdateKindMonitoring,
			"the automation reports the recovery instead of closing the incident")
		r.NotContains(kinds, models.StatusUpdateKindResolved)
	})
}

// TestAutoResolveNeverPostsNothing pins the opt-out.
func TestAutoResolveNeverPostsNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{
		autoPublish: true, delaySeconds: 0, autoResolve: models.AutoResolveNever,
	})

	s.submit(models.ResultStatusDown)

	pubs := s.publications()
	r.Len(pubs, 1)

	before := len(s.updateKinds(pubs[0]))

	s.clk.Advance(5 * time.Minute)
	s.submit(models.ResultStatusUp)

	pubs = s.publications()
	r.Equal(models.PublicationStateInvestigating, pubs[0].PublicState)
	r.Len(s.updateKinds(pubs[0]), before, "auto_resolve=never posts nothing at all")
}

// TestAutoResolveAlwaysResolvesEvenWhenTouched pins the other opt-out.
func TestAutoResolveAlwaysResolvesEvenWhenTouched(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{
		autoPublish: true, delaySeconds: 0, autoResolve: models.AutoResolveAlways,
	})

	s.submit(models.ResultStatusDown)

	pubs := s.publications()
	r.Len(pubs, 1)

	title := "Payments are degraded"
	_, err := s.pubs.UpdatePublication(t.Context(), s.org.Slug, s.page.UID, pubs[0].UID,
		s.userUID, &incidentpublications.UpdatePublicationRequest{Title: &title})
	r.NoError(err)

	s.clk.Advance(5 * time.Minute)
	s.submit(models.ResultStatusUp)

	pubs = s.publications()
	r.Equal(models.PublicationStateResolved, pubs[0].PublicState)
}

// --- Relapse -----------------------------------------------------------------

// TestRelapseReopensTheSamePublication proves a flapping service reads as ONE
// public incident with a history, not as several.
func TestRelapseReopensTheSamePublication(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0})

	s.submit(models.ResultStatusDown)

	pubs := s.publications()
	r.Len(pubs, 1)
	firstUID := pubs[0].UID

	// Recover — the publication auto-resolves.
	s.clk.Advance(30 * time.Second)
	s.submit(models.ResultStatusUp)
	r.Equal(models.PublicationStateResolved, s.publications()[0].PublicState)

	// Relapse inside the reopen cooldown: the incident reopens, and so must the
	// SAME publication.
	s.clk.Advance(30 * time.Second)
	s.submit(models.ResultStatusDown)

	pubs = s.publications()
	r.Len(pubs, 1, "a relapse must not mint a second public incident")
	r.Equal(firstUID, pubs[0].UID)
	r.Equal(models.PublicationStateInvestigating, pubs[0].PublicState)
	r.Nil(pubs[0].ResolvedAt)
}

// --- Storm cap ---------------------------------------------------------------

// TestSubscriberStormCap proves the Nth+1 fan-out inside an hour is dropped
// while the update itself is still posted — the page and the feed stay
// complete, only the mail stops.
func TestSubscriberStormCap(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0})

	s.submit(models.ResultStatusDown)

	pubs := s.publications()
	r.Len(pubs, 1)

	// One wave has already gone out with the opening update. Post updates until
	// well past the default cap of 4.
	const extraUpdates = 8
	for i := range extraUpdates {
		_, err := s.pubs.AppendUpdate(t.Context(), s.org.Slug, s.page.UID, pubs[0].UID, s.userUID,
			&incidentpublications.AppendUpdateRequest{
				Kind:         "info",
				BodyMarkdown: "Still working on it, note " + string(rune('a'+i)),
			})
		r.NoError(err)
	}

	waves := s.notifier.drain()

	// EXACTLY the cap, not merely "at most" it. `LessOrEqual` alone would pass
	// on zero waves — i.e. on a fan-out that never fired at all — which is the
	// vacuous-negative trap the spec's Verification section warns about. The
	// equality is both halves at once: the first `cap` sends went out (fan-out
	// works) and the 9 further updates sent nothing (the cap holds).
	r.Len(waves, models.DefaultPublicationNotifyCap,
		"exactly the first %d updates mail subscribers; every later one inside "+
			"the hour is posted silently", models.DefaultPublicationNotifyCap)

	// …but every update was written. 1 opening + extraUpdates.
	r.Len(s.updateKinds(pubs[0]), 1+extraUpdates,
		"capping the mail must never cost the page or the feed an update")
}

// TestStormCapWindowRollsOver is the POSITIVE CONTROL for the cap: the same
// publication mails again once the rolling hour has elapsed. Without it, a cap
// that had simply broken fan-out permanently would look identical to a cap that
// works.
func TestStormCapWindowRollsOver(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0})

	s.submit(models.ResultStatusDown)

	pubs := s.publications()
	r.Len(pubs, 1)

	// Burn the whole budget for this hour.
	for i := range models.DefaultPublicationNotifyCap + 2 {
		_, err := s.pubs.AppendUpdate(t.Context(), s.org.Slug, s.page.UID, pubs[0].UID, s.userUID,
			&incidentpublications.AppendUpdateRequest{
				Kind:         "info",
				BodyMarkdown: "Note " + string(rune('a'+i)),
			})
		r.NoError(err)
	}

	r.Len(s.notifier.drain(), models.DefaultPublicationNotifyCap)

	// An hour later the window rolls over and mail resumes.
	s.clk.Advance(61 * time.Minute)

	_, err := s.pubs.AppendUpdate(t.Context(), s.org.Slug, s.page.UID, pubs[0].UID, s.userUID,
		&incidentpublications.AppendUpdateRequest{
			Kind: "monitoring", BodyMarkdown: "A fix is deployed and we are watching.",
		})
	r.NoError(err)

	r.Len(s.notifier.drain(), 1,
		"the cap is a rolling hour, not a permanent mute")
}

// --- Manual publications -----------------------------------------------------

// TestManualPublicationIsHumanTouchedFromBirth proves a hand-authored incident
// is never taken over by the automation.
func TestManualPublicationIsHumanTouchedFromBirth(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: false, delaySeconds: 0})

	body := "We are migrating the database this evening."
	resp, err := s.pubs.CreatePublication(t.Context(), s.org.Slug, s.page.UID, s.userUID,
		&incidentpublications.CreatePublicationRequest{
			Title:        "Planned database migration",
			BodyMarkdown: &body,
		})
	r.NoError(err)
	r.True(resp.HumanTouched)
	r.False(resp.AutoCreated)

	full, err := s.pubs.GetPublication(t.Context(), s.org.Slug, s.page.UID, resp.UID)
	r.NoError(err)
	r.Len(full.Updates, 1)
	r.Equal(body, full.Updates[0].BodyMarkdown)
	r.NotNil(full.Updates[0].AuthorUID, "a hand-written update carries its author")
	r.Equal(s.userUID, *full.Updates[0].AuthorUID)
}

// TestAppendUpdateAdvancesState proves the header and the timeline cannot
// disagree: posting a `resolved` update resolves the publication.
func TestAppendUpdateAdvancesState(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0})

	s.submit(models.ResultStatusDown)

	pubs := s.publications()
	r.Len(pubs, 1)

	_, err := s.pubs.AppendUpdate(t.Context(), s.org.Slug, s.page.UID, pubs[0].UID, s.userUID,
		&incidentpublications.AppendUpdateRequest{
			Kind: "resolved", BodyMarkdown: "Everything is back to normal.",
		})
	r.NoError(err)

	pubs = s.publications()
	r.Equal(models.PublicationStateResolved, pubs[0].PublicState)
	r.NotNil(pubs[0].ResolvedAt)
}

// --- Public visibility -------------------------------------------------------

// TestPrivatePagePublicationsAreInvisible proves the public projection applies
// the same visibility gate as the rest of the public status-page surface.
func TestPrivatePagePublicationsAreInvisible(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0})

	s.submit(models.ResultStatusDown)
	r.Len(s.publications(), 1)

	// Positive control first: while the page is public, the incident IS served.
	visible, err := s.pubs.ViewPublicIncidents(t.Context(), s.org.Slug, s.page.Slug, true)
	r.NoError(err)
	r.Len(visible.Incidents, 1)
	r.Equal("public", visible.Visibility)

	private := "private"
	r.NoError(s.dbSvc.UpdateStatusPage(t.Context(), s.page.UID,
		&models.StatusPageUpdate{Visibility: &private}))

	_, err = s.pubs.ViewPublicIncidents(t.Context(), s.org.Slug, s.page.Slug, true)
	r.ErrorIs(err, incidentpublications.ErrStatusPageNotFound,
		"a private page's incidents must be as invisible as the page itself")
}

// --- Defaults ----------------------------------------------------------------

// TestNewPagesDefaultToAutoPublish pins the upgrade contract: the create path
// opts new pages in, and the DDL default leaves everything that already existed
// switched off.
func TestNewPagesDefaultToAutoPublish(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	fresh := models.NewStatusPage(org.UID, "Acme status", "public")
	r.True(fresh.AutoPublish, "a page created through NewStatusPage opts in")
	r.Equal(models.DefaultAutoPublishDelaySeconds, fresh.AutoPublishDelaySeconds)
	r.Equal(string(models.AutoResolveIfUntouched), fresh.AutoResolve)
	r.NoError(dbSvc.CreateStatusPage(ctx, fresh))

	// A row written WITHOUT the create path — the shape an upgraded
	// installation's existing rows have — must land on the DDL default, which
	// is off.
	_, err = dbSvc.DB().NewRaw(
		`insert into status_pages (uid, organization_uid, name, slug, visibility, is_default,
		    enabled, show_availability, show_response_time, history_days, history_period, settings)
		 values (?, ?, 'Legacy', 'legacy', 'public', 0, 1, 1, 1, 90, '90d', '{}')`,
		"legacy-page-uid", org.UID,
	).Exec(ctx)
	r.NoError(err)

	legacy, err := dbSvc.GetStatusPage(ctx, org.UID, "legacy-page-uid")
	r.NoError(err)
	r.False(legacy.AutoPublish,
		"the migration must leave pre-existing pages opted OUT — nobody's internal "+
			"blips go public on upgrade")
	r.Equal(models.DefaultAutoPublishDelaySeconds, legacy.AutoPublishDelaySeconds)
	r.Equal(string(models.AutoResolveIfUntouched), legacy.AutoResolve)
}

// --- Retention guard ---------------------------------------------------------

// TestRetentionGuardCountsLivePublications pins the primitive any future
// incident reaper must consult: an incident referenced by a live publication
// reports a non-zero count, and unpublishing releases it.
func TestRetentionGuardCountsLivePublications(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0})

	s.submit(models.ResultStatusDown)

	pubs := s.publications()
	r.Len(pubs, 1)
	r.NotNil(pubs[0].IncidentUID)

	count, err := s.dbSvc.CountIncidentPublicationsForIncident(t.Context(), *pubs[0].IncidentUID)
	r.NoError(err)
	r.Equal(1, count, "an incident a status page is narrating must not be reapable")

	r.NoError(s.pubs.UnpublishIncident(t.Context(), s.org.Slug, *pubs[0].IncidentUID, pubs[0].UID, s.userUID))

	count, err = s.dbSvc.CountIncidentPublicationsForIncident(t.Context(), *pubs[0].IncidentUID)
	r.NoError(err)
	r.Zero(count, "unpublishing releases the incident again")
}

// --- Helpers -----------------------------------------------------------------

// updateKinds returns the kinds of every narrative entry on a publication.
func (s *pubSetup) updateKinds(pub *models.IncidentPublication) []models.StatusUpdateKind {
	s.t.Helper()

	updates, err := s.dbSvc.ListStatusUpdates(s.t.Context(), s.org.UID, models.StatusUpdatesFilter{
		StatusPageUID:          pub.StatusPageUID,
		IncidentPublicationUID: &pub.UID,
		Limit:                  200,
	})
	require.NoError(s.t, err)

	kinds := make([]models.StatusUpdateKind, 0, len(updates))
	for _, upd := range updates {
		kinds = append(kinds, upd.Kind)
	}

	return kinds
}

// TestTemplatesFallBackToEnglish pins the language resolution: a translated
// page renders in its language, an untranslated one falls back to English
// rather than rendering an empty string.
func TestTemplatesFallBackToEnglish(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0})

	french := "fr-FR"
	r.NoError(s.dbSvc.UpdateStatusPage(t.Context(), s.page.UID,
		&models.StatusPageUpdate{Language: &french}))

	s.submit(models.ResultStatusDown)

	pubs := s.publications()
	r.Len(pubs, 1)
	r.Contains(pubs[0].PublicTitle, "difficultés",
		"a fr-FR page renders French copy")
	r.Contains(pubs[0].PublicTitle, s.publicName)
}

// TestRollupParentPublishesExactlyOnce is the other half of the
// paging-suppressed rule: when one root cause takes several checks down, the
// PARENT incident produces exactly one public incident and its suppressed
// children produce none. Without this, a single upstream failure would paint
// the status page with a wall of near-identical incidents.
func TestRollupParentPublishesExactlyOnce(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0})

	// A second check, also displayed on the page, standing in for the child.
	child := models.NewCheck(s.org.UID, "payments-worker", "http")
	child.Status = models.CheckStatusUp
	r.NoError(s.dbSvc.CreateCheck(t.Context(), child))

	childName := "Payments worker"
	childResource := models.NewStatusPageResource(s.section.UID, child.UID, 2)
	childResource.PublicName = &childName
	r.NoError(s.dbSvc.CreateStatusPageResource(t.Context(), childResource))

	// The root cause opens and publishes.
	parent := models.NewIncident(s.org.UID, s.check.UID, s.clk.Now(), "payments-api is down")
	r.NoError(s.dbSvc.CreateIncident(t.Context(), parent))
	s.pubs.OnIncidentOpened(t.Context(), parent)

	// The child rolls up under it: paging suppressed, cause recorded.
	childIncident := models.NewIncident(s.org.UID, child.UID, s.clk.Now(), "payments-worker is down")
	childIncident.PagingSuppressed = true
	childIncident.CausedByIncidentUID = &parent.UID
	r.NoError(s.dbSvc.CreateIncident(t.Context(), childIncident))
	s.pubs.OnIncidentOpened(t.Context(), childIncident)

	pubs := s.publications()
	r.Len(pubs, 1, "one root cause must yield exactly one public incident")
	r.NotNil(pubs[0].IncidentUID)
	r.Equal(parent.UID, *pubs[0].IncidentUID, "the PARENT is the one that publishes")
}

// TestGroupIncidentPublishesOncePerPage covers the group path end to end: a
// group rendered as one public component yields one publication, titled with
// the GROUP's public name — never with a member check's name, since a group
// component never exposes its members.
func TestGroupIncidentPublishesOncePerPage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	// Build the group first so the setup can attach the check to it.
	probe := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0, noResource: true})

	group := models.NewCheckGroup(probe.org.UID, "Payments platform", "payments-platform")
	r.NoError(probe.dbSvc.CreateCheckGroup(ctx, group))

	// Attach the existing check to the group and put the GROUP on the page.
	probe.check.CheckGroupUID = &group.UID
	r.NoError(probe.dbSvc.UpdateCheck(ctx, probe.check.UID, &models.CheckUpdate{
		CheckGroupUID: &group.UID,
	}))

	groupName := "Payments platform"
	resource := models.NewStatusPageGroupResource(probe.section.UID, group.UID, 1)
	resource.PublicName = &groupName
	r.NoError(probe.dbSvc.CreateStatusPageResource(ctx, resource))

	incident := models.NewIncident(probe.org.UID, probe.check.UID, probe.clk.Now(),
		"Payments platform — 1/3 checks down")
	incident.CheckGroupUID = &group.UID
	r.NoError(probe.dbSvc.CreateIncident(ctx, incident))

	probe.pubs.OnIncidentOpened(ctx, incident)

	pubs := probe.publications()
	r.Len(pubs, 1, "a group incident publishes once per page")
	r.Contains(pubs[0].PublicTitle, groupName)
	r.NotContains(pubs[0].PublicTitle, *probe.check.Slug,
		"a group component never names the member check that triggered it")
	r.NotContains(pubs[0].PublicTitle, "checks down",
		"the internal group title, which leaks the member count, must not be reused")
}

// --- Group membership notes --------------------------------------------------

// TestGroupMemberNoteIsRateLimited proves both halves of the "also affecting X"
// behavior. A new member joining an already-published group incident appends
// one update; a second member joining seconds later appends nothing — the
// posts are coalesced, so a group whose members fail one after another reads as
// one incident rather than as a wall of near-identical entries.
//
// The positive control is built in: the first note MUST appear, so a
// rate-limiter that simply suppressed everything would fail the first
// assertion instead of quietly passing the second.
func TestGroupMemberNoteIsRateLimited(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	s := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0, noResource: true})

	group := models.NewCheckGroup(s.org.UID, "Payments platform", "payments-platform")
	r.NoError(s.dbSvc.CreateCheckGroup(ctx, group))

	r.NoError(s.dbSvc.UpdateCheck(ctx, s.check.UID, &models.CheckUpdate{CheckGroupUID: &group.UID}))

	// Two further members, each with its own PUBLIC component on the page, so
	// naming them is legitimate.
	members := make([]*models.Check, 0, 2)

	for idx, name := range []string{"Payments worker", "Payments ledger"} {
		member := models.NewCheck(s.org.UID, "payments-member-"+string(rune('a'+idx)), "http")
		member.CheckGroupUID = &group.UID
		r.NoError(s.dbSvc.CreateCheck(ctx, member))

		publicName := name
		resource := models.NewStatusPageResource(s.section.UID, member.UID, idx+2)
		resource.PublicName = &publicName
		r.NoError(s.dbSvc.CreateStatusPageResource(ctx, resource))

		members = append(members, member)
	}

	// A group resource for the trigger check itself, so the incident publishes.
	groupName := "Payments platform"
	groupResource := models.NewStatusPageGroupResource(s.section.UID, group.UID, 1)
	groupResource.PublicName = &groupName
	r.NoError(s.dbSvc.CreateStatusPageResource(ctx, groupResource))

	incident := models.NewIncident(s.org.UID, s.check.UID, s.clk.Now(), "Payments platform — 1/3 checks down")
	incident.CheckGroupUID = &group.UID
	r.NoError(s.dbSvc.CreateIncident(ctx, incident))

	s.pubs.OnIncidentOpened(ctx, incident)

	pubs := s.publications()
	r.Len(pubs, 1)

	baseline := len(s.updateKinds(pubs[0]))

	// First member joins → a note is appended.
	s.clk.Advance(20 * time.Second)
	s.pubs.OnGroupMemberJoined(ctx, incident, members[0])

	afterFirst := s.updateKinds(pubs[0])
	r.Len(afterFirst, baseline+1, "a new member joining appends an 'also affecting' note")
	r.Contains(afterFirst, models.StatusUpdateKindInfo)

	// Second member joins seconds later → coalesced, nothing appended.
	s.clk.Advance(30 * time.Second)
	s.pubs.OnGroupMemberJoined(ctx, incident, members[1])

	r.Len(s.updateKinds(pubs[0]), baseline+1,
		"a second member joining inside the rate-limit window is coalesced")

	// Past the window, a further join speaks again — the limiter is a spacing
	// rule, not a one-shot.
	s.clk.Advance(6 * time.Minute)
	s.pubs.OnGroupMemberJoined(ctx, incident, members[1])

	r.Len(s.updateKinds(pubs[0]), baseline+2,
		"past the interval the next join is announced")
}

// TestGroupMemberNoteNeverNamesAnUndisplayedCheck pins the topology-hiding
// rule: a member that is NOT a component on the page contributes no note at
// all, because a group component never lists its members.
func TestGroupMemberNoteNeverNamesAnUndisplayedCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	s := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0, noResource: true})

	group := models.NewCheckGroup(s.org.UID, "Payments platform", "payments-platform")
	r.NoError(s.dbSvc.CreateCheckGroup(ctx, group))
	r.NoError(s.dbSvc.UpdateCheck(ctx, s.check.UID, &models.CheckUpdate{CheckGroupUID: &group.UID}))

	groupName := "Payments platform"
	groupResource := models.NewStatusPageGroupResource(s.section.UID, group.UID, 1)
	groupResource.PublicName = &groupName
	r.NoError(s.dbSvc.CreateStatusPageResource(ctx, groupResource))

	// A member with NO resource of its own on the page.
	hidden := models.NewCheck(s.org.UID, "payments-internal-shard", "http")
	hidden.CheckGroupUID = &group.UID
	r.NoError(s.dbSvc.CreateCheck(ctx, hidden))

	incident := models.NewIncident(s.org.UID, s.check.UID, s.clk.Now(), "Payments platform — 1/3 checks down")
	incident.CheckGroupUID = &group.UID
	r.NoError(s.dbSvc.CreateIncident(ctx, incident))

	s.pubs.OnIncidentOpened(ctx, incident)

	pubs := s.publications()
	r.Len(pubs, 1)

	baseline := len(s.updateKinds(pubs[0]))

	s.clk.Advance(20 * time.Second)
	s.pubs.OnGroupMemberJoined(ctx, incident, hidden)

	r.Len(s.updateKinds(pubs[0]), baseline,
		"a member that is not a public component on the page is never named")
}

// TestZeroPublishDelaySurvivesTheWrite is a regression pin for a bug that made
// the documented "publish immediately" setting unreachable through the API.
//
// bun omits a ZERO-valued field from an INSERT when its struct tag declares a
// `default:`. With `default:60` on auto_publish_delay_seconds, an operator
// asking for 0 had their choice dropped before it reached the database and the
// column landed on 60 — a silent one-minute delay on a page configured for
// none. The tag carries no default now; this test fails if it comes back.
func TestZeroPublishDelaySurvivesTheWrite(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	page := models.NewStatusPage(org.UID, "Acme status", "public")
	page.AutoPublishDelaySeconds = 0
	r.NoError(dbSvc.CreateStatusPage(ctx, page))

	stored, err := dbSvc.GetStatusPage(ctx, org.UID, page.UID)
	r.NoError(err)
	r.Zero(stored.AutoPublishDelaySeconds,
		"a deliberate zero delay must reach the database, not fall back to the column default")

	// And it round-trips through an UPDATE too.
	sixty := 60
	r.NoError(dbSvc.UpdateStatusPage(ctx, page.UID,
		&models.StatusPageUpdate{AutoPublishDelaySeconds: &sixty}))

	zero := 0
	r.NoError(dbSvc.UpdateStatusPage(ctx, page.UID,
		&models.StatusPageUpdate{AutoPublishDelaySeconds: &zero}))

	stored, err = dbSvc.GetStatusPage(ctx, org.UID, page.UID)
	r.NoError(err)
	r.Zero(stored.AutoPublishDelaySeconds)
}
