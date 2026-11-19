package usernotifications

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

// Spec 2026-11-19-02: an impersonation session or a PAT must not add, enable
// or confirm a paging contact for the account, a contact for someone else's
// email address is confirmed by an emailed code before it is paged, and the
// owner is told when a contact is added.

// setupMailerEnv is setupContactEnv with the real auth service as the contact
// mailer, over a job queue the emails can be read back from.
func setupMailerEnv(t *testing.T) (context.Context, *contactEnv) {
	t.Helper()

	ctx, env := setupContactEnv(t)

	cfg := &config.Config{}
	cfg.Server.BaseURL = "https://solidping.example"
	jobs := jobsvc.NewService(env.db.DB(), env.db, notifier.NewLocalEventNotifier(), nil)
	authSvc := auth.NewService(env.db, cfg.Auth, cfg, jobs, nil)

	env.svc = NewService(env.db, nil, WithContactMailer(authSvc))

	return ctx, env
}

// emailJobs returns the queued email jobs of one template.
func emailJobs(t *testing.T, env *contactEnv, template string) []*models.Job {
	t.Helper()

	jobs, err := env.db.ListJobs(t.Context(), nil, 0)
	require.NoError(t, err)

	var out []*models.Job

	for _, job := range jobs {
		if job.Type == string(jobdef.JobTypeEmail) && job.Config["template"] == template {
			out = append(out, job)
		}
	}

	return out
}

func templateData(t *testing.T, job *models.Job) map[string]any {
	t.Helper()

	data, ok := job.Config["templateData"].(map[string]any)
	require.True(t, ok, "template data present: %v", job.Config)

	return data
}

// seedPhoneContact stores an unverified phone contact with its route.
func seedPhoneContact(t *testing.T, env *contactEnv) (*models.UserContact, string) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	contact := models.NewUserContact(env.user.UID, env.org.UID, models.UserContactTypePhone, "+15551230000", "")
	r.NoError(env.db.UpsertUserContact(ctx, contact))
	r.NoError(env.db.EnsureUserNotificationRoute(ctx, env.user.UID, env.org.UID, contact.UID))

	routes, err := env.db.ListUserContactsWithRoutes(ctx, env.user.UID, env.org.UID)
	r.NoError(err)
	r.Len(routes, 1)

	return contact, routes[0].UID
}

// TestChannelBindingRefusedForImpersonationAndPAT is the service wall behind
// the route denylists: CreateContact, PatchRoute, VerifyContact and
// ConfirmVerify refuse an impersonated request and a PAT request, and nothing
// is written. The same calls from the member's own session go through.
func TestChannelBindingRefusedForImpersonationAndPAT(t *testing.T) {
	t.Parallel()

	credentials := []struct {
		name string
		wrap func(context.Context) context.Context
		want error
	}{
		{
			"impersonation",
			func(ctx context.Context) context.Context { return audit.WithImpersonator(ctx, "admin-uid") },
			ErrImpersonationForbidden,
		},
		{"PAT", auth.WithPATCredential, auth.ErrPATForbidden},
	}

	for _, cred := range credentials {
		t.Run(cred.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx, env := setupContactEnv(t)
			guarded := cred.wrap(ctx)

			_, err := env.svc.CreateContact(guarded, env.org.Slug, env.user, CreateContactRequest{
				Type: models.UserContactTypeNtfy, Value: "mallory-topic",
			})
			r.ErrorIs(err, cred.want)

			routes, err := env.db.ListUserContactsWithRoutes(ctx, env.user.UID, env.org.UID)
			r.NoError(err)
			r.Empty(routes, "a refused create leaves no contact behind")

			contact, routeUID := seedPhoneContact(t, env)
			r.NoError(env.db.SetRouteEnabled(ctx, routeUID, false))

			enable := true
			_, err = env.svc.PatchRoute(guarded, env.org.Slug, env.user, routeUID, PatchRouteRequest{Enabled: &enable})
			r.ErrorIs(err, cred.want)

			routes, err = env.db.ListUserContactsWithRoutes(ctx, env.user.UID, env.org.UID)
			r.NoError(err)
			r.False(routes[0].Enabled, "a refused patch leaves the route disabled")

			r.ErrorIs(env.svc.VerifyContact(guarded, env.org.Slug, env.user, contact.UID), cred.want)
			r.ErrorIs(env.svc.ConfirmVerify(guarded, env.org.Slug, env.user, contact.UID, "123456"), cred.want)

			// Removing a channel never redirects a page: still allowed.
			r.NoError(env.svc.DeleteContact(guarded, env.org.Slug, env.user, contact.UID))

			// Positive controls: the member's own session.
			created, err := env.svc.CreateContact(ctx, env.org.Slug, env.user, CreateContactRequest{
				Type: models.UserContactTypeNtfy, Value: "alice-topic",
			})
			r.NoError(err)

			disable := false
			patched, err := env.svc.PatchRoute(ctx, env.org.Slug, env.user, created.UID, PatchRouteRequest{Enabled: &disable})
			r.NoError(err)
			r.False(patched.Enabled)
		})
	}
}

// TestPatchRouteRefusesAnotherUsersRoute: SetRouteEnabled matches on the route
// uid alone, so PatchRoute checks the route is the caller's before flipping it.
func TestPatchRouteRefusesAnotherUsersRoute(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, env := setupContactEnv(t)
	_, routeUID := seedPhoneContact(t, env)
	r.NoError(env.db.SetRouteEnabled(ctx, routeUID, false))

	bob := models.NewUser("bob@acme.com")
	r.NoError(env.db.CreateUser(ctx, bob))
	r.NoError(env.db.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(env.org.UID, bob.UID, models.MemberRoleUser)))

	enable := true
	_, err := env.svc.PatchRoute(ctx, env.org.Slug, bob, routeUID, PatchRouteRequest{Enabled: &enable})
	r.ErrorIs(err, ErrRouteNotFound)

	routes, err := env.db.ListUserContactsWithRoutes(ctx, env.user.UID, env.org.UID)
	r.NoError(err)
	r.False(routes[0].Enabled, "another member cannot re-enable alice's route")
}

