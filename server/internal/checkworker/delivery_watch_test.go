package checkworker

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

var errInjected = errors.New("injected failure")

// faultyDB fails the calls a test switches on and delegates the rest.
type faultyDB struct {
	db.Service

	failListType    string // ListEnabledChecksByType fails for this check type
	failSendList    bool   // ListResults fails for the smtp check's send lookup
	failEmailList   bool   // ListResults fails for the email check's rows
	failSave        bool
	failIncidentSQL bool
	emailUID        string
}

func (d *faultyDB) ListEnabledChecksByType(ctx context.Context, types []string) ([]*models.Check, error) {
	if len(types) == 1 && types[0] == d.failListType {
		return nil, errInjected
	}

	return d.Service.ListEnabledChecksByType(ctx, types)
}

func (d *faultyDB) ListResults(
	ctx context.Context, filter *models.ListResultsFilter,
) (*models.ListResultsResponse, error) {
	isEmail := len(filter.CheckUIDs) == 1 && filter.CheckUIDs[0] == d.emailUID
	if (isEmail && d.failEmailList) || (!isEmail && d.failSendList) {
		return nil, errInjected
	}

	return d.Service.ListResults(ctx, filter)
}

func (d *faultyDB) SaveResultWithStatusTracking(ctx context.Context, result *models.Result) error {
	if d.failSave {
		return errInjected
	}

	return d.Service.SaveResultWithStatusTracking(ctx, result)
}

func (d *faultyDB) FindActiveIncidentByCheckUID(ctx context.Context, checkUID string) (*models.Incident, error) {
	if d.failIncidentSQL {
		return nil, errInjected
	}

	return d.Service.FindActiveIncidentByCheckUID(ctx, checkUID)
}

const watchToken = "feedfacefeedfacefeedfacefeedfacefeedfacefeedface"

type watchFixture struct {
	svc   *sqlite.Service
	fault *faultyDB
	watch *DeliveryWatch
	email *models.Check
	smtp  *models.Check
	now   time.Time
	// worker is the evaluator's workers row (result.worker_uid is a foreign key).
	worker string
}

func newWatchFixture(t *testing.T, emailCfg models.JSONMap) *watchFixture {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	svc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(svc.Initialize(ctx))
	t.Cleanup(func() { _ = svc.Close() })

	org := models.NewOrganization("watch-org", "Watch Org")
	r.NoError(svc.CreateOrganization(ctx, org))

	emailCfg["token"] = watchToken
	email := models.NewCheck(org.UID, "mail", "email")
	email.Config = emailCfg
	r.NoError(svc.CreateCheck(ctx, email))

	smtp := models.NewCheck(org.UID, "sender", "smtp")
	smtp.Config = models.JSONMap{
		"host": "smtp.acme.test", "send_email": true,
		"mail_from": "probe@acme.test", "delivery_check_uid": email.UID,
		"delivery_to": watchToken + "@inbox.acme.test",
	}
	r.NoError(svc.CreateCheck(ctx, smtp))

	fault := &faultyDB{Service: svc}
	jobSvc := jobsvc.NewService(svc.DB(), svc, notifier.NewLocalEventNotifier(), nil)
	incidentSvc := incidents.NewService(fault, jobSvc, clock.Real{}, nil)

	worker := &models.Worker{UID: "00000000-0000-4000-8000-000000000001", Slug: "jobs-test", Name: "jobs:test"}
	registered, err := svc.RegisterOrUpdateWorker(ctx, worker)
	r.NoError(err)

	fault.emailUID = email.UID

	return &watchFixture{
		worker: registered.UID,
		svc:    svc, fault: fault, watch: NewDeliveryWatch(fault, incidentSvc), email: email, smtp: smtp, now: time.Now(),
	}
}

func (f *watchFixture) recordSend(t *testing.T, sentAt time.Time) {
	t.Helper()

	status := int(checkerdef.StatusUp)
	duration := float32(1)
	require.NoError(t, f.svc.SaveResultWithStatusTracking(t.Context(), &models.Result{
		UID: "send-" + sentAt.Format("150405"), OrganizationUID: f.smtp.OrganizationUID, CheckUID: f.smtp.UID,
		PeriodType: models.PeriodTypeRaw, PeriodStart: sentAt, Status: &status, Duration: &duration,
		Metrics:   models.JSONMap{},
		Output:    models.JSONMap{"submitted": true, "sentAt": sentAt.Format(time.RFC3339)},
		CreatedAt: sentAt,
	}))
}

