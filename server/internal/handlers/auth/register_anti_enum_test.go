package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// TestRegisterStoresHashedTokenNotPlaintext is the regression test for spec
// 2026-09-25-30 part 1: the pending registration entry must store
// sha256hex(token), never the plaintext confirmation token, and
// ConfirmRegistration must still resolve the real token by hashing and
// comparing. A DB leak during the 3-day TTL window must not also hand out a
// usable confirmation token alongside the pending password hash.
func TestRegisterStoresHashedTokenNotPlaintext(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fullCfg := &config.Config{
		Server: config.ServerConfig{BaseURL: "https://example.com"},
		Auth: config.AuthConfig{
			JWTSecret:                "test-jwt-secret",
			AccessTokenExpiry:        time.Hour,
			RefreshTokenExpiry:       7 * 24 * time.Hour,
			RegistrationEmailPattern: ".*",
		},
	}
	svc, dbSvc, ctx := setupAuthTestServiceWithJobs(t, fullCfg)

	const email = "hash-check@example.com"

	_, err := svc.Register(ctx, RegisterRequest{Name: "Hash Check", Email: email, Password: "supersecret123"})
	r.NoError(err)

	entries, err := dbSvc.ListStateEntries(ctx, nil, registrationKeyPrefix)
	r.NoError(err)
	r.Len(entries, 1, "exactly one pending entry for this registration")

	val := *entries[0].Value
	r.NotContains(val, "token", "the plaintext token key must never be stored")
	r.NotContains(val, keyToken, "the plaintext token key must never be stored")

	storedHash, ok := val[keyTokenHash].(string)
	r.True(ok, "tokenHash must decode as a string, got %T", val[keyTokenHash])
	r.Len(storedHash, 64, "sha256 hex digest is 64 chars")

	// The plaintext token is recoverable only from the email a real caller
	// would receive — never from the database.
	token := extractRegistrationConfirmToken(t, ctx, dbSvc, email)
	r.NotEmpty(token, "precondition: a confirmation email must have been queued")
	r.Equal(hashPendingToken(token), storedHash, "stored hash must be sha256(token)")

	// Confirming with the real token succeeds and creates the account.
	resp, err := svc.ConfirmRegistration(ctx, token)
	r.NoError(err)
	r.NotNil(resp)

	user, err := dbSvc.GetUserByEmail(ctx, email)
	r.NoError(err)
	r.NotNil(user)

	// The entry is single-use: a second confirmation with the same token
	// gets the same generic "expired/invalid" error as any other unknown
	// token — not a distinct "already confirmed" signal.
	_, err = svc.ConfirmRegistration(ctx, token)
	r.ErrorIs(err, ErrRegistrationExpired)
}

// TestConfirmRegistrationUnknownAndWrongTokenSameGenericError proves a token
// that was never issued and a token that simply doesn't match any pending
// entry are indistinguishable — both resolve to ErrRegistrationExpired, with
// nothing that would let a caller learn whether ANY registration is pending
// for a given guess.
func TestConfirmRegistrationUnknownAndWrongTokenSameGenericError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://example.com")

	_, errNeverIssued := svc.ConfirmRegistration(ctx, "a-token-that-was-never-issued")
	r.ErrorIs(errNeverIssued, ErrRegistrationExpired)

	// A real pending entry exists, but we present the wrong token for it.
	svc.fullCfg.Auth.RegistrationEmailPattern = ".*"
	_, err := svc.Register(ctx, RegisterRequest{
		Name: "Wrong Token", Email: "wrong-token@example.com", Password: "supersecret123",
	})
	r.NoError(err)

	entries, err := dbSvc.ListStateEntries(ctx, nil, registrationKeyPrefix)
	r.NoError(err)
	r.Len(entries, 1)

	_, errWrongToken := svc.ConfirmRegistration(ctx, "definitely-not-the-real-token")
	r.ErrorIs(errWrongToken, ErrRegistrationExpired)
	r.Equal(errNeverIssued, errWrongToken, "an unknown token and a wrong token for a real entry must be indistinguishable")
}

