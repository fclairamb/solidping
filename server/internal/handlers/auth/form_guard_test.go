package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

const formGuardTestIP = "198.51.100.10"

// formGuardTestConfig is a config with registration open and the given guard.
func formGuardTestConfig(guard config.FormGuardConfig) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{BaseURL: "https://status.acme.com"},
		Auth: config.AuthConfig{
			JWTSecret:                "test-jwt-secret",
			AccessTokenExpiry:        time.Hour,
			RefreshTokenExpiry:       7 * 24 * time.Hour,
			RegistrationEmailPattern: ".*",
			FormGuard:                guard,
		},
	}
}

// enabledGuard is the shipped guard without the budgets, so the ticket cases
// are not affected by them.
func enabledGuard() config.FormGuardConfig {
	return config.FormGuardConfig{
		Enabled:     true,
		MinFillTime: config.DefaultFormGuardMinFillTime,
		TicketTTL:   time.Hour,
	}
}

// emailsTo counts the email jobs queued for recipient with template.
//
//nolint:revive // ctx-second matches the other helpers of this package
func emailsTo(t *testing.T, ctx context.Context, dbSvc db.Service, recipient, template string) int {
	t.Helper()

	jobs, err := dbSvc.ListJobs(ctx, nil, 0)
	require.NoError(t, err)

	count := 0

	for _, job := range jobs {
		if job.Type != string(jobdef.JobTypeEmail) {
			continue
		}

		if tpl, _ := job.Config["template"].(string); tpl != template {
			continue
		}

		to, ok := job.Config["to"].([]any)
		if !ok || len(to) == 0 {
			continue
		}

		if addr, _ := to[0].(string); addr == recipient {
			count++
		}
	}

	return count
}

// ticketAt mints a ticket for form as if it had been issued at issuedAt.
func ticketAt(t *testing.T, svc *Service, form string, issuedAt time.Time) string {
	t.Helper()

	resp, err := svc.issueFormTicketAt(form, issuedAt)
	require.NoError(t, err)

	return resp.Ticket
}

// formCase is one submission of the ticket table: the fields it sends, and
// whether the guard must let it through.
type formCase struct {
	name   string
	reason string
	fields func(t *testing.T, svc *Service, form string) FormGuardFields
	passes bool
}

func oldTicket(t *testing.T, svc *Service, form string) string {
	t.Helper()

	return ticketAt(t, svc, form, time.Now().Add(-10*time.Second))
}

func otherForm(form string) string {
	if form == FormRegister {
		return FormPasswordReset
	}

	return FormRegister
}

//nolint:gochecknoglobals // read-only test table shared by both forms
var formCases = []formCase{
	{
		name:   "valid ticket old enough",
		passes: true,
		fields: func(t *testing.T, svc *Service, form string) FormGuardFields {
			t.Helper()

			return FormGuardFields{FormTicket: oldTicket(t, svc, form)}
		},
	},
	{
		name:   "too fast",
		reason: formDropTooFast,
		fields: func(t *testing.T, svc *Service, form string) FormGuardFields {
			t.Helper()

			return FormGuardFields{FormTicket: ticketAt(t, svc, form, time.Now().Add(-time.Second))}
		},
	},
	{
		name:   "honeypot filled",
		reason: formDropHoneypot,
		fields: func(t *testing.T, svc *Service, form string) FormGuardFields {
			t.Helper()

			return FormGuardFields{FormTicket: oldTicket(t, svc, form), Website: "https://acme.com"}
		},
	},
	{
		name:   "missing ticket",
		reason: formDropMissingTicket,
		fields: func(*testing.T, *Service, string) FormGuardFields { return FormGuardFields{} },
	},
	{
		name:   "garbage ticket",
		reason: formDropInvalidTicket,
		fields: func(*testing.T, *Service, string) FormGuardFields {
			return FormGuardFields{FormTicket: "not-a-ticket"}
		},
	},
	{
		name:   "ticket forged with another secret",
		reason: formDropInvalidTicket,
		fields: func(t *testing.T, _ *Service, form string) FormGuardFields {
			t.Helper()

			forgerCfg := formGuardTestConfig(enabledGuard())
			forgerCfg.Auth.JWTSecret = "a-secret-the-server-does-not-have"
			forger := NewService(nil, forgerCfg.Auth, forgerCfg, nil, nil)

			return FormGuardFields{FormTicket: oldTicket(t, forger, form)}
		},
	},
	{
		name:   "expired ticket",
		reason: formDropExpiredTicket,
		fields: func(t *testing.T, svc *Service, form string) FormGuardFields {
			t.Helper()

			return FormGuardFields{FormTicket: ticketAt(t, svc, form, time.Now().Add(-2*time.Hour))}
		},
	},
	{
		name:   "ticket of the other form",
		reason: formDropWrongForm,
		fields: func(t *testing.T, svc *Service, form string) FormGuardFields {
			t.Helper()

			return FormGuardFields{FormTicket: oldTicket(t, svc, otherForm(form))}
		},
	},
}