// TestEmailContactForAnotherAddressStartsUnverified: an address other than
// the member's verified sign-in email is unverified until its emailed code is
// confirmed, cannot be tested meanwhile, and is then usable.
func TestEmailContactForAnotherAddressStartsUnverified(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, env := setupMailerEnv(t)

	created, err := env.svc.CreateContact(ctx, env.org.Slug, env.user, CreateContactRequest{
		Type: models.UserContactTypeEmail, Value: "stranger@example.com",
	})
	r.NoError(err)
	r.Nil(created.Contact.VerifiedAt, "someone else's address is not verified on creation")

	err = env.svc.SendTestNotification(ctx, env.org.Slug, env.user, created.UID,
		NewEmailSenderAdapter(nil, nil), nil, webpush.Options{})
	r.ErrorIs(err, ErrContactNotVerified, "no test mail to an unconfirmed address")

	r.NoError(env.svc.VerifyContact(ctx, env.org.Slug, env.user, created.Contact.UID))

	codes := emailJobs(t, env, email.TemplateContactVerifyCode)
	r.Len(codes, 1)
	r.Equal([]any{"stranger@example.com"}, codes[0].Config["to"], "the code goes to the address itself")

	data := templateData(t, codes[0])
	r.Equal(env.user.Email, data["AccountEmail"], "the mail names the requesting account")

	code, _ := data["Code"].(string)
	r.Len(code, 6)

	r.ErrorIs(env.svc.ConfirmVerify(ctx, env.org.Slug, env.user, created.Contact.UID, wrongCode(code)), ErrCodeMismatch)
	r.NoError(env.svc.ConfirmVerify(ctx, env.org.Slug, env.user, created.Contact.UID, code))

	stored, err := env.db.GetUserContact(ctx, created.Contact.UID)
	r.NoError(err)
	r.NotNil(stored.VerifiedAt, "a confirmed code verifies the contact")
}

func wrongCode(code string) string {
	if code == "000000" {
		return "111111"
	}

	return "000000"
}

// TestEmailContactForOwnVerifiedEmailIsUsableAtOnce is the positive control:
// the member's own verified sign-in email needs no round trip. An unverified
// sign-in email proves nothing and goes through it.
func TestEmailContactForOwnVerifiedEmailIsUsableAtOnce(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, env := setupContactEnv(t)

	unverified, err := env.svc.CreateContact(ctx, env.org.Slug, env.user, CreateContactRequest{
		Type: models.UserContactTypeEmail, Value: env.user.Email,
	})
	r.NoError(err)
	r.Nil(unverified.Contact.VerifiedAt, "an unverified sign-in email is not proof of ownership")
	r.NoError(env.svc.DeleteContact(ctx, env.org.Slug, env.user, unverified.Contact.UID))

	ctx, env = setupContactEnv(t)
	now := time.Now()
	env.user.EmailVerifiedAt = &now

	own, err := env.svc.CreateContact(ctx, env.org.Slug, env.user, CreateContactRequest{
		Type: models.UserContactTypeEmail, Value: "  ALICE@acme.com ",
	})
	r.NoError(err)
	r.NotNil(own.Contact.VerifiedAt, "the verified sign-in email is usable at once")
}

// TestCreateContactRejectsAnInvalidEmail: a code could never reach it.
func TestCreateContactRejectsAnInvalidEmail(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, env := setupContactEnv(t)

	_, err := env.svc.CreateContact(ctx, env.org.Slug, env.user, CreateContactRequest{
		Type: models.UserContactTypeEmail, Value: "not-an-address",
	})
	r.ErrorIs(err, ErrInvalidEmailAddress)
}

// TestCreateContactNotifiesTheOwner: adding a contact emails the account
// owner once; re-posting the same contact adds nothing and sends nothing.
func TestCreateContactNotifiesTheOwner(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, env := setupMailerEnv(t)

	req := CreateContactRequest{Type: models.UserContactTypeNtfy, Value: "alice-topic"}
	_, err := env.svc.CreateContact(ctx, env.org.Slug, env.user, req)
	r.NoError(err)
	_, err = env.svc.CreateContact(ctx, env.org.Slug, env.user, req)
	r.NoError(err)

	notices := emailJobs(t, env, email.TemplateContactAdded)
	r.Len(notices, 1, "one notice for one new contact")
	r.Equal([]any{env.user.Email}, notices[0].Config["to"])

	data := templateData(t, notices[0])
	r.Equal("alice-topic", data["ContactValue"])
	r.Equal("ACME", data["OrgName"])
	r.Equal("https://solidping.example/d/orgs/"+env.org.Slug+"/account/notifications", data["NotificationsURL"])

	_, err = env.svc.CreateContact(ctx, env.org.Slug, env.user, CreateContactRequest{
		Type:  models.UserContactTypeWebPush,
		Value: `{"endpoint":"https://push.example/x","keys":{"p256dh":"a","auth":"b"}}`,
		Label: "Firefox on Linux",
	})
	r.NoError(err)

	notices = emailJobs(t, env, email.TemplateContactAdded)
	r.Len(notices, 2)

	labels := []any{templateData(t, notices[0])["ContactValue"], templateData(t, notices[1])["ContactValue"]}
	r.Contains(labels, "Firefox on Linux", "a web push contact is named by its label, not its JSON")
}
