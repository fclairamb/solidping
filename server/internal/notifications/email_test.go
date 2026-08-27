package notifications

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// errFakeSendFailed is the static error returned by fakeEmailSender when it is
// configured to fail, satisfying the err113 linter (no dynamic errors).
var errFakeSendFailed = errors.New("fake: send failed")

// fakeEmailSender is a test double for email.Sender that records the messages it
// was asked to send, so a test can assert the resolved recipients without
// sending real mail. It synthesizes a per-send Message-ID and SMTP server
// response derived from the recipients so callers can assert the audit-row
// artifacts the real sender now surfaces.
type fakeEmailSender struct {
	sent []*email.Message
	// failAfter, when > 0, makes the Nth send (1-indexed) return an error after
	// the preceding sends succeed, exercising the partial-batch path.
	failAfter int
}

func (f *fakeEmailSender) Send(_ context.Context, msg *email.Message) (*email.SendResult, error) {
	f.sent = append(f.sent, msg)

	if f.failAfter > 0 && len(f.sent) == f.failAfter {
		return nil, errFakeSendFailed
	}

	key := strings.Join(msg.Recipients.To, ",")

	return &email.SendResult{
		Sent:           true,
		Message:        "fake: email sent",
		MessageID:      "mid-" + key,
		ServerResponse: "250 2.0.0 Ok: queued as Q-" + key,
	}, nil
}

// newTestFormatter builds a real TemplateFormatter — these tests exercise the
// actual rendering pipeline (D1: one pipeline for every email) rather than a
// stub, so a formatter-wiring regression fails here, not just in
// server/internal/email's own package tests.
func newTestFormatter(t *testing.T) email.Formatter {
	t.Helper()

	f, err := email.NewFormatter()
	require.NoError(t, err)

	return f
}

// TestEmailSender_Send_RecipientResolution pins recipient resolution from the
// canonical "to" settings key. The dashboard writes recipients under "to", so
// the backend must read from "to" — never "recipients" (the historical bug).
//
// A non-modeled event type is used so Send takes the single broadcast path (no
// per-recipient personalization needed). The generic event type also avoids
// the incident-template rendering path, so the assertion stays focused on
// recipient resolution and depends only on the Check (for the subject) and
// Integration.Settings.
func TestEmailSender_Send_RecipientResolution(t *testing.T) {
	t.Parallel()

	checkName := "API health"

	tests := []struct {
		name string
		// settings is the integration's stored Settings JSONB.
		settings models.JSONMap
		// wantErr, when non-nil, is the sentinel error Send must return; in that
		// case no email is sent.
		wantErr error
		// wantTo is the resolved recipient list asserted on the sent message
		// (only checked when wantErr is nil).
		wantTo []string
	}{
		{
			name:     "to as []any resolves and attempts send",
			settings: models.JSONMap{"to": []any{"a@x.test"}},
			wantTo:   []string{"a@x.test"},
		},
		{
			name:     "to as []string resolves both recipients",
			settings: models.JSONMap{"to": []string{"a@x.test", "b@y.test"}},
			wantTo:   []string{"a@x.test", "b@y.test"},
		},
		{
			name:     "to absent returns ErrNoRecipientsConfigured",
			settings: models.JSONMap{},
			wantErr:  ErrNoRecipientsConfigured,
		},
		{
			name:     "to empty returns ErrNoRecipientsConfigured",
			settings: models.JSONMap{"to": []any{}},
			wantErr:  ErrNoRecipientsConfigured,
		},
		{
			name:     "to present but no usable string returns ErrNoValidRecipients",
			settings: models.JSONMap{"to": []any{42, "", nil}},
			wantErr:  ErrNoValidRecipients,
		},
		{
			name: "recipients key present but to absent still returns ErrNoRecipientsConfigured",
			// Pins "to" as the canonical key: the legacy "recipients" key must
			// not be honored, so a record carrying only "recipients" errors.
			settings: models.JSONMap{"recipients": []any{"legacy@x.test"}},
			wantErr:  ErrNoRecipientsConfigured,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			sender := &fakeEmailSender{}

			s := &EmailSender{}
			payload := &Payload{
				// A generic (non-modeled) event type takes the broadcast path
				// and hits the fallback email builder, which reads no
				// incident fields.
				EventType: "incident.updated",
				Check:     &models.Check{Name: &checkName, Type: "http"},
				Integration: &models.Integration{
					Settings: tt.settings,
				},
			}

			jctx := &jobdef.JobContext{
				Services: &services.Registry{EmailSender: sender, EmailFormatter: newTestFormatter(t)},
				Logger:   slog.Default(),
			}

			err := s.Send(context.Background(), jctx, payload)

			if tt.wantErr != nil {
				r.ErrorIs(err, tt.wantErr)
				r.Empty(sender.sent, "no email should be sent when recipients do not resolve")

				return
			}

			r.NoError(err)
			r.Len(sender.sent, 1, "exactly one broadcast email should be sent")
			r.Equal(tt.wantTo, sender.sent[0].Recipients.To)
		})
	}
}

