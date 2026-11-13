package auth

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestVerifyTelegramLoginRefusesMalformedSignedFields: a result that is
// correctly signed but whose id or date is not a number is still refused.
// The signature only proves who built the fields, not that they are usable.
func TestVerifyTelegramLoginRefusesMalformedSignedFields(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := map[string]func(fields map[string]string){
		"id that is not a number":   func(fields map[string]string) { fields[telegramFieldID] = "acme" },
		"id that is not positive":   func(fields map[string]string) { fields[telegramFieldID] = "0" },
		"date that is not a number": func(fields map[string]string) { fields[telegramFieldAuthDate] = "today" },
	}
	for name, spoil := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fields := telegramLoginFields(now)
			spoil(fields)

			identity, err := VerifyTelegramLogin(signTelegramFields(fields, testTelegramBotToken), testTelegramBotToken, now)
			require.ErrorIs(t, err, ErrTelegramAuthInvalid)
			require.Nil(t, identity)
		})
	}

	// Control: the same fields, untouched, verify.
	identity, err := VerifyTelegramLogin(
		signTelegramFields(telegramLoginFields(now), testTelegramBotToken), testTelegramBotToken, now)
	require.NoError(t, err)
	require.Equal(t, testTelegramUserID, identity.ID)
}

func TestDecodeTelegramAuthResultShapes(t *testing.T) {
	t.Parallel()

	encode := func(document string) string { return base64.URLEncoding.EncodeToString([]byte(document)) }

	fields, err := DecodeTelegramAuthResult(encode(`{"id":42,"is_premium":true,"username":"acme_member"}`))
	require.NoError(t, err)
	require.Equal(t, map[string]string{"id": "42", "is_premium": "true", "username": "acme_member"}, fields)

	for _, document := range []string{`null`, `[1,2]`, `"text"`, `{"id":`} {
		_, err := DecodeTelegramAuthResult(encode(document))
		require.ErrorIs(t, err, ErrTelegramAuthInvalid, "document %s", document)
	}
}

func TestTelegramLoginState(t *testing.T) { //nolint:tparallel // subtests share one service
	t.Parallel()

	f := newTelegramFixture(t, true)
	ctx := t.Context()

	runOAuthStateRefusals(t, oauthStateStore{
		"telegram", telegramOAuthStatePrefix, f.svc.db,
		func() (string, error) { return f.svc.GenerateOAuthState(ctx, "/d", f.org.Slug) },
		func(nonce string) (*OAuthState, error) { return f.svc.ValidateOAuthState(ctx, nonce) },
	})

	// Empty state.
	{
		_, err := f.svc.ValidateOAuthState(ctx, "")
		require.ErrorIs(t, err, ErrInvalidOAuthState)
	}
}

// telegramSignIn runs the service callback with a fresh signed result.
func telegramSignIn(t *testing.T, f *telegramFixture, orgSlug string) (*ProviderOutcome, error) {
	t.Helper()

	return f.svc.HandleCallback(t.Context(), freshTelegramResult(), orgSlug)
}

// TestTelegramCallbackResolvesTheLinkedAccount covers which account a
// verified Telegram identity signs in as, and when it signs in as nobody.
func TestTelegramCallbackResolvesTheLinkedAccount(t *testing.T) {
	t.Parallel()

	t.Run("an unknown organization on the login page is ignored", func(t *testing.T) {
		t.Parallel()

		f := newTelegramFixture(t, true)

		outcome, err := telegramSignIn(t, f, "no-such-org")
		require.NoError(t, err)
		require.Equal(t, f.org.Slug, outcome.OrgSlug, "the organization of the link is used")
		require.Equal(t, f.user.UID, outcome.UserUID)
	})

	t.Run("linked by two different users: nobody is signed in", func(t *testing.T) {
		t.Parallel()

		f := newTelegramFixture(t, true)

		other := models.NewUser("other@acme.com")
		require.NoError(t, f.svc.db.CreateUser(t.Context(), other))

		contact := models.NewUserContact(other.UID, f.org.UID, models.UserContactTypeTelegram,
			strconv.FormatInt(testTelegramUserID, 10), "tg")
		verifiedAt := time.Now()
		contact.VerifiedAt = &verifiedAt
		require.NoError(t, f.svc.db.UpsertUserContact(t.Context(), contact))

		outcome, err := telegramSignIn(t, f, "")
		require.ErrorIs(t, err, ErrTelegramAmbiguous)
		require.Nil(t, outcome)
	})

	t.Run("linked account deleted since", func(t *testing.T) {
		t.Parallel()

		f := newTelegramFixture(t, true)
		require.NoError(t, f.svc.db.DeleteUser(t.Context(), f.user.UID))

		_, err := telegramSignIn(t, f, "")
		require.ErrorIs(t, err, ErrTelegramNotLinked)
	})

	t.Run("organization of the link deleted since", func(t *testing.T) {
		t.Parallel()

		f := newTelegramFixture(t, true)
		require.NoError(t, f.svc.db.DeleteOrganization(t.Context(), f.org.UID))

		_, err := telegramSignIn(t, f, "")
		require.ErrorIs(t, err, ErrTelegramNotLinked)
	})

	t.Run("links cannot be listed", func(t *testing.T) {
		t.Parallel()

		f := newTelegramFixture(t, true)
		fault := installQueryFault(f.svc.db)

		var err error

		fault.during(t, failingSQL(`FROM "user_contacts"`), func() { _, err = telegramSignIn(t, f, "") })
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorContains(t, err, "failed to list telegram contacts")
		require.NotErrorIs(t, err, ErrTelegramNotLinked, "a store failure is not a missing link")
	})
}

