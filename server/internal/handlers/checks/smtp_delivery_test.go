package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/jmap"
)

func setupSMTPDeliveryChecksService(t *testing.T) (*checks.Service, db.Service, *models.Organization) {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))

	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("smtp-delivery-test", "SMTP Delivery Test Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	require.NoError(t, err)

	return checks.NewService(dbSvc, nil, creds, nil), dbSvc, org
}

func configureEmailInboxForTest(t *testing.T, dbSvc db.Service) {
	t.Helper()

	err := dbSvc.SetSystemParameter(t.Context(), jmap.SystemParameterKey, map[string]any{
		"enabled":       true,
		"sessionUrl":    "https://jmap.example.com/session",
		"addressDomain": "inbox.example.com",
	}, false)
	require.NoError(t, err)
}

// createEmailCheck creates a single fixed-slug email check — every test in
// this file needs at most one paired delivery target per org, so the slug is
// a constant rather than a parameter.
func createEmailCheck(t *testing.T, svc *checks.Service, org *models.Organization) checks.CheckResponse {
	t.Helper()

	created, err := svc.CreateCheck(t.Context(), org.Slug, checks.CreateCheckRequest{
		Name: "delivery", Slug: "delivery", Type: "email",
	})
	require.NoError(t, err)

	return created
}

// smtpSendModeConfig builds a send-mode SMTP check config. mail_from is fixed
// (irrelevant to what these tests exercise); deliveryCheckUID varies per test.
func smtpSendModeConfig(deliveryCheckUID string) map[string]any {
	return map[string]any{
		"host":               "mail.example.com",
		"send_email":         true,
		"mail_from":          "prober@example.com",
		"delivery_check_uid": deliveryCheckUID,
	}
}

// TestCreateSendModeSMTPCheck pins the happy path at write time: a valid
// same-org email-check reference with a configured email inbox is accepted.
func TestCreateSendModeSMTPCheck(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, org := setupSMTPDeliveryChecksService(t)
	configureEmailInboxForTest(t, dbSvc)

	emailCheck := createEmailCheck(t, svc, org)

	created, err := svc.CreateCheck(t.Context(), org.Slug, checks.CreateCheckRequest{
		Name: "probe", Slug: "probe", Type: "smtp",
		Config: smtpSendModeConfig(emailCheck.UID),
	})
	r.NoError(err)
	r.Equal(emailCheck.UID, created.Config["delivery_check_uid"])
}

// TestCreateSendModeSMTPCheckRejections covers every write-time guard: cross-
// org reference, wrong-type reference, missing reference, missing mail_from,
// and no email inbox configured.
func TestCreateSendModeSMTPCheckRejections(t *testing.T) {
	t.Parallel()

	svc, dbSvc, org := setupSMTPDeliveryChecksService(t)
	configureEmailInboxForTest(t, dbSvc)

	emailCheck := createEmailCheck(t, svc, org)

	notEmail, err := svc.CreateCheck(t.Context(), org.Slug, checks.CreateCheckRequest{
		Name: "plain", Slug: "plain", Type: "http",
		Config: map[string]any{"url": "https://example.com"},
	})
	require.NoError(t, err)

	otherOrg := models.NewOrganization("smtp-delivery-other", "")
	require.NoError(t, dbSvc.CreateOrganization(t.Context(), otherOrg))
	foreignEmailCheck, err := svc.CreateCheck(t.Context(), otherOrg.Slug, checks.CreateCheckRequest{
		Name: "foreign-delivery", Slug: "foreign-delivery", Type: "email",
	})
	require.NoError(t, err)

	tests := []struct {
		name    string
		config  map[string]any
		wantErr string
	}{
		{
			name:    "cross-org delivery_check_uid rejected",
			config:  smtpSendModeConfig(foreignEmailCheck.UID),
			wantErr: "does not exist in this organization",
		},
		{
			name:    "wrong-type delivery_check_uid rejected",
			config:  smtpSendModeConfig(notEmail.UID),
			wantErr: "only email checks can be a delivery target",
		},
		{
			name:    "missing delivery_check_uid rejected",
			config:  map[string]any{"host": "mail.example.com", "send_email": true, "mail_from": "prober@example.com"},
			wantErr: "is required when send_email is set",
		},
		{
			name:    "missing mail_from rejected",
			config:  map[string]any{"host": "mail.example.com", "send_email": true, "delivery_check_uid": emailCheck.UID},
			wantErr: "is required when send_email is set",
		},
		{
			name:    "nonexistent delivery_check_uid rejected",
			config:  smtpSendModeConfig("00000000-0000-0000-0000-000000000000"),
			wantErr: "does not exist in this organization",
		},
		{
			name: "mail_from carrying a CRLF-smuggled SMTP command is rejected",
			config: map[string]any{
				"host":               "mail.example.com",
				"send_email":         true,
				"mail_from":          "prober@example.com\r\nRCPT TO:<victim@evil.com>",
				"delivery_check_uid": emailCheck.UID,
			},
			wantErr: "must be a single valid email address",
		},
		{
			name: "mail_from carrying a CRLF-injected header is rejected",
			config: map[string]any{
				"host":               "mail.example.com",
				"send_email":         true,
				"mail_from":          "prober@example.com>\r\nBcc: attacker@evil.com\r\nSubject: not a probe",
				"delivery_check_uid": emailCheck.UID,
			},
			wantErr: "must be a single valid email address",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			_, err := svc.CreateCheck(t.Context(), org.Slug, checks.CreateCheckRequest{
				Name: "probe-" + tt.name, Slug: "probe-" + slugify(tt.name), Type: "smtp",
				Config: tt.config,
			})
			r.Error(err)
			r.Contains(err.Error(), tt.wantErr)
		})
	}
}