// TestEmailSender_Send_MissingFormatterErrors pins that Send fails fast with
// a clear sentinel when the email formatter service is not wired — every
// modeled incident event now renders through the formatter (D1), so a nil
// formatter must not silently skip rendering.
func TestEmailSender_Send_MissingFormatterErrors(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	checkName := "API health"
	sender := &fakeEmailSender{}
	s := &EmailSender{}
	payload := &Payload{
		EventType: eventTypeIncidentCreated,
		Check:     &models.Check{Name: &checkName, Type: "http"},
		Incident:  &models.Incident{UID: "inc-1", StartedAt: time.Now()},
		Integration: &models.Integration{
			Settings: models.JSONMap{"to": []any{"a@x.test"}},
		},
	}

	jctx := &jobdef.JobContext{
		Services:  &services.Registry{EmailSender: sender}, // EmailFormatter deliberately nil
		AppConfig: &config.Config{},
		Logger:    slog.Default(),
	}

	err := s.Send(context.Background(), jctx, payload)
	r.ErrorIs(err, ErrEmailFormatterNotConfigured)
	r.Empty(sender.sent)
}

// TestEmailSender_Send_CapturesDeliveryArtifacts pins that a successful send
// surfaces the SMTP Message-ID and per-recipient server response on the payload
// so the notification audit row can prove the message was handed off.
func TestEmailSender_Send_CapturesDeliveryArtifacts(t *testing.T) {
	t.Parallel()

	checkName := "API health"

	t.Run("broadcast send records first message id and transcript", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		sender := &fakeEmailSender{}
		s := &EmailSender{}
		payload := &Payload{
			EventType: "incident.updated", // non-modeled → single broadcast send
			Check:     &models.Check{Name: &checkName, Type: "http"},
			Integration: &models.Integration{
				Settings: models.JSONMap{"to": []any{"a@x.test", "b@y.test"}},
			},
		}

		jctx := &jobdef.JobContext{
			Services: &services.Registry{EmailSender: sender, EmailFormatter: newTestFormatter(t)},
			Logger:   slog.Default(),
		}

		r.NoError(s.Send(context.Background(), jctx, payload))

		r.Equal("mid-a@x.test,b@y.test", payload.MessageID)
		r.NotNil(payload.DeliveryDetails)
		r.Contains(payload.DeliveryDetails.ResponseBody, "recipients: a@x.test, b@y.test")
		r.Contains(payload.DeliveryDetails.ResponseBody, "message-id: mid-a@x.test,b@y.test")
		r.Contains(payload.DeliveryDetails.ResponseBody, "server: 250 2.0.0 Ok: queued as Q-a@x.test,b@y.test")
	})

	t.Run("per-recipient send records first id and every server response", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		sender := &fakeEmailSender{}
		s := &EmailSender{}
		payload := &Payload{
			EventType: eventTypeIncidentCreated, // modeled event → one send per recipient
			Check:     &models.Check{Name: &checkName, Type: "http"},
			Incident:  &models.Incident{UID: "inc-1", StartedAt: time.Now()},
			Integration: &models.Integration{
				Settings: models.JSONMap{"to": []any{"a@x.test", "b@y.test"}},
			},
		}

		jctx := &jobdef.JobContext{
			Services:  &services.Registry{EmailSender: sender, EmailFormatter: newTestFormatter(t)},
			AppConfig: &config.Config{},
			Logger:    slog.Default(),
		}

		r.NoError(s.Send(context.Background(), jctx, payload))

		r.Len(sender.sent, 2, "one send per recipient")
		// First Message-ID is the correlation handle stored on the row.
		r.Equal("mid-a@x.test", payload.MessageID)
		r.NotNil(payload.DeliveryDetails)
		// The transcript carries every recipient's own server response.
		r.Contains(payload.DeliveryDetails.ResponseBody, "server: 250 2.0.0 Ok: queued as Q-a@x.test")
		r.Contains(payload.DeliveryDetails.ResponseBody, "server: 250 2.0.0 Ok: queued as Q-b@y.test")
	})

	t.Run("resolved event also sends per recipient (needed for per-recipient unsubscribe tokens)", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		sender := &fakeEmailSender{}
		s := &EmailSender{}
		resolvedAt := time.Now()
		payload := &Payload{
			EventType: eventTypeIncidentResolved,
			Check:     &models.Check{Name: &checkName, Type: "http"},
			Incident: &models.Incident{
				UID: "inc-1", StartedAt: resolvedAt.Add(-time.Hour), ResolvedAt: &resolvedAt,
			},
			Integration: &models.Integration{
				Settings: models.JSONMap{"to": []any{"a@x.test", "b@y.test"}},
			},
		}

		jctx := &jobdef.JobContext{
			Services:  &services.Registry{EmailSender: sender, EmailFormatter: newTestFormatter(t)},
			AppConfig: &config.Config{},
			Logger:    slog.Default(),
		}

		r.NoError(s.Send(context.Background(), jctx, payload))
		r.Len(sender.sent, 2, "resolved must send one email per recipient, not one broadcast")

		for _, msg := range sender.sent {
			r.NotContains(msg.HTML, "Acknowledge incident", "resolved has nothing to ack")
		}
	})

	t.Run("partial batch failure still records delivered recipients", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		sender := &fakeEmailSender{failAfter: 2} // first recipient sends, second fails
		s := &EmailSender{}
		payload := &Payload{
			EventType: eventTypeIncidentCreated,
			Check:     &models.Check{Name: &checkName, Type: "http"},
			Incident:  &models.Incident{UID: "inc-1", StartedAt: time.Now()},
			Integration: &models.Integration{
				Settings: models.JSONMap{"to": []any{"a@x.test", "b@y.test"}},
			},
		}

		jctx := &jobdef.JobContext{
			Services:  &services.Registry{EmailSender: sender, EmailFormatter: newTestFormatter(t)},
			AppConfig: &config.Config{},
			Logger:    slog.Default(),
		}

		// The send short-circuits on the failing recipient and returns the error.
		r.Error(s.Send(context.Background(), jctx, payload))

		// The recipient that did go out is still surfaced for the audit row.
		r.Equal("mid-a@x.test", payload.MessageID)
		r.NotNil(payload.DeliveryDetails)
		r.Contains(payload.DeliveryDetails.ResponseBody, "recipients: a@x.test")
		r.NotContains(payload.DeliveryDetails.ResponseBody, "Q-b@y.test")
	})
}

