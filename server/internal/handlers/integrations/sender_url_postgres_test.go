package integrations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// Ports for the sender-URL embedded-Postgres suite (spec 2026-09-25-20), one
// per test function so they can run in parallel like every other
// multi-function _postgres_test.go in the repo (see region_migration_postgres_test.go).
// Distinct from every other embedded-Postgres port claimed in the repo (see
// the port-numbering note in internal/db/incident_number_test.go).
const (
	portSenderURLCreateRejectPG = 15551
	portSenderURLCreateAcceptPG = 15552
	portSenderURLUpdateRejectPG = 15553
	portSenderURLUpdateAcceptPG = 15554
)

// newPostgresSenderURLSvc is newGuardedSenderURLSvc (sender_url_test.go)
// against a real embedded Postgres instead of SQLite — the spec's "both DBs"
// requirement for CRUD sender-URL validation. The business logic is
// DB-agnostic (db.Service interface, no engine-specific SQL in the validated
// path), but the repo's convention is a real-engine twin regardless; this is
// it. Self-skips under -short, mirroring every other _postgres_test.go.
func newPostgresSenderURLSvc(
	t *testing.T, port uint32, guard *egress.Guard,
) (*integrations.Service, *models.Organization, context.Context) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     port,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	slug := "pg" + sanitizeSlug(t.Name())
	org := models.NewOrganization(slug, "Sender URL PG Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	svc := integrations.NewService(dbSvc, creds, &services.Registry{EgressGuard: guard}, &config.Config{})

	return svc, org, ctx
}

// TestCreateIntegration_RejectsNonPublicSenderURL_Postgres is the Postgres
// twin of TestCreateIntegration_RejectsNonPublicSenderURL: creating a webhook
// with a metadata-endpoint URL fails closed under an enforcing guard and
// never reaches storage.
func TestCreateIntegration_RejectsNonPublicSenderURL_Postgres(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	guard := egress.New(false) // enforcing: private/loopback/metadata denied
	svc, org, ctx := newPostgresSenderURLSvc(t, portSenderURLCreateRejectPG, guard)

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

// TestCreateIntegration_AcceptsPublicSenderURL_Postgres is the Postgres
// control: a public https:// webhook URL is unaffected and the row is
// created — the same acceptance criterion as the SQLite test, on the real
// engine.
func TestCreateIntegration_AcceptsPublicSenderURL_Postgres(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	guard := egress.New(false)
	svc, org, ctx := newPostgresSenderURLSvc(t, portSenderURLCreateAcceptPG, guard)

	created, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type:     "webhook",
		Name:     "hook",
		Settings: map[string]any{"url": "https://example.com/hook"},
	})
	r.NoError(err)
	r.NotNil(created)
}

// TestUpdateIntegration_RejectsNonPublicSenderURL_Postgres is the Postgres
// twin of the update-path rejection: a webhook created with a public URL,
// then PATCHed to a loopback target, is rejected and the stored URL is left
// unchanged.
func TestUpdateIntegration_RejectsNonPublicSenderURL_Postgres(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	guard := egress.New(false)
	svc, org, ctx := newPostgresSenderURLSvc(t, portSenderURLUpdateRejectPG, guard)

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

// TestUpdateIntegration_AcceptsPublicSenderURL_Postgres is the Postgres
// control for the update-time happy path.
func TestUpdateIntegration_AcceptsPublicSenderURL_Postgres(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	guard := egress.New(false)
	svc, org, ctx := newPostgresSenderURLSvc(t, portSenderURLUpdateAcceptPG, guard)

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
