package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/defaults"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// Spec 2026-10-30-04: a user who signs in belonging to no organization gets
// an invitation's org, a pattern's org, waits for their request, or gets a
// personal org, through every no-org entry point.

const personalOrgTestPassword = "supersecret123"

// personalOrgFixture is a SaaS instance (auto_personal_org on by default)
// with a real job queue, so registration and warning emails are queued.
type personalOrgFixture struct {
	*loginAuditFixture
}

func newPersonalOrgFixture(t *testing.T, mode string) (*personalOrgFixture, context.Context) {
	t.Helper()

	f, ctx := newLoginAuditFixture(t)
	f.svc.fullCfg.Deployment.Mode = mode
	f.svc.fullCfg.Auth.RegistrationEmailPattern = ".*"

	return &personalOrgFixture{loginAuditFixture: f}, ctx
}

// seededOrg creates an org with one admin member of the given email.
func (f *personalOrgFixture) seededOrg(
	ctx context.Context, t *testing.T, slug, memberEmail string,
) (*models.Organization, *models.User) {
	t.Helper()

	org := models.NewOrganization(slug, "Org "+slug)
	require.NoError(t, f.db.CreateOrganization(ctx, org))

	member := models.NewUser(memberEmail)
	require.NoError(t, f.db.CreateUser(ctx, member))
	require.NoError(t, f.db.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(org.UID, member.UID, models.MemberRoleAdmin)))

	return org, member
}

// passwordUser creates an org-less account with a password.
func (f *personalOrgFixture) passwordUser(ctx context.Context, t *testing.T, email, name string) *models.User {
	t.Helper()

	hash, err := passwords.Hash(personalOrgTestPassword)
	require.NoError(t, err)

	user := models.NewUser(email)
	user.Name = name
	user.PasswordHash = &hash
	require.NoError(t, f.db.CreateUser(ctx, user))

	return user
}

// personalOrgCase prepares the state before the sign-in.
type personalOrgCase struct {
	name string
	mode string
	// prepare runs before the sign-in; user is nil for the registration
	// entry point (the account does not exist yet).
	prepare func(ctx context.Context, t *testing.T, f *personalOrgFixture, email string, user *models.User)
	// skipRegistration: the case needs an existing account.
	skipRegistration bool
	wantOrgSlug      string // "" = stays org-less
	wantPersonal     bool
	wantRole         models.MemberRole
}

// signInOutcome is what an entry point landed the user on.
type signInOutcome struct {
	orgSlug string
	action  LoginAction
}

type signInEntryPoint struct {
	name string
	// existing: the account exists before the sign-in.
	existing bool
	signIn   func(ctx context.Context, t *testing.T, f *personalOrgFixture, email string) signInOutcome
}

func personalOrgEntryPoints() []signInEntryPoint {
	return []signInEntryPoint{
		{
			name: "password login", existing: true,
			signIn: func(ctx context.Context, t *testing.T, f *personalOrgFixture, email string) signInOutcome {
				t.Helper()

				resp, err := f.svc.Login(ctx, "", email, personalOrgTestPassword, Context{})
				require.NoError(t, err)

				slug := ""
				if resp.Organization != nil {
					slug = resp.Organization.Slug
					require.NotEmpty(t, resp.RefreshToken, "an org session carries a refresh token")
				}

				return signInOutcome{orgSlug: slug, action: resp.LoginAction}
			},
		},
		{
			name: "registration confirm", existing: false,
			signIn: func(ctx context.Context, t *testing.T, f *personalOrgFixture, email string) signInOutcome {
				t.Helper()

				_, err := f.svc.Register(ctx, RegisterRequest{
					Name: "Alice Martin", Email: email, Password: personalOrgTestPassword,
				})
				require.NoError(t, err)

				resp, err := f.svc.ConfirmRegistration(ctx, extractRegistrationConfirmToken(t, ctx, f.db, email))
				require.NoError(t, err)

				if resp.Organization == nil {
					return signInOutcome{action: resp.LoginAction}
				}

				return signInOutcome{orgSlug: resp.Organization.Slug, action: LoginActionDefault}
			},
		},
		{
			name: "provider login", existing: true,
			signIn: func(ctx context.Context, t *testing.T, f *personalOrgFixture, email string) signInOutcome {
				t.Helper()

				// A social sign-in from the platform's default login page,
				// the common SaaS entry: that org never admits sign-ins.
				org, err := f.db.GetOrganizationBySlug(ctx, defaults.Organization)
				require.NoError(t, err)

				user, err := f.db.GetUserByEmail(ctx, email)
				require.NoError(t, err)

				result, err := f.svc.CompleteOrgLogin(ctx, org, user, WithLoginMethod(signupMethodGoogle))
				require.NoError(t, err)

				if result.FallbackOrgSlug == "" {
					require.Empty(t, result.RefreshToken)

					return signInOutcome{action: LoginActionNoOrg}
				}

				require.NotEmpty(t, result.RefreshToken)

				return signInOutcome{orgSlug: result.FallbackOrgSlug, action: LoginActionDefault}
			},
		},
	}
}