// TestConfirmRegistrationExpiredEntrySameGenericError proves an entry whose
// TTL has already elapsed behaves exactly like an unknown token — the state
// store's own expiry filtering (expires_at > now()) already drops it from
// every list/get, so ConfirmRegistration never even sees it.
func TestConfirmRegistrationExpiredEntrySameGenericError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://example.com")

	const knownToken = "expired-registration-token" //nolint:gosec // test fixture value, not a credential
	hash, err := passwords.Hash("supersecret123")
	r.NoError(err)

	stateValue := &models.JSONMap{
		keyTokenHash:   hashPendingToken(knownToken),
		keyEmail:       "expired-reg@example.com",
		keyName:        "Expired Reg",
		"passwordHash": hash,
	}
	alreadyExpired := -time.Minute
	r.NoError(dbSvc.SetStateEntry(ctx, nil,
		registrationKeyPrefix+"expired-reg@example.com", stateValue, &alreadyExpired))

	_, err = svc.ConfirmRegistration(ctx, knownToken)
	r.ErrorIs(err, ErrRegistrationExpired)
}

// TestRegisterTakenEmailBehavesLikeSuccess is the service-level regression
// test for spec 2026-09-25-30 part 2 (anti-enumeration): registering with an
// email that already has an account must produce the exact same success
// response as a brand-new signup, with none of a real signup's side effects.
func TestRegisterTakenEmailBehavesLikeSuccess(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fullCfg := &config.Config{
		Server: config.ServerConfig{BaseURL: "https://example.com"},
		Auth: config.AuthConfig{
			JWTSecret:                "test-jwt-secret",
			AccessTokenExpiry:        time.Hour,
			RefreshTokenExpiry:       7 * 24 * time.Hour,
			RegistrationEmailPattern: ".*",
		},
	}
	svc, dbSvc, ctx := setupAuthTestServiceWithJobs(t, fullCfg)

	const existingEmail = "already-registered@example.com"

	hash, err := passwords.Hash("originalpass1")
	r.NoError(err)
	existingUser := models.NewUser(existingEmail)
	existingUser.PasswordHash = &hash
	r.NoError(dbSvc.CreateUser(ctx, existingUser))

	resp, err := svc.Register(ctx, RegisterRequest{
		Name: "Impersonator", Email: existingEmail, Password: "supersecret123",
	})
	r.NoError(err)
	r.Equal(registerSuccessResponse, resp, "a taken email must get the exact success response, not an error")

	entries, err := dbSvc.ListStateEntries(ctx, nil, registrationKeyPrefix)
	r.NoError(err)
	r.Empty(entries, "no pending registration entry must be created for a taken email")

	r.Empty(extractRegistrationConfirmToken(t, ctx, dbSvc, existingEmail),
		"no confirmation email must be sent for a taken email")

	// The original account and its password are untouched.
	unchanged, err := dbSvc.GetUserByEmail(ctx, existingEmail)
	r.NoError(err)
	r.True(passwords.Verify("originalpass1", *unchanged.PasswordHash))
}

// TestRegisterHandlerTakenEmailIsByteIdenticalToNewSignup drives the real
// HTTP handler (not just the service) for both a brand-new email and one
// that's already registered, and asserts the two responses are
// byte-identical: same status, same body, same relevant headers. An API
// consumer — or an attacker probing for registered emails — must not be able
// to tell the two cases apart from the wire response.
func TestRegisterHandlerTakenEmailIsByteIdenticalToNewSignup(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://example.com")
	svc.fullCfg.Auth.RegistrationEmailPattern = ".*"
	handler := NewHandler(svc, &config.Config{})

	const existingEmail = "handler-taken@example.com"

	hash, err := passwords.Hash("originalpass1")
	r.NoError(err)
	existingUser := models.NewUser(existingEmail)
	existingUser.PasswordHash = &hash
	r.NoError(dbSvc.CreateUser(ctx, existingUser))

	doRegister := func(email string) *httptest.ResponseRecorder {
		t.Helper()

		body := fmt.Sprintf(`{"name":"New User","email":%q,"password":"supersecret123"}`, email)
		httpReq := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/auth/register", strings.NewReader(body))
		rec := httptest.NewRecorder()
		r.NoError(handler.Register(rec, httpReq))

		return rec
	}

	takenRec := doRegister(existingEmail)
	newRec := doRegister("handler-brand-new@example.com")

	r.Equal(http.StatusOK, takenRec.Code, "a taken email must not surface as an error status")
	r.Equal(newRec.Code, takenRec.Code)
	r.JSONEq(newRec.Body.String(), takenRec.Body.String(), "the two response bodies must be identical")
	r.Equal(newRec.Header().Get("Content-Type"), takenRec.Header().Get("Content-Type"))
}
