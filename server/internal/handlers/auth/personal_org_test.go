package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

const personalOrgPassword = "supersecret123"

// personalOrgScenario is what already exists for the user before they sign in.
type personalOrgScenario struct {
	name string
	// invited seeds a pending invitation to another org for the email.
	invited bool
	// pattern seeds a registration email pattern matching the email.
	pattern bool
	// request seeds a pending membership request of the user.
	request bool
	// wantPersonal is whether exactly one personal org must be created.
	wantPersonal bool
}

func personalOrgScenarios() []personalOrgScenario {
	return []personalOrgScenario{
		{name: "pending invite", invited: true},
		{name: "pattern match", pattern: true},
		{name: "pending membership request", request: true},
		{name: "nothing else applies", wantPersonal: true},
	}
}

// personalOrgOutcome is what a sign-in flow handed back.
type personalOrgOutcome struct {
	orgSlug string
	// action is only set by flows that report one.
	action LoginAction
}

type personalOrgFlow struct {
	name string
	// canHaveRequest is false for flows where the user does not exist before
	// the flow runs, so no membership request can predate it.
	canHaveRequest bool
	// run signs the user in and returns the outcome.
	run func(t *testing.T, f *loginAuditFixture, ctx context.Context, email string) personalOrgOutcome
}

func personalOrgFlows() []personalOrgFlow {
	return []personalOrgFlow{
		{
			name: "password login", canHaveRequest: true,
			run: func(t *testing.T, f *loginAuditFixture, ctx context.Context, email string) personalOrgOutcome {
				t.Helper()

				resp, err := f.svc.Login(ctx, "", email, personalOrgPassword, Context{})
				require.NoError(t, err)

				out := personalOrgOutcome{action: resp.LoginAction}
				if resp.Organization != nil {
					out.orgSlug = resp.Organization.Slug
				}

				return out
			},
		},
		{
			name: "registration confirm",
			run: func(t *testing.T, f *loginAuditFixture, ctx context.Context, email string) personalOrgOutcome {
				t.Helper()

				resp, err := f.svc.ConfirmRegistration(ctx, extractRegistrationConfirmToken(t, ctx, f.db, email))
				require.NoError(t, err)

				out := personalOrgOutcome{action: resp.LoginAction}
				if resp.Organization != nil {
					out.orgSlug = resp.Organization.Slug
				}

				return out
			},
		},
		{
			name: "provider login", canHaveRequest: true,
			run: func(t *testing.T, f *loginAuditFixture, ctx context.Context, email string) personalOrgOutcome {
				t.Helper()

				// The demo org never admits anybody nor files a request, so
				// the sign-in ends org-less unless something else gives an org.
				demo, err := f.db.GetOrganizationBySlug(ctx, "demo")
				require.NoError(t, err)

				user, err := f.db.GetUserByEmail(ctx, email)
				require.NoError(t, err)

				res, err := f.svc.CompleteOrgLogin(ctx, demo, user, WithLoginMethod(signupMethodGoogle))
				require.NoError(t, err)
				require.True(t, res.Pending)

				return personalOrgOutcome{orgSlug: res.FallbackOrgSlug}
			},
		},
	}
}

func seedPersonalOrgScenario(
	ctx context.Context, t *testing.T, f *loginAuditFixture, flow personalOrgFlow,
	scenario personalOrgScenario, email string,
) {
	t.Helper()

	f.svc.fullCfg.Auth.RegistrationEmailPattern = ".*"

	if scenario.invited {
		ttl := time.Hour
		key := inviteKeyPrefix + "tok-personal"

		require.NoError(t, f.db.SetStateEntry(ctx, &f.org.UID, key, &models.JSONMap{
			keyEmail: email, "role": string(models.MemberRoleUser),
		}, &ttl))
		require.NoError(t, f.db.SetStateEntry(ctx, nil, key, &models.JSONMap{
			keyInviteOrgUID: f.org.UID,
		}, &ttl))
	}

	if scenario.pattern {
		require.NoError(t, f.db.SetOrgParameter(ctx, f.org.UID, "registration.email_pattern", ".*@acme\\.com", false))
	}

	if flow.name == "provider login" {
		joinTestOrg(ctx, t, f.db, "demo", true)
	}

	if flow.name == "registration confirm" {
		_, err := f.svc.Register(ctx, RegisterRequest{Name: "Jane Doe", Email: email, Password: personalOrgPassword})
		require.NoError(t, err)

		return
	}

	hash, err := passwords.Hash(personalOrgPassword)
	require.NoError(t, err)

	user := models.NewUser(email)
	user.Name = "Jane Doe"
	user.PasswordHash = &hash
	require.NoError(t, f.db.CreateUser(ctx, user))

	if scenario.request {
		require.NoError(t, f.db.CreateMembershipRequest(ctx, models.NewMembershipRequest(f.org.UID, user.UID, nil)))
	}
}

