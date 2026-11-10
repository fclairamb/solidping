package checks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/rewraptask"
	"github.com/fclairamb/solidping/server/internal/sealing"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

var errParamInjected = errors.New("injected parameter failure")

// paramFaultDB fails organization parameter reads: every key, or only the
// shared region keys.
type paramFaultDB struct {
	db.Service

	failAll        atomic.Bool
	failRegionKeys atomic.Bool
}

func (p *paramFaultDB) GetOrgParameter(ctx context.Context, orgUID, key string) (*models.Parameter, error) {
	if p.failAll.Load() || (p.failRegionKeys.Load() && key == sealing.ParamKeyRegionKeys) {
		return nil, errParamInjected
	}

	return p.Service.GetOrgParameter(ctx, orgUID, key)
}

// newFaultSealingService is a checks service over a database whose parameter
// reads can be switched to fail, with the sealing regions defined and the peer
// re-wrap wired.
func newFaultSealingService(t *testing.T) (*checks.Service, *paramFaultDB, db.Service, *models.Organization) {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	require.NoError(t, err)

	org := models.NewOrganization("fault-test", "Fault Test Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))
	defineSealRegions(t, dbSvc, org.UID)

	faulty := &paramFaultDB{Service: dbSvc}
	svc := checks.NewService(faulty, nil, creds, nil)
	svc.SetRewrapper(rewraptask.Register(rewraptask.Deps{DB: faulty, Tasks: workertasks.NewService(dbSvc.DB())}))

	return svc, faulty, dbSvc, org
}

func renameTable(t *testing.T, dbSvc db.Service, table string) {
	t.Helper()

	_, err := dbSvc.DB().ExecContext(t.Context(), "ALTER TABLE "+table+" RENAME TO "+table+"_gone")
	require.NoError(t, err)
}

func TestClientSealingValidationErrors(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, faulty, dbSvc, org := newFaultSealingService(t)
	ctx := t.Context()

	keys := enrollSealAgent(t, dbSvc, org.UID, "dc1-agent")
	sealed := clientSeal(t, map[string]any{"basicAuth": "alice:hunter2"}, keys.X25519Recipient)

	importOne := func(doc *checks.ExportDocument) checks.ImportError {
		t.Helper()

		res, err := svc.ImportChecks(ctx, org.Slug, doc, true)
		r.NoError(err)
		r.Len(res.Errors, 1)

		return res.Errors[0]
	}

	// A public region.
	public := sealedDoc(org.Slug, "public", sealed)
	public.Checks[0].Regions = []string{"eu-west-1"}
	r.Contains(importOne(public).Error, "private")

	// A private region without any agent.
	empty := sealedDoc(org.Slug, "empty-region", sealed)
	empty.Checks[0].Regions = []string{"@dc2"}
	r.Contains(importOne(empty).Error, "enroll an agent")

	// Keys that are not secret fields of the type.
	bad := &checks.SealedSecretsRequest{Keys: []string{"nope"}, Fingerprints: sealed.Fingerprints, CT: sealed.CT}
	r.NotEmpty(importOne(sealedDoc(org.Slug, "bad-shape", bad)).Error)

	// The region lookup fails.
	faulty.failAll.Store(true)
	r.Contains(importOne(sealedDoc(org.Slug, "no-lookup", sealed)).Error, "check private region")
	faulty.failAll.Store(false)

	// The shared region keys cannot be read.
	faulty.failRegionKeys.Store(true)
	r.NotEmpty(importOne(sealedDoc(org.Slug, "no-keys", sealed)).Error)
	faulty.failRegionKeys.Store(false)
}

// TestApplySealedSecretsRegionsFromExistingCheck: a manifest entry that omits
// its regions is validated against the regions of the check it updates.
func TestApplySealedSecretsRegionsFromExistingCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org, keys := setupBrowserSealing(t, sqliteSealingEnv)
	ctx := t.Context()

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "existing", Slug: "existing", Type: "http", Regions: []string{sealTestRegion},
		Config: map[string]any{"url": "https://internal.example.com"},
	})
	r.NoError(err)

	sealed := clientSeal(t, map[string]any{"basicAuth": "alice:hunter2"}, keys.X25519Recipient)

	noRegions := func(slug string) *checks.ExportDocument {
		doc := sealedDoc(org.Slug, slug, sealed)
		doc.Checks[0].Regions = nil

		return doc
	}

	// Unknown check, no regions: nothing says where it runs.
	res, err := svc.ApplyChecks(ctx, org.Slug, noRegions("brand-new"), checks.ApplyOptions{DryRun: true})
	r.NoError(err)
	r.Len(res.Errors, 1)

	// Existing check: its regions are used.
	res, err = svc.ApplyChecks(ctx, org.Slug, noRegions(*created.Slug), checks.ApplyOptions{DryRun: true})
	r.NoError(err)
	r.Empty(res.Errors)

	// The update path of an import validates the blob too, and passes.
	imported, err := svc.ImportChecks(ctx, org.Slug, sealedDoc(org.Slug, *created.Slug, sealed), true)
	r.NoError(err)
	r.Empty(imported.Errors)
	r.Equal(1, imported.Updated)

	// ... and refuses a blob whose keys are not secret fields of the type.
	wrong := &checks.SealedSecretsRequest{
		Keys: []string{"notASecretField"}, Fingerprints: sealed.Fingerprints, CT: sealed.CT,
	}
	imported, err = svc.ImportChecks(ctx, org.Slug, sealedDoc(org.Slug, *created.Slug, wrong), true)
	r.NoError(err)
	r.Len(imported.Errors, 1)
	r.Contains(imported.Errors[0].Error, "notASecretField")

	// The same update, for real: the blob is stored as a browser blob.
	res, err = svc.ApplyChecks(ctx, org.Slug, sealedDoc(org.Slug, *created.Slug, sealed), checks.ApplyOptions{})
	r.NoError(err)
	r.Empty(res.Errors)
	r.Equal(1, res.Updated)

	row, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, *created.Slug)
	r.NoError(err)
	r.NotNil(row.ConfigSealed)
}

