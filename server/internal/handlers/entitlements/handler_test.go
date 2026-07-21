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