// TestTelegramRoutesRefusals covers the answers of the Telegram login routes
// that are not a sign-in.
func TestTelegramRoutesRefusals(t *testing.T) {
	t.Parallel()

	newState := func(t *testing.T, f *telegramFixture) string {
		t.Helper()

		state, err := f.svc.GenerateOAuthState(t.Context(), "/d/orgs/"+f.org.Slug, f.org.Slug)
		require.NoError(t, err)

		return url.QueryEscape(state)
	}

	t.Run("login for an unknown organization", func(t *testing.T) {
		t.Parallel()

		f := newTelegramFixture(t, true)

		_, code, _ := errorRedirect(t, serveRoute(t, f.handler.Login, "/api/v1/auth/telegram/login?org=no-such-org"))
		require.Equal(t, "ORGANIZATION_NOT_FOUND", code)
	})

	t.Run("login when the state cannot be stored", func(t *testing.T) {
		t.Parallel()

		f := newTelegramFixture(t, true)
		fault := installQueryFault(f.svc.db)

		var rec *httptest.ResponseRecorder

		fault.during(t, failingSQL(`"state_entries"`), func() {
			rec = serveRoute(t, f.handler.Login, "/api/v1/auth/telegram/login?org="+f.org.Slug)
		})
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.Empty(t, rec.Header().Get("Location"))
	})

	t.Run("callback when the sign-in is switched off", func(t *testing.T) {
		t.Parallel()

		f := newTelegramFixture(t, true)
		state := newState(t, f)
		f.handler.cfg.Telegram.LoginEnabled = false

		_, code, description := errorRedirect(t,
			serveRoute(t, f.handler.Callback, telegramLoginCallbackPath+"?state="+state+"&tgAuthResult=abc"))
		require.Equal(t, OAuthCodeFailed, code)
		require.Contains(t, description, "not enabled")
	})

	t.Run("callback after the user canceled at Telegram", func(t *testing.T) {
		t.Parallel()

		f := newTelegramFixture(t, true)

		path, code, _ := errorRedirect(t,
			serveRoute(t, f.handler.Callback, telegramLoginCallbackPath+"?state="+newState(t, f)+"&tgAuthResult=false"))
		require.Equal(t, "access_denied", code)
		require.Equal(t, "/d/orgs/"+f.org.Slug, path, "back to the page the login started from")
	})

	t.Run("callback with a repeated field", func(t *testing.T) {
		t.Parallel()

		f := newTelegramFixture(t, true)

		_, code, _ := errorRedirect(t, serveRoute(t, f.handler.Callback,
			telegramLoginCallbackPath+"?state="+newState(t, f)+"&hash=abc&id=1&id=2"))
		require.Equal(t, OAuthCodeTelegramInvalid, code)
	})

	t.Run("callback for a Telegram account linked by two users", func(t *testing.T) {
		t.Parallel()

		f := newTelegramFixture(t, true)

		other := models.NewUser("other@acme.com")
		require.NoError(t, f.svc.db.CreateUser(t.Context(), other))

		// Linked in another organization, so the requested one does not settle it.
		elsewhere := models.NewOrganization("acme-elsewhere", "Elsewhere")
		require.NoError(t, f.svc.db.CreateOrganization(t.Context(), elsewhere))

		for _, link := range []struct{ user, org string }{{other.UID, elsewhere.UID}, {f.user.UID, elsewhere.UID}} {
			contact := models.NewUserContact(link.user, link.org, models.UserContactTypeTelegram,
				strconv.FormatInt(testTelegramUserID, 10), "tg")
			verifiedAt := time.Now()
			contact.VerifiedAt = &verifiedAt
			require.NoError(t, f.svc.db.UpsertUserContact(t.Context(), contact))
		}

		state, err := f.svc.GenerateOAuthState(t.Context(), "/d", "")
		require.NoError(t, err)

		_, code, _ := errorRedirect(t, f.callback(t, state, freshTelegramResult()))
		require.Equal(t, OAuthCodeTelegramAmbiguous, code)
	})

	t.Run("callback when the store fails", func(t *testing.T) {
		t.Parallel()

		f := newTelegramFixture(t, true)
		state, err := f.svc.GenerateOAuthState(t.Context(), "/d", f.org.Slug)
		require.NoError(t, err)

		fault := installQueryFault(f.svc.db)

		var rec *httptest.ResponseRecorder

		fault.during(t, failingSQL(`FROM "user_contacts"`), func() { rec = f.callback(t, state, freshTelegramResult()) })

		_, code, description := errorRedirect(t, rec)
		require.Equal(t, OAuthCodeFailed, code)
		require.NotContains(t, description, "user_contacts", "the cause stays in the logs")
	})
}