func personalOrgCases() []personalOrgCase {
	saas, self := config.DeploymentModeSaaS, config.DeploymentModeSelfHosted

	return []personalOrgCase{
		{
			name: "pending invitation joins the inviting org", mode: saas,
			prepare: func(ctx context.Context, t *testing.T, f *personalOrgFixture, email string, _ *models.User) {
				t.Helper()

				org, inviter := f.seededOrg(ctx, t, "inviter", "boss@inviter.example")
				_, err := f.svc.CreateInvitation(ctx, org.Slug, inviter.UID, InviteRequest{
					Email: email, Role: string(models.MemberRoleViewer), ExpiresIn: "24h", App: "dash0",
				})
				require.NoError(t, err)
			},
			wantOrgSlug: "inviter", wantRole: models.MemberRoleViewer,
		},
		{
			name: "email pattern joins the matching org", mode: saas,
			prepare: func(ctx context.Context, t *testing.T, f *personalOrgFixture, _ string, _ *models.User) {
				t.Helper()

				org, _ := f.seededOrg(ctx, t, "patterned", "boss@patterned.example")
				require.NoError(t, f.db.SetOrgParameter(
					ctx, org.UID, "registration.email_pattern", `@acme\.com$`, false))
			},
			wantOrgSlug: "patterned", wantRole: models.MemberRoleUser,
		},
		{
			name: "pending membership request keeps the user waiting", mode: saas,
			prepare: func(ctx context.Context, t *testing.T, f *personalOrgFixture, _ string, user *models.User) {
				t.Helper()

				org, _ := f.seededOrg(ctx, t, "wanted", "boss@wanted.example")
				require.NoError(t, f.db.CreateMembershipRequest(ctx, models.NewMembershipRequest(org.UID, user.UID, nil)))
			},
			skipRegistration: true,
		},
		{
			name: "nothing else creates a personal org", mode: saas,
			wantOrgSlug: "alice", wantPersonal: true, wantRole: models.MemberRoleOwner,
		},
		{
			name: "slug collision gets a suffix", mode: saas,
			prepare: func(ctx context.Context, t *testing.T, f *personalOrgFixture, _ string, _ *models.User) {
				t.Helper()

				f.seededOrg(ctx, t, "alice", "other-alice@elsewhere.example")
			},
			wantOrgSlug: "alice2", wantPersonal: true, wantRole: models.MemberRoleOwner,
		},
		{
			name: "self-hosted default creates nothing", mode: self,
		},
	}
}

