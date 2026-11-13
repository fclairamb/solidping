package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

const (
	regEmail    = "alice@acme.com"
	regPassword = "supersecret123"
	// autoJoinKey is the org parameter holding the auto-join email pattern.
	autoJoinKey     = "registration.email_pattern"
	autoJoinPattern = `@acme\.com$`
)

// registrationWorld is a service with a pending registration for regEmail.
type registrationWorld struct {
	*loginAuditFixture

	ctx   context.Context //nolint:containedctx // test fixture
	fault *queryFault
	token string
	seen  map[string]bool
}

// newRegistrationWorld registers regEmail and returns the confirmation token
// the email carries. prepare runs before the sign-up.
func newRegistrationWorld(t *testing.T, prepare func(w *registrationWorld)) *registrationWorld {
	t.Helper()

	f, ctx := newLoginAuditFixture(t)
	f.svc.fullCfg.Auth.RegistrationEmailPattern = ".*@acme.com"

	w := &registrationWorld{
		loginAuditFixture: f, ctx: ctx, fault: installQueryFault(f.db), seen: map[string]bool{},
	}
	if prepare != nil {
		prepare(w)
	}

	w.token = w.register(t)

	return w
}

func (w *registrationWorld) register(t *testing.T) string {
	t.Helper()

	_, err := w.svc.Register(w.ctx, RegisterRequest{Name: "Alice", Email: regEmail, Password: regPassword})
	require.NoError(t, err)

	jobs, err := w.db.ListJobs(w.ctx, nil, 0)
	require.NoError(t, err)

	// Each sign-up mails one link: the token is the one not seen before.
	token := ""

	for _, job := range jobs {
		data, _ := job.Config["templateData"].(map[string]any)
		link, _ := data["ConfirmURL"].(string)

		if candidate := link[strings.LastIndex(link, "/")+1:]; candidate != "" && !w.seen[candidate] {
			w.seen[candidate] = true
			token = candidate
		}
	}

	require.NotEmpty(t, token, "the sign-up must have mailed a confirmation link")

	return token
}

// registered returns the account of regEmail, nil when there is none.
func (w *registrationWorld) registered(t *testing.T) *models.User {
	t.Helper()

	users, err := w.db.ListUsers(w.ctx)
	require.NoError(t, err)

	for _, user := range users {
		if user.Email == regEmail {
			return user
		}
	}

	return nil
}

func (w *registrationWorld) memberships(t *testing.T) []*models.OrganizationMember {
	t.Helper()

	user := w.registered(t)
	require.NotNil(t, user)

	members, err := w.db.ListMembersByUser(w.ctx, user.UID)
	require.NoError(t, err)

	return members
}

// TestRegisterRefusesAddressesOutsideThePattern covers the instance-wide
// registration pattern.
func TestRegisterRefusesAddressesOutsideThePattern(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f, ctx := newLoginAuditFixture(t)
	req := RegisterRequest{Name: "Bob", Email: "bob@acme.com", Password: regPassword}

	f.svc.fullCfg.Auth.RegistrationEmailPattern = `.*@staff\.acme\.com$`
	_, err := f.svc.Register(ctx, req)
	r.ErrorIs(err, ErrEmailNotAllowed)

	f.svc.fullCfg.Auth.RegistrationEmailPattern = "(unclosed"
	_, err = f.svc.Register(ctx, req)
	r.ErrorContains(err, "invalid registration email pattern")

	entry, err := f.db.GetStateEntry(ctx, nil, registrationKeyPrefix+req.Email)
	r.NoError(err)
	r.Nil(entry, "a refused sign-up leaves no pending registration")
}

// TestConfirmRegistrationOfAStalePendingEntry: a confirmation link whose
// pending registration is gone, replaced or incomplete is expired, and
// creates no account.
func TestConfirmRegistrationOfAStalePendingEntry(t *testing.T) {
	t.Parallel()

	entryKey := registrationKeyPrefix + regEmail

	tests := []struct {
		name    string
		spoil   func(t *testing.T, w *registrationWorld)
		want    error
		user    bool
		dropped bool
	}{
		{
			name: "pending registration removed",
			spoil: func(t *testing.T, w *registrationWorld) {
				t.Helper()

				_, err := w.db.DeleteStateEntry(w.ctx, nil, entryKey)
				require.NoError(t, err)
			},
			want: ErrRegistrationExpired,
		},
		{
			name: "pending registration without a password hash",
			spoil: func(t *testing.T, w *registrationWorld) {
				t.Helper()

				entry, err := w.db.GetStateEntry(w.ctx, nil, entryKey)
				require.NoError(t, err)

				value := *entry.Value
				delete(value, "passwordHash")
				require.NoError(t, w.db.SetStateEntry(w.ctx, nil, entryKey, &value, nil))
			},
			want: ErrRegistrationExpired,
		},
		{
			name: "account created in the meantime",
			spoil: func(t *testing.T, w *registrationWorld) {
				t.Helper()

				require.NoError(t, w.db.CreateUser(w.ctx, models.NewUser(regEmail)))
			},
			want:    ErrEmailAlreadyTaken,
			user:    true,
			dropped: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := newRegistrationWorld(t, nil)
			tt.spoil(t, w)

			resp, err := w.svc.ConfirmRegistration(w.ctx, w.token)
			require.ErrorIs(t, err, tt.want)
			require.Nil(t, resp)
			require.Equal(t, tt.user, w.registered(t) != nil)

			if tt.user {
				require.Nil(t, w.registered(t).PasswordHash, "the existing account is left untouched")
			}

			entry, err := w.db.GetStateEntry(w.ctx, nil, entryKey)
			require.NoError(t, err)

			if tt.dropped {
				require.Nil(t, entry, "the useless pending registration is dropped")
			}
		})
	}

	t.Run("token replaced by a newer sign-up", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		w := newRegistrationWorld(t, nil)
		newer := w.register(t)
		r.NotEqual(w.token, newer)

		_, err := w.svc.ConfirmRegistration(w.ctx, w.token)
		r.ErrorIs(err, ErrRegistrationExpired)
		r.Nil(w.registered(t))

		resp, err := w.svc.ConfirmRegistration(w.ctx, newer)
		r.NoError(err)
		r.Equal(regEmail, resp.User.Email)
	})
}

