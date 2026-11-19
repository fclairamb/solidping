package checkworker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

const watchToken = "feedfacefeedfacefeedfacefeedfacefeedfacefeedface"

type watchFixture struct {
	svc   *sqlite.Service
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

	jobSvc := jobsvc.NewService(svc.DB(), svc, notifier.NewLocalEventNotifier(), nil)
	incidentSvc := incidents.NewService(svc, jobSvc, clock.Real{}, nil)

	worker := &models.Worker{UID: "00000000-0000-4000-8000-000000000001", Slug: "jobs-test", Name: "jobs:test"}
	registered, err := svc.RegisterOrUpdateWorker(ctx, worker)
	r.NoError(err)

	return &watchFixture{
		worker: registered.UID,
		svc:    svc, watch: NewDeliveryWatch(svc, incidentSvc), email: email, smtp: smtp, now: time.Now(),
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
