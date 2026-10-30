package auth

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/defaults"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// Spec 2026-10-30-04: a user who signs in belonging to no organization gets
// an invitation's org, a pattern's org, waits for their request, or gets a
// personal org, through every no-org entry point.

const personalOrgTestPassword = "supersecret123"

// personalOrgFixture is a SaaS instance with a real job queue, so registration emails are queued.
type personalOrgFixture struct {
	*loginAuditFixture
}

func newPersonalOrgFixture(t *testing.T) (*personalOrgFixture, context.Context) {
	t.Helper()

	f, ctx := newLoginAuditFixture(t)
	// The default-org provider flow (join_policy.go) is a SaaS behavior.
	f.svc.fullCfg.Deployment.Mode = config.DeploymentModeSaaS
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
	return []personalOrgCase{
		{
			name: "pending invitation joins the inviting org",
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
			name: "email pattern joins the matching org",
			prepare: func(ctx context.Context, t *testing.T, f *personalOrgFixture, _ string, _ *models.User) {
				t.Helper()

				org, _ := f.seededOrg(ctx, t, "patterned", "boss@patterned.example")
				require.NoError(t, f.db.SetOrgParameter(
					ctx, org.UID, "registration.email_pattern", `@acme\.com$`, false))
			},
			wantOrgSlug: "patterned", wantRole: models.MemberRoleUser,
		},
		{
			name: "pending membership request keeps the user waiting",
			prepare: func(ctx context.Context, t *testing.T, f *personalOrgFixture, _ string, user *models.User) {
				t.Helper()

				org, _ := f.seededOrg(ctx, t, "wanted", "boss@wanted.example")
				require.NoError(t, f.db.CreateMembershipRequest(ctx, models.NewMembershipRequest(org.UID, user.UID, nil)))
			},
			skipRegistration: true,
		},
		{
			name:        "nothing else creates a personal org",
			wantOrgSlug: "alice", wantPersonal: true, wantRole: models.MemberRoleOwner,
		},
		{
			name: "slug collision gets a suffix",
			prepare: func(ctx context.Context, t *testing.T, f *personalOrgFixture, _ string, _ *models.User) {
				t.Helper()

				f.seededOrg(ctx, t, "alice", "other-alice@elsewhere.example")
			},
			wantOrgSlug: "alice2", wantPersonal: true, wantRole: models.MemberRoleOwner,
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
				f, ctx := newPersonalOrgFixture(t)

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
	f, ctx := newPersonalOrgFixture(t)
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
// does the same.
func TestPersonalOrgNotRecreatedAfterDeletion(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f, ctx := newPersonalOrgFixture(t)
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

	// Same for a hand-made last org: deleting it lands on /no-org.
	bob := f.passwordUser(ctx, t, "bob@acme.com", "Bob")
	made, err := f.svc.CreateOrg(ctx, bob.UID, CreateOrgRequest{Name: "Bob's shop"}, Context{})
	r.NoError(err)

	afterDelete, err := f.svc.DeleteOrg(ctx, made.Slug, bob.UID, DeleteOrgRequest{Slug: made.Slug}, Context{})
	r.NoError(err)
	r.Equal(LoginActionNoOrg, afterDelete.LoginAction)
	r.Nil(afterDelete.Organization)
}

// TestPersonalOrgSkipsSuperAdminsAndDemoUsers: neither may create an org by
// hand, so none is created for them.
func TestPersonalOrgSkipsSuperAdminsAndDemoUsers(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f, ctx := newPersonalOrgFixture(t)

	admin := f.passwordUser(ctx, t, "root@acme.com", "Root")
	admin.SuperAdmin = true
	r.Nil(f.svc.ensurePersonalOrg(ctx, admin))

	demo := f.passwordUser(ctx, t, "demo@acme.com", "Demo")
	demo.Demo = true
	r.Nil(f.svc.ensurePersonalOrg(ctx, demo))
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