// TestConfirmRegistrationSurfacesStoreFailures: a store failure during the
// confirmation is an error naming the step, never a silent success.
func TestConfirmRegistrationSurfacesStoreFailures(t *testing.T) {
	t.Parallel()

	// An organization adopting the new account, so the confirmation goes all
	// the way to an org-scoped session.
	adopting := func(w *registrationWorld) {
		_ = w.db.SetOrgParameter(w.ctx, w.org.UID, autoJoinKey, autoJoinPattern, false)
	}

	tests := []struct {
		name    string
		fault   sqlFault
		wantMsg string
		created bool
	}{
		{
			"reading the pending registration", failingSQL(`FROM "state_entries"`).after(1),
			"failed to get registration", false,
		},
		{"checking the address is free", failingSQL(`FROM "users"`), "", false},
		{"creating the account", failingSQL(`INSERT INTO "users"`), "failed to create user", false},
		{
			"reading the adopting organization", failingSQL(`FROM "organizations"`),
			"failed to get organization", true,
		},
		{"storing the session", failingSQL(`INSERT INTO "user_tokens"`), "failed to store refresh token", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := newRegistrationWorld(t, adopting)

			var (
				resp *LoginResponse
				err  error
			)

			w.fault.during(t, tt.fault, func() {
				resp, err = w.svc.ConfirmRegistration(w.ctx, w.token)
			})
			require.ErrorIs(t, err, context.Canceled)
			require.ErrorContains(t, err, tt.wantMsg)
			require.Nil(t, resp)
			require.Equal(t, tt.created, w.registered(t) != nil)

			if tt.created {
				return
			}

			// Nothing was consumed: the same link works once the store is back.
			resp, err = w.svc.ConfirmRegistration(w.ctx, w.token)
			require.NoError(t, err)
			require.Equal(t, w.org.Slug, resp.Organization.Slug)
		})
	}
}

// TestAutoJoinSkipsOrganizationsItCannotUse: at confirmation the account joins
// the organizations whose pattern matches its address. An organization whose
// pattern is unusable, that is full, or whose membership cannot be written is
// skipped, and the confirmation still succeeds.
func TestAutoJoinSkipsOrganizationsItCannotUse(t *testing.T) {
	t.Parallel()

	pattern := func(value any) func(w *registrationWorld) {
		return func(w *registrationWorld) {
			_ = w.db.SetOrgParameter(w.ctx, w.org.UID, autoJoinKey, value, false)
		}
	}

	tests := []struct {
		name    string
		prepare func(w *registrationWorld)
		fault   *sqlFault
		joined  bool
	}{
		{name: "control: a matching pattern", prepare: pattern(autoJoinPattern), joined: true},
		{name: "empty pattern", prepare: pattern("")},
		{name: "pattern that is not a string", prepare: pattern(42)},
		{name: "organization at its seat cap", prepare: func(w *registrationWorld) {
			pattern(autoJoinPattern)(w)
			w.svc.entitlements = &stubEntitlementsChecker{err: errSSOQuotaTest}
		}},
		{
			name: "patterns cannot be listed", prepare: pattern(autoJoinPattern),
			fault: &sqlFault{fragments: []string{`FROM "parameters"`, autoJoinKey}},
		},
		{
			name: "membership cannot be written", prepare: pattern(autoJoinPattern),
			fault: &sqlFault{fragments: []string{`INSERT INTO "organization_members"`}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := newRegistrationWorld(t, tt.prepare)

			var (
				resp *LoginResponse
				err  error
			)

			confirm := func() { resp, err = w.svc.ConfirmRegistration(w.ctx, w.token) }
			if tt.fault != nil {
				w.fault.during(t, *tt.fault, confirm)
			} else {
				confirm()
			}

			require.NoError(t, err)
			require.NotEmpty(t, resp.AccessToken)

			members := w.memberships(t)
			if !tt.joined {
				require.Empty(t, members)
				require.Equal(t, LoginActionNoOrg, resp.LoginAction)
				require.Nil(t, resp.Organization)

				return
			}

			require.Len(t, members, 1)
			require.Equal(t, w.org.UID, members[0].OrganizationUID)
			require.Equal(t, models.MemberRoleUser, members[0].Role)
			require.Equal(t, w.org.Slug, resp.Organization.Slug)
		})
	}
}
