package reportschedules

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/uptimereport"
)

// recordingMailer captures every SendReport call so tests can assert exactly
// how many mails were enqueued, without a real queue.
type recordingMailer struct {
	mu   sync.Mutex
	sent []string // recipients
}

func (m *recordingMailer) SendReport(
	_ context.Context, _, recipient string, _ *uptimereport.Data, _ string,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.sent = append(m.sent, recipient)

	return nil
}

func (m *recordingMailer) sentTo() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]string(nil), m.sent...)
}

// testSendEnv builds a Service wired to a fresh in-memory SQLite, a real
// (nil-slos) Builder, and a recordingMailer — enough to exercise TestSend end
// to end, since an org-wide schedule with zero checks in the org short-circuits
// Builder.Build before it ever needs a real SLO service.
type testSendEnv struct {
	svc    *Service
	mailer *recordingMailer
	org    *models.Organization
}

func setupTestSendEnv(t *testing.T) *testSendEnv {
	t.Helper()

	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("test-send-org", "Test Send Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	cfg := &config.Config{}
	builder := uptimereport.NewBuilder(dbSvc, cfg, nil)
	mailer := &recordingMailer{}

	return &testSendEnv{
		svc:    NewService(dbSvc, cfg, builder, mailer),
		mailer: mailer,
		org:    org,
	}
}

// createSchedule creates a bare org-wide monthly schedule to test-send against.
func (e *testSendEnv) createSchedule(t *testing.T) Response {
	t.Helper()

	ctx := context.Background()

	row, err := e.svc.Create(ctx, e.org.Slug, &CreateRequest{
		Name:      "Test schedule",
		Frequency: models.ReportFrequencyMonthly,
	})
	require.NoError(t, err)

	return row
}

// TestTestSend_SuppressedRecipient covers the secondary defect fixed alongside
// the 202-empty-body client bug: a suppressed recipient must be reported, not
// silently swallowed as if the mail had been queued.
func TestTestSend_SuppressedRecipient(t *testing.T) {
	t.Parallel()

	env := setupTestSendEnv(t)
	schedule := env.createSchedule(t)

	const recipient = "unsubscribed@example.com"

	ctx := context.Background()
	sup := models.NewEmailSuppression(env.org.UID, recipient, nil, models.EmailSuppressionSourceLink)
	require.NoError(t, env.svc.db.CreateEmailSuppression(ctx, sup))

	err := env.svc.TestSend(ctx, env.org.Slug, schedule.UID, recipient, time.Now())

	require.ErrorIs(t, err, ErrRecipientSuppressed,
		"suppressed recipient must surface ErrRecipientSuppressed, not a silent nil success")
	require.Empty(t, env.mailer.sentTo(), "a suppressed recipient must never be handed to the mailer")
}

// TestTestSend_NonSuppressedRecipient is the positive control for the test
// above: a normal recipient still enqueues exactly one mail, proving the
// suppression check didn't just start rejecting everything.
func TestTestSend_NonSuppressedRecipient(t *testing.T) {
	t.Parallel()

	env := setupTestSendEnv(t)
	schedule := env.createSchedule(t)

	const recipient = "operator@example.com"

	err := env.svc.TestSend(context.Background(), env.org.Slug, schedule.UID, recipient, time.Now())

	require.NoError(t, err)
	require.Equal(t, []string{recipient}, env.mailer.sentTo())
}

// TestTestSend_ZeroCheckOrgStillSends is spec case 13. TestSend is the manual
// "send me one now" path (service.go TestSend -> the same Builder.Build the
// sweep uses): a user pressing the button has explicitly asked to see what
// the report looks like, so it must keep sending even for a zero-check org,
// unlike the scheduled sweep which now suppresses that same empty scope
// (job_uptime_report.go's guard A). setupTestSendEnv's org has no checks by
// definition, so this pins that behaviour is unaffected by the sweep-side fix.
func TestTestSend_ZeroCheckOrgStillSends(t *testing.T) {
	t.Parallel()

	env := setupTestSendEnv(t)
	schedule := env.createSchedule(t)

	const recipient = "operator@example.com"

	err := env.svc.TestSend(context.Background(), env.org.Slug, schedule.UID, recipient, time.Now())

	require.NoError(t, err)
	require.Equal(t, []string{recipient}, env.mailer.sentTo(),
		"a manual test send must still mail an empty report, unlike the scheduled sweep")
}
