package entitlements_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	authpkg "github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	enthandler "github.com/fclairamb/solidping/server/internal/handlers/entitlements"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

type entHandlerSetup struct {
	dbSvc  *sqlite.Service
	org    *models.Organization
	user   *models.User
	router *httpx.Router
}

func newEntHandlerSetup(t *testing.T) *entHandlerSetup {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("ent-h", "Ent Handler Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("admin@example.com")
	r.NoError(dbSvc.CreateUser(ctx, user))
	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
	r.NoError(dbSvc.CreateOrganizationMember(ctx, member))

	svc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	handler := enthandler.NewHandler(svc, dbSvc, &config.Config{})

	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/entitlements")
	group.GET("", handler.Get)
	group.PUT("", handler.Put)
	group.PATCH("", handler.Patch)

	return &entHandlerSetup{dbSvc: dbSvc, org: org, user: user, router: router}
}

func (h *entHandlerSetup) do(
	t *testing.T, method, path string, body any,
) *httptest.ResponseRecorder {
	t.Helper()

	return h.doWithRole(t, method, path, body, "admin")
}

func (h *entHandlerSetup) doWithRole(
	t *testing.T, method, path string, body any, role string,
) *httptest.ResponseRecorder {
	t.Helper()

	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		require.NoError(t, err)
	}

	ctx := context.WithValue(t.Context(), base.ContextKeyUser, h.user)
	ctx = context.WithValue(ctx, base.ContextKeyClaims, &authpkg.Claims{
		UserUID: h.user.UID,
		OrgSlug: h.org.Slug,
		Role:    role,
	})
	req := httptest.NewRequestWithContext(ctx, method, path, bytes.NewBuffer(bodyBytes))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)

	return rec
}

// doAsService sends a request the handler recognizes as the BILLING SERVICE
// rather than a signed-in human, via the legacy static bearer. A user
// principal can never claim to be billing (sourceFor decides the source, not
// the body), so a precedence test has to come through this door to be testing
// the real thing.
func (h *entHandlerSetup) doAsService(
	t *testing.T, method, path string, body any,
) *httptest.ResponseRecorder {
	t.Helper()

	const serviceBearer = "svc-token-for-precedence-tests"

	require.NoError(t, h.dbSvc.SetSystemParameter(
		t.Context(), enthandler.ParamServiceToken, serviceBearer, true))
	require.NoError(t, h.dbSvc.SetSystemParameter(
		t.Context(), enthandler.ParamAllowLegacyServiceToken, true, false))

	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		require.NoError(t, err)
	}

	req := httptest.NewRequestWithContext(
		t.Context(), method, path, bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+serviceBearer)

	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)

	return rec
}

func (h *entHandlerSetup) path() string {
	return "/api/v1/orgs/" + h.org.Slug + "/entitlements"
}

func TestGetWithoutUsageOmitsUsage(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newEntHandlerSetup(t)

	rec := h.do(t, http.MethodGet, h.path(), nil)
	r.Equal(http.StatusOK, rec.Code)

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	_, hasUsage := body["usage"]
	r.False(hasUsage, "usage must be absent without ?with=usage")
}

