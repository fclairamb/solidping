package jobtypes_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
	"github.com/fclairamb/solidping/server/internal/watchdog"
)

// strandedRegionSlug is the region every watchdog job fixture strands, and
// strandedJobCount how many overdue jobs it strands there — comfortably past
// the default 5-job blast-radius bar.
const (
	strandedRegionSlug = "eu2"
	strandedJobCount   = 8
)

// safeBuffer is a bytes.Buffer usable as a slog destination from a test that
// also reads it.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// watchdogEnv is one wired instance: DB, job service, a capturing logger and
// the org the fixtures hang off.
type watchdogEnv struct {
	jctx *jobdef.JobContext
	db   db.Service
	org  *models.Organization
	logs *safeBuffer
}

// newWatchdogEnv builds the job context the watchdog runs in. The logger is
// captured because "logged as undeliverable" is a REQUIREMENT of this spec,
// not a debugging aid: a silent drop on the alerting path is the bug the
// watchdog exists to kill, so the log line is asserted like any other output.
func newWatchdogEnv(t *testing.T) *watchdogEnv {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	jobSvc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

	logs := &safeBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	return &watchdogEnv{
		jctx: &jobdef.JobContext{
			Services:  &services.Registry{Jobs: jobSvc},
			DB:        dbSvc.DB(),
			DBService: dbSvc,
			Logger:    logger,
		},
		db:   dbSvc,
		org:  org,
		logs: logs,
	}
}

// strandRegion reproduces the 2026-08-24 shape at the storage level: N jobs
// carrying a region slug, every one of them hours overdue, and no worker
// anywhere announcing that region.
func (e *watchdogEnv) strandRegion(t *testing.T) {
	t.Helper()

	const slug = strandedRegionSlug

	overdue := time.Now().Add(-3 * time.Hour)

	for i := range strandedJobCount {
		check := models.NewCheck(e.org.UID, fmt.Sprintf("stranded-%d", i), "http")
		check.Period = timeutils.Duration(time.Minute)
		require.NoError(t, e.db.CreateCheck(t.Context(), check))

		region := slug
		job := &models.CheckJob{
			UID:             uuid.New().String(),
			OrganizationUID: e.org.UID,
			CheckUID:        check.UID,
			Region:          &region,
			Type:            "http",
			Period:          timeutils.Duration(time.Minute),
			ScheduledAt:     &overdue,
			UpdatedAt:       time.Now(),
		}

		_, err := e.db.DB().NewInsert().Model(job).Exec(t.Context())
		require.NoError(t, err)
	}
}

// configure writes the platform_watchdog system parameter.
func (e *watchdogEnv) configure(t *testing.T, value map[string]any) {
	t.Helper()

	require.NoError(t, e.db.SetSystemParameter(t.Context(), watchdog.ParamPlatformWatchdog, value, false))
}

// addRecipient creates a user, makes them a member of the org, and gives them
// one enabled email route — the shape an operator actually has.
func (e *watchdogEnv) addRecipient(t *testing.T, email string, withRoute bool) string {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	user := models.NewUser(email)
	r.NoError(e.db.CreateUser(ctx, user))
	r.NoError(e.db.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(e.org.UID, user.UID, models.MemberRoleAdmin)))

	if withRoute {
		r.NoError(e.db.EnsureDefaultEmailRoute(ctx, user.UID, e.org.UID, email))
	}

	return user.UID
}

// runWatchdog executes one full watchdog run.
func (e *watchdogEnv) runWatchdog(t *testing.T) error {
	t.Helper()

	definition := &jobtypes.PlatformWatchdogJobDefinition{}

	runner, err := definition.CreateJobRun(nil)
	require.NoError(t, err)

	return runner.Run(t.Context(), e.jctx)
}

// jobsOfType reads the queue directly. It goes to the table rather than
// through ListJobs because the watchdog enqueues across org scopes — its own
// reschedule is global, an email job carries the recipient's org — and a
// single org-scoped listing would miss one of them.
func (e *watchdogEnv) jobsOfType(t *testing.T, jobType jobdef.JobType) []*models.Job {
	t.Helper()

	var jobs []*models.Job

	err := e.db.DB().NewSelect().
		Model(&jobs).
		Where("type = ?", string(jobType)).
		Where("deleted_at IS NULL").
		Scan(t.Context())
	require.NoError(t, err)

	return jobs
}

// emailJobs lists the email jobs the run enqueued — the observable proof that
// a digest was actually handed to a delivery path.
func (e *watchdogEnv) emailJobs(t *testing.T) []*models.Job {
	t.Helper()

	return e.jobsOfType(t, jobdef.JobTypeEmail)
}

// anomalyMarkers lists the persisted anti-flood markers.
func (e *watchdogEnv) anomalyMarkers(t *testing.T) []*models.StateEntry {
	t.Helper()

	entries, err := e.db.ListStateEntries(t.Context(), nil, watchdog.StateKeyPrefix)
	require.NoError(t, err)

	return entries
}