func (f *watchFixture) emailRows(t *testing.T) []models.Result {
	t.Helper()

	var rows []models.Result
	require.NoError(t, f.svc.DB().NewSelect().Model(&rows).
		Where("check_uid = ?", f.email.UID).Where("period_type = ?", models.PeriodTypeRaw).
		Where("COALESCE(json_extract(output, '$.message'), '') <> 'Check created'").
		Scan(t.Context()))

	return rows
}

func TestDeliveryWatchRecordsOneTimeoutPerMissedProbe(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newWatchFixture(t, models.JSONMap{"max_delivery_delay": "10m"})
	sentAt := f.now.Add(-11 * time.Minute).Truncate(time.Second)
	f.recordSend(t, sentAt)

	wrote, err := f.watch.Sweep(t.Context(), f.worker, f.now)
	r.NoError(err)
	r.Equal(1, wrote)

	rows := f.emailRows(t)
	r.Len(rows, 1)
	r.Equal(int(checkerdef.StatusDown), *rows[0].Status)
	r.Equal("sent at "+sentAt.UTC().Format("15:04")+", not received after 10m", rows[0].Output["message"])
	r.Equal(sentAt.Format(time.RFC3339), rows[0].Output["sentAt"])
	r.Equal(f.smtp.UID, rows[0].Output["sourceCheckUid"])
	r.Equal(true, rows[0].Output["evaluation"])

	wrote, err = f.watch.Sweep(t.Context(), f.worker, f.now.Add(time.Minute))
	r.NoError(err)
	r.Zero(wrote)
	r.Len(f.emailRows(t), 1)
}

func TestDeliveryWatchIgnoresYoungSend(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newWatchFixture(t, models.JSONMap{"max_delivery_delay": "10m"})
	f.recordSend(t, f.now.Add(-5*time.Minute).Truncate(time.Second))

	wrote, err := f.watch.Sweep(t.Context(), f.worker, f.now)
	r.NoError(err)
	r.Zero(wrote)
	r.Empty(f.emailRows(t))
}

func TestDeliveryWatchSkipsCheckWithoutLimit(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newWatchFixture(t, models.JSONMap{})
	f.recordSend(t, f.now.Add(-3*time.Hour).Truncate(time.Second))

	wrote, err := f.watch.Sweep(t.Context(), f.worker, f.now)
	r.NoError(err)
	r.Zero(wrote)
	r.Empty(f.emailRows(t))
}

// TestDeliveryWatchSkipsEmailCheckWithoutPairedSMTP: a check fed only by
// third-party mail (Date-sourced sentAt) has no send to wait for, so it never
// gets a timeout row and keeps its period-based verdict.
func TestDeliveryWatchSkipsEmailCheckWithoutPairedSMTP(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newWatchFixture(t, models.JSONMap{"max_delivery_delay": "10m"})
	r.NoError(f.svc.DeleteCheck(t.Context(), f.smtp.UID))

	status := int(checkerdef.StatusUp)
	dated := f.now.Add(-3 * time.Hour).Truncate(time.Second)
	r.NoError(f.svc.SaveResultWithStatusTracking(t.Context(), &models.Result{
		UID: "third-party", OrganizationUID: f.email.OrganizationUID, CheckUID: f.email.UID,
		PeriodType: models.PeriodTypeRaw, PeriodStart: dated, Status: &status,
		Metrics: models.JSONMap{},
		Output: models.JSONMap{
			"message": "Email received", "sentAt": dated.Format(time.RFC3339), "sentAtSource": "date",
		},
		CreatedAt: dated,
	}))

	wrote, err := f.watch.Sweep(t.Context(), f.worker, f.now)
	r.NoError(err)
	r.Zero(wrote)
	r.Len(f.emailRows(t), 1, "only the third-party mail, no timeout row")
}

