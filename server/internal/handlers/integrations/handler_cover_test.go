package integrations_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/integrations/slack"
)

type coverEnv struct {
	*identityFixture
	router  *httpx.Router
	handler *integrations.Handler
}

func newCoverEnv(t *testing.T, slug string) *coverEnv {
	t.Helper()

	fix := newIdentityFixture(t.Context(), t, slug)
	handler := integrations.NewHandler(fix.svc, &config.Config{})

	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/integrations")
	group.GET("", handler.ListIntegrations)
	group.POST("", handler.CreateIntegration)
	group.GET("/:uid", handler.GetIntegration)
	group.PATCH("/:uid", handler.UpdateIntegration)
	group.DELETE("/:uid", handler.DeleteIntegration)
	group.POST("/:uid/rotate-secret", handler.RotateWebhookSecret)
	group.POST("/:uid/test", handler.TestIntegration)
	group.GET("/:uid/identities", handler.ListIdentities)
	group.POST("/:uid/identities/sync", handler.SyncIdentities)
	group.PUT("/:uid/identities/:userUid", handler.SetIdentity)
	group.DELETE("/:uid/identities/:userUid", handler.DeleteIdentity)

	return &coverEnv{identityFixture: fix, router: router, handler: handler}
}

func (e *coverEnv) do(t *testing.T, role, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	ctx := context.WithValue(t.Context(), base.ContextKeyOrganization, e.org)
	if role != "" {
		ctx = context.WithValue(ctx, base.ContextKeyClaims, &auth.Claims{
			UserUID: "test-user", OrgSlug: e.org.Slug, Role: role,
		})
	}

	req := httptest.NewRequestWithContext(ctx, method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)

	return rec
}

func (e *coverEnv) base() string { return "/api/v1/orgs/" + e.org.Slug + "/integrations" }

func TestHandlerListAndGetIntegration(t *testing.T) {
	t.Parallel()

	env := newCoverEnv(t, "cover-list")

	tests := []struct {
		name   string
		path   string
		code   int
		expect string
	}{
		{"list all", env.base(), http.StatusOK, env.conn.UID},
		{"list filtered by type", env.base() + "?type=slack", http.StatusOK, env.conn.UID},
		{"list filtered no match", env.base() + "?type=webhook", http.StatusOK, ""},
		{"get existing", env.base() + "/" + env.conn.UID, http.StatusOK, "Acme Slack"},
		{"get unknown", env.base() + "/nope", http.StatusNotFound, "INTEGRATION_NOT_FOUND"},
		{"list unknown org", "/api/v1/orgs/nope/integrations", http.StatusNotFound, "ORGANIZATION_NOT_FOUND"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := env.do(t, "admin", http.MethodGet, tt.path, "")
			require.Equal(t, tt.code, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), tt.expect)
		})
	}
}

func TestHandlerUpdateIntegration(t *testing.T) {
	t.Parallel()

	env := newCoverEnv(t, "cover-update")

	tests := []struct {
		name string
		role string
		path string
		body string
		code int
	}{
		{"rename", "admin", env.base() + "/" + env.conn.UID, `{"name":"Renamed Slack"}`, http.StatusOK},
		{"bad json", "admin", env.base() + "/" + env.conn.UID, `{`, http.StatusUnprocessableEntity},
		{"unknown", "admin", env.base() + "/nope", `{"name":"x"}`, http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := env.do(t, tt.role, http.MethodPatch, tt.path, tt.body)
			require.Equal(t, tt.code, rec.Code, rec.Body.String())
		})
	}
}

func TestHandlerUpdateSourceIntegrationRequiresAdmin(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newCoverEnv(t, "cover-update-k8s")

	created, err := env.svc.CreateIntegration(t.Context(), env.org.Slug, k8sCreateBody())
	r.NoError(err)

	path := env.base() + "/" + created.UID

	r.Equal(http.StatusForbidden, env.do(t, "viewer", http.MethodPatch, path, `{"name":"x"}`).Code)
	r.Equal(http.StatusForbidden, env.do(t, "", http.MethodPatch, path, `{"name":"x"}`).Code)
	r.Equal(http.StatusOK, env.do(t, "admin", http.MethodPatch, path, `{"name":"renamed"}`).Code)
	r.Equal(http.StatusNoContent, env.do(t, "admin", http.MethodDelete, path, "").Code)
	r.Equal(http.StatusNotFound, env.do(t, "admin", http.MethodDelete, path, "").Code)
}

