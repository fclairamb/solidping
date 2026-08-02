package usernotifications

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// fakeWhatsAppSender records the codes it was asked to deliver. It is injected
// per Service instance (WithWhatsAppSender) rather than swapped into a package
// global, so parallel tests never race on it.
type fakeWhatsAppSender struct {
	mu    sync.Mutex
	sends []struct{ to, code string }
	err   error
}

func (f *fakeWhatsAppSender) SendVerificationCode(_ context.Context, to, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.err != nil {
		return f.err
	}

	f.sends = append(f.sends, struct{ to, code string }{to, code})

	return nil
}

func (f *fakeWhatsAppSender) last() (string, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.sends) == 0 {
		return "", "", false
	}

	s := f.sends[len(f.sends)-1]

	return s.to, s.code, true
}

func (f *fakeWhatsAppSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.sends)
}

type waEnv struct {
	svc     *Service
	org     *models.Organization
	user    *models.User
	contact *models.UserContact
	sender  *fakeWhatsAppSender
}

// setupWhatsAppEnv builds a service with an unverified WhatsApp contact and a
// per-service fake sender.
func setupWhatsAppEnv(t *testing.T, opts ...Option) *waEnv {
	t.Helper()

	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("wa-verify-org", "WA Verify Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("wa-user@example.com")
	require.NoError(t, dbSvc.CreateUser(ctx, user))

	contact := models.NewUserContact(
		user.UID, org.UID, models.UserContactTypeWhatsApp, "+15551234567", "whatsapp",
	)
	require.NoError(t, dbSvc.UpsertUserContact(ctx, contact))

	sender := &fakeWhatsAppSender{}
	svc := NewService(dbSvc, nil, append([]Option{WithWhatsAppSender(sender)}, opts...)...)

	return &waEnv{svc: svc, org: org, user: user, contact: contact, sender: sender}
}

func TestWhatsAppVerify_RoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	env := setupWhatsAppEnv(t)

	r.NoError(env.svc.VerifyContact(ctx, env.org.Slug, env.user, env.contact.UID))

	to, code, ok := env.sender.last()
	r.True(ok)
	r.Equal("+15551234567", to)
	r.Len(code, 6)

	// The stored hash is the hash of the code we actually sent — not the code.
	reloaded, err := env.svc.db.GetUserContact(ctx, env.contact.UID)
	r.NoError(err)
	r.NotNil(reloaded.VerifyCodeHash)
	r.Equal(hashCode(code), *reloaded.VerifyCodeHash)
	r.Nil(reloaded.VerifiedAt)

	// Wrong code first: rejected, and the contact stays unverified.
	r.ErrorIs(env.svc.ConfirmVerify(ctx, env.org.Slug, env.user, env.contact.UID, "000000"), ErrCodeMismatch)

	reloaded, err = env.svc.db.GetUserContact(ctx, env.contact.UID)
	r.NoError(err)
	r.Nil(reloaded.VerifiedAt)

	// Right code: verified.
	r.NoError(env.svc.ConfirmVerify(ctx, env.org.Slug, env.user, env.contact.UID, code))

	reloaded, err = env.svc.db.GetUserContact(ctx, env.contact.UID)
	r.NoError(err)
	r.NotNil(reloaded.VerifiedAt)

	// Idempotent: confirming again succeeds without a pending code.
	r.NoError(env.svc.ConfirmVerify(ctx, env.org.Slug, env.user, env.contact.UID, code))

	// And re-issuing on a verified contact is refused.
	r.ErrorIs(env.svc.VerifyContact(ctx, env.org.Slug, env.user, env.contact.UID), ErrAlreadyVerified)
}

// TestWhatsAppVerify_ResendLimiterApplies proves the WhatsApp transport reuses
// the shared resend limiter rather than its own.
func TestWhatsAppVerify_ResendLimiterApplies(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	env := setupWhatsAppEnv(t)

	for range verifyResendMax {
		r.NoError(env.svc.VerifyContact(ctx, env.org.Slug, env.user, env.contact.UID))
	}

	r.ErrorIs(env.svc.VerifyContact(ctx, env.org.Slug, env.user, env.contact.UID), ErrResendTooSoon)
	// The throttled attempt sent nothing.
	r.Equal(verifyResendMax, env.sender.count())
}

