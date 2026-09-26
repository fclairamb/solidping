package integrations_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// newGuardedSenderURLSvc builds an integrations.Service wired to a
// services.Registry carrying guard, so CRUD validation exercises
// notifications.ValidateSenderURL exactly as production wiring does (spec
// 2026-09-25-20). newWebhookTestSvc/newHandlerTestEnv elsewhere in this
// package pass a nil registry, which makes the sender-URL check a no-op — not
// suitable for this file.
func newGuardedSenderURLSvc(
	t *testing.T, guard *egress.Guard,
) (*integrations.Service, *models.Organization, context.Context) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	slug := "su" + sanitizeSlug(t.Name())
	org := models.NewOrganization(slug, "Sender URL Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	svc := integrations.NewService(dbSvc, creds, &services.Registry{EgressGuard: guard}, &config.Config{})

	return svc, org, ctx
}

// TestCreateIntegration_RejectsNonPublicSenderURL is the spec's CRUD
// acceptance criterion: creating a webhook with a metadata-endpoint URL fails
// closed under an enforcing guard, and never reaches storage.
func TestCreateIntegration_RejectsNonPublicSenderURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	guard := egress.New(false) // enforcing: private/loopback/metadata denied
	svc, org, ctx := newGuardedSenderURLSvc(t, guard)

	_, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type:     "webhook",
		Name:     "hook",
		Settings: map[string]any{"url": "http://169.254.169.254/latest/meta-data"},
	})
	r.ErrorIs(err, integrations.ErrInvalidSettings)

	list, listErr := svc.ListIntegrations(ctx, org.Slug, nil)
	r.NoError(listErr)
	r.Empty(list.Data, "a rejected create must never reach storage")
}

// TestCreateIntegration_AcceptsPublicSenderURL is the create-time happy path:
// a public https:// webhook URL passes validation and the row is created.
func TestCreateIntegration_AcceptsPublicSenderURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	guard := egress.New(false)
	svc, org, ctx := newGuardedSenderURLSvc(t, guard)

	created, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type:     "webhook",
		Name:     "hook",
		Settings: map[string]any{"url": "https://example.com/hook"},
	})
	r.NoError(err)
	r.NotNil(created)
}

// TestUpdateIntegration_RejectsNonPublicSenderURL covers the update path: a
// webhook created with a public URL, then PATCHed to a loopback target, is
// rejected and the stored URL is left unchanged.
func TestUpdateIntegration_RejectsNonPublicSenderURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	guard := egress.New(false)
	svc, org, ctx := newGuardedSenderURLSvc(t, guard)

	created, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type:     "webhook",
		Name:     "hook",
		Settings: map[string]any{"url": "https://example.com/hook"},
	})
	r.NoError(err)

	_, err = svc.UpdateIntegration(ctx, org.Slug, created.UID, integrations.UpdateIntegrationRequest{
		Settings: map[string]any{"url": "http://127.0.0.1:9999/hook"},
	})
	r.ErrorIs(err, integrations.ErrInvalidSettings)

	after, getErr := svc.GetIntegration(ctx, org.Slug, created.UID)
	r.NoError(getErr)
	r.Equal("https://example.com/hook", after.Settings["url"], "rejected PATCH must not persist")
}

// TestUpdateIntegration_AcceptsPublicSenderURL is the update-time happy path.
func TestUpdateIntegration_AcceptsPublicSenderURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	guard := egress.New(false)
	svc, org, ctx := newGuardedSenderURLSvc(t, guard)

	created, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type:     "webhook",
		Name:     "hook",
		Settings: map[string]any{"url": "https://example.com/hook"},
	})
	r.NoError(err)

	updated, err := svc.UpdateIntegration(ctx, org.Slug, created.UID, integrations.UpdateIntegrationRequest{
		Settings: map[string]any{"url": "https://example.com/new-hook"},
	})
	r.NoError(err)
	r.Equal("https://example.com/new-hook", updated.Settings["url"])
}

// TestCreateIntegration_SenderURLAllowedWhenPrivateTargetsAllowed asserts the
// policy escape hatch: with the guard built allow_private=true (self-hosted
// default, or an operator override), a loopback webhook URL is accepted —
// matching the deployment mode every existing sender test already runs under.
func TestCreateIntegration_SenderURLAllowedWhenPrivateTargetsAllowed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	guard := egress.New(true) // self-hosted / allow_private_targets=true
	svc, org, ctx := newGuardedSenderURLSvc(t, guard)

	created, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type:     "webhook",
		Name:     "hook",
		Settings: map[string]any{"url": "http://127.0.0.1:9999/hook"},
	})
	r.NoError(err)
	r.NotNil(created)
}