// TestCreateSendModeSMTPCheckNoEmailInbox pins the instance-wide prerequisite:
// with no email.inbox system parameter configured at all, send mode is
// refused regardless of how valid the reference is.
func TestCreateSendModeSMTPCheckNoEmailInbox(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, org := setupSMTPDeliveryChecksService(t)
	// Deliberately NOT calling configureEmailInboxForTest.

	emailCheck := createEmailCheck(t, svc, org)

	_, err := svc.CreateCheck(t.Context(), org.Slug, checks.CreateCheckRequest{
		Name: "probe", Slug: "probe", Type: "smtp",
		Config: smtpSendModeConfig(emailCheck.UID),
	})
	r.Error(err)
	r.Contains(err.Error(), "no email inbox configured")
}

// TestCreateSendModeSMTPCheckMinInterval pins the 60s floor (spec
// 2026-08-19-04): send mode must not be allowed to flood the paired inbox.
func TestCreateSendModeSMTPCheckMinInterval(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, org := setupSMTPDeliveryChecksService(t)
	configureEmailInboxForTest(t, dbSvc)

	emailCheck := createEmailCheck(t, svc, org)
	shortPeriod := "10s"

	_, err := svc.CreateCheck(t.Context(), org.Slug, checks.CreateCheckRequest{
		Name: "probe", Slug: "probe", Type: "smtp",
		Period: &shortPeriod,
		Config: smtpSendModeConfig(emailCheck.UID),
	})
	r.Error(err)
	r.Contains(err.Error(), "at least 1m0s")

	// The same period is completely fine for a plain (non-send-mode) SMTP
	// check — the floor is specific to send mode, never a general SMTP rule.
	_, err = svc.CreateCheck(t.Context(), org.Slug, checks.CreateCheckRequest{
		Name: "plain-fast", Slug: "plain-fast", Type: "smtp",
		Period: &shortPeriod,
		Config: map[string]any{"host": "mail.example.com"},
	})
	r.NoError(err)
}

// TestUpdateSendModeSMTPCheckValidatesOnPatch pins that PATCH re-validates
// the same rules as create (UpdateCheck never calls checker.Validate, so
// validateSMTPDeliveryConfig is the only gate on this path).
func TestUpdateSendModeSMTPCheckValidatesOnPatch(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, org := setupSMTPDeliveryChecksService(t)
	configureEmailInboxForTest(t, dbSvc)

	notEmail, err := svc.CreateCheck(t.Context(), org.Slug, checks.CreateCheckRequest{
		Name: "plain", Slug: "plain", Type: "http",
		Config: map[string]any{"url": "https://example.com"},
	})
	require.NoError(t, err)

	plain, err := svc.CreateCheck(t.Context(), org.Slug, checks.CreateCheckRequest{
		Name: "probe", Slug: "probe", Type: "smtp",
		Config: map[string]any{"host": "mail.example.com"},
	})
	require.NoError(t, err)

	patchConfig := smtpSendModeConfig(notEmail.UID)
	_, err = svc.UpdateCheck(t.Context(), org.Slug, plain.UID, &checks.UpdateCheckRequest{
		Config: &patchConfig,
	})
	r.Error(err, "a PATCH turning on send_email with a wrong-type reference must be rejected")
	r.Contains(err.Error(), "only email checks can be a delivery target")
}

