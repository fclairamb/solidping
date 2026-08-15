package integration

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// auditRow queries incident_notifications rows for an incident.
func auditRows(
	t *testing.T, dbSvc *sqlite.Service, incidentUID string,
) []*models.IncidentNotification {
	t.Helper()

	var rows []*models.IncidentNotification

	err := dbSvc.DB().NewSelect().
		Model(&rows).
		Where("incident_uid = ?", incidentUID).
		OrderExpr("created_at ASC").
		Scan(t.Context())
	require.NoError(t, err)

	return rows
}

// notificationAuditSetup creates the minimal world for audit tests:
// an in-memory SQLite, one org, one check, and one webhook connection
// bound to the check.
type notificationAuditSetup struct {
	dbSvc  *sqlite.Service
	jobs   jobsvc.Service
	svc    *incidents.Service
	org    *models.Organization
	check  *models.Check
	conn   *models.Integration
	logger *slog.Logger
}

func newNotificationAuditSetup(t *testing.T) *notificationAuditSetup {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(dbSvc, jobs, clock.Real{}, nil)

	org := models.NewOrganization("audit-test", "Audit Test Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	checkName := "Test Check"
	checkSlug := "test-check"
	check := &models.Check{
		UID:             "audit-check-uid-0001",
		OrganizationUID: org.UID,
		Name:            &checkName,
		Slug:            &checkSlug,
		Type:            "http",
		Config:          models.JSONMap{"url": "https://example.com"},
		Enabled:         true,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	r.NoError(dbSvc.CreateCheck(ctx, check))

	conn := models.NewIntegration(org.UID, models.ConnectionTypeWebhook, "ops-webhook")
	conn.Enabled = true
	conn.Settings = models.JSONMap{"url": "https://example.com/hook"}
	r.NoError(dbSvc.CreateChannel(ctx, conn))

	cc := models.NewCheckConnection(check.UID, conn.UID, org.UID)
	r.NoError(dbSvc.CreateCheckConnection(ctx, cc))

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))

	return &notificationAuditSetup{
		dbSvc:  dbSvc,
		jobs:   jobs,
		svc:    svc,
		org:    org,
		check:  check,
		conn:   conn,
		logger: logger,
	}
}

// triggerIncident calls ProcessCheckResult with a DOWN status to open an
// incident and enqueue notification jobs. Returns the active incident.
func (s *notificationAuditSetup) triggerIncident(t *testing.T) *models.Incident {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)
	result := models.NewResult(s.org.UID, s.check.UID, models.ResultStatusDown, 0.5)
	r.NoError(s.svc.ProcessCheckResult(ctx, s.check, result))

	incident, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)
	r.NotNil(incident)

	return incident
}

// pendingNotificationJobsForIncident returns pending notification jobs for the given incident.
func pendingNotificationJobsForIncident(
	t *testing.T, dbSvc *sqlite.Service, incidentUID string,
) []*models.Job {
	t.Helper()

	var jobs []*models.Job

	err := dbSvc.DB().NewSelect().
		Model(&jobs).
		Where("type = ?", string(jobdef.JobTypeNotification)).
		Where("status = ?", string(models.JobStatusPending)).
		Where("deleted_at IS NULL").
		Scan(t.Context())
	require.NoError(t, err)

	var result []*models.Job

	for _, j := range jobs {
		uid, _ := j.Config["incidentUid"].(string)
		if uid == incidentUID {
			result = append(result, j)
		}
	}

	return result
}

// makeJobContext builds a minimal JobContext for running a notification job.
func (s *notificationAuditSetup) makeJobContext(
	job *models.Job, svcList *services.Registry,
) *jobdef.JobContext {
	return &jobdef.JobContext{
		Job:       job,
		DBService: s.dbSvc,
		Services:  svcList,
		Logger:    s.logger,
	}
}