// TestCreateIntegration_RejectsNonPublicSenderURL_OtherSenderTypes checks the
// mapping from connection type to its URL settings key covers the other five
// sender families the spec names, not just webhook.
func TestCreateIntegration_RejectsNonPublicSenderURL_OtherSenderTypes(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cases := []struct {
		connType string
		settings map[string]any
	}{
		{"gotify", map[string]any{"server_url": "http://169.254.169.254", "app_token": "tok"}},
		{"ntfy", map[string]any{"serverUrl": "http://169.254.169.254", "topic": "alerts"}},
		{"matrix", map[string]any{
			"homeserverUrl": "http://169.254.169.254", "accessToken": "tok", "roomId": "!r:x",
		}},
		{"googlechat", map[string]any{"webhook_url": "http://169.254.169.254/hook"}},
		{"mattermost", map[string]any{"webhook_url": "http://169.254.169.254/hook"}},
	}

	for _, tc := range cases {
		t.Run(tc.connType, func(t *testing.T) {
			t.Parallel()

			guard := egress.New(false)
			svc, org, ctx := newGuardedSenderURLSvc(t, guard)

			_, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
				Type:     tc.connType,
				Name:     tc.connType + "-conn",
				Settings: tc.settings,
			})
			r.ErrorIs(err, integrations.ErrInvalidSettings, tc.connType)
		})
	}
}

// TestCreateIntegrationHTTP_NonPublicSenderURL is the HTTP-layer assertion:
// POST /orgs/:org/integrations with a metadata-endpoint webhook URL answers
// 400 VALIDATION_ERROR, matching the spec's literal acceptance criterion.
func TestCreateIntegrationHTTP_NonPublicSenderURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	ctx := t.Context()
	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	org := models.NewOrganization("sender-url-http-test", "Sender URL HTTP Test Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	svc := integrations.NewService(
		dbSvc, creds, &services.Registry{EgressGuard: egress.New(false)}, &config.Config{})
	handler := integrations.NewHandler(svc, &config.Config{})

	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/integrations")
	group.POST("", handler.CreateIntegration)

	env := &handlerTestEnv{router: router, svc: svc, org: org}

	rec := env.do(t, "admin", http.MethodPost, env.basePath(), integrations.CreateIntegrationRequest{
		Type:     "webhook",
		Name:     "hook",
		Settings: map[string]any{"url": "http://169.254.169.254/latest/meta-data"},
	})

	r.Equal(http.StatusBadRequest, rec.Code)
	r.Contains(rec.Body.String(), string(base.ErrorCodeValidationError))
}

// TestTestIntegration_RejectsNonPublicSenderURL covers a row that predates
// (or was saved under a looser) policy: a webhook is created while the guard
// allows private targets, the operator then tightens the policy, and
// POST .../integrations/:uid/test on the now-disallowed URL must fail with a
// validation error and reach the target zero times — the sender's own
// defensive ValidateSenderURL call, not CRUD, is what catches this case.
func TestTestIntegration_RejectsNonPublicSenderURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var requestsReceived atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestsReceived.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := t.Context()
	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	org := models.NewOrganization("policy-change-org", "Policy Change Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// The same *services.Registry a real process shares between CRUD and
	// delivery — mutating its EgressGuard field below is exactly what
	// tightening SP_EGRESS_ALLOW_PRIVATE (or the system parameter) does at
	// runtime, with no restart.
	reg := &services.Registry{EgressGuard: egress.New(true)} // permissive at creation time
	svc := integrations.NewService(dbSvc, creds, reg, &config.Config{})

	created, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type:     "webhook",
		Name:     "hook",
		Settings: map[string]any{"url": srv.URL},
	})
	r.NoError(err)

	// The operator tightens the policy — same registry a real process would
	// share between CRUD and delivery, so this is exactly what "the policy
	// changes under an existing integration" means in practice.
	reg.EgressGuard = egress.New(false)

	result, testErr := svc.TestIntegration(ctx, org.Slug, created.UID)
	r.NoError(testErr, "TestIntegration reports failure in the result, not as a Go error")
	r.False(result.Success)
	r.NotEmpty(result.Error)

	r.Zero(requestsReceived.Load(), "the target must never receive a request once the policy denies it")
}
