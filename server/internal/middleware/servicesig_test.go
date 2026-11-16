package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/servicesig"
)

// Internal test (package middleware): the assertions reach the unexported
// service-authorization marker and the body cap constant directly.

const serviceSigTestParam = "test.service_signing_keys"

// serviceSigEnv is an AuthMiddleware backed by in-memory SQLite with one
// signing key seeded under serviceSigTestParam.
type serviceSigEnv struct {
	mw   *AuthMiddleware
	keys servicesig.KeySet
}

func newServiceSigEnv(t *testing.T) *serviceSigEnv {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	keys := servicesig.KeySet{{ID: "k1", Secret: "test-signing-secret"}}
	r.NoError(dbSvc.SetSystemParameter(ctx, serviceSigTestParam,
		`[{"id":"k1","secret":"test-signing-secret"}]`, true))

	return &serviceSigEnv{
		mw:   NewAuthMiddleware(nil, dbSvc, &config.Config{}),
		keys: keys,
	}
}

// runSigned builds a signed POST carrying body, runs it through
// ServiceSignature, and reports what the downstream handler saw.
func (e *serviceSigEnv) runSigned(t *testing.T, body []byte) (int, bool, bool, []byte) {
	t.Helper()
	r := require.New(t)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"/api/v1/orgs/acme/entitlements", bytes.NewReader(body))
	r.NoError(servicesig.SignRequest(req, e.keys, body, time.Now()))

	var called, authorized bool
	var seenBody []byte

	next := func(writer http.ResponseWriter, req *http.Request) error {
		called = true
		authorized = isServiceAuthorized(req.Context())

		read, err := io.ReadAll(req.Body)
		if err != nil {
			return err
		}

		seenBody = read
		writer.WriteHeader(http.StatusOK)

		return nil
	}

	rec := httptest.NewRecorder()
	r.NoError(e.mw.ServiceSignature(serviceSigTestParam)(next)(rec, req))

	return rec.Code, called, authorized, seenBody
}

// TestServiceSignatureValidRequestIsTrustedService pins the happy path: a
// correctly signed request reaches the handler as a trusted service, and the
// body the signature covered is handed back byte-for-byte.
func TestServiceSignatureValidRequestIsTrustedService(t *testing.T) {
	t.Parallel()

	env := newServiceSigEnv(t)
	body := []byte(`{"tier":"pro"}`)

	code, called, authorized, seenBody := env.runSigned(t, body)

	require.Equal(t, http.StatusOK, code)
	require.True(t, called, "the handler must run for a valid signature")
	require.True(t, authorized, "a valid signature marks the request as a trusted service")
	require.Equal(t, body, seenBody, "the handler must read back the exact signed bytes")
}

// TestServiceSignatureNoHeaderPassesThroughUnsigned: without the header the
// middleware is a no-op, so the legacy bearer / user-auth path still applies.
func TestServiceSignatureNoHeaderPassesThroughUnsigned(t *testing.T) {
	t.Parallel()

	env := newServiceSigEnv(t)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/orgs/acme/entitlements", http.NoBody)

	var called, authorized bool

	next := func(writer http.ResponseWriter, req *http.Request) error {
		called = true
		authorized = isServiceAuthorized(req.Context())
		writer.WriteHeader(http.StatusOK)

		return nil
	}

	rec := httptest.NewRecorder()
	require.NoError(t, env.mw.ServiceSignature(serviceSigTestParam)(next)(rec, req))

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, called)
	require.False(t, authorized, "no signature means no service authorization")
}

// TestServiceSignatureTamperedBodyDenied: the signature covers the exact body
// bytes, so swapping the payload after signing fails verification.
func TestServiceSignatureTamperedBodyDenied(t *testing.T) {
	t.Parallel()

	env := newServiceSigEnv(t)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/orgs/acme/entitlements",
		bytes.NewReader([]byte(`{"tier":"free"}`)))
	require.NoError(t, servicesig.SignRequest(req, env.keys, []byte(`{"tier":"pro"}`), time.Now()))

	called := false
	next := func(http.ResponseWriter, *http.Request) error {
		called = true

		return nil
	}

	rec := httptest.NewRecorder()
	require.NoError(t, env.mw.ServiceSignature(serviceSigTestParam)(next)(rec, req))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.False(t, called, "a tampered body must never reach the handler")
}

// TestServiceSignatureOversizedBodyDenied is the regression test for the
// pre-authentication buffer cap: the body is VALIDLY signed, so without the
// cap the request would verify and pass. The read happens before verification
// and must refuse to buffer more than maxServiceSignedBodyBytes, collapsing
// into the same generic 401 as every other rejection.
func TestServiceSignatureOversizedBodyDenied(t *testing.T) {
	t.Parallel()

	env := newServiceSigEnv(t)
	body := bytes.Repeat([]byte("a"), maxServiceSignedBodyBytes+1)

	code, called, _, _ := env.runSigned(t, body)

	require.Equal(t, http.StatusUnauthorized, code)
	require.False(t, called, "an oversized body must be refused before verification")
}

// TestServiceSignatureBodyAtCapAccepted pins the boundary: exactly
// maxServiceSignedBodyBytes bytes still verify (the cap is inclusive, not an
// off-by-one that would break a legitimate maximal write).
func TestServiceSignatureBodyAtCapAccepted(t *testing.T) {
	t.Parallel()

	env := newServiceSigEnv(t)
	body := bytes.Repeat([]byte("a"), maxServiceSignedBodyBytes)

	code, called, authorized, seenBody := env.runSigned(t, body)

	require.Equal(t, http.StatusOK, code)
	require.True(t, called)
	require.True(t, authorized)
	require.Equal(t, body, seenBody)
}

// TestServiceSignatureEmptyBodyAccepted: a bodyless signed request (nil body)
// still verifies — the hash covers the empty byte slice.
func TestServiceSignatureEmptyBodyAccepted(t *testing.T) {
	t.Parallel()

	env := newServiceSigEnv(t)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orgs/acme/entitlements", http.NoBody)
	require.NoError(t, servicesig.SignRequest(req, env.keys, nil, time.Now()))

	var called, authorized bool

	next := func(writer http.ResponseWriter, req *http.Request) error {
		called = true
		authorized = isServiceAuthorized(req.Context())
		writer.WriteHeader(http.StatusOK)

		return nil
	}

	rec := httptest.NewRecorder()
	require.NoError(t, env.mw.ServiceSignature(serviceSigTestParam)(next)(rec, req))

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, called)
	require.True(t, authorized)
}