func TestCreateSealedRejectsInvalidShape(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, _, org, keys := setupBrowserSealing(t, sqliteSealingEnv)

	sealed := clientSeal(t, map[string]any{"basicAuth": "alice:hunter2"}, keys.X25519Recipient)
	sealed.Keys = []string{"notASecretField"}

	_, err := svc.CreateCheck(t.Context(), org.Slug, sealedCheckRequest("bad-shape", sealed, sealTestRegion))
	r.Error(err)
	r.Contains(err.Error(), "notASecretField")
}

func TestRewrapServiceErrors(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, _, dbSvc, org := newFaultSealingService(t)
	ctx := t.Context()

	_, err := svc.StartRewrap(ctx, "no-such-org", &checks.RewrapRequest{})
	r.ErrorIs(err, checks.ErrOrganizationNotFound)

	_, err = svc.RewrapStatus(ctx, "no-such-org", nil)
	r.ErrorIs(err, checks.ErrOrganizationNotFound)

	// A plain check and a check of another region are never candidates.
	_, err = svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "plain", Slug: "plain", Type: "http", Config: map[string]any{"url": "https://example.com"},
	})
	r.NoError(err)

	keys := enrollSealAgent(t, dbSvc, org.UID, "dc1-agent")
	_, err = svc.CreateCheck(ctx, org.Slug, sealedCheckRequest("sealed-dc1",
		clientSeal(t, map[string]any{"basicAuth": "x:y"}, keys.X25519Recipient), sealTestRegion))
	r.NoError(err)

	out, err := svc.StartRewrap(ctx, org.Slug, &checks.RewrapRequest{Region: "@dc2"})
	r.NoError(err)
	r.Empty(out)

	// Named checks are reported, here as up to date.
	out, err = svc.StartRewrap(ctx, org.Slug, &checks.RewrapRequest{CheckUIDs: []string{"sealed-dc1", "plain"}})
	r.NoError(err)
	r.Len(out, 2)
	r.Equal("sealed-dc1", out[0].CheckName)
	r.Equal(rewraptask.CodeUpToDate, out[0].ErrorCode)
	r.Equal(rewraptask.CodeNotSealed, out[1].ErrorCode)
}

func TestRewrapServiceDatabaseErrors(t *testing.T) {
	t.Parallel()

	t.Run("region lookup", func(t *testing.T) {
		t.Parallel()

		svc, faulty, _, org := newFaultSealingService(t)
		faulty.failAll.Store(true)

		_, err := svc.StartRewrap(t.Context(), org.Slug, &checks.RewrapRequest{Region: sealTestRegion})
		require.Error(t, err)
	})

	t.Run("check lookup", func(t *testing.T) {
		t.Parallel()

		svc, _, dbSvc, org := newFaultSealingService(t)
		renameTable(t, dbSvc, "checks")

		_, err := svc.StartRewrap(t.Context(), org.Slug, &checks.RewrapRequest{CheckUIDs: []string{"x"}})
		require.Error(t, err)
		require.NotErrorIs(t, err, checks.ErrCheckNotFound)
	})

	t.Run("check listing", func(t *testing.T) {
		t.Parallel()

		svc, _, dbSvc, org := newFaultSealingService(t)
		renameTable(t, dbSvc, "checks")

		_, err := svc.StartRewrap(t.Context(), org.Slug, &checks.RewrapRequest{})
		require.Error(t, err)
	})

	t.Run("start", func(t *testing.T) {
		t.Parallel()

		svc, _, dbSvc, org := newFaultSealingService(t)
		keys := enrollSealAgent(t, dbSvc, org.UID, "dc1-agent")
		_, err := svc.CreateCheck(t.Context(), org.Slug, sealedCheckRequest("sealed",
			clientSeal(t, map[string]any{"basicAuth": "x:y"}, keys.X25519Recipient), sealTestRegion))
		require.NoError(t, err)

		renameTable(t, dbSvc, "worker_tasks")

		_, err = svc.StartRewrap(t.Context(), org.Slug, &checks.RewrapRequest{})
		require.Error(t, err)
	})

	t.Run("status", func(t *testing.T) {
		t.Parallel()

		svc, _, dbSvc, org := newFaultSealingService(t)
		renameTable(t, dbSvc, "worker_tasks")

		_, err := svc.RewrapStatus(t.Context(), org.Slug, nil)
		require.Error(t, err)
	})
}

