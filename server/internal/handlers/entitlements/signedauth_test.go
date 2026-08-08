package entitlements_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	authpkg "github.com/fclairamb/solidping/server/internal/handlers/auth"
	enthandler "github.com/fclairamb/solidping/server/internal/handlers/entitlements"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/servicesig"
)

// This file drives the REAL middleware chain the billing service hits —
// ServiceSignature -> ServiceTokenBypass -> RequireAuth -> RequireOrgAccess ->
// handler — over an httptest server, so every assertion is about what an
// attacker on the wire can and cannot do, not about an internal helper.

const (
	sigKeyID     = "billing-2026-08"
	sigSecret    = "inbound-signing-secret"
	legacyBearer = "legacy-static-service-token"
)

type signedSetup struct {
	db     *sqlite.Service
	server *httptest.Server
	orgA   *models.Organization
	orgB   *models.Organization
}

func newSignedSetup(t *testing.T) *signedSetup {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	orgA := models.NewOrganization("acme", "Acme")
	orgB := models.NewOrganization("globex", "Globex")
	r.NoError(dbSvc.CreateOrganization(ctx, orgA))
	r.NoError(dbSvc.CreateOrganization(ctx, orgB))

	authCfg := config.AuthConfig{
		JWTSecret:          "test-jwt-secret",
		AccessTokenExpiry:  time.Hour,
		RefreshTokenExpiry: 7 * 24 * time.Hour,
	}
	fullCfg := &config.Config{Auth: authCfg}
	authService := authpkg.NewService(dbSvc, authCfg, fullCfg, nil, nil)
	authMw := middleware.NewAuthMiddleware(authService, dbSvc, fullCfg)

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSaaS), 0)
	handler := enthandler.NewHandler(entSvc, dbSvc, fullCfg)

	guard := func(h httpx.HandlerFunc) httpx.HandlerFunc {
		return authMw.ServiceSignature(enthandler.ParamServiceSigningKeys)(
			authMw.ServiceTokenBypass(
				enthandler.ParamServiceToken, enthandler.ParamAllowLegacyServiceToken,
			)(
				authMw.RequireAuth(authMw.RequireOrgAccess(h)),
			),
		)
	}

	router := httpx.New()
	router.PUT("/api/v1/orgs/:org/entitlements", guard(handler.Put))
	router.GET("/api/v1/orgs/:org/entitlements", guard(handler.Get))

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &signedSetup{db: dbSvc, server: server, orgA: orgA, orgB: orgB}
}

// withSigningKeys configures the inbound verification key set.
func (s *signedSetup) withSigningKeys(t *testing.T, keys servicesig.KeySet) {
	t.Helper()

	raw, err := json.Marshal(keys)
	require.NoError(t, err)
	require.NoError(t, s.db.SetSystemParameter(
		t.Context(), enthandler.ParamServiceSigningKeys, string(raw), true))
}

// withLegacyToken configures the deprecated static bearer and whether it is
// still accepted.
func (s *signedSetup) withLegacyToken(t *testing.T, allowed bool) {
	t.Helper()

	require.NoError(t, s.db.SetSystemParameter(
		t.Context(), enthandler.ParamServiceToken, legacyBearer, true))
	require.NoError(t, s.db.SetSystemParameter(
		t.Context(), enthandler.ParamAllowLegacyServiceToken, allowed, false))
}

func (s *signedSetup) path(org string) string {
	return "/api/v1/orgs/" + org + "/entitlements"
}

// put builds a PUT and lets the caller mutate it before it goes on the wire.
func (s *signedSetup) put(
	t *testing.T, org string, body []byte, mutate func(req *http.Request),
) (int, string) {
	t.Helper()
	r := require.New(t)

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodPut, s.server.URL+s.path(org), bytes.NewReader(body))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")

	// Sign against the path only (no host, no query) — the wire contract.
	req.URL.Path = s.path(org)

	if mutate != nil {
		mutate(req)
	}

	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	respBody, err := io.ReadAll(resp.Body)
	r.NoError(err)

	return resp.StatusCode, string(respBody)
}