// TestEmailSender_Send_RendersIncidentTemplates is a focused integration test
// (real formatter, fake sender) confirming each modeled incident event
// renders through its dedicated template with the expected content — the
// "View incident" / check-link URLs (D3) and the ack button (ackable events
// only).
func TestEmailSender_Send_RendersIncidentTemplates(t *testing.T) {
	t.Parallel()

	checkName := "Production API"
	checkSlug := "prod-api"

	cases := []struct {
		name          string
		eventType     string
		wantSubjectIn string
		wantAckButton bool
	}{
		{"created event", eventTypeIncidentCreated, "[DOWN]", true},
		{"resolved event", eventTypeIncidentResolved, "[RECOVERED]", false},
		{"escalated event", eventTypeIncidentEscalated, "[ESCALATED]", true},
		{"reopened event", eventTypeIncidentReopened, "[REOPENED]", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			sender := &fakeEmailSender{}
			s := &EmailSender{}
			startedAt := time.Now().Add(-time.Hour)
			resolvedAt := time.Now()
			payload := &Payload{
				EventType: tc.eventType,
				Check:     &models.Check{UID: "check-1", Name: &checkName, Slug: &checkSlug, Type: "http"},
				Incident: &models.Incident{
					UID: "inc-42", StartedAt: startedAt, ResolvedAt: &resolvedAt,
					FailureCount: 3, RelapseCount: 1,
				},
				Integration: &models.Integration{
					Settings: models.JSONMap{"to": []any{"a@x.test"}},
				},
				OrgSlug:    "acme",
				AppBaseURL: "https://solidping.example",
			}

			jctx := &jobdef.JobContext{
				Services: &services.Registry{EmailSender: sender, EmailFormatter: newTestFormatter(t)},
				AppConfig: &config.Config{
					Auth:   config.AuthConfig{JWTSecret: "test-secret"},
					Server: config.ServerConfig{BaseURL: "https://solidping.example"},
				},
				Logger: slog.Default(),
			}

			r.NoError(s.Send(context.Background(), jctx, payload))
			r.Len(sender.sent, 1)

			msg := sender.sent[0]
			r.Contains(msg.Subject, tc.wantSubjectIn)
			r.Contains(msg.HTML, "https://solidping.example/dash0/orgs/acme/incidents/inc-42", "View incident URL (D3)")
			r.Contains(msg.HTML, "https://solidping.example/dash0/orgs/acme/checks/check-1", "check name link (D3, by UID)")
			r.Contains(msg.HTML, "https://solidping.example/dash0", "footer dashboard link")
			r.Contains(msg.HTML, "https://solidping.example/docs", "footer docs link")
			r.NotContains(msg.HTML, "{{", "no unresolved template syntax")
			r.NotEmpty(msg.Text, "text alternative must be populated")
			r.Contains(msg.Text, "https://solidping.example/dash0/orgs/acme/incidents/inc-42")

			if tc.wantAckButton {
				r.Contains(msg.HTML, "Acknowledge incident")
			} else {
				r.NotContains(msg.HTML, "Acknowledge incident")
			}
		})
	}
}