// TestPersonalOrgAcrossNoOrgEntryPoints is the table of spec 2026-10-30-04:
// every no-org entry point resolves the same way.
func TestPersonalOrgAcrossNoOrgEntryPoints(t *testing.T) {
	t.Parallel()

	for _, entry := range personalOrgEntryPoints() {
		for _, tc := range personalOrgCases() {
			if tc.skipRegistration && !entry.existing {
				continue
			}

			t.Run(entry.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				r := require.New(t)
				f, ctx := newPersonalOrgFixture(t, tc.mode)

				// The platform default org the provider login starts from.
				f.seededOrg(ctx, t, defaults.Organization, "operator@solidping.example")

				const email = "alice@acme.com"

				var user *models.User
				if entry.existing {
					user = f.passwordUser(ctx, t, email, "Alice Martin")
				}

				if tc.prepare != nil {
					tc.prepare(ctx, t, f, email, user)
				}

				got := entry.signIn(ctx, t, f, email)

				user, err := f.db.GetUserByEmail(ctx, email)
				r.NoError(err)

				personal, err := f.db.GetAutoPersonalOrgByOwner(ctx, user.UID, true)
				r.NoError(err)

				if tc.wantOrgSlug == "" {
					r.Equal(LoginActionNoOrg, got.action)
					r.Empty(got.orgSlug)
					r.Nil(personal, "no personal org may be created")

					return
				}

				r.Equal(tc.wantOrgSlug, got.orgSlug)
				r.Equal(LoginActionDefault, got.action)

				org, err := f.db.GetOrganizationBySlug(ctx, tc.wantOrgSlug)
				r.NoError(err)

				member, err := f.db.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
				r.NoError(err)
				r.Equal(tc.wantRole, member.Role)

				if !tc.wantPersonal {
					r.Nil(personal, "joining an existing org must not also create a personal org")

					return
				}

				r.NotNil(personal)
				r.Equal(org.UID, personal.UID)
				r.Equal("Alice's organization", personal.Name)
				r.NotNil(org.AutoPersonalOwnerUID)
				r.Equal(user.UID, *org.AutoPersonalOwnerUID)
			})
		}
	}
}

// TestPersonalOrgIsIdempotent: a second sign-in lands on the same org, and
// concurrent first sign-ins create one org between them.
func TestPersonalOrgIsIdempotent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f, ctx := newPersonalOrgFixture(t, config.DeploymentModeSaaS)
	user := f.passwordUser(ctx, t, "alice@acme.com", "Alice")

	first, err := f.svc.Login(ctx, "", user.Email, personalOrgTestPassword, Context{})
	r.NoError(err)
	r.NotNil(first.Organization)

	second, err := f.svc.Login(ctx, "", user.Email, personalOrgTestPassword, Context{})
	r.NoError(err)
	r.NotNil(second.Organization)
	r.Equal(first.Organization.Slug, second.Organization.Slug)

	racer := f.passwordUser(ctx, t, "bob@acme.com", "Bob")

	const racers = 8

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		slugs = map[string]struct{}{}
	)

	for range racers {
		wg.Go(func() {
			if member := f.svc.ensurePersonalOrg(ctx, racer); member != nil {
				mu.Lock()
				slugs[member.Organization.Slug] = struct{}{}
				mu.Unlock()
			}
		})
	}

	wg.Wait()

	r.Len(slugs, 1, "every concurrent sign-in lands on the same org")

	members, err := f.db.ListMembersByUser(ctx, racer.UID)
	r.NoError(err)
	r.Len(members, 1, "one personal org, one membership")

	// The database itself refuses a second live personal org.
	ownerUID := racer.UID
	dup := models.NewOrganization("bob-dup", "Bob dup")
	dup.AutoPersonalOwnerUID = &ownerUID
	r.Error(f.db.CreateOrganization(ctx, dup))
}

