package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

func newPasskeyCoverHandler(t *testing.T, f *coverFixture, enabled bool) *PasskeyHandler {
	t.Helper()

	baseURL := "https://example.com"
	if !enabled {
		baseURL = "http://plain.example.com"
	}

	cfg := &config.Config{
		Auth: config.AuthConfig{
			JWTSecret: "test-jwt-secret",
			WebAuthn:  config.WebAuthnConfig{Enabled: true},
		},
		Server: config.ServerConfig{BaseURL: baseURL},
	}
	authSvc := NewService(f.svc.db, cfg.Auth, cfg, nil, nil)

	return NewPasskeyHandler(NewPasskeyService(authSvc, f.svc.db), base.NewHandlerBase(cfg))
}

func passkeyDo(
	t *testing.T, ctx context.Context, fn func(http.ResponseWriter, *http.Request) error, //nolint:revive // test helper
	claims *Claims, params map[string]string, body string,
) *httptest.ResponseRecorder {
	t.Helper()

	if claims != nil {
		ctx = context.WithValue(ctx, base.ContextKeyClaims, claims)
	}

	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}

	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/x", strings.NewReader(body))
	rec := httptest.NewRecorder()
	require.NoError(t, fn(rec, req))

	return rec
}

func TestPasskeyHandlerGatesAndErrors(t *testing.T) { //nolint:tparallel // subtests share one fixture
	t.Parallel()

	f := newCoverFixture(t)
	enabled := newPasskeyCoverHandler(t, f, true)
	disabled := newPasskeyCoverHandler(t, f, false)

	cases := []struct {
		name   string
		fn     func(http.ResponseWriter, *http.Request) error
		claims *Claims
		params map[string]string
		body   string
		want   int
	}{
		{"register begin unauth", enabled.RegisterBegin, nil, nil, "", http.StatusUnauthorized},
		{"register begin ok", enabled.RegisterBegin, f.admin, nil, "", http.StatusOK},
		{"register begin disabled", disabled.RegisterBegin, f.admin, nil, "", http.StatusServiceUnavailable},
		{"register finish unauth", enabled.RegisterFinish, nil, nil, "", http.StatusUnauthorized},
		{"register finish bad json", enabled.RegisterFinish, f.admin, nil, "{", http.StatusUnprocessableEntity},
		{"register finish missing", enabled.RegisterFinish, f.admin, nil, `{}`, http.StatusUnprocessableEntity},
		{
			"register finish bad session", enabled.RegisterFinish, f.admin, nil,
			`{"session":"junk","credential":{}}`, http.StatusUnauthorized,
		},
		{"login begin bad json", enabled.LoginBegin, nil, nil, "{", http.StatusUnprocessableEntity},
		{"login begin empty body", enabled.LoginBegin, nil, nil, "", http.StatusOK},
		{"login begin unknown email", enabled.LoginBegin, nil, nil, `{"email":"ghost@acme.com"}`, http.StatusOK},
		{"login begin known email", enabled.LoginBegin, nil, nil, `{"email":"admin@acme.com"}`, http.StatusOK},
		{"login begin disabled", disabled.LoginBegin, nil, nil, "", http.StatusServiceUnavailable},
		{"login finish bad json", enabled.LoginFinish, nil, nil, "{", http.StatusUnprocessableEntity},
		{"login finish missing", enabled.LoginFinish, nil, nil, `{}`, http.StatusUnprocessableEntity},
		{
			"login finish bad session", enabled.LoginFinish, nil, nil,
			`{"session":"junk","credential":{}}`, http.StatusUnauthorized,
		},
		{"list unauth", enabled.List, nil, nil, "", http.StatusUnauthorized},
		{"list ok", enabled.List, f.admin, nil, "", http.StatusOK},
		{"rename unauth", enabled.Rename, nil, nil, "", http.StatusUnauthorized},
		{"rename no uid", enabled.Rename, f.admin, nil, "", http.StatusBadRequest},
		{"rename bad json", enabled.Rename, f.admin, map[string]string{"uid": "x"}, "{", http.StatusUnprocessableEntity},
		{"rename missing", enabled.Rename, f.admin, map[string]string{"uid": "x"}, `{"name":"n"}`, http.StatusNotFound},
		{"delete unauth", enabled.Delete, nil, nil, "", http.StatusUnauthorized},
		{"delete no uid", enabled.Delete, f.admin, nil, "", http.StatusBadRequest},
		{"delete missing", enabled.Delete, f.admin, map[string]string{"uid": "x"}, "", http.StatusNotFound},
	}

	for _, tc := range cases { //nolint:paralleltest // subtests share one fixture and run in order
		t.Run(tc.name, func(t *testing.T) {
			rec := passkeyDo(t, f.ctx, tc.fn, tc.claims, tc.params, tc.body)
			require.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
}

func TestPasskeyHandlerRowLifecycle(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newCoverFixture(t)
	h := newPasskeyCoverHandler(t, f, true)

	aaguid := "0acf3011-bdb4-4757-8c34-9d6da9bd1936"
	row := models.NewUserPasskey(f.member.UserUID, "Laptop", []byte("cred-1"), []byte("pub"))
	row.AAGUID = &aaguid
	r.NoError(f.svc.db.CreateUserPasskey(f.ctx, row))

	rec := passkeyDo(t, f.ctx, h.List, f.member, nil, "")
	r.Equal(http.StatusOK, rec.Code)
	r.Contains(rec.Body.String(), "Laptop")
	r.Contains(rec.Body.String(), "Apple Passkey (iOS)")

	// Another user cannot rename it.
	rec = passkeyDo(t, f.ctx, h.Rename, f.admin, map[string]string{"uid": row.UID}, `{"name":"x"}`)
	r.Equal(http.StatusNotFound, rec.Code)

	// Blank name is refused.
	rec = passkeyDo(t, f.ctx, h.Rename, f.member, map[string]string{"uid": row.UID}, `{"name":"  "}`)
	r.Equal(http.StatusUnauthorized, rec.Code)

	rec = passkeyDo(t, f.ctx, h.Rename, f.member, map[string]string{"uid": row.UID}, `{"name":"Desk"}`)
	r.Equal(http.StatusOK, rec.Code)

	rec = passkeyDo(t, f.ctx, h.Delete, f.member, map[string]string{"uid": row.UID}, "")
	r.Equal(http.StatusOK, rec.Code)

	// Passwordless user with a single passkey cannot delete it.
	user := models.NewUser("nopw@acme.com")
	r.NoError(f.svc.db.CreateUser(f.ctx, user))
	only := models.NewUserPasskey(user.UID, "Key", []byte("cred-2"), []byte("pub"))
	r.NoError(f.svc.db.CreateUserPasskey(f.ctx, only))

	rec = passkeyDo(t, f.ctx, h.Delete, &Claims{UserUID: user.UID}, map[string]string{"uid": only.UID}, "")
	r.Equal(http.StatusConflict, rec.Code)

	// With a second passkey it can.
	second := models.NewUserPasskey(user.UID, "Key2", []byte("cred-3"), []byte("pub"))
	r.NoError(f.svc.db.CreateUserPasskey(f.ctx, second))

	rec = passkeyDo(t, f.ctx, h.Delete, &Claims{UserUID: user.UID}, map[string]string{"uid": only.UID}, "")
	r.Equal(http.StatusOK, rec.Code)

	// Login begin for a user with a passkey exercises the allow-list path.
	rec = passkeyDo(t, f.ctx, h.LoginBegin, nil, nil, `{"email":"nopw@acme.com"}`)
	r.Equal(http.StatusOK, rec.Code)
}

func TestAAGUIDLabel(t *testing.T) {
	t.Parallel()

	require.Equal(t, AAGUIDLabelSecurityKey, aaguidLabel(""))
	require.Equal(t, AAGUIDLabelSecurityKey, aaguidLabel("unknown"))
	require.Equal(t, "YubiKey Bio", aaguidLabel("83c47309-aabb-4108-8470-8be838b573cb"))
}

func TestMembershipRequestHandlers(t *testing.T) { //nolint:tparallel // subtests share one fixture
	t.Parallel()

	f := newCoverFixture(t)
	h := f.h
	orgP := map[string]string{"org": f.org}
	uidP := map[string]string{"org": f.org, "uid": "nope"}

	cases := []struct {
		name string
		call coverCall
		want int
	}{
		{"create unauth", coverCall{fn: h.CreateMembershipRequestHandler}, http.StatusUnauthorized},
		{
			"create bad json",
			coverCall{fn: h.CreateMembershipRequestHandler, claims: f.member, body: "{"},
			http.StatusUnprocessableEntity,
		},
		{
			"create no slug",
			coverCall{fn: h.CreateMembershipRequestHandler, claims: f.member, body: `{}`},
			http.StatusUnprocessableEntity,
		},
		{"create ghost org", coverCall{
			fn: h.CreateMembershipRequestHandler, claims: f.member,
			body: `{"orgSlug":"ghost"}`,
		}, http.StatusNotFound},
		{"create already member", coverCall{
			fn: h.CreateMembershipRequestHandler, claims: f.member,
			body: `{"orgSlug":"hcov"}`,
		}, http.StatusConflict},
		{"list own unauth", coverCall{fn: h.ListOwnMembershipRequestsHandler}, http.StatusUnauthorized},
		{"list own ok", coverCall{fn: h.ListOwnMembershipRequestsHandler, claims: f.member}, http.StatusOK},
		{"cancel unauth", coverCall{fn: h.CancelMembershipRequestHandler}, http.StatusUnauthorized},
		{"cancel missing", coverCall{
			fn: h.CancelMembershipRequestHandler, claims: f.member,
			params: map[string]string{"uid": "nope"},
		}, http.StatusNotFound},
		{"list org unauth", coverCall{fn: h.ListOrgMembershipRequestsHandler}, http.StatusUnauthorized},
		{
			"list org non admin",
			coverCall{fn: h.ListOrgMembershipRequestsHandler, claims: f.member, params: orgP},
			http.StatusForbidden,
		},
		{"list org ok", coverCall{
			fn: h.ListOrgMembershipRequestsHandler, claims: f.admin, params: orgP,
			query: "status=pending",
		}, http.StatusOK},
		{"list org ghost", coverCall{
			fn: h.ListOrgMembershipRequestsHandler, claims: f.admin,
			params: map[string]string{"org": "ghost"},
		}, http.StatusNotFound},
		{"approve unauth", coverCall{fn: h.ApproveMembershipRequestHandler}, http.StatusUnauthorized},
		{
			"approve non admin",
			coverCall{fn: h.ApproveMembershipRequestHandler, claims: f.member, params: uidP},
			http.StatusForbidden,
		},
		{
			"approve missing",
			coverCall{fn: h.ApproveMembershipRequestHandler, claims: f.admin, params: uidP},
			http.StatusNotFound,
		},
		{"reject unauth", coverCall{fn: h.RejectMembershipRequestHandler}, http.StatusUnauthorized},
		{
			"reject non admin",
			coverCall{fn: h.RejectMembershipRequestHandler, claims: f.member, params: uidP},
			http.StatusForbidden,
		},
		{
			"reject missing",
			coverCall{fn: h.RejectMembershipRequestHandler, claims: f.admin, params: uidP},
			http.StatusNotFound,
		},
	}

	for _, tc := range cases { //nolint:paralleltest // subtests share one fixture and run in order
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do(t, tc.call)
			require.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
}
