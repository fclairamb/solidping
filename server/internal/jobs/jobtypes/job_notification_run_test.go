package jobtypes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifications"
)

// notificationEnv is the reminder world plus what one notification job needs:
// a webhook receiver, the job row and its pending audit row.
type notificationEnv struct {
	*reminderEnv

	server   *httptest.Server
	status   atomic.Int64
	received atomic.Int64
	job      *models.Job
}

func newNotificationEnv(t *testing.T) *notificationEnv {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	env := &notificationEnv{reminderEnv: newReminderEnv(t)}
	env.status.Store(http.StatusOK)

	env.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		env.received.Add(1)
		w.WriteHeader(int(env.status.Load()))
	}))
	t.Cleanup(env.server.Close)

	settings := models.JSONMap{"url": env.server.URL}
	r.NoError(env.db.UpdateChannel(ctx, env.webhook.UID, &models.IntegrationUpdate{Settings: &settings}))

	job, err := env.jobs.CreateJob(ctx, env.org.UID, string(jobdef.JobTypeNotification), json.RawMessage(`{}`), nil)
	r.NoError(err)

	env.job = job
	env.jctx.Job = job

	r.NoError(env.db.CreateIncidentNotification(ctx, models.NewIncidentNotificationForJob(
		env.org.UID, env.incident.UID, string(models.EventTypeIncidentCreated),
		models.IncidentNotificationSourceCheckConnection,
		env.webhook.UID, job.UID, string(models.ConnectionTypeWebhook), nil, nil,
	)))

	return env
}

func (e *notificationEnv) run(t *testing.T, connectionUID, incidentUID string) error {
	t.Helper()

	cfg, err := json.Marshal(NotificationJobConfig{
		ConnectionUID: connectionUID, IncidentUID: incidentUID,
		EventType: string(models.EventTypeIncidentCreated),
	})
	require.NoError(t, err)

	runner, err := (&NotificationJobDefinition{}).CreateJobRun(cfg)
	require.NoError(t, err)

	return runner.Run(t.Context(), e.jctx)
}

// audit returns the status, error and message id of the job's audit row.
func (e *notificationEnv) audit(t *testing.T) (string, string, string) {
	t.Helper()

	var row struct {
		Status    string  `bun:"status"`
		Error     *string `bun:"error"`
		MessageID *string `bun:"message_id"`
	}

	require.NoError(t, e.db.DB().NewSelect().TableExpr("incident_notifications").
		Column("status", "error", "message_id").
		Where("job_uid = ?", e.job.UID).Scan(t.Context(), &row))

	deref := func(value *string) string {
		if value == nil {
			return ""
		}

		return *value
	}

	return row.Status, deref(row.Error), deref(row.MessageID)
}

func (e *notificationEnv) channel(t *testing.T) *models.Integration {
	t.Helper()

	channel, err := e.db.GetChannel(t.Context(), e.webhook.UID)
	require.NoError(t, err)

	return channel
}

func TestNotificationJob_ConfigIsValidated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config string
		want   error
	}{
		{"no incident", `{"connectionUid":"c","eventType":"incident.created"}`, ErrMissingIncidentUID},
		{"no event type", `{"connectionUid":"c","incidentUid":"i"}`, ErrMissingEventType},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runner, err := (&NotificationJobDefinition{}).CreateJobRun(json.RawMessage(tt.config))
			require.ErrorIs(t, err, tt.want)
			require.Nil(t, runner)
		})
	}
}