// signWith stamps a request with a signature over the given body, using the
// supplied key and timestamp. Everything the tests attack goes through here.
func signWith(key servicesig.Key, body []byte, at time.Time) func(*http.Request) {
	return func(req *http.Request) {
		ts := at.Unix()
		req.Header.Set(servicesig.HeaderTimestamp, strconv.FormatInt(ts, 10))
		req.Header.Set(servicesig.HeaderKeyID, key.ID)
		req.Header.Set(servicesig.HeaderSignature,
			servicesig.SignaturePrefix+servicesig.Sign(key, ts, req.Method, req.URL.Path, body))
	}
}

func currentKey() servicesig.Key {
	return servicesig.Key{ID: sigKeyID, Secret: sigSecret}
}

func limitsBody(maxChecks int) []byte {
	return []byte(`{"limits":{"maxChecks":` + strconv.Itoa(maxChecks) + `}}`)
}

// maxChecks reads the stored row, so every assertion is about what actually
// landed in the database rather than about the response body.
func (s *signedSetup) maxChecks(t *testing.T, org *models.Organization) *int {
	t.Helper()

	row, err := s.db.GetOrgEntitlements(t.Context(), org.UID)
	require.NoError(t, err)

	if row == nil {
		return nil
	}

	return row.Payload.Limits.MaxChecks
}

// TestSignedEntitlementsPutSucceeds is the acceptance criterion "a PUT with a
// valid signature succeeds and is treated as service-authorized (cross-org
// write still works)": there is no user token anywhere in these requests, and
// the same key writes two different orgs.
func TestSignedEntitlementsPutSucceeds(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	setup := newSignedSetup(t)
	setup.withSigningKeys(t, servicesig.KeySet{currentKey()})

	for _, org := range []*models.Organization{setup.orgA, setup.orgB} {
		body := limitsBody(42)
		code, respBody := setup.put(t, org.Slug, body, signWith(currentKey(), body, time.Now()))
		r.Equal(http.StatusOK, code, "org %s: %s", org.Slug, respBody)

		stored := setup.maxChecks(t, org)
		r.NotNil(stored, "org %s must have entitlements written", org.Slug)
		r.Equal(42, *stored)
	}
}

// TestSignedPutIsAttributedToTheBillingService checks the signed write keeps
// the service attribution the static bearer used to provide.
func TestSignedPutIsAttributedToTheBillingService(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	setup := newSignedSetup(t)
	setup.withSigningKeys(t, servicesig.KeySet{currentKey()})

	body := limitsBody(7)
	code, respBody := setup.put(t, setup.orgA.Slug, body, signWith(currentKey(), body, time.Now()))
	r.Equal(http.StatusOK, code, respBody)

	row, err := setup.db.GetOrgEntitlements(t.Context(), setup.orgA.UID)
	r.NoError(err)
	r.NotNil(row)
	r.Equal(models.EntitlementSourceBilling, row.Payload.Source,
		"a signed write must be attributed to billing, like the legacy service token was")
}

// TestTamperedBodyIsRejected — one byte of the body changed invalidates the
// signature. This is the property the static bearer never had.
func TestTamperedBodyIsRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	setup := newSignedSetup(t)
	setup.withSigningKeys(t, servicesig.KeySet{currentKey()})

	signedBody := limitsBody(100)
	tampered := limitsBody(900) // captured signature, rewritten limits

	code, _ := setup.put(t, setup.orgA.Slug, tampered, signWith(currentKey(), signedBody, time.Now()))
	r.Equal(http.StatusUnauthorized, code)
	r.Nil(setup.maxChecks(t, setup.orgA), "the tampered write must not have landed")

	// Positive control: the same request with the signature over the body it
	// actually carries succeeds.
	code, _ = setup.put(t, setup.orgA.Slug, tampered, signWith(currentKey(), tampered, time.Now()))
	r.Equal(http.StatusOK, code)
	r.Equal(900, *setup.maxChecks(t, setup.orgA))
}