// TestFormGuardRegister drives Register with each ticket case. A dropped
// submission must get the very success response and queue no email; the
// positive control then submits the same address with a valid ticket and
// proves the email does arrive, so "no email" is not an artifact of the
// counting helper.
func TestFormGuardRegister(t *testing.T) {
	t.Parallel()

	for i, tc := range formCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			svc, dbSvc, ctx := setupAuthTestServiceWithJobs(t, formGuardTestConfig(enabledGuard()))
			email := fmt.Sprintf("guard-register-%d@acme.com", i)

			var before float64
			if tc.reason != "" {
				before = testutil.ToFloat64(prommetrics.AuthFormDropped.WithLabelValues(FormRegister, tc.reason))
			}

			resp, err := svc.Register(ctx, RegisterRequest{
				Email: email, Password: "supersecret123", ClientIP: formGuardTestIP,
				FormGuardFields: tc.fields(t, svc, FormRegister),
			})
			r.NoError(err)
			r.Equal(registerSuccessResponse, resp, "a dropped submission answers exactly like a success")

			if tc.passes {
				r.Equal(1, emailsTo(t, ctx, dbSvc, email, "registration.html"))

				return
			}

			r.Zero(emailsTo(t, ctx, dbSvc, email, "registration.html"), "a dropped submission sends nothing")
			entry, err := dbSvc.GetStateEntry(ctx, nil, registrationKeyPrefix+email)
			r.NoError(err)
			r.Nil(entry, "a dropped submission stores no pending registration")
			r.GreaterOrEqual(
				testutil.ToFloat64(prommetrics.AuthFormDropped.WithLabelValues(FormRegister, tc.reason))-before, 1.0,
				"the drop is counted under its reason")

			// Positive control: the same address with a valid ticket is sent.
			_, err = svc.Register(ctx, RegisterRequest{
				Email: email, Password: "supersecret123", ClientIP: formGuardTestIP,
				FormGuardFields: FormGuardFields{FormTicket: oldTicket(t, svc, FormRegister)},
			})
			r.NoError(err)
			r.Equal(1, emailsTo(t, ctx, dbSvc, email, "registration.html"))
		})
	}
}

// TestFormGuardPasswordReset is TestFormGuardRegister for the reset form.
func TestFormGuardPasswordReset(t *testing.T) {
	t.Parallel()

	for i, tc := range formCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			svc, dbSvc, ctx := setupAuthTestServiceWithJobs(t, formGuardTestConfig(enabledGuard()))
			email := fmt.Sprintf("guard-reset-%d@acme.com", i)
			createPasswordUser(t, ctx, dbSvc, email)

			resp, err := svc.RequestPasswordReset(ctx, RequestPasswordResetRequest{
				Email: email, FormGuardFields: tc.fields(t, svc, FormPasswordReset),
			}, formGuardTestIP)
			r.NoError(err)
			r.Equal("If an account exists with that email, a reset link has been sent.", resp.Message)

			if tc.passes {
				r.Equal(1, emailsTo(t, ctx, dbSvc, email, "password-reset.html"))

				return
			}

			r.Zero(emailsTo(t, ctx, dbSvc, email, "password-reset.html"), "a dropped submission sends nothing")

			// Positive control.
			_, err = svc.RequestPasswordReset(ctx, RequestPasswordResetRequest{
				Email: email, FormGuardFields: FormGuardFields{FormTicket: oldTicket(t, svc, FormPasswordReset)},
			}, formGuardTestIP)
			r.NoError(err)
			r.Equal(1, emailsTo(t, ctx, dbSvc, email, "password-reset.html"))
		})
	}
}

//nolint:revive // ctx-second matches the other helpers of this package
func createPasswordUser(t *testing.T, ctx context.Context, dbSvc db.Service, email string) {
	t.Helper()

	hash, err := passwords.Hash("originalpass1")
	require.NoError(t, err)

	user := models.NewUser(email)
	user.PasswordHash = &hash
	require.NoError(t, dbSvc.CreateUser(ctx, user))
}