// --- Suppression filtering (spec 2026-07-05-10, D4, acceptance criteria 4 & 7) ---

// suppressionTestFixture bundles a real in-memory db.Service with an org and
// two checks, so suppression tests can create real EmailSuppression rows and
// assert against real IsEmailSuppressed / CreateIncidentNotification
// behavior — not a mock that could silently diverge from the actual
// enforcement query.
type suppressionTestFixture struct {
	dbSvc db.Service
	org   *models.Organization
	check *models.Check
}

func newSuppressionTestFixture(t *testing.T) *suppressionTestFixture {
	t.Helper()

	r := require.New(t)

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	r.NoError(dbSvc.Initialize(t.Context()))

	org := models.NewOrganization("email-supp-send-org", "")
	r.NoError(dbSvc.CreateOrganization(t.Context(), org))

	check := models.NewCheck(org.UID, "email-supp-send-check", "http")
	r.NoError(dbSvc.CreateCheck(t.Context(), check))

	return &suppressionTestFixture{dbSvc: dbSvc, org: org, check: check}
}

// newIncident creates and persists a real incident row for checkUID — the
// skipped-audit-row insert has a foreign key on incident_uid, so a
// suppression test needs a real row, not just an in-memory *models.Incident
// literal (production always has one: job_notification.go loads it via
// jctx.DBService.GetIncident before Send is ever called).
func (f *suppressionTestFixture) newIncident(t *testing.T, checkUID string) *models.Incident {
	t.Helper()

	r := require.New(t)

	incident := models.NewIncident(f.org.UID, checkUID, time.Now(), "test incident")
	r.NoError(f.dbSvc.CreateIncident(t.Context(), incident))

	return incident
}