// TestReplayOutsideTheSkewWindowIsRejected — a captured request stops working
// once it is older than the skew window.
func TestReplayOutsideTheSkewWindowIsRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	setup := newSignedSetup(t)
	setup.withSigningKeys(t, servicesig.KeySet{currentKey()})

	body := limitsBody(11)
	stale := time.Now().Add(-servicesig.MaxSkew - time.Minute)

	code, _ := setup.put(t, setup.orgA.Slug, body, signWith(currentKey(), body, stale))
	r.Equal(http.StatusUnauthorized, code)
	r.Nil(setup.maxChecks(t, setup.orgA))

	// Positive control: inside the window, byte-identical otherwise.
	fresh := time.Now().Add(-servicesig.MaxSkew + time.Minute)
	code, _ = setup.put(t, setup.orgA.Slug, body, signWith(currentKey(), body, fresh))
	r.Equal(http.StatusOK, code)
	r.Equal(11, *setup.maxChecks(t, setup.orgA))
}

// TestUnknownKeyIDIsRejected — a signature from a key that is not in the
// parameter is refused even though it is otherwise well formed.
func TestUnknownKeyIDIsRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	setup := newSignedSetup(t)
	setup.withSigningKeys(t, servicesig.KeySet{currentKey()})

	body := limitsBody(5)
	rogue := servicesig.Key{ID: "retired-key", Secret: sigSecret}

	code, _ := setup.put(t, setup.orgA.Slug, body, signWith(rogue, body, time.Now()))
	r.Equal(http.StatusUnauthorized, code)
	r.Nil(setup.maxChecks(t, setup.orgA))
}

// TestRotationAcceptsBothKeys — the acceptance criterion for rotation, driven
// end to end: two keys configured, either signs a successful write.
func TestRotationAcceptsBothKeys(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	setup := newSignedSetup(t)
	newKey := servicesig.Key{ID: "k-new", Secret: "new-secret"}
	oldKey := servicesig.Key{ID: "k-old", Secret: "old-secret"}
	setup.withSigningKeys(t, servicesig.KeySet{newKey, oldKey})

	for i, key := range []servicesig.Key{newKey, oldKey} {
		body := limitsBody(100 + i)
		code, respBody := setup.put(t, setup.orgA.Slug, body, signWith(key, body, time.Now()))
		r.Equal(http.StatusOK, code, "key %s: %s", key.ID, respBody)
		r.Equal(100+i, *setup.maxChecks(t, setup.orgA))
	}

	// A third, unconfigured key is still refused — the set is not a free pass.
	body := limitsBody(999)
	code, _ := setup.put(t, setup.orgA.Slug, body,
		signWith(servicesig.Key{ID: "k-rogue", Secret: "rogue"}, body, time.Now()))
	r.Equal(http.StatusUnauthorized, code)
}

// TestNoSignatureAndNoCredentialsIsRejected — the base case: without any proof
// of identity the endpoint is closed.
func TestNoSignatureAndNoCredentialsIsRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	setup := newSignedSetup(t)
	setup.withSigningKeys(t, servicesig.KeySet{currentKey()})

	code, _ := setup.put(t, setup.orgA.Slug, limitsBody(1), nil)
	r.Equal(http.StatusUnauthorized, code)
	r.Nil(setup.maxChecks(t, setup.orgA))
}

// TestSignatureRejectedWhenNoKeysConfigured — signing does not become a way in
// on an install that has no inbound key set at all.
func TestSignatureRejectedWhenNoKeysConfigured(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	setup := newSignedSetup(t)

	body := limitsBody(1)
	code, _ := setup.put(t, setup.orgA.Slug, body, signWith(currentKey(), body, time.Now()))
	r.Equal(http.StatusUnauthorized, code)
	r.Nil(setup.maxChecks(t, setup.orgA))
}