// The delivery and the audit row move together: sent with the webhook id on
// success, failed with the reason on a refusal, left pending when the failure
// is a network one that a retry can fix.
func TestNotificationJob_DeliveryOutcomes(t *testing.T) {
	t.Parallel()

	t.Run("delivered", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		env := newNotificationEnv(t)

		r.NoError(env.run(t, env.webhook.UID, env.incident.UID))
		r.Equal(int64(1), env.received.Load())

		status, errMsg, messageID := env.audit(t)
		r.Equal(models.IncidentNotificationStatusSent, status)
		r.Empty(errMsg)
		r.NotEmpty(messageID, "the webhook id is kept on the audit row")

		// First delivery of a channel without a signing secret: one is
		// generated and persisted. Without a master key it is stored in clear.
		channel := env.channel(t)
		r.NotEmpty(channel.Settings["signingSecret"])
		r.Nil(channel.SettingsPrivate)
	})

	t.Run("refused by the receiver", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		env := newNotificationEnv(t)
		env.status.Store(http.StatusBadRequest)

		err := env.run(t, env.webhook.UID, env.incident.UID)
		r.ErrorIs(err, notifications.ErrWebhookRequestFailed)
		r.False(jobdef.IsRetryable(err))
		r.Equal(int64(1), env.received.Load())

		status, errMsg, _ := env.audit(t)
		r.Equal(models.IncidentNotificationStatusFailed, status)
		r.Contains(errMsg, "status 400")
	})

	t.Run("receiver unreachable", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		env := newNotificationEnv(t)
		env.server.Close()

		err := env.run(t, env.webhook.UID, env.incident.UID)
		r.Error(err)
		r.True(jobdef.IsRetryable(err), "a network failure is worth a retry")

		status, _, _ := env.audit(t)
		r.Equal(models.IncidentNotificationStatusPending, status, "the retry will settle the row")
	})
}

// Everything the job needs is loaded before anything is sent: when one piece
// is missing the job fails with the matching error and the receiver hears
// nothing.
func TestNotificationJob_MissingPieces(t *testing.T) {
	t.Parallel()

	const unknown = "00000000-0000-0000-0000-000000000000"

	tests := []struct {
		name string
		// arrange returns the connection and incident UIDs to run with.
		arrange func(t *testing.T, env *notificationEnv) (string, string)
		fail    []string
		want    error
	}{
		{
			name:    "unknown connection",
			arrange: func(_ *testing.T, env *notificationEnv) (string, string) { return unknown, env.incident.UID },
			want:    ErrConnectionNotFound,
		},
		{
			name:    "unknown incident",
			arrange: func(_ *testing.T, env *notificationEnv) (string, string) { return env.webhook.UID, unknown },
			want:    ErrIncidentNotFound,
		},
		{
			name: "check cannot be loaded",
			arrange: func(_ *testing.T, env *notificationEnv) (string, string) {
				return env.webhook.UID, env.incident.UID
			},
			fail: []string{`FROM "checks"`},
			want: ErrCheckNotFound,
		},
		{
			// A data-source integration has no sender.
			name: "connection type cannot notify",
			arrange: func(t *testing.T, env *notificationEnv) (string, string) {
				t.Helper()

				freebox := models.NewIntegration(env.org.UID, models.ConnectionTypeFreebox, "box")
				require.NoError(t, env.db.CreateChannel(t.Context(), freebox))

				return freebox.UID, env.incident.UID
			},
			want: ErrSenderNotFound,
		},
		{
			// Sealed settings and no master key: never send with half a credential.
			name: "sealed settings without a master key",
			arrange: func(t *testing.T, env *notificationEnv) (string, string) {
				t.Helper()

				sealed := "v1:not-a-plaintext-envelope"
				require.NoError(t, env.db.UpdateChannel(t.Context(), env.webhook.UID,
					&models.IntegrationUpdate{SettingsPrivate: &sealed}))

				return env.webhook.UID, env.incident.UID
			},
			want: ErrEncryptionDisabled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := newNotificationEnv(t)
			connectionUID, incidentUID := tt.arrange(t, env)

			var fault *queryFault
			if tt.fail != nil {
				fault = failQueries(env.db.DB(), tt.fail...)
			}

			err := env.run(t, connectionUID, incidentUID)

			if fault != nil {
				fault.requireFired(t)
			}

			r.ErrorIs(err, tt.want)
			r.False(jobdef.IsRetryable(err))
			r.Zero(env.received.Load(), "nothing is sent")

			status, _, _ := env.audit(t)
			r.Equal(models.IncidentNotificationStatusPending, status)
		})
	}
}

func notificationCredentials(t *testing.T, env *notificationEnv) {
	t.Helper()

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}

	creds, err := credentials.NewService(key, credentials.NewParamStore(env.db))
	require.NoError(t, err)

	env.jctx.Services.Credentials = creds
}