func TestGetWithUsageIncludesUsage(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newEntHandlerSetup(t)

	rec := h.do(t, http.MethodGet, h.path()+"?with=usage", nil)
	r.Equal(http.StatusOK, rec.Code)

	var body struct {
		Usage *entcore.Usage `json:"usage"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.NotNil(body.Usage, "usage must be present with ?with=usage")
}

func TestPatchAcceptsMaxChecks(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newEntHandlerSetup(t)

	rec := h.do(t, http.MethodPatch, h.path(), map[string]any{
		"limits": map[string]any{"maxChecks": 50},
	})
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var body struct {
		Limits struct {
			MaxChecks *int `json:"maxChecks"`
		} `json:"limits"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.NotNil(body.Limits.MaxChecks)
	r.Equal(50, *body.Limits.MaxChecks)
}

func TestPutAcceptsMaxChecks(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newEntHandlerSetup(t)

	rec := h.do(t, http.MethodPut, h.path(), map[string]any{
		"source": string(models.EntitlementSourceAdmin),
		"limits": map[string]any{"maxChecks": 7},
	})
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	// GET reflects the stored maxChecks in the limits block.
	getRec := h.do(t, http.MethodGet, h.path(), nil)
	r.Equal(http.StatusOK, getRec.Code)

	var body struct {
		Limits struct {
			MaxChecks *int `json:"maxChecks"`
		} `json:"limits"`
	}
	r.NoError(json.Unmarshal(getRec.Body.Bytes(), &body))
	r.NotNil(body.Limits.MaxChecks)
	r.Equal(7, *body.Limits.MaxChecks)
}

func TestPutAcceptsMaxDeportedAgents(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newEntHandlerSetup(t)

	rec := h.do(t, http.MethodPut, h.path(), map[string]any{
		"source": string(models.EntitlementSourceAdmin),
		"limits": map[string]any{"maxDeportedAgents": 3},
	})
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	// GET reflects the stored maxDeportedAgents in the limits block.
	getRec := h.do(t, http.MethodGet, h.path(), nil)
	r.Equal(http.StatusOK, getRec.Code)

	var body struct {
		Limits struct {
			MaxDeportedAgents *int `json:"maxDeportedAgents"`
		} `json:"limits"`
	}
	r.NoError(json.Unmarshal(getRec.Body.Bytes(), &body))
	r.NotNil(body.Limits.MaxDeportedAgents)
	r.Equal(3, *body.Limits.MaxDeportedAgents)
}

// TestPutAcceptsMonthlyMessagingLimits round-trips the SMS/voice monthly caps
// through the real PUT handler and reads them back via GET.
func TestPutAcceptsMonthlyMessagingLimits(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newEntHandlerSetup(t)

	rec := h.do(t, http.MethodPut, h.path(), map[string]any{
		"source": string(models.EntitlementSourceAdmin),
		"limits": map[string]any{"maxSmsPerMonth": 500, "maxCallsPerMonth": 50},
	})
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	getRec := h.do(t, http.MethodGet, h.path(), nil)
	r.Equal(http.StatusOK, getRec.Code)

	var body struct {
		Limits struct {
			MaxSmsPerMonth   *int `json:"maxSmsPerMonth"`
			MaxCallsPerMonth *int `json:"maxCallsPerMonth"`
		} `json:"limits"`
	}
	r.NoError(json.Unmarshal(getRec.Body.Bytes(), &body))
	r.NotNil(body.Limits.MaxSmsPerMonth)
	r.Equal(500, *body.Limits.MaxSmsPerMonth)
	r.NotNil(body.Limits.MaxCallsPerMonth)
	r.Equal(50, *body.Limits.MaxCallsPerMonth)
}

// putMaxUsers PUTs a limits body and returns the recorder. rawLimits is the
// raw limits object so tests can send the deprecated alias or both keys.
func (h *entHandlerSetup) putLimits(t *testing.T, rawLimits map[string]any) *httptest.ResponseRecorder {
	t.Helper()

	return h.do(t, http.MethodPut, h.path(), map[string]any{
		"source": string(models.EntitlementSourceAdmin),
		"limits": rawLimits,
	})
}

func readMaxUsers(t *testing.T, body []byte) *int {
	t.Helper()

	var parsed struct {
		Limits struct {
			MaxUsers *int `json:"maxUsers"`
		} `json:"limits"`
	}
	require.NoError(t, json.Unmarshal(body, &parsed))

	return parsed.Limits.MaxUsers
}

func TestPutAcceptsMaxUsers(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newEntHandlerSetup(t)

	rec := h.putLimits(t, map[string]any{"maxUsers": 11})
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	getRec := h.do(t, http.MethodGet, h.path(), nil)
	got := readMaxUsers(t, getRec.Body.Bytes())
	r.NotNil(got)
	r.Equal(11, *got)
	// Response must never carry the deprecated key.
	r.NotContains(getRec.Body.String(), "maxSsoUsers")
}

// TestPutAcceptsMaxSsoUsersAlias exercises the decode-only alias that keeps
// already-deployed billing services working during the transition.
func TestPutAcceptsMaxSsoUsersAlias(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newEntHandlerSetup(t)

	rec := h.putLimits(t, map[string]any{"maxSsoUsers": 8})
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	getRec := h.do(t, http.MethodGet, h.path(), nil)
	got := readMaxUsers(t, getRec.Body.Bytes())
	r.NotNil(got)
	r.Equal(8, *got)
}

// TestPutRejectsBothUserKeys verifies sending both maxUsers and the deprecated
// maxSsoUsers alias is rejected as a client validation error (the decode error
// flows through WriteValidationError → 422).
func TestPutRejectsBothUserKeys(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newEntHandlerSetup(t)

	rec := h.putLimits(t, map[string]any{"maxUsers": 5, "maxSsoUsers": 5})
	r.Equal(http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	r.Contains(rec.Body.String(), "mutually exclusive")
}

// getUpgradeURL extracts the upgradeUrl field from a GET response.
func getUpgradeURL(t *testing.T, rec *httptest.ResponseRecorder) (string, bool) {
	t.Helper()

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	raw, ok := body["upgradeUrl"]
	if !ok {
		return "", false
	}
	s, ok := raw.(string)

	return s, ok
}

func TestGetAdminUpgradeURLHasSignedBillingToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newEntHandlerSetup(t)
	ctx := t.Context()

	const secret = "shared-billing-secret"
	r.NoError(h.dbSvc.SetSystemParameter(ctx, enthandler.ParamUpgradeURLTemplate,
		"https://billing.example.com/upgrade?org={org}", false))
	r.NoError(h.dbSvc.SetSystemParameter(ctx, enthandler.ParamBillingInboundSecret, secret, true))

	rec := h.do(t, http.MethodGet, h.path(), nil)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	upgradeURL, ok := getUpgradeURL(t, rec)
	r.True(ok, "admin must receive an upgradeUrl")

	base, frag, found := strings.Cut(upgradeURL, "#bt=")
	r.True(found, "upgradeUrl must carry a #bt= token fragment: %s", upgradeURL)
	r.Equal("https://billing.example.com/upgrade?org="+h.org.Slug, base)
	r.NotEmpty(frag)

	// The fragment token must verify against the configured secret and carry
	// the expected claims.
	parsed, err := jwt.Parse(frag, func(token *jwt.Token) (any, error) {
		_, isHMAC := token.Method.(*jwt.SigningMethodHMAC)
		r.True(isHMAC, "token must be HS256")

		return []byte(secret), nil
	})
	r.NoError(err)
	r.True(parsed.Valid)

	claims, ok := parsed.Claims.(jwt.MapClaims)
	r.True(ok)
	r.Equal("billing", claims["purpose"])
	r.Equal(h.org.Slug, claims["org"])
	r.Equal(h.user.UID, claims["sub"])
	r.Equal(h.user.Email, claims["email"])
	r.NotNil(claims["iat"])
	r.NotNil(claims["exp"])
	// exp must be ~1h after iat.
	iat, ok := claims["iat"].(float64)
	r.True(ok)
	exp, ok := claims["exp"].(float64)
	r.True(ok)
	r.InDelta(3600, exp-iat, 5)
}

func TestGetAdminUpgradeURLNoSecretHasNoFragment(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newEntHandlerSetup(t)
	ctx := t.Context()

	r.NoError(h.dbSvc.SetSystemParameter(ctx, enthandler.ParamUpgradeURLTemplate,
		"https://billing.example.com/upgrade?org={org}", false))
	// No billing_inbound_secret configured.

	rec := h.do(t, http.MethodGet, h.path(), nil)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	upgradeURL, ok := getUpgradeURL(t, rec)
	r.True(ok, "admin must still receive an upgradeUrl without a secret")
	r.Equal("https://billing.example.com/upgrade?org="+h.org.Slug, upgradeURL)
	r.NotContains(upgradeURL, "#bt=")
}

func TestGetNonAdminOmitsUpgradeURL(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newEntHandlerSetup(t)
	ctx := t.Context()

	r.NoError(h.dbSvc.SetSystemParameter(ctx, enthandler.ParamUpgradeURLTemplate,
		"https://billing.example.com/upgrade?org={org}", false))
	r.NoError(h.dbSvc.SetSystemParameter(ctx, enthandler.ParamBillingInboundSecret, "s", true))

	rec := h.doWithRole(t, http.MethodGet, h.path(), nil, "user")
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	_, ok := getUpgradeURL(t, rec)
	r.False(ok, "non-admin must not receive an upgradeUrl")
}

// doForOrg issues a request as an admin of an arbitrary org, so a token minted
// for one org can be compared against another org's slug.
func (h *entHandlerSetup) doForOrg(
	t *testing.T, method, path, orgSlug string,
) *httptest.ResponseRecorder {
	t.Helper()

	ctx := context.WithValue(t.Context(), base.ContextKeyUser, h.user)
	ctx = context.WithValue(ctx, base.ContextKeyClaims, &authpkg.Claims{
		UserUID: h.user.UID,
		OrgSlug: orgSlug,
		Role:    "admin",
	})
	req := httptest.NewRequestWithContext(ctx, method, path, nil)
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)

	return rec
}

