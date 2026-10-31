package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// guardFaultService is a form-guard service whose storage can be made to fail.
func guardFaultService(t *testing.T, guard config.FormGuardConfig) (*Service, *personalOrgFaultDB) {
	t.Helper()

	f, _ := newLoginAuditFixture(t)
	f.svc.fullCfg = formGuardTestConfig(guard)

	fault := &personalOrgFaultDB{Service: f.db}
	f.svc.db = fault

	return f.svc, fault
}

func TestIssueFormTicketRejectsUnknownForm(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _ := guardFaultService(t, enabledGuard())

	_, err := svc.IssueFormTicket("newsletter")
	r.ErrorIs(err, ErrUnknownForm)

	resp, err := svc.IssueFormTicket(FormRegister)
	r.NoError(err)
	r.NotEmpty(resp.Ticket)
}

// TestFormGuardStorageErrorsFailOpen: a broken counter store must not turn
// every sign-up into an error; the submission is let through (and logged).
func TestFormGuardStorageErrorsFailOpen(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		guard config.FormGuardConfig
		form  string
		set   func(p *personalOrgFaultDB)
	}{
		{
			name: "ip budget counter fails", guard: config.FormGuardConfig{PerIPPerHour: 5},
			form: FormPasswordReset, set: func(p *personalOrgFaultDB) { p.failBump = true },
		},
		{
			name: "pending registrations cannot be listed", guard: config.FormGuardConfig{PerIPPerHour: 5},
			form: FormRegister, set: func(p *personalOrgFaultDB) { p.failStateList = true },
		},
		{
			name: "email budget counter fails", guard: config.FormGuardConfig{PerEmailPerHour: 5},
			form: FormRegister, set: func(p *personalOrgFaultDB) { p.failBump = true },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			svc, fault := guardFaultService(t, tc.guard)
			tc.set(fault)

			reason, err := svc.screenFormSubmission(
				t.Context(), tc.form, FormGuardFields{}, formGuardTestIP, "bot@acme.com")
			r.NoError(err)
			r.Empty(reason)
		})
	}
}

// TestFormGuardTicketUseRecordErrorFailsOpen: when the single-use record
// cannot be written, a valid ticket is still accepted.
func TestFormGuardTicketUseRecordErrorFailsOpen(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, fault := guardFaultService(t, enabledGuard())
	ticket := oldTicket(t, svc, FormRegister)
	fault.failBump = true

	reason, err := svc.screenFormSubmission(
		t.Context(), FormRegister, FormGuardFields{FormTicket: ticket}, "", "a@acme.com")
	r.NoError(err)
	r.Empty(reason)
}

// TestFormGuardRejectsIncompleteTicket: a ticket that verifies but lacks an ID
// or an expiry is not one the server issued.
func TestFormGuardRejectsIncompleteTicket(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		claims formTicketClaims
	}{
		{
			name: "no id",
			claims: formTicketClaims{
				Form: FormRegister, IssuedAtMs: time.Now().Add(-time.Minute).UnixMilli(),
				RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))},
			},
		},
		{
			name: "no expiry",
			claims: formTicketClaims{
				Form: FormRegister, IssuedAtMs: time.Now().Add(-time.Minute).UnixMilli(),
				RegisteredClaims: jwt.RegisteredClaims{ID: "abc"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			svc, _ := guardFaultService(t, enabledGuard())

			signed, err := svc.signJWT(formTicketPurpose, tc.claims)
			r.NoError(err)

			r.Equal(formDropInvalidTicket, svc.checkFormTicket(
				t.Context(), FormRegister, FormGuardFields{FormTicket: signed}, time.Now()))
		})
	}
}

func TestPasswordResetFormIPBudgetSurfacesRateLimit(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _ := guardFaultService(t, config.FormGuardConfig{PerIPPerHour: 1})
	req := RequestPasswordResetRequest{Email: "nobody@acme.com"}

	_, err := svc.RequestPasswordReset(t.Context(), req, formGuardTestIP)
	r.NoError(err)

	_, err = svc.RequestPasswordReset(t.Context(), req, formGuardTestIP)
	r.ErrorIs(err, ErrRateLimited)
}

func TestValidateRegistrationRejections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		want    error
		wantMsg string
	}{
		{name: "pattern does not compile", pattern: "(", wantMsg: "invalid registration email pattern"},
		{name: "email outside the pattern", pattern: ".*@corp\\.com", want: ErrEmailNotAllowed},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			svc, _ := guardFaultService(t, config.FormGuardConfig{})
			svc.fullCfg.Auth.RegistrationEmailPattern = tc.pattern

			err := svc.validateRegistration("jane@acme.com", "supersecret123")
			r.Error(err)

			if tc.want != nil {
				r.ErrorIs(err, tc.want)
			}

			if tc.wantMsg != "" {
				r.ErrorContains(err, tc.wantMsg)
			}
		})
	}
}

func TestAutoJoinMatchingOrgsSkipsOrgsThatCannotAdmit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		set  func(svc *Service, fault *personalOrgFaultDB)
	}{
		{name: "org at its member cap", set: func(svc *Service, _ *personalOrgFaultDB) {
			svc.entitlements = &stubEntitlementsChecker{err: errSSOQuotaTest}
		}},
		{name: "membership cannot be stored", set: func(_ *Service, fault *personalOrgFaultDB) {
			fault.failCreateMember = true
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			f, fault, _, ctx := newPersonalOrgFaultFixture(t)
			user := models.NewUser("newcomer@acme.com")
			r.NoError(f.db.CreateUser(ctx, user))
			r.NoError(f.db.SetOrgParameter(ctx, f.org.UID, "registration.email_pattern", ".*@acme\\.com", false))
			tc.set(f.svc, fault)

			f.svc.autoJoinMatchingOrgs(ctx, user.UID, user.Email)

			members, err := f.db.ListMembersByUser(ctx, user.UID)
			r.NoError(err)
			r.Empty(members)
		})
	}
}