// TestPlatformWatchdogDisabledHasNoSideEffects: `enabled: false` must not
// evaluate, must not persist anomaly state, and must not deliver — while still
// rescheduling, so flipping the parameter to true takes effect within one
// interval rather than needing a restart.
func TestPlatformWatchdogDisabledHasNoSideEffects(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newWatchdogEnv(t)

	env.strandRegion(t)
	env.configure(t, map[string]any{"enabled": false, "recipients": []string{"whoever"}})

	r.NoError(env.runWatchdog(t))

	r.Empty(env.anomalyMarkers(t), "a disabled watchdog must not persist anomaly state")
	r.Empty(env.emailJobs(t), "a disabled watchdog must not deliver anything")

	r.Len(env.jobsOfType(t, jobdef.JobTypePlatformWatchdog), 1,
		"a disabled watchdog must still keep itself scheduled")
}

// TestPlatformWatchdogDeliversToRecipientRoutes is the end-to-end path: a
// stranded region becomes a digest, and that digest reaches the recipient
// through the notification route THEY configured — no new medium, no
// hardcoded address.
func TestPlatformWatchdogDeliversToRecipientRoutes(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newWatchdogEnv(t)

	env.strandRegion(t)

	recipient := env.addRecipient(t, "alice@acme.com", true)
	env.configure(t, map[string]any{"enabled": true, "recipients": []string{recipient}})

	r.NoError(env.runWatchdog(t))

	emails := env.emailJobs(t)
	r.Len(emails, 1, "one digest per run, not one message per anomaly")

	payload := jobConfigText(t, emails[0])
	r.Contains(payload, "alice@acme.com")
	r.Contains(payload, "platform anomaly")
	r.Contains(payload, "system/regions/migrate",
		"the digest must carry the ready-to-run remediation")

	markers := env.anomalyMarkers(t)
	r.Len(markers, 1)
	r.Equal(watchdog.StateKeyPrefix+"dark-region:"+strandedRegionSlug, markers[0].Key)

	// A second run inside the quiet window must stay silent — the anti-flood
	// contract seen from the delivery side.
	r.NoError(env.runWatchdog(t))
	r.Len(env.emailJobs(t), 1, "an ongoing anomaly must not re-deliver every hour")
}

// TestPlatformWatchdogLogsUndeliverableRecipient: a recipient with no enabled
// route must be NAMED, and the run must still succeed for everybody else.
// Silent drops are the bug this spec exists to kill.
func TestPlatformWatchdogLogsUndeliverableRecipient(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newWatchdogEnv(t)

	env.strandRegion(t)

	routeless := env.addRecipient(t, "bob@acme.com", false)
	reachable := env.addRecipient(t, "alice@acme.com", true)

	env.configure(t, map[string]any{
		"enabled":    true,
		"recipients": []string{routeless, reachable},
	})

	r.NoError(env.runWatchdog(t), "one unreachable recipient must not fail the run")

	logs := env.logs.String()
	r.Contains(logs, "undeliverable")
	r.Contains(logs, routeless)

	r.Len(env.emailJobs(t), 1, "the reachable recipient still gets the digest")
}

// TestPlatformWatchdogWarnsWhenEnabledWithoutRecipients: enabled with nobody
// to tell still evaluates and logs, but says so loudly — an operator must not
// be able to believe they are covered when they are not.
func TestPlatformWatchdogWarnsWhenEnabledWithoutRecipients(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newWatchdogEnv(t)

	env.strandRegion(t)
	env.configure(t, map[string]any{"enabled": true})

	r.NoError(env.runWatchdog(t))

	logs := env.logs.String()
	r.Contains(logs, "no recipients")
	r.Contains(logs, "Platform watchdog anomaly",
		"the anomaly is still logged even when it cannot be delivered")
	r.Empty(env.emailJobs(t))
	r.Len(env.anomalyMarkers(t), 1, "the anomaly is still tracked for the metrics path")
}

// TestPlatformWatchdogRecoveryNoticeReachesTheOperator closes the loop: after
// the fix, "the watchdog now sees 0 stranded jobs" is the operator's exit
// criterion, so the recovery has to arrive on the same route — exactly once.
func TestPlatformWatchdogRecoveryNoticeReachesTheOperator(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newWatchdogEnv(t)

	env.strandRegion(t)

	recipient := env.addRecipient(t, "alice@acme.com", true)
	env.configure(t, map[string]any{"enabled": true, "recipients": []string{recipient}})

	r.NoError(env.runWatchdog(t))
	r.Len(env.emailJobs(t), 1)

	// The operator runs the migration: the stranded jobs are gone.
	_, err := env.db.DB().NewDelete().
		Model((*models.CheckJob)(nil)).
		Where("region = ?", strandedRegionSlug).
		Exec(t.Context())
	r.NoError(err)

	r.NoError(env.runWatchdog(t))

	emails := env.emailJobs(t)
	r.Len(emails, 2, "the recovery is announced")
	r.True(hasAllClear(t, emails), "the recovery digest says all clear")

	// And exactly once.
	r.NoError(env.runWatchdog(t))
	r.Len(env.emailJobs(t), 2, "a recovery must never be announced twice")
}

// hasAllClear reports whether any enqueued email carries the all-clear
// subject.
func hasAllClear(t *testing.T, jobs []*models.Job) bool {
	t.Helper()

	for _, job := range jobs {
		if strings.Contains(jobConfigText(t, job), "All clear") {
			return true
		}
	}

	return false
}

// jobConfigText renders a job's JSON config back to text so assertions can
// look for the digest content the job carries.
func jobConfigText(t *testing.T, job *models.Job) string {
	t.Helper()

	raw, err := json.Marshal(job.Config)
	require.NoError(t, err)

	return string(raw)
}