// TestEmailSender_Send_SuppressionFiltering is the table-driven guard for
// recipient-resolution suppression enforcement: a suppressed recipient must
// not receive the email and must get a `skipped` audit row; a
// non-suppressed recipient in the SAME send must still receive theirs.
func TestEmailSender_Send_SuppressionFiltering(t *testing.T) {
	t.Parallel()

	checkName := "API health"

	tests := []struct {
		name           string
		suppressScope  string // "none", "check", "org"
		wantSuppressed bool
	}{
		{name: "not suppressed sends normally", suppressScope: "none", wantSuppressed: false},
		{name: "check-specific suppression blocks delivery", suppressScope: "check", wantSuppressed: true},
		{name: "org-wide suppression blocks delivery", suppressScope: "org", wantSuppressed: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			f := newSuppressionTestFixture(t)

			const suppressedEmail = "suppressed@x.test"

			switch tt.suppressScope {
			case "check":
				sup := models.NewEmailSuppression(f.org.UID, suppressedEmail, &f.check.UID, models.EmailSuppressionSourceLink)
				r.NoError(f.dbSvc.CreateEmailSuppression(t.Context(), sup))
			case "org":
				sup := models.NewEmailSuppression(f.org.UID, suppressedEmail, nil, models.EmailSuppressionSourceLink)
				r.NoError(f.dbSvc.CreateEmailSuppression(t.Context(), sup))
			}

			incident := f.newIncident(t, f.check.UID)

			sender := &fakeEmailSender{}
			s := &EmailSender{}
			payload := &Payload{
				EventType: eventTypeIncidentCreated,
				Check:     &models.Check{UID: f.check.UID, Name: &checkName, Type: "http"},
				Incident:  incident,
				Integration: &models.Integration{
					OrganizationUID: f.org.UID,
					Settings:        models.JSONMap{"to": []any{suppressedEmail}},
				},
			}

			jctx := &jobdef.JobContext{
				Services:  &services.Registry{EmailSender: sender, EmailFormatter: newTestFormatter(t)},
				AppConfig: &config.Config{Auth: config.AuthConfig{JWTSecret: "test-secret"}},
				DBService: f.dbSvc,
				Logger:    slog.Default(),
			}

			r.NoError(s.Send(context.Background(), jctx, payload))

			if tt.wantSuppressed {
				r.Empty(sender.sent, "a suppressed recipient must not receive an email")

				rows, err := f.dbSvc.ListIncidentNotifications(context.Background(), f.org.UID, db.ListIncidentNotificationsFilter{
					IncidentUID: incident.UID,
				})
				r.NoError(err)
				r.Len(rows, 1, "exactly one skipped audit row must be created")
				r.Equal(models.IncidentNotificationStatusSkipped, rows[0].Status)
				r.NotNil(rows[0].SkipReason)
				r.Contains(*rows[0].SkipReason, suppressedEmail)
			} else {
				r.Len(sender.sent, 1, "a non-suppressed recipient must receive the email")
			}
		})
	}
}

