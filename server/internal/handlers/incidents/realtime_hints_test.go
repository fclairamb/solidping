package incidents_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/realtime"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// hintRecorder captures decoded hints from the org.events bus channel.
type hintRecorder struct {
	mu    sync.Mutex
	hints []realtime.Hint
}

func recordHints(t *testing.T, bus notifier.EventNotifier) *hintRecorder {
	t.Helper()

	rec := &hintRecorder{}
	ch := bus.Listen(realtime.ChannelOrgEvents)

	go func() {
		for payload := range ch {
			hint, err := realtime.DecodeHint(payload)
			if err != nil {
				continue
			}
			rec.mu.Lock()
			rec.hints = append(rec.hints, hint)
			rec.mu.Unlock()
		}
	}()

	return rec
}

// kindsFor returns the union of all hinted kinds for the org.
func (rec *hintRecorder) kindsFor(orgUID string) map[string]bool {
	rec.mu.Lock()
	defer rec.mu.Unlock()

	out := make(map[string]bool)
	for _, h := range rec.hints {
		if h.Org != orgUID {
			continue
		}
		for _, k := range h.Kinds {
			out[k] = true
		}
	}

	return out
}

// checkUidsFor returns the union of all hinted check uids for the org.
func (rec *hintRecorder) checkUidsFor(orgUID string) map[string]bool {
	rec.mu.Lock()
	defer rec.mu.Unlock()

	out := make(map[string]bool)
	for _, h := range rec.hints {
		if h.Org != orgUID {
			continue
		}
		for _, uid := range h.CheckUids {
			out[uid] = true
		}
	}

	return out
}

// hintSetup is an in-memory SQLite world with a real realtime publisher wired
// into the incidents service, capturing every hint that reaches the bus.
type hintSetup struct {
	svc   *incidents.Service
	dbSvc *sqlite.Service
	rec   *hintRecorder
	org   *models.Organization
	check *models.Check
}

func newHintSetup(t *testing.T) *hintSetup {
	t.Helper()
	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	bus := notifier.NewLocalEventNotifier()
	rec := recordHints(t, bus)
	pub := realtime.NewPublisher(t.Context(), bus, 50*time.Millisecond, nil)
	t.Cleanup(func() {
		pub.Close()
		_ = bus.Close()
	})

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clock.Real{}, pub)

	org := models.NewOrganization("hints-test", "Hints Test")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	check.Status = models.CheckStatusUp
	check.ConfirmationPeriodSeconds = 0 // open the incident on the first failure
	check.RecoveryPeriodSeconds = 0     // resolve on the first success
	r.NoError(dbSvc.CreateCheck(ctx, check))

	return &hintSetup{svc: svc, dbSvc: dbSvc, rec: rec, org: org, check: check}
}

func (s *hintSetup) submit(t *testing.T, status models.ResultStatus) {
	t.Helper()
	result := models.NewResult(s.org.UID, s.check.UID, status, 0)
	require.NoError(t, s.dbSvc.CreateResult(t.Context(), result))
	require.NoError(t, s.svc.ProcessCheckResult(context.Background(), s.check, result))
}

// TestProcessCheckResult_PublishesHintsOnIncidentOpen drives up -> down with a
// zero confirmation window and asserts the full hint fan-out: `results` for
// the persisted result, `checks` for the status transition (immediate), and
// `incidents` + `events` alongside the incident-created event.
func TestProcessCheckResult_PublishesHintsOnIncidentOpen(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	setup := newHintSetup(t)
	setup.submit(t, models.ResultStatusDown)

	r.Eventually(func() bool {
		kinds := setup.rec.kindsFor(setup.org.UID)

		return kinds["results"] && kinds["checks"] && kinds["incidents"] && kinds["events"]
	}, 2*time.Second, 10*time.Millisecond,
		"incident open must hint results+checks+incidents+events, got %v",
		setup.rec.kindsFor(setup.org.UID))
}

// TestProcessCheckResult_HintsCarryTheCheckUid proves the v2 attribution
// contract: every hint published from the incident-open path (results,
// checks, incidents, events) carries this check's uid, so a `check`-scoped
// WebSocket subscriber can be routed to without watching the whole org.
func TestProcessCheckResult_HintsCarryTheCheckUid(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	setup := newHintSetup(t)
	setup.submit(t, models.ResultStatusDown)

	r.Eventually(func() bool {
		return setup.rec.checkUidsFor(setup.org.UID)[setup.check.UID]
	}, 2*time.Second, 10*time.Millisecond,
		"every hint on the incident-open path must carry the check uid, got %v",
		setup.rec.checkUidsFor(setup.org.UID))
}

// TestProcessCheckResult_SteadyStateOnlyHintsResults keeps the check up: no
// status transition, no incident — only the coalesced `results` hint flows.
func TestProcessCheckResult_SteadyStateOnlyHintsResults(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	setup := newHintSetup(t)
	setup.check.Status = models.CheckStatusUp
	setup.check.StatusStreak = 5
	setup.submit(t, models.ResultStatusUp)

	r.Eventually(func() bool {
		return setup.rec.kindsFor(setup.org.UID)["results"]
	}, 2*time.Second, 10*time.Millisecond, "a persisted result must hint `results`")

	kinds := setup.rec.kindsFor(setup.org.UID)
	r.False(kinds["checks"], "steady state must not hint a status transition")
	r.False(kinds["incidents"], "steady state must not hint incidents")
}

// TestProcessCheckResult_NilPublisherIsSafe pins the disabled-feature path: a
// nil publisher must not panic anywhere in the pipeline.
func TestProcessCheckResult_NilPublisherIsSafe(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx := t.Context()
	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clock.Real{}, nil)

	org := models.NewOrganization("nilrt-test", "Nil RT Test")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	check := models.NewCheck(org.UID, "api", "http")
	check.Status = models.CheckStatusUp
	check.ConfirmationPeriodSeconds = 0
	r.NoError(dbSvc.CreateCheck(ctx, check))

	result := models.NewResult(org.UID, check.UID, models.ResultStatusDown, 0)
	r.NoError(dbSvc.CreateResult(ctx, result))
	r.NoError(svc.ProcessCheckResult(context.Background(), check, result))
}