func TestAutoPersonalOrg(t *testing.T) {
	t.Parallel()

	for _, flow := range personalOrgFlows() {
		for _, scenario := range personalOrgScenarios() {
			if scenario.request && !flow.canHaveRequest {
				continue
			}

			t.Run(flow.name+" / "+scenario.name, func(t *testing.T) {
				t.Parallel()
				r := require.New(t)

				f, ctx := newLoginAuditFixture(t)
				email := "jane@acme.com"

				seedPersonalOrgScenario(ctx, t, f, flow, scenario, email)

				before, err := f.db.ListOrganizations(ctx)
				r.NoError(err)

				out := flow.run(t, f, ctx, email)

				user, err := f.db.GetUserByEmail(ctx, email)
				r.NoError(err)

				members, err := f.db.ListMembersByUser(ctx, user.UID)
				r.NoError(err)

				after, err := f.db.ListOrganizations(ctx)
				r.NoError(err)

				if !scenario.wantPersonal {
					created := len(after) - len(before)
					r.Zero(created, "no personal org when something else may admit the user")

					return
				}

				r.Len(after, len(before)+1, "exactly one personal org is created")
				r.Len(members, 1)
				r.Equal(models.MemberRoleOwner, members[0].Role)
				r.Equal(out.orgSlug, members[0].Organization.Slug)
				r.Equal("jane", out.orgSlug)
				r.Equal("Jane's organization", members[0].Organization.Name)

				if out.action != "" {
					r.Equal(LoginActionDefault, out.action)
				}
			})
		}
	}
}

func TestAutoPersonalOrgOnlyOnce(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f, ctx := newLoginAuditFixture(t)
	flow := personalOrgFlows()[0]
	email := "jane@acme.com"

	seedPersonalOrgScenario(ctx, t, f, flow, personalOrgScenario{wantPersonal: true}, email)

	first := flow.run(t, f, ctx, email)
	second := flow.run(t, f, ctx, email)

	r.Equal(first.orgSlug, second.orgSlug)

	orgs, err := f.db.ListOrganizations(ctx)
	r.NoError(err)
	r.Len(orgs, 2, "the fixture org plus a single personal org")
}

func TestAutoPersonalOrgSlugCollisionGetsSuffix(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f, ctx := newLoginAuditFixture(t)
	r.NoError(f.db.CreateOrganization(ctx, models.NewOrganization("jane", "Taken")))

	flow := personalOrgFlows()[0]
	email := "jane@acme.com"

	seedPersonalOrgScenario(ctx, t, f, flow, personalOrgScenario{wantPersonal: true}, email)

	out := flow.run(t, f, ctx, email)
	r.Equal("jane2", out.orgSlug)
}

func TestAutoPersonalOrgLocalizedName(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f, ctx := newLoginAuditFixture(t)

	hash, err := passwords.Hash(personalOrgPassword)
	r.NoError(err)

	user := models.NewUser("marie@acme.com")
	user.Name = "Marie Curie"
	user.PasswordHash = &hash
	user.Preferences = &models.UserPreferences{Locale: "fr"}
	r.NoError(f.db.CreateUser(ctx, user))

	resp, err := f.svc.Login(ctx, "", user.Email, personalOrgPassword, Context{})
	r.NoError(err)
	r.Equal("L'organisation de Marie", resp.Organization.Name)
}