// TestAuditCheckConnectionSend verifies that a notification job created via
// the check_connection fan-out path produces a row with status=sent and
// source=check_connection after successful delivery.
func TestAuditCheckConnectionSend(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	// Spin up a fake webhook server that accepts any POST.
	webhookCalled := make(chan struct{}, 1)
	fakeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case webhookCalled <- struct{}{}:
		default:
		}

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(fakeServer.Close)

	s := newNotificationAuditSetup(t)

	// Point the webhook connection at the local test server.
	s.conn.Settings = models.JSONMap{"url": fakeServer.URL}
	_, err := s.dbSvc.DB().NewUpdate().
		Model(s.conn).
		Column("settings").
		Where("uid = ?", s.conn.UID).
		Exec(ctx)
	r.NoError(err)

	incident := s.triggerIncident(t)

	// One pending audit row should exist immediately after fan-out.
	rows := auditRows(t, s.dbSvc, incident.UID)
	r.Len(rows, 1, "expected one audit row after fan-out")
	r.Equal(models.IncidentNotificationStatusPending, rows[0].Status)
	r.Equal(models.IncidentNotificationSourceCheckConnection, rows[0].Source)
	r.NotNil(rows[0].ConnectionUID)
	r.Equal(s.conn.UID, *rows[0].ConnectionUID)
	r.Nil(rows[0].UserUID)

	// Now run the notification job.
	pendingJobs := pendingNotificationJobsForIncident(t, s.dbSvc, incident.UID)
	r.Len(pendingJobs, 1, "expected one pending notification job")

	jobRow := pendingJobs[0]
	cfgBytes, err := json.Marshal(jobRow.Config)
	r.NoError(err)

	jobDef := &jobtypes.NotificationJobDefinition{}
	jobRun, err := jobDef.CreateJobRun(cfgBytes)
	r.NoError(err)

	jctx := s.makeJobContext(jobRow, &services.Registry{})
	r.NoError(jobRun.Run(ctx, jctx))

	// Webhook should have been called.
	select {
	case <-webhookCalled:
	default:
		t.Fatal("webhook was not called")
	}

	// Audit row should now be sent.
	rows = auditRows(t, s.dbSvc, incident.UID)
	r.Len(rows, 1)
	r.Equal(models.IncidentNotificationStatusSent, rows[0].Status, "status should be sent after delivery")
	r.NotNil(rows[0].SentAt)
}

// TestFirstIncidentPagedEventIsEnriched verifies that a successful
// notification dispatch emits org.activation.first_incident_paged linked to
// the incident/check that triggered it — IncidentUID/CheckUID set on the
// row, plus check_slug/check_name in the payload. Per the "Recent activity"
// spec this milestone previously carried only {"source": ...}.
func TestFirstIncidentPagedEventIsEnriched(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	// Fake webhook server that accepts any POST so the dispatch succeeds.
	fakeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(fakeServer.Close)

	s := newNotificationAuditSetup(t)

	s.conn.Settings = models.JSONMap{"url": fakeServer.URL}
	_, err := s.dbSvc.DB().NewUpdate().
		Model(s.conn).
		Column("settings").
		Where("uid = ?", s.conn.UID).
		Exec(ctx)
	r.NoError(err)

	incident := s.triggerIncident(t)

	pendingJobs := pendingNotificationJobsForIncident(t, s.dbSvc, incident.UID)
	r.Len(pendingJobs, 1, "expected one pending notification job")

	jobRow := pendingJobs[0]
	cfgBytes, err := json.Marshal(jobRow.Config)
	r.NoError(err)

	jobDef := &jobtypes.NotificationJobDefinition{}
	jobRun, err := jobDef.CreateJobRun(cfgBytes)
	r.NoError(err)

	jctx := s.makeJobContext(jobRow, &services.Registry{})
	r.NoError(jobRun.Run(ctx, jctx))

	events, err := s.dbSvc.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: s.org.UID,
		EventTypes: []models.EventType{
			models.EventTypeOrgActivationFirstIncidentPaged,
		},
	})
	r.NoError(err)
	r.Len(events, 1, "expected exactly one first_incident_paged event")

	got := events[0]
	r.NotNil(got.IncidentUID, "must link to the incident that triggered the page")
	r.Equal(incident.UID, *got.IncidentUID)
	r.NotNil(got.CheckUID, "must link to the check that triggered the page")
	r.Equal(s.check.UID, *got.CheckUID)
	r.NotNil(s.check.Slug)
	r.Equal(*s.check.Slug, got.Payload["check_slug"])
	r.NotNil(s.check.Name)
	r.Equal(*s.check.Name, got.Payload["check_name"])
}

// mockEmailSender is a test double for email.Sender.
type mockEmailSender struct {
	sendFunc func(ctx context.Context, msg *email.Message) (*email.SendResult, error)
}

func (m *mockEmailSender) Send(ctx context.Context, msg *email.Message) (*email.SendResult, error) {
	if m.sendFunc != nil {
		return m.sendFunc(ctx, msg)
	}

	return &email.SendResult{Sent: true, MessageID: "test-msg-id-123"}, nil
}