// fragmentToken returns the `#bt=` fragment of the admin upgrade URL, failing
// the test when it is absent.
func (h *entHandlerSetup) fragmentToken(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	r := require.New(t)

	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	upgradeURL, ok := getUpgradeURL(t, rec)
	r.True(ok, "admin must receive an upgradeUrl")

	_, frag, found := strings.Cut(upgradeURL, "#bt=")
	r.True(found, "upgradeUrl must carry a #bt= token fragment: %s", upgradeURL)
	r.NotEmpty(frag)

	return frag
}

// seedUpgradeTemplate configures the upgrade URL template used by every
// upgrade-token test.
func (h *entHandlerSetup) seedUpgradeTemplate(t *testing.T) {
	t.Helper()
	require.NoError(t, h.dbSvc.SetSystemParameter(t.Context(), enthandler.ParamUpgradeURLTemplate,
		"https://billing.example.com/upgrade?org={org}", false))
}

// keyFunc builds a jwt.Keyfunc for a fixed HS256 secret.
func keyFunc(secret string) jwt.Keyfunc {
	return func(token *jwt.Token) (any, error) {
		if _, isHMAC := token.Method.(*jwt.SigningMethodHMAC); !isHMAC {
			return nil, jwt.ErrSignatureInvalid
		}

		return []byte(secret), nil
	}
}