// TestWhatsAppVerify_NoProvider proves an instance without WhatsApp credentials
// refuses the flow *before* stamping a code we could never deliver.
func TestWhatsAppVerify_NoProvider(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupWhatsAppEnv(t)
	// Drop the injected sender and leave the config inactive.
	env.svc.whatsAppSender = nil
	env.svc.whatsAppCfg = config.WhatsAppConfig{Enabled: false}

	r.ErrorIs(
		env.svc.VerifyContact(ctx, env.org.Slug, env.user, env.contact.UID),
		ErrNoWhatsAppProvider,
	)

	reloaded, err := env.svc.db.GetUserContact(ctx, env.contact.UID)
	r.NoError(err)
	r.Nil(reloaded.VerifyCodeHash)
}

// TestWhatsAppVerify_SendFailurePropagates proves a transport failure surfaces
// rather than silently reporting success.
func TestWhatsAppVerify_SendFailurePropagates(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupWhatsAppEnv(t)
	env.sender.err = errors.New("graph exploded") //nolint:err113 // test-local sentinel

	r.Error(env.svc.VerifyContact(ctx, env.org.Slug, env.user, env.contact.UID))
}

// TestWhatsAppVerify_ExpiredCode proves the shared TTL applies.
func TestWhatsAppVerify_ExpiredCode(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	env := setupWhatsAppEnv(t)

	r.NoError(env.svc.VerifyContact(ctx, env.org.Slug, env.user, env.contact.UID))

	_, code, ok := env.sender.last()
	r.True(ok)

	// Jump past the TTL.
	env.svc.clock = func() time.Time { return time.Now().Add(verifyCodeTTL + time.Minute) }

	r.ErrorIs(
		env.svc.ConfirmVerify(ctx, env.org.Slug, env.user, env.contact.UID, code),
		ErrCodeExpired,
	)
}

// TestWhatsAppVerify_AttemptCap proves the shared attempt cap applies and burns
// the code.
func TestWhatsAppVerify_AttemptCap(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	env := setupWhatsAppEnv(t)

	r.NoError(env.svc.VerifyContact(ctx, env.org.Slug, env.user, env.contact.UID))

	_, code, ok := env.sender.last()
	r.True(ok)

	for range verifyMaxAttempts {
		r.ErrorIs(
			env.svc.ConfirmVerify(ctx, env.org.Slug, env.user, env.contact.UID, "999999"),
			ErrCodeMismatch,
		)
	}

	// The code is burnt — even the right one no longer works.
	r.ErrorIs(
		env.svc.ConfirmVerify(ctx, env.org.Slug, env.user, env.contact.UID, code),
		ErrNoPendingCode,
	)
}

// TestWhatsAppContactIsDistinctFromPhone proves the two contact types coexist
// for the same number and that verifying one does not verify the other. This is
// the Meta opt-in requirement: SMS consent is not WhatsApp consent.
func TestWhatsAppContactIsDistinctFromPhone(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	env := setupWhatsAppEnv(t)

	phone := models.NewUserContact(
		env.user.UID, env.org.UID, models.UserContactTypePhone, env.contact.Value, "mobile",
	)
	r.NoError(env.svc.db.UpsertUserContact(ctx, phone))
	r.NotEqual(phone.UID, env.contact.UID)

	r.NoError(env.svc.VerifyContact(ctx, env.org.Slug, env.user, env.contact.UID))

	_, code, ok := env.sender.last()
	r.True(ok)
	r.NoError(env.svc.ConfirmVerify(ctx, env.org.Slug, env.user, env.contact.UID, code))

	whatsAppContact, err := env.svc.db.GetUserContact(ctx, env.contact.UID)
	r.NoError(err)
	r.NotNil(whatsAppContact.VerifiedAt)

	phoneContact, err := env.svc.db.GetUserContact(ctx, phone.UID)
	r.NoError(err)
	r.Nil(phoneContact.VerifiedAt, "verifying WhatsApp must not verify the SMS contact")
}

