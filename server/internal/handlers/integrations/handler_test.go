package integrations_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
)

// handlerTestEnv bundles a router wired to a real integrations handler plus the
// org it is scoped to, for exercising the HTTP-level admin guard.
type handlerTestEnv struct {
	router *bunrouter.Router
	svc    *integrations.Service
	org    *models.Organization
}

// newHandlerTestEnv builds an in-memory integrations handler + router. Routes
// are registered exactly as in server.go (RequireAuth is simulated by the test
// injecting claims/org into the request context directly).
func newHandlerTestEnv(t *testing.T) *handlerTestEnv {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	org := models.NewOrganization("integ-handler-test", "Integ Handler Test Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	svc := integrations.NewService(dbSvc, creds, nil, nil)
	handler := integrations.NewHandler(svc, &config.Config{})

	router := bunrouter.New()
	group := router.NewGroup("/api/v1/orgs/:org/integrations")
	group.POST("", handler.CreateIntegration)
	group.GET("/:uid", handler.GetIntegration)
	group.PATCH("/:uid", handler.UpdateIntegration)
	group.DELETE("/:uid", handler.DeleteIntegration)

	return &handlerTestEnv{router: router, svc: svc, org: org}
}

// do issues a request to an integrations path with the given role in the JWT
// claims and the org resolved into context (as RequireAuth/RequireOrgAccess
// would). An empty role means "no claims" (unauthenticated context).
func (e *handlerTestEnv) do(
	t *testing.T, role, method, path string, body any,
) *httptest.ResponseRecorder {
	t.Helper()

	r := require.New(t)

	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		r.NoError(err)
	}

	ctx := context.WithValue(t.Context(), base.ContextKeyOrganization, e.org)
	if role != "" {
		ctx = context.WithValue(ctx, base.ContextKeyClaims, &auth.Claims{
			UserUID: "test-user", OrgSlug: e.org.Slug, Role: role,
		})
	}

	req := httptest.NewRequestWithContext(ctx, method, path, bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)

	return rec
}

func (e *handlerTestEnv) basePath() string {
	return "/api/v1/orgs/" + e.org.Slug + "/integrations"
}

// k8sCreateBody is a minimal valid kubernetes cluster-connection create body.
func k8sCreateBody() integrations.CreateIntegrationRequest {
	return integrations.CreateIntegrationRequest{
		Type: "kubernetes",
		Name: "prod-cluster",
		Settings: map[string]any{
			"apiServer": "https://10.0.0.1:6443",
			"token":     "super-secret-token",
		},
	}
}

// TestCreateKubernetesConnectionNonAdminForbidden asserts a non-admin (viewer)
// org member cannot register a kubernetes cluster connection — the spec's
// "non-admin cannot register a connection (403)" acceptance criterion.
func TestCreateKubernetesConnectionNonAdminForbidden(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newHandlerTestEnv(t)

	rec := env.do(t, "viewer", http.MethodPost, env.basePath(), k8sCreateBody())
	r.Equal(http.StatusForbidden, rec.Code)
	r.Contains(rec.Body.String(), string(base.ErrorCodeForbidden))
}

// TestCreateKubernetesConnectionAdminSucceeds asserts the admin happy path for
// registering a kubernetes connection still works after adding the guard.
func TestCreateKubernetesConnectionAdminSucceeds(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newHandlerTestEnv(t)

	rec := env.do(t, "admin", http.MethodPost, env.basePath(), k8sCreateBody())
	r.Equal(http.StatusCreated, rec.Code)
	r.Contains(rec.Body.String(), `"type":"kubernetes"`)
}

// TestDeleteKubernetesConnectionNonAdminForbidden asserts a non-admin cannot
// delete a kubernetes cluster connection (403), while the resource remains.
func TestDeleteKubernetesConnectionNonAdminForbidden(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newHandlerTestEnv(t)

	// Seed the connection via the service so its existence is independent of
	// the create guard under test.
	conn, err := env.svc.CreateIntegration(t.Context(), env.org.Slug, k8sCreateBody())
	r.NoError(err)

	rec := env.do(t, "viewer", http.MethodDelete, env.basePath()+"/"+conn.UID, nil)
	r.Equal(http.StatusForbidden, rec.Code)
	r.Contains(rec.Body.String(), string(base.ErrorCodeForbidden))

	// The connection must still exist.
	_, err = env.svc.GetIntegration(t.Context(), env.org.Slug, conn.UID)
	r.NoError(err)
}

// TestDeleteKubernetesConnectionAdminSucceeds asserts an admin can delete a
// kubernetes cluster connection (204) after the guard.
func TestDeleteKubernetesConnectionAdminSucceeds(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newHandlerTestEnv(t)

	conn, err := env.svc.CreateIntegration(t.Context(), env.org.Slug, k8sCreateBody())
	r.NoError(err)

	rec := env.do(t, "admin", http.MethodDelete, env.basePath()+"/"+conn.UID, nil)
	r.Equal(http.StatusNoContent, rec.Code)
}

// TestCreateNotificationChannelNonAdminAllowed guards the boundary: notification
// channels (here a webhook) stay self-service, so a non-admin can still create
// one. The admin guard must apply only to source-type (cluster) connections.
func TestCreateNotificationChannelNonAdminAllowed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newHandlerTestEnv(t)

	body := integrations.CreateIntegrationRequest{
		Type:     "webhook",
		Name:     "hook",
		Settings: map[string]any{"webhook_url": "https://example.com/hook"},
	}
	rec := env.do(t, "viewer", http.MethodPost, env.basePath(), body)
	r.Equal(http.StatusCreated, rec.Code)
	r.Contains(rec.Body.String(), `"type":"webhook"`)
}

// TestDeleteNotificationChannelNonAdminAllowed mirrors the create boundary for
// delete: a non-admin can delete their own notification channel.
func TestDeleteNotificationChannelNonAdminAllowed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newHandlerTestEnv(t)

	conn, err := env.svc.CreateIntegration(t.Context(), env.org.Slug, integrations.CreateIntegrationRequest{
		Type:     "webhook",
		Name:     "hook",
		Settings: map[string]any{"webhook_url": "https://example.com/hook"},
	})
	r.NoError(err)

	rec := env.do(t, "viewer", http.MethodDelete, env.basePath()+"/"+conn.UID, nil)
	r.Equal(http.StatusNoContent, rec.Code)
}