// TestPersonalOrgNotRecreatedAfterDeletion: deleting the personal org lands
// on /no-org (no endless re-creation), while deleting a hand-made last org
// follows the no-org rule and yields a personal org.
func TestPersonalOrgNotRecreatedAfterDeletion(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f, ctx := newPersonalOrgFixture(t, config.DeploymentModeSaaS)
	user := f.passwordUser(ctx, t, "alice@acme.com", "Alice")

	login, err := f.svc.Login(ctx, "", user.Email, personalOrgTestPassword, Context{})
	r.NoError(err)
	r.NotNil(login.Organization)

	slug := login.Organization.Slug

	resp, err := f.svc.DeleteOrg(ctx, slug, user.UID, DeleteOrgRequest{Slug: slug}, Context{})
	r.NoError(err)
	r.Equal(LoginActionNoOrg, resp.LoginAction)

	again, err := f.svc.Login(ctx, "", user.Email, personalOrgTestPassword, Context{})
	r.NoError(err)
	r.Equal(LoginActionNoOrg, again.LoginAction)
	r.Nil(again.Organization)

	// A user whose last, hand-made org goes away gets a personal one.
	bob := f.passwordUser(ctx, t, "bob@acme.com", "Bob")
	made, err := f.svc.CreateOrg(ctx, bob.UID, CreateOrgRequest{Name: "Bob's shop"}, Context{})
	r.NoError(err)

	afterDelete, err := f.svc.DeleteOrg(ctx, made.Slug, bob.UID, DeleteOrgRequest{Slug: made.Slug}, Context{})
	r.NoError(err)
	r.NotNil(afterDelete.Organization)

	personal, err := f.db.GetAutoPersonalOrgByOwner(ctx, bob.UID, false)
	r.NoError(err)
	r.NotNil(personal)
	r.Equal(personal.Slug, afterDelete.Organization.Slug)
}

// TestPersonalOrgSkipsSuperAdminsAndDemoUsers: neither may create an org by
// hand, so none is created for them.
func TestPersonalOrgSkipsSuperAdminsAndDemoUsers(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f, ctx := newPersonalOrgFixture(t, config.DeploymentModeSaaS)

	admin := f.passwordUser(ctx, t, "root@acme.com", "Root")
	admin.SuperAdmin = true
	r.Nil(f.svc.ensurePersonalOrg(ctx, admin))

	demo := f.passwordUser(ctx, t, "demo@acme.com", "Demo")
	demo.Demo = true
	r.Nil(f.svc.ensurePersonalOrg(ctx, demo))

	f.svc.fullCfg.Auth.AutoPersonalOrg = config.BoolPtr(false)
	plain := f.passwordUser(ctx, t, "plain@acme.com", "Plain")
	r.Nil(f.svc.ensurePersonalOrg(ctx, plain), "an explicit false turns SaaS off")
}

// TestPersonalOrgNameFallback: without a usable first name the org gets a
// friendly two-word name, stable for the user.
func TestPersonalOrgNameFallback(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	user := models.NewUser("x@acme.com")
	user.Name = "  "
	name, slugBase := personalOrgName(user)
	r.NotEmpty(name)
	r.Equal(name, slugBase)

	again, _ := personalOrgName(user)
	r.Equal(name, again)

	user.Name = "Zoé Durand"
	user.Preferences = &models.UserPreferences{Locale: "fr"}
	name, slugBase = personalOrgName(user)
	r.Equal("Zoé", slugBase)
	r.Equal("L'organisation de Zoé", name, "named in the user's language")
}

