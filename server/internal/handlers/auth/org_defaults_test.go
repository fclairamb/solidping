package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// setupOrgDefaultsTest wires up an auth.Service against an in-memory sqlite
// db, exactly like auth's own internal setupAuthTestService — duplicated
// here because this file lives in package auth_test (checks, which is needed
// to exercise the auto-attach handshake, transitively imports auth via
// middleware, so this suite cannot be package auth without an import cycle).
func setupOrgDefaultsTest(t *testing.T, dbService db.Service) (*auth.Service, context.Context) {
	t.Helper()

	ctx := t.Context()

	authCfg := config.AuthConfig{
		JWTSecret:          "test-jwt-secret",
		AccessTokenExpiry:  0,
		RefreshTokenExpiry: 0,
	}
	fullCfg := &config.Config{Auth: authCfg}

	return auth.NewService(dbService, authCfg, fullCfg, nil, nil), ctx
}

func newOrgDefaultsTestDB(t *testing.T) *sqlite.Service {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	return dbSvc
}

// toRecipients normalizes a JSONMap["to"] value read back from either
// dialect: sqlite/postgres jsonb decoding yields []any, but a value written
// in-process (never round-tripped) stays []string.
func toRecipients(t *testing.T, raw any) []string {
	t.Helper()

	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			require.True(t, ok, "recipient element must be a string, got %T", item)
			out = append(out, s)
		}

		return out
	default:
		t.Fatalf("unexpected type for recipients: %T", raw)

		return nil
	}
}

// TestCreateOrgSeedsDefaultEmailIntegrationAndWeeklyReport pins the core of
// spec 2026-08-28-15: a self-created org gets exactly one enabled, isDefault
// email integration addressed to the owner, and exactly one enabled,
// org-wide weekly report schedule with the owner as recipient.
func TestCreateOrgSeedsDefaultEmailIntegrationAndWeeklyReport(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	dbService := newOrgDefaultsTestDB(t)
	svc, ctx := setupOrgDefaultsTest(t, dbService)

	user := models.NewUser("owner@acme.com")
	r.NoError(dbService.CreateUser(ctx, user))

	resp, err := svc.CreateOrg(ctx, user.UID, auth.CreateOrgRequest{Name: "Acme Co", Slug: "acme-co"}, auth.Context{})
	r.NoError(err)

	integrations, err := dbService.ListChannels(ctx, &models.ListIntegrationsFilter{OrganizationUID: resp.UID})
	r.NoError(err)
	r.Len(integrations, 1)

	integration := integrations[0]
	r.Equal(models.ConnectionTypeEmail, integration.Type)
	r.True(integration.Enabled)
	r.True(integration.IsDefault)
	r.Equal([]string{user.Email}, toRecipients(t, integration.Settings["to"]))

	schedules, err := dbService.ListReportSchedules(ctx, resp.UID)
	r.NoError(err)
	r.Len(schedules, 1)

	schedule := schedules[0]
	r.Equal(models.ReportFrequencyWeekly, schedule.Frequency)
	r.True(schedule.Enabled)
	r.True(schedule.IsOrgWide())
	r.Equal([]string{user.Email}, schedule.Recipients)

	// The seeded integration must be indistinguishable from a hand-created
	// one in the events feed: an integration.created audit event exists for
	// it.
	events, err := dbService.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: resp.UID,
		EventTypes:      []models.EventType{models.EventTypeIntegrationCreated},
	})
	r.NoError(err)
	r.Len(events, 1)
}

// TestNewOrgCheckGetsSeededIntegrationAttached proves the auto-attach
// handshake end to end: a check created in the freshly-seeded org comes out
// with the default email integration already attached, via the existing
// isDefault channel mechanism (checks/service.go's ListDefaultChannels).
func TestNewOrgCheckGetsSeededIntegrationAttached(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	dbService := newOrgDefaultsTestDB(t)
	svc, ctx := setupOrgDefaultsTest(t, dbService)

	user := models.NewUser("owner@acme.com")
	r.NoError(dbService.CreateUser(ctx, user))

	resp, err := svc.CreateOrg(ctx, user.UID, auth.CreateOrgRequest{Name: "Acme Co", Slug: "acme-co"}, auth.Context{})
	r.NoError(err)

	integrations, err := dbService.ListChannels(ctx, &models.ListIntegrationsFilter{OrganizationUID: resp.UID})
	r.NoError(err)
	r.Len(integrations, 1)
	seededIntegration := integrations[0]

	creds, err := credentials.NewService(nil, nil)
	r.NoError(err)
	checksSvc := checks.NewService(dbService, nil, creds, nil)

	checkResp, err := checksSvc.CreateCheck(ctx, resp.Slug, checks.CreateCheckRequest{
		Name: "Homepage",
		Slug: "homepage",
		Type: "http",
		Config: map[string]any{
			"url": "https://acme.com",
		},
	})
	r.NoError(err)

	attached, err := dbService.ListChannelsForCheck(ctx, checkResp.UID)
	r.NoError(err)
	r.Len(attached, 1)
	r.Equal(seededIntegration.UID, attached[0].UID)
}