// TestVerifyContact_RejectsNonVerifiableTypes keeps the gate closed for email /
// web push / Slack contacts.
func TestVerifyContact_RejectsNonVerifiableTypes(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	env := setupWhatsAppEnv(t)

	for _, contactType := range []string{
		models.UserContactTypeEmail, models.UserContactTypeWebPush, models.UserContactTypeSlackUser,
	} {
		c := models.NewUserContact(env.user.UID, env.org.UID, contactType, contactType+"-value", "")
		r.NoError(env.svc.db.UpsertUserContact(ctx, c))
		r.ErrorIs(env.svc.VerifyContact(ctx, env.org.Slug, env.user, c.UID), ErrNotAPhoneContact)
		r.ErrorIs(env.svc.ConfirmVerify(ctx, env.org.Slug, env.user, c.UID, "123456"), ErrNotAPhoneContact)
	}
}

// TestCreateContact_WhatsAppRequiresE164 proves malformed numbers are refused
// at creation, before anyone can try to verify them.
func TestCreateContact_WhatsAppRequiresE164(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	env := setupWhatsAppEnv(t)

	for _, bad := range []string{"0612345678", "+0612345678", "not a number", "12345"} {
		_, err := env.svc.CreateContact(ctx, env.org.Slug, env.user, CreateContactRequest{
			Type: models.UserContactTypeWhatsApp, Value: bad,
		})
		r.ErrorIs(err, ErrInvalidWhatsAppNumber, "expected %q to be rejected", bad)
	}

	route, err := env.svc.CreateContact(ctx, env.org.Slug, env.user, CreateContactRequest{
		Type: models.UserContactTypeWhatsApp, Value: "+33612345678",
	})
	r.NoError(err)
	r.Equal(models.UserContactTypeWhatsApp, route.Contact.Type)
	// Never auto-verified: WhatsApp reachability must be proven.
	r.Nil(route.Contact.VerifiedAt)
}

// TestWhatsAppTemplateSender_SendsAuthenticationTemplate exercises the real
// production sender against a fake Graph API, proving it posts the configured
// authentication template with the code in both the body and the copy-code
// button.
func TestWhatsAppTemplateSender_SendsAuthenticationTemplate(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var gotPath, gotBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotPath = req.URL.Path

		buf := make([]byte, req.ContentLength)
		_, _ = req.Body.Read(buf)
		gotBody = string(buf)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.OTP"}]}`))
	}))
	t.Cleanup(srv.Close)

	sender := NewWhatsAppTemplateSenderWithBaseURL(config.WhatsAppConfig{
		Enabled:          true,
		AccessToken:      "tok",
		PhoneNumberID:    "PNID",
		APIVersion:       "v23.0",
		VerifyTemplate:   "solidping_verify",
		TemplateLanguage: "fr",
	}, srv.URL)

	r.NoError(sender.SendVerificationCode(context.Background(), "+33612345678", "424242"))
	r.Equal("/v23.0/PNID/messages", gotPath)
	r.Contains(gotBody, `"name":"solidping_verify"`)
	r.Contains(gotBody, `"code":"fr"`)
	r.Contains(gotBody, `"424242"`)
	r.Contains(gotBody, `"sub_type":"url"`)
}

// TestWhatsAppTemplateSender_InactiveConfig proves the production sender fails
// closed rather than calling Graph without credentials.
func TestWhatsAppTemplateSender_InactiveConfig(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	sender := NewWhatsAppTemplateSender(config.WhatsAppConfig{Enabled: true})
	r.ErrorIs(sender.SendVerificationCode(context.Background(), "+33612345678", "1"), ErrNoWhatsAppProvider)
}