// TestFormGuardTicketIsSingleUse: a bot that waited out one ticket cannot
// reuse it. The first use is the positive control.
func TestFormGuardTicketIsSingleUse(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestServiceWithJobs(t, formGuardTestConfig(enabledGuard()))
	ticket := oldTicket(t, svc, FormRegister)

	for _, email := range []string{"first-use@acme.com", "replay@acme.com"} {
		_, err := svc.Register(ctx, RegisterRequest{
			Email: email, Password: "supersecret123", FormGuardFields: FormGuardFields{FormTicket: ticket},
		})
		r.NoError(err)
	}

	r.Equal(1, emailsTo(t, ctx, dbSvc, "first-use@acme.com", "registration.html"))
	r.Zero(emailsTo(t, ctx, dbSvc, "replay@acme.com", "registration.html"))
}

// TestFormGuardDisabledAcceptsNoTicket: with the guard off (the zero value),
// a ticket-less submission is sent, as it was before the guard existed.
func TestFormGuardDisabledAcceptsNoTicket(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestServiceWithJobs(t, formGuardTestConfig(config.FormGuardConfig{}))

	_, err := svc.Register(ctx, RegisterRequest{Email: "no-guard@acme.com", Password: "supersecret123"})
	r.NoError(err)
	r.Equal(1, emailsTo(t, ctx, dbSvc, "no-guard@acme.com", "registration.html"))
}

// postJSON drives handler with a JSON body from remoteAddr, optionally with an
// X-Forwarded-For header.
func postJSON(
	t *testing.T, handle func(http.ResponseWriter, *http.Request) error, path, body, remoteAddr, xff string,
) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	req.RemoteAddr = remoteAddr

	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}

	rec := httptest.NewRecorder()
	require.NoError(t, handle(rec, req))

	return rec
}

// TestFormGuardPerEmailBudgetIsSilent: past the per-address budget, sign-up
// and reset answer byte for byte like a sent email, and send nothing. Another
// address from the same client is still sent (positive control).
func TestFormGuardPerEmailBudgetIsSilent(t *testing.T) {
	t.Parallel()

	forms := []struct {
		form     string
		path     string
		template string
		body     func(email string) string
		handle   func(h *Handler) func(http.ResponseWriter, *http.Request) error
	}{
		{
			form: FormRegister, path: "/api/v1/auth/register", template: "registration.html",
			body: func(email string) string {
				return fmt.Sprintf(`{"email":%q,"password":"supersecret123"}`, email)
			},
			handle: func(h *Handler) func(http.ResponseWriter, *http.Request) error { return h.Register },
		},
		{
			form: FormPasswordReset, path: "/api/v1/auth/request-password-reset", template: "password-reset.html",
			body: func(email string) string { return fmt.Sprintf(`{"email":%q}`, email) },
			handle: func(h *Handler) func(http.ResponseWriter, *http.Request) error {
				return h.RequestPasswordReset
			},
		},
	}

	for _, tc := range forms {
		t.Run(tc.form, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			cfg := formGuardTestConfig(config.FormGuardConfig{PerEmailPerHour: 2})
			svc, dbSvc, ctx := setupAuthTestServiceWithJobs(t, cfg)
			handler := NewHandler(svc, cfg)

			target := "budget-" + tc.form + "@acme.com"
			control := "control-" + tc.form + "@acme.com"

			if tc.form == FormPasswordReset {
				createPasswordUser(t, ctx, dbSvc, target)
				createPasswordUser(t, ctx, dbSvc, control)
			}

			// Each request comes from its own address, so the per-minute reset
			// limit and the per-IP budget stay out of the picture.
			recs := make([]*httptest.ResponseRecorder, 0, 3)
			for i := range 3 {
				recs = append(recs, postJSON(t, tc.handle(handler), tc.path, tc.body(target),
					fmt.Sprintf("192.0.2.%d:1234", i+1), ""))
			}

			r.Equal(2, emailsTo(t, ctx, dbSvc, target, tc.template), "the budget allows exactly 2")

			for _, rec := range recs[1:] {
				r.Equal(recs[0].Code, rec.Code, "an over-budget answer has the success status")
				r.JSONEq(recs[0].Body.String(), rec.Body.String(), "and the success body")
			}

			r.Equal(http.StatusOK, recs[2].Code)

			postJSON(t, tc.handle(handler), tc.path, tc.body(control), "192.0.2.50:1234", "")
			r.Equal(1, emailsTo(t, ctx, dbSvc, control, tc.template), "another address is still sent")
		})
	}
}