// TestUpgradeTokenUsesDedicatedSecret is the regression test for the spec: with
// entitlements.billing_upgrade_token_secret set, the `#bt=` token must verify
// against THAT secret and must FAIL against the inbound bearer. If the fallback
// ordering were reversed (bearer preferred), the second assertion breaks.
func TestUpgradeTokenUsesDedicatedSecret(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newEntHandlerSetup(t)
	ctx := t.Context()

	const (
		bearer         = "inbound-bearer-secret"
		upgradeSigning = "dedicated-upgrade-token-secret"
	)
	h.seedUpgradeTemplate(t)
	r.NoError(h.dbSvc.SetSystemParameter(ctx, enthandler.ParamBillingInboundSecret, bearer, true))
	r.NoError(h.dbSvc.SetSystemParameter(ctx, enthandler.ParamBillingUpgradeTokenSecret, upgradeSigning, true))

	frag := h.fragmentToken(t, h.do(t, http.MethodGet, h.path(), nil))

	// Negative: the leaked bearer must NOT verify the token.
	_, err := jwt.Parse(frag, keyFunc(bearer))
	r.Error(err, "a token minted with the dedicated secret must not verify against the inbound bearer")
	r.ErrorIs(err, jwt.ErrSignatureInvalid)

	// Positive: it verifies against the dedicated secret, with unchanged claims.
	parsed, err := jwt.Parse(frag, keyFunc(upgradeSigning))
	r.NoError(err)
	r.True(parsed.Valid)

	claims, ok := parsed.Claims.(jwt.MapClaims)
	r.True(ok)
	r.Equal("billing", claims["purpose"])
	r.Equal(h.org.Slug, claims["org"])
	r.Equal(h.user.UID, claims["sub"])
	r.Equal(h.user.Email, claims["email"])

	iat, ok := claims["iat"].(float64)
	r.True(ok)
	exp, ok := claims["exp"].(float64)
	r.True(ok)
	r.InDelta(3600, exp-iat, 5)
}