// TestColleagueHint: shown for a non-webmail domain used by exactly one org
// that accepts requests, and nowhere else.
func TestColleagueHint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		email   string
		prepare func(ctx context.Context, t *testing.T, f *personalOrgFixture)
		want    string
	}{
		{
			name: "one org shares the company domain", email: "alice@acme.com",
			prepare: func(ctx context.Context, t *testing.T, f *personalOrgFixture) {
				t.Helper()
				f.seededOrg(ctx, t, "acme", "bob@acme.com")
			},
			want: "acme",
		},
		{
			name: "webmail domains never hint", email: "alice@gmail.com",
			prepare: func(ctx context.Context, t *testing.T, f *personalOrgFixture) {
				t.Helper()
				f.seededOrg(ctx, t, "gmailers", "bob@gmail.com")
			},
		},
		{
			name: "two matching orgs hint nothing", email: "alice@acme.com",
			prepare: func(ctx context.Context, t *testing.T, f *personalOrgFixture) {
				t.Helper()
				f.seededOrg(ctx, t, "acme", "bob@acme.com")
				f.seededOrg(ctx, t, "acme-labs", "carol@acme.com")
			},
		},
		{
			name: "an org that suppresses requests is not named", email: "alice@acme.com",
			prepare: func(ctx context.Context, t *testing.T, f *personalOrgFixture) {
				t.Helper()
				// The SaaS platform default org never admits requests.
				f.seededOrg(ctx, t, defaults.Organization, "bob@acme.com")
			},
		},
		{
			name: "a colleague's personal org does not count", email: "alice@acme.com",
			prepare: func(ctx context.Context, t *testing.T, f *personalOrgFixture) {
				t.Helper()
				bob := f.passwordUser(ctx, t, "bob@acme.com", "Bob")
				require.NotNil(t, f.svc.ensurePersonalOrg(ctx, bob))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			f, ctx := newPersonalOrgFixture(t, config.DeploymentModeSaaS)
			tt.prepare(ctx, t, f)

			user := f.passwordUser(ctx, t, tt.email, "Alice")
			r.NotNil(f.svc.ensurePersonalOrg(ctx, user))

			hint, err := f.svc.ColleagueHint(ctx, user.UID)
			r.NoError(err)

			if tt.want == "" {
				r.Nil(hint)

				return
			}

			r.NotNil(hint)
			r.Equal(tt.want, hint.Org.Slug)
			r.Equal("acme.com", hint.Domain)
			r.Equal("alice", hint.PersonalOrgSlug)

			// Once asked, the banner has nothing more to offer.
			_, err = f.svc.CreateMembershipRequest(ctx, user.UID, MembershipRequestCreateRequest{OrgSlug: tt.want})
			r.NoError(err)

			hint, err = f.svc.ColleagueHint(ctx, user.UID)
			r.NoError(err)
			r.Nil(hint)
		})
	}
}

// backdateOrg moves an org's creation into the past.
func (f *personalOrgFixture) backdateOrg(ctx context.Context, t *testing.T, orgUID string, age time.Duration) {
	t.Helper()

	_, err := f.db.DB().NewUpdate().
		Table("organizations").
		Set("created_at = ?", time.Now().Add(-age)).
		Where("uid = ?", orgUID).
		Exec(ctx)
	require.NoError(t, err)
}

// cleanupWarnings counts the queued warning emails.
func (f *personalOrgFixture) cleanupWarnings(ctx context.Context, t *testing.T) int {
	t.Helper()

	jobs, err := f.db.ListJobs(ctx, nil, 0)
	require.NoError(t, err)

	count := 0

	for _, job := range jobs {
		if template, _ := job.Config["template"].(string); job.Type == string(jobdef.JobTypeEmail) &&
			template == "personal-org-cleanup-warning.html" {
			count++
		}
	}

	return count
}