func TestDeliveryWatchSkipsSendThatArrived(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newWatchFixture(t, models.JSONMap{"max_delivery_delay": "10m"})
	sentAt := f.now.Add(-30 * time.Minute).Truncate(time.Second)
	f.recordSend(t, sentAt)

	// The probe arrived 13 minutes late: the handler recorded it (down, late)
	// with the same sentAt, so the watch has nothing to add.
	status := int(checkerdef.StatusDown)
	r.NoError(f.svc.SaveResultWithStatusTracking(t.Context(), &models.Result{
		UID: "late", OrganizationUID: f.email.OrganizationUID, CheckUID: f.email.UID,
		PeriodType: models.PeriodTypeRaw, PeriodStart: sentAt.Add(13 * time.Minute), Status: &status,
		Metrics:   models.JSONMap{},
		Output:    models.JSONMap{"sentAt": sentAt.Format(time.RFC3339)},
		CreatedAt: sentAt.Add(13 * time.Minute),
	}))

	wrote, err := f.watch.Sweep(t.Context(), f.worker, f.now)
	r.NoError(err)
	r.Zero(wrote)
	r.Len(f.emailRows(t), 1)
}

// saveSignal writes an inbound email signal row (no worker_uid) for the email check.
func (f *watchFixture) saveSignal(t *testing.T, uid string, at time.Time, status checkerdef.Status) {
	t.Helper()

	st := int(status)
	require.NoError(t, f.svc.SaveResultWithStatusTracking(t.Context(), &models.Result{
		UID: uid, OrganizationUID: f.email.OrganizationUID, CheckUID: f.email.UID,
		PeriodType: models.PeriodTypeRaw, PeriodStart: at, Status: &st,
		Metrics: models.JSONMap{}, Output: models.JSONMap{"message": "Email received"}, CreatedAt: at,
	}))
}

// TestPassiveVerdictKeepsWatchTimeoutUntilNewerEmail: after the watch wrote a
// timeout row, the once-per-period evaluation must not turn it back to up from
// an older up signal; a real email newer than the timeout lifts the hold.
func TestPassiveVerdictKeepsWatchTimeoutUntilNewerEmail(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newWatchFixture(t, models.JSONMap{"max_delivery_delay": "10m"})
	f.saveSignal(t, "older-up", f.now.Add(-30*time.Minute), checkerdef.StatusUp)
	f.recordSend(t, f.now.Add(-11*time.Minute).Truncate(time.Second))

	wrote, err := f.watch.Sweep(t.Context(), f.worker, f.now)
	r.NoError(err)
	r.Equal(1, wrote)

	be := backend.NewDirectBackend(f.svc, nil, nil, nil, nil)
	job := &models.CheckJob{
		OrganizationUID: f.email.OrganizationUID, CheckUID: f.email.UID, Type: string(checkerdef.CheckTypeEmail),
		Period: timeutils.Duration(time.Hour), Check: f.email,
	}

	status, output, grace, err := passiveVerdict(t.Context(), be, job, f.now.Add(time.Minute))
	r.NoError(err)
	r.False(grace)
	r.Equal(checkerdef.StatusDown, status)
	r.Contains(output[outputKeyMessage], "not received after 10m")

	// A newer real email lifts the hold.
	f.saveSignal(t, "newer-up", f.now.Add(2*time.Minute), checkerdef.StatusUp)

	status, _, _, err = passiveVerdict(t.Context(), be, job, f.now.Add(3*time.Minute))
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, status)
}

// replaceSMTP swaps the fixture's send-mode smtp check for one with this config.
func (f *watchFixture) replaceSMTP(t *testing.T, cfg models.JSONMap) {
	t.Helper()

	require.NoError(t, f.svc.DeleteCheck(t.Context(), f.smtp.UID))

	smtp := models.NewCheck(f.email.OrganizationUID, "sender-2", "smtp")
	smtp.Config = cfg
	require.NoError(t, f.svc.CreateCheck(t.Context(), smtp))
	f.smtp = smtp
}

// A send-mode check that only names the inbox address (no delivery_check_uid)
// is paired through the token in the local part, case-insensitively and
// ignoring a +tag.
func TestDeliveryWatchPairsThroughTokenInDeliveryTo(t *testing.T) {
	t.Parallel()

	for _, to := range []string{
		strings.ToUpper(watchToken) + "@inbox.acme.test",
		watchToken + "+nightly@inbox.acme.test",
	} {
		r := require.New(t)
		f := newWatchFixture(t, models.JSONMap{"max_delivery_delay": "10m"})
		f.replaceSMTP(t, models.JSONMap{
			"host": "smtp.acme.test", "send_email": true, "mail_from": "probe@acme.test", "delivery_to": to,
		})
		f.recordSend(t, f.now.Add(-11*time.Minute).Truncate(time.Second))

		wrote, err := f.watch.Sweep(t.Context(), f.worker, f.now)
		r.NoError(err, to)
		r.Equal(1, wrote, to)
		r.Len(f.emailRows(t), 1, to)
	}
}