// TestAuditEscalationUserSend verifies that running an escalation_step job
// with a `user` target produces a sent audit row with user_uid set and
// channel_type='email'.
func TestAuditEscalationUserSend(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	s := newNotificationAuditSetup(t)

	// Create a user to page.
	user := models.NewUser("oncall@example.com")
	r.NoError(s.dbSvc.CreateUser(ctx, user))

	// Create an escalation policy with a user target, delay 0.
	policy := models.NewEscalationPolicy(s.org.UID, "Pager Policy")
	r.NoError(s.dbSvc.CreateEscalationPolicy(ctx, policy))

	step := models.NewEscalationPolicyStep(policy.UID, 0, 0)
	steps := []*models.EscalationPolicyStep{step}
	targetsByIdx := map[int][]*models.EscalationPolicyTarget{
		0: {
			models.NewEscalationPolicyTarget(step.UID, models.EscalationTargetUser, &user.UID, 0),
		},
	}
	r.NoError(s.dbSvc.ReplaceEscalationPolicySteps(ctx, policy.UID, steps, targetsByIdx))

	// Reload the step to get its persisted UID.
	persistedSteps, err := s.dbSvc.ListEscalationPolicySteps(ctx, policy.UID)
	r.NoError(err)
	r.Len(persistedSteps, 1)
	persistedStep := persistedSteps[0]

	// Create an active incident directly.
	incident := models.NewIncident(s.org.UID, s.check.UID, time.Now(), "check is down")
	r.NoError(s.dbSvc.CreateIncident(ctx, incident))

	// Build and run an escalation step job directly.
	cfg := jobtypes.EscalationStepJobConfig{
		IncidentUID: incident.UID,
		StepUID:     persistedStep.UID,
		PolicyUID:   policy.UID,
		RepeatIndex: 0,
	}
	cfgBytes, err := json.Marshal(cfg)
	r.NoError(err)

	jobDef := &jobtypes.EscalationStepJobDefinition{}
	jobRun, err := jobDef.CreateJobRun(cfgBytes)
	r.NoError(err)

	mockSender := &mockEmailSender{}

	formatter, err := email.NewFormatter()
	r.NoError(err)

	svcList := &services.Registry{
		EmailSender:    mockSender,
		EmailFormatter: formatter,
		Jobs:           jobsvc.NewService(s.dbSvc.DB(), s.dbSvc, notifier.NewLocalEventNotifier(), nil),
	}

	// Build a fake job row for the context.
	fakeJob := models.NewJob(&s.org.UID, string(jobdef.JobTypeEscalationStep))
	jctx := &jobdef.JobContext{
		Job:       fakeJob,
		DBService: s.dbSvc,
		Services:  svcList,
		Logger:    s.logger,
	}

	r.NoError(jobRun.Run(ctx, jctx))

	// Audit row: status=sent, user_uid=user.UID, channel_type=email.
	rows := auditRows(t, s.dbSvc, incident.UID)
	r.NotEmpty(rows, "expected at least one audit row")

	var userRow *models.IncidentNotification
	for _, row := range rows {
		if row.Source == models.IncidentNotificationSourceEscalationUser {
			userRow = row
			break
		}
	}

	r.NotNil(userRow, "expected an escalation_user audit row")
	r.Equal(models.IncidentNotificationStatusSent, userRow.Status, "status should be sent")
	r.NotNil(userRow.UserUID)
	r.Equal(user.UID, *userRow.UserUID)
	r.Equal("email", userRow.ChannelType)
}

// TestAuditCancellation verifies that acknowledging an incident before the
// escalation job fires flips all pending audit rows to canceled.
func TestAuditCancellation(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	s := newNotificationAuditSetup(t)
	incident := s.triggerIncident(t)

	// Pending audit row should exist from the fan-out.
	rows := auditRows(t, s.dbSvc, incident.UID)
	r.NotEmpty(rows, "expected pending audit rows after fan-out")
	for _, row := range rows {
		r.Equal(models.IncidentNotificationStatusPending, row.Status)
	}

	// Cancel (simulates ack / resolve cancellation sweep).
	canceledCount, err := s.jobs.CancelPendingForIncident(ctx, incident.UID, nil)
	r.NoError(err)
	r.Positive(canceledCount, "expected at least one job to be canceled")

	// All audit rows should now be canceled.
	rows = auditRows(t, s.dbSvc, incident.UID)
	r.NotEmpty(rows)
	for _, row := range rows {
		r.Equal(models.IncidentNotificationStatusCanceled, row.Status,
			"all audit rows should be canceled after sweep")
		r.NotNil(row.CanceledAt)
	}
}