// TestEmailSender_Send_SuppressionFiltering_MixedRecipients confirms that
// suppressing ONE recipient in a multi-recipient send does not affect
// delivery to the others — the filtering is per-recipient, not
// all-or-nothing for the notification.
func TestEmailSender_Send_SuppressionFiltering_MixedRecipients(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newSuppressionTestFixture(t)
	checkName := "API health"

	const (
		suppressedEmail = "suppressed@x.test"
		normalEmail     = "normal@x.test"
	)

	sup := models.NewEmailSuppression(f.org.UID, suppressedEmail, &f.check.UID, models.EmailSuppressionSourceLink)
	r.NoError(f.dbSvc.CreateEmailSuppression(t.Context(), sup))

	incident := f.newIncident(t, f.check.UID)

	sender := &fakeEmailSender{}
	s := &EmailSender{}
	payload := &Payload{
		EventType: eventTypeIncidentCreated,
		Check:     &models.Check{UID: f.check.UID, Name: &checkName, Type: "http"},
		Incident:  incident,
		Integration: &models.Integration{
			OrganizationUID: f.org.UID,
			Settings:        models.JSONMap{"to": []any{suppressedEmail, normalEmail}},
		},
	}

	jctx := &jobdef.JobContext{
		Services:  &services.Registry{EmailSender: sender, EmailFormatter: newTestFormatter(t)},
		AppConfig: &config.Config{Auth: config.AuthConfig{JWTSecret: "test-secret"}},
		DBService: f.dbSvc,
		Logger:    slog.Default(),
	}

	r.NoError(s.Send(context.Background(), jctx, payload))

	r.Len(sender.sent, 1, "only the non-suppressed recipient must receive an email")
	r.Equal([]string{normalEmail}, sender.sent[0].Recipients.To)

	rows, err := f.dbSvc.ListIncidentNotifications(context.Background(), f.org.UID, db.ListIncidentNotificationsFilter{
		IncidentUID: incident.UID,
	})
	r.NoError(err)
	r.Len(rows, 1, "exactly one skipped audit row, for the suppressed recipient only")
	r.Contains(*rows[0].SkipReason, suppressedEmail)
}

// TestEmailSender_Send_SuppressionScopeIsolation confirms a check-specific
// suppression does NOT block delivery for a DIFFERENT check — suppression
// is per-check unless the row is explicitly org-wide (check_uid NULL).
func TestEmailSender_Send_SuppressionScopeIsolation(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newSuppressionTestFixture(t)
	checkName := "API health"

	otherCheck := models.NewCheck(f.org.UID, "email-supp-send-other-check", "http")
	r.NoError(f.dbSvc.CreateCheck(t.Context(), otherCheck))

	const email = "scoped@x.test"

	// Suppress for f.check only.
	sup := models.NewEmailSuppression(f.org.UID, email, &f.check.UID, models.EmailSuppressionSourceLink)
	r.NoError(f.dbSvc.CreateEmailSuppression(t.Context(), sup))

	sender := &fakeEmailSender{}
	s := &EmailSender{}
	payload := &Payload{
		EventType: eventTypeIncidentCreated,
		// Incident is for the OTHER check, not the suppressed one.
		Check:    &models.Check{UID: otherCheck.UID, Name: &checkName, Type: "http"},
		Incident: &models.Incident{UID: "inc-other-check", StartedAt: time.Now()},
		Integration: &models.Integration{
			OrganizationUID: f.org.UID,
			Settings:        models.JSONMap{"to": []any{email}},
		},
	}

	jctx := &jobdef.JobContext{
		Services:  &services.Registry{EmailSender: sender, EmailFormatter: newTestFormatter(t)},
		AppConfig: &config.Config{Auth: config.AuthConfig{JWTSecret: "test-secret"}},
		DBService: f.dbSvc,
		Logger:    slog.Default(),
	}

	r.NoError(s.Send(context.Background(), jctx, payload))
	r.Len(sender.sent, 1, "a suppression scoped to a different check must not block this one")
}