func TestHandlerCreateIntegrationErrors(t *testing.T) {
	t.Parallel()

	env := newCoverEnv(t, "cover-create")

	tests := []struct {
		name string
		body string
		code int
	}{
		{"bad json", `{`, http.StatusUnprocessableEntity},
		{"invalid type", `{"type":"carrier-pigeon","name":"x"}`, http.StatusUnprocessableEntity},
		{"slack manual create", `{"type":"slack","name":"x"}`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := env.do(t, "admin", http.MethodPost, env.base(), tt.body)
			require.Equal(t, tt.code, rec.Code, rec.Body.String())
		})
	}
}

func TestHandlerWebhookRotateAndTest(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newCoverEnv(t, "cover-rotate")

	created, err := env.svc.CreateIntegration(t.Context(), env.org.Slug, integrations.CreateIntegrationRequest{
		Type:     "webhook",
		Name:     "hook",
		Settings: map[string]any{"url": "https://example.com/hook"},
	})
	r.NoError(err)

	rec := env.do(t, "admin", http.MethodPost, env.base()+"/"+created.UID+"/rotate-secret", "")
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	r.Contains(rec.Body.String(), "whsec_")

	// Rotating a non-webhook integration is a validation error.
	rec = env.do(t, "admin", http.MethodPost, env.base()+"/"+env.conn.UID+"/rotate-secret", "")
	r.Equal(http.StatusBadRequest, rec.Code, rec.Body.String())

	rec = env.do(t, "admin", http.MethodPost, env.base()+"/nope/rotate-secret", "")
	r.Equal(http.StatusNotFound, rec.Code)

	// Testing an unknown integration, and a data-source-only (freebox) one.
	rec = env.do(t, "admin", http.MethodPost, env.base()+"/nope/test", "")
	r.Equal(http.StatusNotFound, rec.Code)

	freebox := models.NewIntegration(env.org.UID, models.ConnectionTypeFreebox, "box")
	r.NoError(env.dbSvc.CreateChannel(t.Context(), freebox))

	rec = env.do(t, "admin", http.MethodPost, env.base()+"/"+freebox.UID+"/test", "")
	r.Equal(http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestHandlerIdentities(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newCoverEnv(t, "cover-ident")
	ctx := t.Context()

	alice := env.addMember(ctx, t, "alice@acme.com", "Alice")
	bob := env.addMember(ctx, t, "bob@acme.com", "Bob")
	env.lookup.byEmail["alice@acme.com"] = &slack.SlackUser{ID: "U1"}

	identBase := env.base() + "/" + env.conn.UID + "/identities"

	rec := env.do(t, "admin", http.MethodGet, identBase, "")
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	rec = env.do(t, "admin", http.MethodPost, identBase+"/sync", "")
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	r.Contains(rec.Body.String(), "U1")

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		code   int
	}{
		{"set ok", http.MethodPut, identBase + "/" + bob.UID, `{"externalId":"U2","displayName":"Bobby"}`, http.StatusOK},
		{"set bad json", http.MethodPut, identBase + "/" + bob.UID, `{`, http.StatusUnprocessableEntity},
		{"set missing external id", http.MethodPut, identBase + "/" + bob.UID, `{}`, http.StatusUnprocessableEntity},
		{"set empty body", http.MethodPut, identBase + "/" + bob.UID, ``, http.StatusUnprocessableEntity},
		{"set non member", http.MethodPut, identBase + "/ghost", `{"externalId":"U3"}`, http.StatusNotFound},
		{"set claimed", http.MethodPut, identBase + "/" + alice.UID + "x", `{"externalId":"U2"}`, http.StatusNotFound},
		{"delete ok", http.MethodDelete, identBase + "/" + bob.UID, "", http.StatusNoContent},
		{"delete idempotent", http.MethodDelete, identBase + "/" + bob.UID, "", http.StatusNoContent},
		{"list unknown integration", http.MethodGet, env.base() + "/nope/identities", "", http.StatusNotFound},
		{"sync unknown integration", http.MethodPost, env.base() + "/nope/identities/sync", "", http.StatusNotFound},
		{"delete unknown integration", http.MethodDelete, env.base() + "/nope/identities/x", "", http.StatusNotFound},
	}

	for _, tt := range tests {
		rec := env.do(t, "admin", tt.method, tt.path, tt.body)
		r.Equal(tt.code, rec.Code, "%s: %s", tt.name, rec.Body.String())
	}

	// Claim conflict: alice owns U1 after sync.
	rec = env.do(t, "admin", http.MethodPut, identBase+"/"+bob.UID, `{"externalId":"U1"}`)
	r.Equal(http.StatusConflict, rec.Code, rec.Body.String())
}

func TestHandlerIdentitiesUnsupportedAndDisconnected(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newCoverEnv(t, "cover-ident-bad")
	ctx := t.Context()

	hook := models.NewIntegration(env.org.UID, models.ConnectionTypeWebhook, "hook")
	r.NoError(env.dbSvc.CreateChannel(ctx, hook))

	rec := env.do(t, "admin", http.MethodGet, env.base()+"/"+hook.UID+"/identities", "")
	r.Equal(http.StatusBadRequest, rec.Code, rec.Body.String())

	empty := models.NewIntegration(env.org.UID, models.ConnectionTypeSlack, "Empty Slack")
	r.NoError(env.dbSvc.CreateChannel(ctx, empty))

	rec = env.do(t, "admin", http.MethodPost, env.base()+"/"+empty.UID+"/identities/sync", "")
	r.Equal(http.StatusConflict, rec.Code, rec.Body.String())
}

func TestHandlerErrorMappings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		code int
	}{
		{"org", integrations.ErrOrganizationNotFound, http.StatusNotFound},
		{"connection", integrations.ErrConnectionNotFound, http.StatusNotFound},
		{"type", integrations.ErrInvalidConnectionType, http.StatusUnprocessableEntity},
		{"freebox not pairing", integrations.ErrFreeboxNotPairing, http.StatusConflict},
		{"freebox mismatch", integrations.ErrFreeboxTypeMismatch, http.StatusBadRequest},
		{"not webhook", integrations.ErrNotWebhookChannel, http.StatusBadRequest},
		{"not notifiable", integrations.ErrIntegrationNotNotifiable, http.StatusBadRequest},
		{"slack manual", integrations.ErrSlackManualCreate, http.StatusBadRequest},
		{"teams manual", integrations.ErrMSTeamsBotManualCreate, http.StatusBadRequest},
		{"teams destination", integrations.ErrMSTeamsBotUnknownDestination, http.StatusBadRequest},
		{"pairing failed", integrations.ErrFreeboxPairingFailed, http.StatusBadGateway},
		{"invalid settings", integrations.ErrInvalidSettings, http.StatusBadRequest},
		{"unsupported", integrations.ErrIdentitiesUnsupportedType, http.StatusBadRequest},
		{"slack not connected", integrations.ErrSlackNotConnected, http.StatusConflict},
		{"external id", integrations.ErrIdentityExternalIDRequired, http.StatusUnprocessableEntity},
		{"member", integrations.ErrIdentityMemberNotFound, http.StatusNotFound},
		{"claimed", integrations.ErrIdentityAlreadyClaimed, http.StatusConflict},
		{"wrapped", fmt.Errorf("x: %w", integrations.ErrConnectionNotFound), http.StatusNotFound},
		{"unknown", errors.New("boom"), http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := integrations.NewHandler(nil, &config.Config{})
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)

			require.NoError(t, integrations.HandleIdentityErrorForTest(h, rec, req, tt.err))
			require.Equal(t, tt.code, rec.Code, rec.Body.String())
		})
	}
}