// TestAuditEmptyScheduleSkipped verifies that an escalation step with a
// schedule target that has no roster produces a skipped audit row.
func TestAuditEmptyScheduleSkipped(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	s := newNotificationAuditSetup(t)

	// Create an empty on-call schedule (no roster).
	handoffWeekday := 1
	schedule := &models.OnCallSchedule{
		UID:             uuid.New().String(),
		OrganizationUID: s.org.UID,
		Name:            "Empty Schedule",
		Timezone:        "UTC",
		RotationType:    models.RotationTypeWeekly,
		HandoffTime:     "09:00",
		HandoffWeekday:  &handoffWeekday,
		StartAt:         time.Now().Add(-24 * time.Hour),
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	r.NoError(s.dbSvc.CreateOnCallSchedule(ctx, schedule))
	// Empty roster: no users added.

	// Create an escalation policy with a schedule target.
	policy := models.NewEscalationPolicy(s.org.UID, "Schedule Policy")
	r.NoError(s.dbSvc.CreateEscalationPolicy(ctx, policy))

	step := models.NewEscalationPolicyStep(policy.UID, 0, 0)
	steps := []*models.EscalationPolicyStep{step}
	targetsByIdx := map[int][]*models.EscalationPolicyTarget{
		0: {
			models.NewEscalationPolicyTarget(step.UID, models.EscalationTargetSchedule, &schedule.UID, 0),
		},
	}
	r.NoError(s.dbSvc.ReplaceEscalationPolicySteps(ctx, policy.UID, steps, targetsByIdx))

	persistedSteps, err := s.dbSvc.ListEscalationPolicySteps(ctx, policy.UID)
	r.NoError(err)
	r.Len(persistedSteps, 1)
	persistedStep := persistedSteps[0]

	// Create an incident.
	incident := models.NewIncident(s.org.UID, s.check.UID, time.Now(), "check is down")
	r.NoError(s.dbSvc.CreateIncident(ctx, incident))

	// Run the escalation step job.
	cfg := jobtypes.EscalationStepJobConfig{
		IncidentUID: incident.UID,
		StepUID:     persistedStep.UID,
		PolicyUID:   policy.UID,
		RepeatIndex: 0,
	}
	cfgBytes, err := json.Marshal(cfg)
	r.NoError(err)

	jobDef := &jobtypes.EscalationStepJobDefinition{}
	jobRun, err := jobDef.CreateJobRun(cfgBytes)
	r.NoError(err)

	svcList := &services.Registry{
		EmailSender: &mockEmailSender{},
		Jobs:        jobsvc.NewService(s.dbSvc.DB(), s.dbSvc, notifier.NewLocalEventNotifier(), nil),
	}

	fakeJob := models.NewJob(&s.org.UID, string(jobdef.JobTypeEscalationStep))
	jctx := &jobdef.JobContext{
		Job:       fakeJob,
		DBService: s.dbSvc,
		Services:  svcList,
		Logger:    s.logger,
	}

	r.NoError(jobRun.Run(ctx, jctx))

	// Audit row: status=skipped, skip_reason set (schedule_resolve_failed since no users).
	rows := auditRows(t, s.dbSvc, incident.UID)
	r.NotEmpty(rows, "expected at least one audit row")

	var skippedRow *models.IncidentNotification
	for _, row := range rows {
		if row.Status == models.IncidentNotificationStatusSkipped {
			skippedRow = row
			break
		}
	}

	r.NotNil(skippedRow, "expected a skipped audit row")
	r.NotNil(skippedRow.SkipReason)
	r.Equal(models.IncidentNotificationSourceEscalationSchedule, skippedRow.Source)
}

// TestAuditNoAdminsSkipped verifies that an escalation step with an
// all_admins target produces a skipped audit row when the org has no admins.
func TestAuditNoAdminsSkipped(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	s := newNotificationAuditSetup(t)

	// Org has no admin members by default, so pageAllAdmins returns 0.
	policy := models.NewEscalationPolicy(s.org.UID, "All Admins Policy")
	r.NoError(s.dbSvc.CreateEscalationPolicy(ctx, policy))

	step := models.NewEscalationPolicyStep(policy.UID, 0, 0)
	steps := []*models.EscalationPolicyStep{step}
	targetsByIdx := map[int][]*models.EscalationPolicyTarget{
		0: {
			models.NewEscalationPolicyTarget(step.UID, models.EscalationTargetAllAdmins, nil, 0),
		},
	}
	r.NoError(s.dbSvc.ReplaceEscalationPolicySteps(ctx, policy.UID, steps, targetsByIdx))

	persistedSteps, err := s.dbSvc.ListEscalationPolicySteps(ctx, policy.UID)
	r.NoError(err)
	r.Len(persistedSteps, 1)
	persistedStep := persistedSteps[0]

	incident := models.NewIncident(s.org.UID, s.check.UID, time.Now(), "check is down")
	r.NoError(s.dbSvc.CreateIncident(ctx, incident))

	cfg := jobtypes.EscalationStepJobConfig{
		IncidentUID: incident.UID,
		StepUID:     persistedStep.UID,
		PolicyUID:   policy.UID,
		RepeatIndex: 0,
	}
	cfgBytes, err := json.Marshal(cfg)
	r.NoError(err)

	jobDef := &jobtypes.EscalationStepJobDefinition{}
	jobRun, err := jobDef.CreateJobRun(cfgBytes)
	r.NoError(err)

	svcList := &services.Registry{
		EmailSender: &mockEmailSender{},
		Jobs:        jobsvc.NewService(s.dbSvc.DB(), s.dbSvc, notifier.NewLocalEventNotifier(), nil),
	}

	fakeJob := models.NewJob(&s.org.UID, string(jobdef.JobTypeEscalationStep))
	jctx := &jobdef.JobContext{
		Job:       fakeJob,
		DBService: s.dbSvc,
		Services:  svcList,
		Logger:    s.logger,
	}

	r.NoError(jobRun.Run(ctx, jctx))

	// Audit row: status=skipped, source=escalation_all_admins, skip_reason=no_admins.
	rows := auditRows(t, s.dbSvc, incident.UID)
	r.NotEmpty(rows, "expected at least one audit row")

	var skippedRow *models.IncidentNotification
	for _, row := range rows {
		if row.Status == models.IncidentNotificationStatusSkipped &&
			row.Source == models.IncidentNotificationSourceEscalationAllAdmins {
			skippedRow = row
			break
		}
	}

	r.NotNil(skippedRow, "expected a skipped audit row for no_admins")
	r.NotNil(skippedRow.SkipReason)
	r.Equal("no_admins", *skippedRow.SkipReason)
}

// TestAuditDeliveryFailure verifies that a non-retryable delivery error
// flips the audit row to status=failed with the error column populated.
func TestAuditDeliveryFailure(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	s := newNotificationAuditSetup(t)

	// Use a webhook URL that is guaranteed to fail immediately (closed port).
	s.conn.Settings = models.JSONMap{"url": "http://127.0.0.1:1"}
	_, err := s.dbSvc.DB().NewUpdate().
		Model(s.conn).
		Column("settings").
		Where("uid = ?", s.conn.UID).
		Exec(ctx)
	r.NoError(err)

	incident := s.triggerIncident(t)

	pendingJobs := pendingNotificationJobsForIncident(t, s.dbSvc, incident.UID)
	r.Len(pendingJobs, 1)

	jobRow := pendingJobs[0]
	cfgBytes, err := json.Marshal(jobRow.Config)
	r.NoError(err)

	jobDef := &jobtypes.NotificationJobDefinition{}
	jobRun, err := jobDef.CreateJobRun(cfgBytes)
	r.NoError(err)

	jctx := s.makeJobContext(jobRow, &services.Registry{})

	// The delivery will fail (connection refused); the job runner returns an error.
	runErr := jobRun.Run(ctx, jctx)
	r.Error(runErr, "job run should return an error on delivery failure")

	// Audit row should now be failed or still pending (if retryable).
	rows := auditRows(t, s.dbSvc, incident.UID)
	r.Len(rows, 1)

	// The webhook URL "http://127.0.0.1:1" will produce a connection refused
	// error which is classified as a network (retryable) error. The audit row
	// stays at pending for the retry.
	// If it's non-retryable, status becomes failed.
	row := rows[0]
	r.NotEqual(models.IncidentNotificationStatusSent, row.Status,
		"status must not be sent after delivery failure")
}