// TestSweepAbandonedPersonalOrgs: warn at day 23, delete at day 30, and never
// touch an org that has a check, a second member, was made by hand, or is
// younger than 30 days.
func TestSweepAbandonedPersonalOrgs(t *testing.T) {
	t.Parallel()

	const day = 24 * time.Hour

	t.Run("warns at day 23 and deletes at day 30", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		f, ctx := newPersonalOrgFixture(t, config.DeploymentModeSaaS)
		user := f.passwordUser(ctx, t, "alice@acme.com", "Alice")
		org := f.svc.ensurePersonalOrg(ctx, user).Organization

		f.backdateOrg(ctx, t, org.UID, 22*day)
		res, err := f.svc.SweepAbandonedPersonalOrgs(ctx, time.Now())
		r.NoError(err)
		r.Equal(PersonalOrgSweepResult{}, res, "day 22: nothing yet")

		f.backdateOrg(ctx, t, org.UID, 23*day+time.Hour)
		res, err = f.svc.SweepAbandonedPersonalOrgs(ctx, time.Now())
		r.NoError(err)
		r.Equal(PersonalOrgSweepResult{Warned: 1}, res)
		r.Equal(1, f.cleanupWarnings(ctx, t), "the owner is emailed")

		res, err = f.svc.SweepAbandonedPersonalOrgs(ctx, time.Now().Add(day))
		r.NoError(err)
		r.Equal(PersonalOrgSweepResult{}, res, "warned once, and day 24 is younger than 30 days")
		r.Equal(1, f.cleanupWarnings(ctx, t))

		res, err = f.svc.SweepAbandonedPersonalOrgs(ctx, time.Now().Add(7*day))
		r.NoError(err)
		r.Equal(PersonalOrgSweepResult{Deleted: 1}, res, "day 30: deleted")

		_, err = f.db.GetOrganizationBySlug(ctx, org.Slug)
		r.Error(err, "the org is gone")

		members, err := f.db.ListMembersByUser(ctx, user.UID)
		r.NoError(err)
		r.Empty(members, "deleted through DeleteOrg: memberships go too")
	})

	t.Run("an org first seen past day 30 still gets seven days", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		f, ctx := newPersonalOrgFixture(t, config.DeploymentModeSaaS)
		user := f.passwordUser(ctx, t, "alice@acme.com", "Alice")
		org := f.svc.ensurePersonalOrg(ctx, user).Organization
		f.backdateOrg(ctx, t, org.UID, 40*day)

		res, err := f.svc.SweepAbandonedPersonalOrgs(ctx, time.Now())
		r.NoError(err)
		r.Equal(PersonalOrgSweepResult{Warned: 1}, res)

		res, err = f.svc.SweepAbandonedPersonalOrgs(ctx, time.Now().Add(6*day))
		r.NoError(err)
		r.Equal(PersonalOrgSweepResult{}, res)

		res, err = f.svc.SweepAbandonedPersonalOrgs(ctx, time.Now().Add(7*day+time.Hour))
		r.NoError(err)
		r.Equal(PersonalOrgSweepResult{Deleted: 1}, res)
	})

	keepers := []struct {
		name    string
		prepare func(ctx context.Context, t *testing.T, f *personalOrgFixture, org *models.Organization)
	}{
		{
			name: "one check",
			prepare: func(ctx context.Context, t *testing.T, f *personalOrgFixture, org *models.Organization) {
				t.Helper()
				require.NoError(t, f.db.CreateCheck(ctx, models.NewCheck(org.UID, "site", "http")))
			},
		},
		{
			name: "a second member",
			prepare: func(ctx context.Context, t *testing.T, f *personalOrgFixture, org *models.Organization) {
				t.Helper()
				bob := models.NewUser("bob@acme.com")
				require.NoError(t, f.db.CreateUser(ctx, bob))
				require.NoError(t, f.db.CreateOrganizationMember(
					ctx, models.NewOrganizationMember(org.UID, bob.UID, models.MemberRoleUser)))
			},
		},
		{
			name: "auto_personal=false",
			prepare: func(ctx context.Context, t *testing.T, f *personalOrgFixture, org *models.Organization) {
				t.Helper()
				_, err := f.db.DB().NewUpdate().Table("organizations").
					Set("auto_personal_owner_uid = NULL").Where("uid = ?", org.UID).Exec(ctx)
				require.NoError(t, err)
			},
		},
	}

	for _, keeper := range keepers {
		t.Run("keeps an org with "+keeper.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			f, ctx := newPersonalOrgFixture(t, config.DeploymentModeSaaS)
			user := f.passwordUser(ctx, t, "alice@acme.com", "Alice")
			org := f.svc.ensurePersonalOrg(ctx, user).Organization
			keeper.prepare(ctx, t, f, org)
			f.backdateOrg(ctx, t, org.UID, 60*day)

			for _, at := range []time.Time{time.Now(), time.Now().Add(30 * day)} {
				res, err := f.svc.SweepAbandonedPersonalOrgs(ctx, at)
				r.NoError(err)
				r.Equal(PersonalOrgSweepResult{}, res)
			}

			_, err := f.db.GetOrganizationBySlug(ctx, org.Slug)
			r.NoError(err, "the org is kept")
			r.Zero(f.cleanupWarnings(ctx, t), "and its owner never warned")
		})
	}
}