// TestOrgsCreatedOutsideCreateOrgGetNoSeededDefaults pins the exclusions:
// the bootstrap default org (job_startup.go's ensureDefaultOrganization) and
// the test-mode fixtures (testdata.go) both create organizations by writing
// the row directly via db.CreateOrganization, never through CreateOrg — so
// they must never pick up a seeded integration or report schedule. This
// exercises that exact code path rather than CreateOrg.
func TestOrgsCreatedOutsideCreateOrgGetNoSeededDefaults(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	dbService := newOrgDefaultsTestDB(t)
	ctx := t.Context()

	org := models.NewOrganization("default", "Default Org")
	r.NoError(dbService.CreateOrganization(ctx, org))

	integrations, err := dbService.ListChannels(ctx, &models.ListIntegrationsFilter{OrganizationUID: org.UID})
	r.NoError(err)
	r.Empty(integrations)

	schedules, err := dbService.ListReportSchedules(ctx, org.UID)
	r.NoError(err)
	r.Empty(schedules)
}

// failingUserLookupDB wraps a real db.Service and forces GetUser to fail, to
// exercise the best-effort seed contract without disturbing anything else
// CreateOrg does.
type failingUserLookupDB struct {
	db.Service
}

func (f *failingUserLookupDB) GetUser(_ context.Context, _ string) (*models.User, error) {
	return nil, errors.New("forced failure: user lookup")
}

// failingCreateChannelDB wraps a real db.Service and forces the integration
// insert to fail, so the report schedule seed can be observed succeeding on
// its own.
type failingCreateChannelDB struct {
	db.Service
}

func (f *failingCreateChannelDB) CreateChannel(_ context.Context, _ *models.Integration) error {
	return errors.New("forced failure: create channel")
}

// TestCreateOrgSurvivesSeedFailure pins the best-effort posture: a signup
// that 500s because a convenience row could not be written would be strictly
// worse than the org staying silent, so any failure in the default-alerting
// seed must be swallowed and CreateOrg must still return a successful
// OrgResponse.
func TestCreateOrgSurvivesSeedFailure(t *testing.T) {
	t.Parallel()

	t.Run("owner lookup fails", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		dbService := newOrgDefaultsTestDB(t)

		user := models.NewUser("owner@acme.com")
		r.NoError(dbService.CreateUser(t.Context(), user))

		failingDB := &failingUserLookupDB{Service: dbService}
		svc, ctx := setupOrgDefaultsTest(t, failingDB)

		resp, err := svc.CreateOrg(ctx, user.UID, auth.CreateOrgRequest{Name: "Acme Co", Slug: "acme-co"}, auth.Context{})
		r.NoError(err)
		r.NotEmpty(resp.UID)

		// Neither row was seeded, since the owner's email could not be
		// resolved — nothing to attach it to.
		integrations, err := dbService.ListChannels(ctx, &models.ListIntegrationsFilter{OrganizationUID: resp.UID})
		r.NoError(err)
		r.Empty(integrations)

		schedules, err := dbService.ListReportSchedules(ctx, resp.UID)
		r.NoError(err)
		r.Empty(schedules)
	})

	t.Run("integration insert fails, report schedule still seeded", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		dbService := newOrgDefaultsTestDB(t)

		user := models.NewUser("owner@acme.com")
		r.NoError(dbService.CreateUser(t.Context(), user))

		failingDB := &failingCreateChannelDB{Service: dbService}
		svc, ctx := setupOrgDefaultsTest(t, failingDB)

		resp, err := svc.CreateOrg(ctx, user.UID, auth.CreateOrgRequest{Name: "Acme Co", Slug: "acme-co"}, auth.Context{})
		r.NoError(err)
		r.NotEmpty(resp.UID)

		integrations, err := dbService.ListChannels(ctx, &models.ListIntegrationsFilter{OrganizationUID: resp.UID})
		r.NoError(err)
		r.Empty(integrations)

		// The report schedule seed is independent of the integration seed —
		// one failing must not take the other down with it.
		schedules, err := dbService.ListReportSchedules(ctx, resp.UID)
		r.NoError(err)
		r.Len(schedules, 1)
	})
}