// TestUpgradeTokenFallsBackToInboundSecret pins acceptance criterion 2: with
// the dedicated parameter unset, behavior is exactly what it is today.
func TestUpgradeTokenFallsBackToInboundSecret(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newEntHandlerSetup(t)
	ctx := t.Context()

	const bearer = "inbound-bearer-secret"
	h.seedUpgradeTemplate(t)
	r.NoError(h.dbSvc.SetSystemParameter(ctx, enthandler.ParamBillingInboundSecret, bearer, true))
	// entitlements.billing_upgrade_token_secret deliberately NOT set.

	frag := h.fragmentToken(t, h.do(t, http.MethodGet, h.path(), nil))

	parsed, err := jwt.Parse(frag, keyFunc(bearer))
	r.NoError(err, "with the dedicated secret unset the token must still verify against the bearer")
	r.True(parsed.Valid)
}

// TestUpgradeTokenDedicatedSecretAloneIsEnough covers the split-completed state:
// only the dedicated secret is configured, no bearer at all.
func TestUpgradeTokenDedicatedSecretAloneIsEnough(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newEntHandlerSetup(t)
	ctx := t.Context()

	const upgradeSigning = "dedicated-upgrade-token-secret"
	h.seedUpgradeTemplate(t)
	r.NoError(h.dbSvc.SetSystemParameter(ctx, enthandler.ParamBillingUpgradeTokenSecret, upgradeSigning, true))

	frag := h.fragmentToken(t, h.do(t, http.MethodGet, h.path(), nil))

	parsed, err := jwt.Parse(frag, keyFunc(upgradeSigning))
	r.NoError(err)
	r.True(parsed.Valid)
}

// TestUpgradeTokenNeitherSecretHasNoFragment pins acceptance criterion 5.
func TestUpgradeTokenNeitherSecretHasNoFragment(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newEntHandlerSetup(t)

	h.seedUpgradeTemplate(t)
	// Neither billing secret configured.

	rec := h.do(t, http.MethodGet, h.path(), nil)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	upgradeURL, ok := getUpgradeURL(t, rec)
	r.True(ok)
	r.Equal("https://billing.example.com/upgrade?org="+h.org.Slug, upgradeURL)
	r.NotContains(upgradeURL, "#bt=")
}

// TestUpgradeTokenCarriesTheRequestedOrgOnly checks the org claim is the org
// the URL was built for — billing enforces the match, but the claim has to be
// right on this side.
func TestUpgradeTokenCarriesTheRequestedOrgOnly(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newEntHandlerSetup(t)
	ctx := t.Context()

	const upgradeSigning = "dedicated-upgrade-token-secret"
	h.seedUpgradeTemplate(t)
	r.NoError(h.dbSvc.SetSystemParameter(ctx, enthandler.ParamBillingUpgradeTokenSecret, upgradeSigning, true))

	other := models.NewOrganization("ent-h-other", "Other Org")
	r.NoError(h.dbSvc.CreateOrganization(ctx, other))
	r.NoError(h.dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(other.UID, h.user.UID, models.MemberRoleAdmin)))
	r.NotEqual(h.org.Slug, other.Slug)

	orgClaim := func(rec *httptest.ResponseRecorder) string {
		parsed, err := jwt.Parse(h.fragmentToken(t, rec), keyFunc(upgradeSigning))
		r.NoError(err)
		claims, ok := parsed.Claims.(jwt.MapClaims)
		r.True(ok)
		slug, ok := claims["org"].(string)
		r.True(ok)

		return slug
	}

	firstPath := "/api/v1/orgs/" + h.org.Slug + "/entitlements"
	otherPath := "/api/v1/orgs/" + other.Slug + "/entitlements"

	r.Equal(h.org.Slug, orgClaim(h.doForOrg(t, http.MethodGet, firstPath, h.org.Slug)))
	r.Equal(other.Slug, orgClaim(h.doForOrg(t, http.MethodGet, otherPath, other.Slug)))
}