func TestRewrapUnavailableWithoutRewrapper(t *testing.T) {
	t.Parallel()

	svc, _, org := setupEncryptedChecksService(t)

	_, err := svc.RewrapStatus(t.Context(), org.Slug, nil)
	require.ErrorIs(t, err, checks.ErrRewrapUnavailable)
}

// sealingRouter serves the sealing endpoints of one service.
func sealingRouter(svc *checks.Service, org *models.Organization) *httpx.Router {
	handler := checks.NewHandler(svc, &config.Config{})

	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/sealing")
	group.Use(asSuperAdmin(org))
	group.GET("/noncompliant-checks", handler.NonCompliantChecks)
	group.GET("/reseal-checks", handler.ResealChecks)
	group.POST("/rewrap", handler.StartRewrap)
	group.GET("/rewrap", handler.RewrapStatus)

	return router
}

func callSealing(
	t *testing.T, router *httpx.Router, method, org, path, body string,
) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, "/api/v1/orgs/"+org+"/sealing"+path,
		bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

func TestSealingHandlers(t *testing.T) {
	t.Parallel()

	svc, _, dbSvc, org := newFaultSealingService(t)
	router := sealingRouter(svc, org)

	keys := enrollSealAgent(t, dbSvc, org.UID, "dc1-agent")
	_, err := svc.CreateCheck(t.Context(), org.Slug, sealedCheckRequest("sealed",
		clientSeal(t, map[string]any{"basicAuth": "x:y"}, keys.X25519Recipient), sealTestRegion))
	require.NoError(t, err)

	t.Run("lists", func(t *testing.T) {
		t.Parallel()

		for _, path := range []string{"/noncompliant-checks", "/reseal-checks"} {
			rec := callSealing(t, router, http.MethodGet, org.Slug, path, "")
			require.Equal(t, http.StatusOK, rec.Code, path)

			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Contains(t, body, "data")
		}
	})

	t.Run("lists of an unknown org", func(t *testing.T) {
		t.Parallel()

		for _, path := range []string{"/noncompliant-checks", "/reseal-checks"} {
			rec := callSealing(t, router, http.MethodGet, "no-such-org", path, "")
			require.Equal(t, http.StatusNotFound, rec.Code, path)
		}
	})

	t.Run("start", func(t *testing.T) {
		t.Parallel()

		rec := callSealing(t, router, http.MethodPost, org.Slug, "/rewrap", `{"checkUids":["sealed"]}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), rewraptask.CodeUpToDate)

		rec = callSealing(t, router, http.MethodPost, org.Slug, "/rewrap", "")
		require.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("start errors", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name, org, body string
			status          int
		}{
			{"invalid json", org.Slug, `{`, http.StatusUnprocessableEntity},
			{"unknown org", "no-such-org", `{}`, http.StatusNotFound},
			{"unknown check", org.Slug, `{"checkUids":["nope"]}`, http.StatusNotFound},
			{"not a private region", org.Slug, `{"region":"@nope"}`, http.StatusBadRequest},
		}

		for _, tc := range cases {
			rec := callSealing(t, router, http.MethodPost, tc.org, "/rewrap", tc.body)
			require.Equal(t, tc.status, rec.Code, tc.name)
		}
	})

	t.Run("status", func(t *testing.T) {
		t.Parallel()

		rec := callSealing(t, router, http.MethodGet, org.Slug, "/rewrap?check=a,%20,b", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		rec = callSealing(t, router, http.MethodGet, "no-such-org", "/rewrap", "")
		require.Equal(t, http.StatusNotFound, rec.Code)
	})
}

func TestSealingHandlersWithoutRewrapper(t *testing.T) {
	t.Parallel()

	svc, _, org := setupEncryptedChecksService(t)
	router := sealingRouter(svc, org)

	rec := callSealing(t, router, http.MethodPost, org.Slug, "/rewrap", "")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "REWRAP_UNAVAILABLE")

	rec = callSealing(t, router, http.MethodGet, org.Slug, "/rewrap", "")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestSealingHandlersInternalError(t *testing.T) {
	t.Parallel()

	svc, _, dbSvc, org := newFaultSealingService(t)
	router := sealingRouter(svc, org)
	renameTable(t, dbSvc, "worker_tasks")

	rec := callSealing(t, router, http.MethodGet, org.Slug, "/rewrap", "")
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}