// Send-mode checks that do not point at a watched email check are left alone.
func TestDeliveryWatchIgnoresUnpairedSMTPChecks(t *testing.T) {
	t.Parallel()

	base := func(extra models.JSONMap) models.JSONMap {
		cfg := models.JSONMap{"host": "smtp.acme.test", "send_email": true, "mail_from": "probe@acme.test"}
		for k, v := range extra {
			cfg[k] = v
		}

		return cfg
	}

	cases := map[string]models.JSONMap{
		"not in send mode":     base(models.JSONMap{"send_email": false, "delivery_to": watchToken + "@inbox.acme.test"}),
		"invalid config":       {"host": 42},
		"no delivery address":  base(nil),
		"no domain":            base(models.JSONMap{"delivery_to": watchToken}),
		"empty local part":     base(models.JSONMap{"delivery_to": "@inbox.acme.test"}),
		"another token":        base(models.JSONMap{"delivery_to": "someoneelse@inbox.acme.test"}),
		"unknown check uid":    base(models.JSONMap{"delivery_check_uid": "nope", "delivery_to": "x@inbox.acme.test"}),
		"empty local with tag": base(models.JSONMap{"delivery_to": "+tag@inbox.acme.test"}),
	}

	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			f := newWatchFixture(t, models.JSONMap{"max_delivery_delay": "10m"})
			f.replaceSMTP(t, cfg)
			f.recordSend(t, f.now.Add(-3*time.Hour).Truncate(time.Second))

			wrote, err := f.watch.Sweep(t.Context(), f.worker, f.now)
			r.NoError(err)
			r.Zero(wrote)
			r.Empty(f.emailRows(t))
		})
	}
}

func TestDeliveryWatchSkipsSMTPCheckThatNeverSent(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newWatchFixture(t, models.JSONMap{"max_delivery_delay": "10m"})

	wrote, err := f.watch.Sweep(t.Context(), f.worker, f.now)
	r.NoError(err)
	r.Zero(wrote)
	r.Empty(f.emailRows(t))
}

func TestDeliveryWatchSkipsSendWithoutUsableSentAt(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newWatchFixture(t, models.JSONMap{"max_delivery_delay": "10m"})

	status := int(checkerdef.StatusUp)
	at := f.now.Add(-3 * time.Hour)
	r.NoError(f.svc.SaveResultWithStatusTracking(t.Context(), &models.Result{
		UID: "garbled", OrganizationUID: f.smtp.OrganizationUID, CheckUID: f.smtp.UID,
		PeriodType: models.PeriodTypeRaw, PeriodStart: at, Status: &status, Metrics: models.JSONMap{},
		Output: models.JSONMap{"sentAt": "yesterday-ish"}, CreatedAt: at,
	}))

	wrote, err := f.watch.Sweep(t.Context(), f.worker, f.now)
	r.NoError(err)
	r.Zero(wrote)
	r.Empty(f.emailRows(t))
}

func TestDeliveryWatchSkipsEmailCheckWithMalformedConfig(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newWatchFixture(t, models.JSONMap{"max_delivery_delay": "10m"})
	bad := models.JSONMap{"token": 42, "max_delivery_delay": "10m"}
	r.NoError(f.svc.UpdateCheck(t.Context(), f.email.UID, &models.CheckUpdate{Config: &bad}))
	f.recordSend(t, f.now.Add(-3*time.Hour).Truncate(time.Second))

	wrote, err := f.watch.Sweep(t.Context(), f.worker, f.now)
	r.NoError(err)
	r.Zero(wrote)
}

func TestDeliveryWatchSweepReportsListingFailures(t *testing.T) {
	t.Parallel()

	for _, checkType := range []string{"email", "smtp"} {
		r := require.New(t)
		f := newWatchFixture(t, models.JSONMap{"max_delivery_delay": "10m"})
		f.fault.failListType = checkType

		_, err := f.watch.Sweep(t.Context(), f.worker, f.now)
		r.ErrorIs(err, errInjected, checkType)
	}
}