// TestUpdateSendModeSMTPCheckRejectsMailFromInjectionOnPatch is the PATCH-path
// half of the mail_from injection guard: UpdateCheck never calls
// checker.Validate(), so validateSMTPDeliveryConfig re-validating mail_from on
// every PATCH is the only thing stopping an existing plain SMTP check from
// being turned into a send-mode one with an injection payload.
func TestUpdateSendModeSMTPCheckRejectsMailFromInjectionOnPatch(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, org := setupSMTPDeliveryChecksService(t)
	configureEmailInboxForTest(t, dbSvc)

	emailCheck := createEmailCheck(t, svc, org)

	plain, err := svc.CreateCheck(t.Context(), org.Slug, checks.CreateCheckRequest{
		Name: "probe", Slug: "probe", Type: "smtp",
		Config: map[string]any{"host": "mail.example.com"},
	})
	require.NoError(t, err)

	patchConfig := map[string]any{
		"host":               "mail.example.com",
		"send_email":         true,
		"mail_from":          "prober@example.com\r\nRCPT TO:<victim@evil.com>",
		"delivery_check_uid": emailCheck.UID,
	}
	_, err = svc.UpdateCheck(t.Context(), org.Slug, plain.UID, &checks.UpdateCheckRequest{
		Config: &patchConfig,
	})
	r.Error(err, "a PATCH carrying a CRLF-smuggled mail_from must be rejected")
	r.Contains(err.Error(), "must be a single valid email address")
}

// TestUpdateSendModeSMTPCheckRejectsShortIntervalOnPatch pins the PATCH-path
// half of the 60s minimum-interval guard: a single PATCH that flips
// send_email on AND shortens period below 60s in the same request must be
// rejected, regardless of the order the two fields are processed in.
func TestUpdateSendModeSMTPCheckRejectsShortIntervalOnPatch(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, org := setupSMTPDeliveryChecksService(t)
	configureEmailInboxForTest(t, dbSvc)

	emailCheck := createEmailCheck(t, svc, org)

	// Created with a period well above the floor, unmodified so far.
	plainPeriod := "5m"
	plain, err := svc.CreateCheck(t.Context(), org.Slug, checks.CreateCheckRequest{
		Name: "probe", Slug: "probe", Type: "smtp",
		Period: &plainPeriod,
		Config: map[string]any{"host": "mail.example.com"},
	})
	require.NoError(t, err)

	patchConfig := smtpSendModeConfig(emailCheck.UID)
	shortPeriod := "10s"
	_, err = svc.UpdateCheck(t.Context(), org.Slug, plain.UID, &checks.UpdateCheckRequest{
		Config: &patchConfig,
		Period: &shortPeriod,
	})
	r.Error(err, "a single PATCH turning on send_email AND shortening period below 60s must be rejected")
	r.Contains(err.Error(), "at least 1m0s")

	// Confirm the rejection actually stuck — the check must still be a plain,
	// non-send-mode SMTP check on its original period.
	reloaded, err := svc.GetCheck(t.Context(), org.Slug, plain.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.NotNil(reloaded.Period)
	r.Equal("00:05:00", *reloaded.Period)
	r.NotContains(reloaded.Config, "send_email")
}

// slugify makes a test-name-derived string into something CreateCheck's slug
// validator accepts (lowercase letters/digits/hyphens only).
func slugify(s string) string {
	out := make([]rune, 0, len(s))

	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			out = append(out, c)
		case c >= 'A' && c <= 'Z':
			out = append(out, c+('a'-'A'))
		case c == ' ' || c == '-' || c == '_':
			out = append(out, '-')
		}
	}

	return string(out)
}