// TestFormGuardPerIPBudget: past the per-IP budget of unconfirmed sign-ups the
// endpoint answers 429, a forged X-Forwarded-For does not mint a fresh budget
// when no proxy is trusted, and another client is unaffected (positive
// control). Confirming a sign-up frees its slot, and signing the same address
// up again does not take a second one.
func TestFormGuardPerIPBudget(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := formGuardTestConfig(config.FormGuardConfig{PerIPPerHour: 2})
	svc, dbSvc, ctx := setupAuthTestServiceWithJobs(t, cfg)
	handler := NewHandler(svc, cfg)

	register := func(email, remoteAddr, xff string) *httptest.ResponseRecorder {
		return postJSON(t, handler.Register, "/api/v1/auth/register",
			fmt.Sprintf(`{"email":%q,"password":"supersecret123"}`, email), remoteAddr, xff)
	}

	r.Equal(http.StatusOK, register("ip-1@acme.com", "203.0.113.7:1000", "").Code)
	r.Equal(http.StatusOK, register("ip-2@acme.com", "203.0.113.7:1001", "").Code)

	limited := register("ip-3@acme.com", "203.0.113.7:1002", "10.9.9.9")
	r.Equal(http.StatusTooManyRequests, limited.Code, "the forged X-Forwarded-For is ignored")
	r.Zero(emailsTo(t, ctx, dbSvc, "ip-3@acme.com", "registration.html"))

	r.Equal(http.StatusOK, register("ip-4@acme.com", "203.0.113.8:1000", "").Code)
	r.Equal(1, emailsTo(t, ctx, dbSvc, "ip-4@acme.com", "registration.html"), "another client is still sent")

	r.Equal(http.StatusOK, register("ip-2@acme.com", "203.0.113.7:1003", "").Code,
		"the same address again replaces its pending entry")

	_, err := svc.ConfirmRegistration(ctx, extractRegistrationConfirmToken(t, ctx, dbSvc, "ip-1@acme.com"))
	r.NoError(err)

	r.Equal(http.StatusOK, register("ip-3@acme.com", "203.0.113.7:1004", "").Code, "a confirmation frees a slot")
	r.Equal(1, emailsTo(t, ctx, dbSvc, "ip-3@acme.com", "registration.html"))
	r.Equal(http.StatusTooManyRequests, register("ip-5@acme.com", "203.0.113.7:1005", "").Code,
		"and only one")
}

// TestPasswordResetIPLimitIgnoresForgedXFF: the reset form's per-minute IP
// limit goes through the trusted-proxy resolution, so it can no longer be
// dodged with a fresh X-Forwarded-For per request.
func TestPasswordResetIPLimitIgnoresForgedXFF(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := formGuardTestConfig(config.FormGuardConfig{})
	svc, _, _ := setupAuthTestServiceWithJobs(t, cfg)
	handler := NewHandler(svc, cfg)

	for i := range passwordResetMaxPerIP {
		rec := postJSON(t, handler.RequestPasswordReset, "/api/v1/auth/request-password-reset",
			`{"email":"nobody@acme.com"}`, "203.0.113.9:1000", fmt.Sprintf("10.0.0.%d", i))
		r.Equal(http.StatusOK, rec.Code)
	}

	rec := postJSON(t, handler.RequestPasswordReset, "/api/v1/auth/request-password-reset",
		`{"email":"nobody@acme.com"}`, "203.0.113.9:1000", "10.0.0.200")
	r.Equal(http.StatusTooManyRequests, rec.Code)
}

// TestFormTicketHandler covers GET /api/v1/auth/form-ticket.
func TestFormTicketHandler(t *testing.T) {
	t.Parallel()

	cfg := formGuardTestConfig(config.DefaultFormGuardConfig())
	svc, _, _ := setupAuthTestServiceWithJobs(t, cfg)
	handler := NewHandler(svc, cfg)

	tests := []struct {
		name   string
		query  string
		status int
	}{
		{name: "register", query: "form=register", status: http.StatusOK},
		{name: "password reset", query: "form=password-reset", status: http.StatusOK},
		{name: "unknown form", query: "form=login", status: http.StatusUnprocessableEntity},
		{name: "no form", query: "", status: http.StatusUnprocessableEntity},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
				"/api/v1/auth/form-ticket?"+tc.query, nil)
			rec := httptest.NewRecorder()
			r.NoError(handler.FormTicket(rec, req))
			r.Equal(tc.status, rec.Code)

			if tc.status != http.StatusOK {
				return
			}

			var body FormTicketResponse
			r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
			r.NotEmpty(body.Ticket)
			r.Equal(config.DefaultFormGuardMinFillTime.Milliseconds(), body.MinAgeMs)
			r.WithinDuration(time.Now().Add(config.DefaultFormGuardTicketTTL), body.ExpiresAt, time.Minute)
			r.Equal("no-store", rec.Header().Get("Cache-Control"))
		})
	}
}