// TestLegacyBearerHonorsTheEscapeHatch is the migration criterion: with
// allow_legacy_service_token=true a legacy bearer still writes (and warns);
// with false the very same request is refused.
func TestLegacyBearerHonorsTheEscapeHatch(t *testing.T) {
	t.Parallel()

	withBearer := func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+legacyBearer)
	}

	t.Run("allowed", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		logs := captureLogs(t)
		setup := newSignedSetup(t)
		setup.withLegacyToken(t, true)

		code, respBody := setup.put(t, setup.orgA.Slug, limitsBody(3), withBearer)
		r.Equal(http.StatusOK, code, respBody)
		r.Equal(3, *setup.maxChecks(t, setup.orgA))
		r.Contains(logs.String(), "DEPRECATED",
			"the legacy path must warn so operators can watch it go quiet")
	})

	t.Run("disallowed", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		setup := newSignedSetup(t)
		setup.withLegacyToken(t, false)

		code, _ := setup.put(t, setup.orgA.Slug, limitsBody(3), withBearer)
		r.Equal(http.StatusUnauthorized, code)
		r.Nil(setup.maxChecks(t, setup.orgA))
	})

	t.Run("a wrong bearer never works", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		setup := newSignedSetup(t)
		setup.withLegacyToken(t, true)

		code, _ := setup.put(t, setup.orgA.Slug, limitsBody(3), func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer "+legacyBearer+"x")
		})
		r.Equal(http.StatusUnauthorized, code)
	})
}

// TestLegacyDisabledStillAcceptsSignatures — turning the escape hatch off must
// not take the signed channel down with it. This is what makes step 3 of the
// migration a parameter flip rather than an outage.
func TestLegacyDisabledStillAcceptsSignatures(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	setup := newSignedSetup(t)
	setup.withSigningKeys(t, servicesig.KeySet{currentKey()})
	setup.withLegacyToken(t, false)

	body := limitsBody(21)
	code, respBody := setup.put(t, setup.orgA.Slug, body, signWith(currentKey(), body, time.Now()))
	r.Equal(http.StatusOK, code, respBody)
	r.Equal(21, *setup.maxChecks(t, setup.orgA))
}

// TestSignatureDoesNotLeakTheReason — every rejection is one generic 401; the
// body must not tell an attacker which check failed.
func TestSignatureDoesNotLeakTheReason(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	setup := newSignedSetup(t)
	setup.withSigningKeys(t, servicesig.KeySet{currentKey()})

	body := limitsBody(1)
	bodies := make([]string, 0, 3)

	for _, mutate := range []func(*http.Request){
		signWith(servicesig.Key{ID: "nope", Secret: sigSecret}, body, time.Now()),
		signWith(currentKey(), body, time.Now().Add(-2*servicesig.MaxSkew)),
		signWith(servicesig.Key{ID: sigKeyID, Secret: "wrong"}, body, time.Now()),
	} {
		code, respBody := setup.put(t, setup.orgA.Slug, body, mutate)
		r.Equal(http.StatusUnauthorized, code)
		bodies = append(bodies, respBody)

		lower := strings.ToLower(respBody)
		for _, leak := range []string{"skew", "key id", "signature", "timestamp", sigKeyID} {
			r.NotContains(lower, strings.ToLower(leak),
				"the 401 body must not name the failed check")
		}
	}

	r.Equal(bodies[0], bodies[1], "all rejections must be indistinguishable")
	r.Equal(bodies[1], bodies[2], "all rejections must be indistinguishable")
}

// syncBuffer is a mutex-guarded buffer so a parallel test can install it as the
// slog destination without racing.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// captureLogs redirects slog to a buffer for the duration of the test.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()

	buf := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return buf
}

var _ io.Writer = (*syncBuffer)(nil)