// TestNoOrgOutcomesAreCounted: the /no-org funnel is measured (spec
// 2026-10-30-04 step 7). Counters are process-wide and other tests run in
// parallel, so this checks increments, not totals.
func TestNoOrgOutcomesAreCounted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	count := func(outcome string) float64 {
		return testutil.ToFloat64(prommetrics.NoOrgOutcomes.WithLabelValues(outcome))
	}

	self, ctx := newPersonalOrgFixture(t, config.DeploymentModeSelfHosted)
	carol := self.passwordUser(ctx, t, "carol@acme.com", "Carol")

	landed := count(noOrgOutcomeLanded)
	_, err := self.svc.Login(ctx, "", carol.Email, personalOrgTestPassword, Context{})
	r.NoError(err)
	r.Greater(count(noOrgOutcomeLanded), landed, "landing on /no-org is counted")

	wanted, _ := self.seededOrg(ctx, t, "wanted", "boss@wanted.example")
	requested := count(noOrgOutcomeJoinRequested)
	_, err = self.svc.CreateMembershipRequest(ctx, carol.UID, MembershipRequestCreateRequest{OrgSlug: wanted.Slug})
	r.NoError(err)
	r.Greater(count(noOrgOutcomeJoinRequested), requested, "a join request from zero is counted")

	created := count(noOrgOutcomeCreated)
	_, err = self.svc.CreateOrg(ctx, carol.UID, CreateOrgRequest{Name: "Carol's shop"}, Context{})
	r.NoError(err)
	r.Greater(count(noOrgOutcomeCreated), created, "creating the first org by hand is counted")

	saas, ctx := newPersonalOrgFixture(t, config.DeploymentModeSaaS)
	dave := saas.passwordUser(ctx, t, "dave@acme.com", "Dave")

	autoCreated := count(noOrgOutcomeAutoCreated)
	_, err = saas.svc.Login(ctx, "", dave.Email, personalOrgTestPassword, Context{})
	r.NoError(err)
	r.Greater(count(noOrgOutcomeAutoCreated), autoCreated, "an auto-created personal org is counted")
}

// TestColleagueHintHandler: GET /api/v1/auth/colleague-hint answers the hint,
// or {"org": null} with nothing else when there is none.
func TestColleagueHintHandler(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f, ctx := newPersonalOrgFixture(t, config.DeploymentModeSaaS)
	f.seededOrg(ctx, t, "acme", "bob@acme.com")

	alice := f.passwordUser(ctx, t, "alice@acme.com", "Alice")
	r.NotNil(f.svc.ensurePersonalOrg(ctx, alice))

	loner := f.passwordUser(ctx, t, "loner@solo.example", "Loner")

	handler := NewHandler(f.svc, f.svc.fullCfg)

	call := func(userUID string) string {
		reqCtx := context.WithValue(ctx, base.ContextKeyClaims, &Claims{UserUID: userUID})
		req := httptest.NewRequestWithContext(reqCtx, http.MethodGet, "/api/v1/auth/colleague-hint", nil)
		rec := httptest.NewRecorder()
		r.NoError(handler.ColleagueHintHandler(rec, req))
		r.Equal(http.StatusOK, rec.Code)

		return strings.TrimSpace(rec.Body.String())
	}

	r.JSONEq(`{"org":{"slug":"acme","name":"Org acme"},"domain":"acme.com","personalOrgSlug":"alice"}`, call(alice.UID))
	r.JSONEq(`{"org":null}`, call(loner.UID))
}