// With a master key the generated signing secret is sealed: it leaves the
// public settings and the next delivery can still open it.
func TestNotificationJob_SigningSecretIsSealedWhenEncryptionIsOn(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newNotificationEnv(t)
	notificationCredentials(t, env)

	r.NoError(env.run(t, env.webhook.UID, env.incident.UID))

	channel := env.channel(t)
	r.NotContains(channel.Settings, "signingSecret", "the secret is not stored in clear")
	r.Equal(env.server.URL, channel.Settings["url"], "public settings are kept")
	r.NotNil(channel.SettingsPrivate)
	r.NotEmpty(*channel.SettingsPrivate)
	r.NotNil(channel.SettingsPrivateKeys)
	r.JSONEq(`["signingSecret"]`, *channel.SettingsPrivateKeys)

	// The second delivery opens the sealed secret instead of generating one.
	sealed := *channel.SettingsPrivate
	r.NoError(env.run(t, env.webhook.UID, env.incident.UID))
	r.Equal(int64(2), env.received.Load())
	r.Equal(sealed, *env.channel(t).SettingsPrivate)
}

// A sealed envelope the key cannot open fails the job before anything is sent.
func TestNotificationJob_UnreadableSealedSettings(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newNotificationEnv(t)
	notificationCredentials(t, env)

	sealed := "v1:not-an-envelope"
	r.NoError(env.db.UpdateChannel(t.Context(), env.webhook.UID,
		&models.IntegrationUpdate{SettingsPrivate: &sealed}))

	err := env.run(t, env.webhook.UID, env.incident.UID)
	r.ErrorContains(err, "decrypt connection settings")
	r.NotErrorIs(err, ErrEncryptionDisabled)
	r.Zero(env.received.Load())
}

// The org row only feeds links and branding: when it cannot be read the
// notification still goes out.
func TestNotificationJob_OrgLookupFailureStillDelivers(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newNotificationEnv(t)
	fault := failQueries(env.db.DB(), `FROM "organizations"`)

	r.NoError(env.run(t, env.webhook.UID, env.incident.UID))
	fault.requireFired(t)
	r.Equal(int64(1), env.received.Load())
}

func TestLookupOrgIdentity(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newNotificationEnv(t)
	run := &NotificationJobRun{}

	logo := "https://acme.com/logo.png"
	_, err := env.db.DB().NewUpdate().TableExpr("organizations").
		Set("logo_url = ?", logo).Where("uid = ?", env.org.UID).Exec(t.Context())
	r.NoError(err)

	r.Equal(orgIdentity{Slug: env.org.Slug, Name: env.org.Name, LogoURL: logo},
		run.lookupOrgIdentity(t.Context(), env.jctx, env.jctx.Logger, env.org.UID))
	r.Equal(orgIdentity{},
		run.lookupOrgIdentity(t.Context(), env.jctx, env.jctx.Logger, "00000000-0000-0000-0000-000000000000"))
}

// retryChainJobs answers GetJob from a fixed set of rows.
type retryChainJobs struct {
	jobsvc.Service

	rows map[string]*models.Job
}

func (j *retryChainJobs) GetJob(_ context.Context, uid string) (*models.Job, error) {
	job, ok := j.rows[uid]
	if !ok {
		return nil, errTestBoom
	}

	return job, nil
}

// The first job of a retry chain names the delivery across every attempt.
// When a link cannot be loaded, the oldest UID reached is used.
func TestRootJobUID_StopsAtAnUnreadableLink(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	second, first := "second", "first"
	jobs := &retryChainJobs{rows: map[string]*models.Job{
		"second": {UID: "second", PreviousJobUID: &first},
		"first":  {UID: "first"},
	}}
	jctx := sweepJctx(jobs)

	jctx.Job = &models.Job{UID: "third", PreviousJobUID: &second}
	r.Equal("first", rootJobUID(t.Context(), jctx), "positive control: the chain is walked to its root")

	delete(jobs.rows, "second")
	r.Equal("second", rootJobUID(t.Context(), jctx))
}