// A pair that cannot be evaluated is logged and skipped: the sweep goes on
// and writes nothing for it.
func TestDeliveryWatchSweepSurvivesPairFailures(t *testing.T) {
	t.Parallel()

	cases := map[string]func(f *watchFixture){
		"send lookup":  func(f *watchFixture) { f.fault.failSendList = true },
		"email lookup": func(f *watchFixture) { f.fault.failEmailList = true },
		"save":         func(f *watchFixture) { f.fault.failSave = true },
	}

	for name, fail := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			f := newWatchFixture(t, models.JSONMap{"max_delivery_delay": "10m"})
			f.recordSend(t, f.now.Add(-11*time.Minute).Truncate(time.Second))
			fail(f)

			wrote, err := f.watch.Sweep(t.Context(), f.worker, f.now)
			r.NoError(err)
			r.Zero(wrote)
			r.Empty(f.emailRows(t))
		})
	}
}

// The timeout row is kept even when the incident pipeline fails on it.
func TestDeliveryWatchKeepsRowWhenIncidentProcessingFails(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newWatchFixture(t, models.JSONMap{"max_delivery_delay": "10m"})
	f.recordSend(t, f.now.Add(-11*time.Minute).Truncate(time.Second))
	f.fault.failIncidentSQL = true

	wrote, err := f.watch.Sweep(t.Context(), f.worker, f.now)
	r.NoError(err)
	r.Equal(1, wrote)
	r.Len(f.emailRows(t), 1)
}

// The hold also ends when a real email arrived between the timeout and a later
// re-write of it: that re-write is the newest row but carries the old
// timeoutAt, and the newer signal must win.
func TestPassiveVerdictReleasesHoldForEmailBetweenTimeoutAndRewrite(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newWatchFixture(t, models.JSONMap{"max_delivery_delay": "10m"})
	f.recordSend(t, f.now.Add(-11*time.Minute).Truncate(time.Second))

	wrote, err := f.watch.Sweep(t.Context(), f.worker, f.now)
	r.NoError(err)
	r.Equal(1, wrote)

	var timeout models.Result
	r.NoError(f.svc.DB().NewSelect().Model(&timeout).
		Where("check_uid = ?", f.email.UID).Where("worker_uid IS NOT NULL").Scan(t.Context()))

	rewriteAt := f.now.Add(5 * time.Minute)
	down := int(checkerdef.StatusDown)
	r.NoError(f.svc.SaveResultWithStatusTracking(t.Context(), &models.Result{
		UID: "rewrite", OrganizationUID: f.email.OrganizationUID, CheckUID: f.email.UID,
		PeriodType: models.PeriodTypeRaw, PeriodStart: rewriteAt, WorkerUID: &f.worker, Status: &down,
		Metrics: models.JSONMap{}, Output: timeout.Output, CreatedAt: rewriteAt,
	}))
	f.saveSignal(t, "between", f.now.Add(2*time.Minute), checkerdef.StatusUp)

	be := backend.NewDirectBackend(f.svc, nil, nil, nil, nil)
	job := &models.CheckJob{
		OrganizationUID: f.email.OrganizationUID, CheckUID: f.email.UID, Type: string(checkerdef.CheckTypeEmail),
		Period: timeutils.Duration(time.Hour), Check: f.email,
	}

	status, _, _, err := passiveVerdict(t.Context(), be, job, f.now.Add(6*time.Minute))
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, status)
}

// syncBuffer is a log sink the evaluator may write to from its own goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// The evaluator sweeps at most once per deliveryWatchInterval, and logs a
// sweep that fails.
func TestPassiveEvaluatorSweepsDeliveriesOnItsInterval(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env, _ := newPassiveEvalEnv(t)
	evaluator := env.evaluator(t, "node-watch")

	logs := &syncBuffer{}
	evaluator.logger = slog.New(slog.NewTextHandler(logs, nil))
	evaluator.watch = NewDeliveryWatch(&faultyDB{Service: env.db, failListType: "email"}, nil)

	evaluator.sweepDeliveries(t.Context())
	r.Contains(logs.String(), "Delivery watch sweep failed")

	first := evaluator.lastWatch
	r.False(first.IsZero())

	before := logs.String()

	evaluator.sweepDeliveries(t.Context())
	r.Equal(first, evaluator.lastWatch, "inside the interval nothing runs")
	r.Equal(before, logs.String())

	evaluator.lastWatch = time.Now().Add(-2 * deliveryWatchInterval)
	evaluator.sweepDeliveries(t.Context())
	r.True(evaluator.lastWatch.After(first), "after the interval it sweeps again")
}