// TestGetAlwaysIncludesChecksPerMinute pins the payload contract the over-limit
// banner depends on (spec 2026-08-26-03): checksPerMinute is NOT behind
// ?with=usage, because the checks list renders the banner and must not pay for
// the whole usage roll-up to learn it is being throttled.
func TestGetAlwaysIncludesChecksPerMinute(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newEntHandlerSetup(t)

	rec := h.do(t, http.MethodGet, h.path(), nil)
	r.Equal(http.StatusOK, rec.Code)

	// The raw shape first: camelCase keys, and a null (not absent, not 0) limit
	// for an org on the unlimited self-hosted default.
	var raw struct {
		ChecksPerMinute map[string]any `json:"checksPerMinute"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &raw))
	r.NotNil(raw.ChecksPerMinute, "checksPerMinute must be present without ?with=usage")
	r.Contains(raw.ChecksPerMinute, "demand")
	r.Contains(raw.ChecksPerMinute, "limit")
	r.Contains(raw.ChecksPerMinute, "skippedToday")
	r.Nil(raw.ChecksPerMinute["limit"], "an unlimited cap serializes as null, never as a number")

	var body struct {
		ChecksPerMinute *entcore.ChecksPerMinute `json:"checksPerMinute"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.NotNil(body.ChecksPerMinute)
	r.Zero(body.ChecksPerMinute.Demand, "an org with no checks schedules nothing")
	r.Zero(body.ChecksPerMinute.SkippedToday)
}

// TestGetChecksPerMinuteReportsDemandAndSkips walks the full path an over-limit
// org takes: a cap written through the API, real checks producing demand, and
// recorded skips — all surfacing on the GET the dashboard reads.
func TestGetChecksPerMinuteReportsDemandAndSkips(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	h := newEntHandlerSetup(t)

	// Two 1-minute checks, one of them in three regions: 3 + 1 = 4 per minute.
	multi := models.NewCheck(h.org.UID, "multi-region", "http")
	multi.Regions = []string{"eu", "us", "jp"}
	r.NoError(h.dbSvc.CreateCheck(ctx, multi))

	single := models.NewCheck(h.org.UID, "single-region", "tcp")
	r.NoError(h.dbSvc.CreateCheck(ctx, single))

	// A heartbeat consumes no execution budget and must not inflate demand.
	passive := models.NewCheck(h.org.UID, "passive-beat", "heartbeat")
	r.NoError(h.dbSvc.CreateCheck(ctx, passive))

	rec := h.do(t, http.MethodPut, h.path(), map[string]any{
		"limits": map[string]any{"maxChecksPerMinute": 2},
	})
	r.Equal(http.StatusOK, rec.Code)

	svc := entcore.NewService(h.dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc.RecordRateLimitedSkip(ctx, h.org.UID)
	svc.RecordRateLimitedSkip(ctx, h.org.UID)

	rec = h.do(t, http.MethodGet, h.path(), nil)
	r.Equal(http.StatusOK, rec.Code)

	var body struct {
		ChecksPerMinute *entcore.ChecksPerMinute `json:"checksPerMinute"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.NotNil(body.ChecksPerMinute)
	r.InDelta(4.0, body.ChecksPerMinute.Demand, 0.0001,
		"3 regions + 1 region per minute, with the heartbeat excluded")
	r.NotNil(body.ChecksPerMinute.Limit)
	r.Equal(2, *body.ChecksPerMinute.Limit)
	r.Equal(2, body.ChecksPerMinute.SkippedToday)
	r.True(body.ChecksPerMinute.Over())
}

// TestPutBillingOntoAdminRowReportsSuppression exercises the BILLING front
// door (the org-scoped PUT) against the precedence rule: 200, unchanged
// limits, and a body that says so. A 200 that quietly lied about having
// applied the push would be worse than no editor at all.
func TestPutBillingOntoAdminRowReportsSuppression(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newEntHandlerSetup(t)

	// A SUPERADMIN write is what mints the suppressing override, even when it
	// arrives on the org-scoped route.
	h.user.SuperAdmin = true

	rec := h.do(t, http.MethodPut, h.path(), map[string]any{
		"source": string(models.EntitlementSourceAdmin),
		"limits": map[string]any{"maxChecks": 5000},
	})
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var applied struct {
		Applied bool `json:"applied"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &applied))
	r.True(applied.Applied)

	rec = h.doAsService(t, http.MethodPut, h.path(), map[string]any{
		"limits": map[string]any{"maxChecks": 100},
	})
	r.Equal(http.StatusOK, rec.Code, "billing must not be made to error-loop")

	var suppressed struct {
		Applied      bool   `json:"applied"`
		SuppressedBy string `json:"suppressedBy"`
		Message      string `json:"message"`
		Limits       struct {
			MaxChecks *int `json:"maxChecks"`
		} `json:"limits"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &suppressed))
	r.False(suppressed.Applied)
	r.Equal(string(models.EntitlementSourceAdmin), suppressed.SuppressedBy)
	r.NotEmpty(suppressed.Message)
	r.NotNil(suppressed.Limits.MaxChecks)
	r.Equal(5000, *suppressed.Limits.MaxChecks, "the admin override must survive the push")
}


// TestOrgAdminWriteDoesNotOutrankBilling is the security half of the
// precedence rule, and the reason `org-admin` exists as a separate source.
//
// The org-scoped PUT is open to any org admin whenever
// entitlements.admin_writes_enabled is unset (it defaults to TRUE), which on a
// SaaS install that never set SP_ENTITLEMENTS_ADMIN_WRITES is every org admin.
// Before the precedence rule that door was harmless — billing's next reconcile
// corrected whatever it wrote. If such a write could mint `admin`, it would now
// be a permanent lockout: any org admin could grant themselves limits AND stop
// the billing service from ever correcting them, until a superadmin noticed.
//
// So: an ORG admin's write must be overwritable by billing. The superadmin's
// must not. Both halves are asserted here, because either one alone passes on a
// broken implementation.
func TestOrgAdminWriteDoesNotOutrankBilling(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	t.Run("an org admin's write is corrected by the next billing push", func(t *testing.T) {
		t.Parallel()
		rr := require.New(t)
		h := newEntHandlerSetup(t)

		// Not a superadmin. It even ASKS to be recorded as `admin` — the source
		// is a server decision, not something a body may assert.
		rec := h.do(t, http.MethodPut, h.path(), map[string]any{
			"source": string(models.EntitlementSourceAdmin),
			"limits": map[string]any{"maxChecks": 5000},
		})
		rr.Equal(http.StatusOK, rec.Code, rec.Body.String())

		var written struct {
			Source  string `json:"source"`
			Applied bool   `json:"applied"`
		}
		rr.NoError(json.Unmarshal(rec.Body.Bytes(), &written))
		rr.Equal(string(models.EntitlementSourceOrgAdmin), written.Source,
			"an org admin may not mint the superadmin override source")
		rr.True(written.Applied)

		rec = h.doAsService(t, http.MethodPut, h.path(), map[string]any{
			"limits": map[string]any{"maxChecks": 100},
		})
		rr.Equal(http.StatusOK, rec.Code, rec.Body.String())

		var pushed struct {
			Applied bool `json:"applied"`
			Limits  struct {
				MaxChecks *int `json:"maxChecks"`
			} `json:"limits"`
		}
		rr.NoError(json.Unmarshal(rec.Body.Bytes(), &pushed))
		rr.True(pushed.Applied, "billing must still be able to correct an org-admin row")
		rr.NotNil(pushed.Limits.MaxChecks)
		rr.Equal(100, *pushed.Limits.MaxChecks)
	})

	// POSITIVE CONTROL: the same sequence, from a superadmin, DOES suppress.
	// Without this the assertion above would pass just as happily on an
	// implementation where suppression never fires at all.
	t.Run("a superadmin's write is not", func(t *testing.T) {
		t.Parallel()
		rr := require.New(t)
		h := newEntHandlerSetup(t)
		h.user.SuperAdmin = true

		rec := h.do(t, http.MethodPut, h.path(), map[string]any{
			"limits": map[string]any{"maxChecks": 5000},
		})
		rr.Equal(http.StatusOK, rec.Code, rec.Body.String())

		var written struct {
			Source string `json:"source"`
		}
		rr.NoError(json.Unmarshal(rec.Body.Bytes(), &written))
		rr.Equal(string(models.EntitlementSourceAdmin), written.Source)

		rec = h.doAsService(t, http.MethodPut, h.path(), map[string]any{
			"limits": map[string]any{"maxChecks": 100},
		})
		rr.Equal(http.StatusOK, rec.Code, rec.Body.String())

		var pushed struct {
			Applied bool `json:"applied"`
			Limits  struct {
				MaxChecks *int `json:"maxChecks"`
			} `json:"limits"`
		}
		rr.NoError(json.Unmarshal(rec.Body.Bytes(), &pushed))
		rr.False(pushed.Applied)
		rr.NotNil(pushed.Limits.MaxChecks)
		rr.Equal(5000, *pushed.Limits.MaxChecks)
	})

	r.True(true) // the subtests carry the assertions
}