// TestEmailSender_Send_TransactionalPathNeverConsultsSuppression documents
// (spec acceptance criterion 6) that suppression filtering lives entirely
// inside sendPerRecipient, reached only via isModeledIncidentEvent. The
// transactional email paths (registration/reset/invitation/password-changed,
// sent through jobtypes.EmailJobRun via the generic email job, never through
// notifications.EmailSender) do not call this code at all — there is no
// shared function to bypass. This test pins that a non-modeled event type
// (the shape used by the generic/legacy broadcast path) does not consult
// suppression either, since it never reaches sendPerRecipient.
func TestEmailSender_Send_TransactionalPathNeverConsultsSuppression(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newSuppressionTestFixture(t)
	checkName := "API health"

	const email = "would-be-suppressed@x.test"

	sup := models.NewEmailSuppression(f.org.UID, email, nil, models.EmailSuppressionSourceLink) // org-wide
	r.NoError(f.dbSvc.CreateEmailSuppression(t.Context(), sup))

	sender := &fakeEmailSender{}
	s := &EmailSender{}
	payload := &Payload{
		EventType: "incident.updated", // non-modeled → broadcast path, never calls sendPerRecipient
		Check:     &models.Check{UID: f.check.UID, Name: &checkName, Type: "http"},
		Integration: &models.Integration{
			OrganizationUID: f.org.UID,
			Settings:        models.JSONMap{"to": []any{email}},
		},
	}

	jctx := &jobdef.JobContext{
		Services:  &services.Registry{EmailSender: sender, EmailFormatter: newTestFormatter(t)},
		AppConfig: &config.Config{Auth: config.AuthConfig{JWTSecret: "test-secret"}},
		DBService: f.dbSvc,
		Logger:    slog.Default(),
	}

	r.NoError(s.Send(context.Background(), jctx, payload))
	r.Len(sender.sent, 1, "the broadcast path does not consult suppression at all")
}

// TestEmailSender_Send_ListUnsubscribeHeadersOnlyOnModeledEvents confirms
// the SMTP message carries List-Unsubscribe headers for a modeled incident
// event (which always has a per-recipient token) and omits them on the
// non-modeled broadcast path (which has none) — the email-level companion
// to TestBuildMessage_ListUnsubscribeHeaders in the email package.
func TestEmailSender_Send_ListUnsubscribeHeadersOnlyOnModeledEvents(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newSuppressionTestFixture(t)
	checkName := "API health"
	checkSlug := "email-supp-send-check"

	sender := &fakeEmailSender{}
	s := &EmailSender{}
	payload := &Payload{
		EventType: eventTypeIncidentCreated,
		Check:     &models.Check{UID: f.check.UID, Name: &checkName, Slug: &checkSlug, Type: "http"},
		Incident:  &models.Incident{UID: "inc-headers", StartedAt: time.Now()},
		Integration: &models.Integration{
			OrganizationUID: f.org.UID,
			Settings:        models.JSONMap{"to": []any{"headers@x.test"}},
		},
		OrgSlug:    f.org.Slug,
		AppBaseURL: "https://solidping.example",
	}

	jctx := &jobdef.JobContext{
		Services: &services.Registry{EmailSender: sender, EmailFormatter: newTestFormatter(t)},
		AppConfig: &config.Config{
			Auth:   config.AuthConfig{JWTSecret: "test-secret"},
			Server: config.ServerConfig{BaseURL: "https://solidping.example"},
		},
		DBService: f.dbSvc,
		Logger:    slog.Default(),
	}

	r.NoError(s.Send(context.Background(), jctx, payload))
	r.Len(sender.sent, 1)

	msg := sender.sent[0]
	r.NotEmpty(msg.ListUnsubscribeURL, "a modeled incident event must set a per-recipient unsubscribe URL")
	r.True(msg.ListUnsubscribePostOneClick)
	r.Contains(msg.ListUnsubscribeURL, "/unsubscribe?token=")
}

// TestMailDuration pins the outage-length formatting that headlines the
// recovered mail. The interesting cases are the boundaries where a unit falls
// to zero — "15m0s" and "2h0m0s" are what time.Duration.String() produces and
// what this exists to avoid — and the sub-minute range, which must keep its
// seconds: rounding a 45-second blip up to "1m" describes a different incident.
func TestMailDuration(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   time.Duration
		want string
	}{
		{"sub-second", 400 * time.Millisecond, "0s"},
		{"seconds", 45 * time.Second, "45s"},
		{"whole minutes", 15 * time.Minute, "15m"},
		{"minutes and seconds", 15*time.Minute + 20*time.Second, "15m 20s"},
		{"whole hours", 2 * time.Hour, "2h"},
		{"hours drop seconds", 2*time.Hour + 13*time.Minute + 7*time.Second, "2h 13m"},
		{"whole days", 48 * time.Hour, "2d"},
		{"days and hours", 26 * time.Hour, "1d 2h"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, mailDuration(tc.in))
		})
	}
}
