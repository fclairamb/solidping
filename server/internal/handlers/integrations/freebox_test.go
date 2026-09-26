package integrations_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/integrations/freebox"
)

// memDEKStore is a tiny in-memory implementation of credentials.DEKStore
// so we can exercise the encryption path without standing up Postgres.
type memDEKStore struct {
	data map[string][]byte
}

func newMemDEKStore() *memDEKStore { return &memDEKStore{data: map[string][]byte{}} }

func (s *memDEKStore) LoadDEK(_ context.Context, orgUID string) ([]byte, bool, error) {
	v, ok := s.data[orgUID]
	if !ok {
		return nil, false, nil
	}

	return v, true, nil
}

func (s *memDEKStore) SaveDEK(_ context.Context, orgUID string, wrapped []byte) error {
	s.data[orgUID] = wrapped

	return nil
}

func newKEK(t *testing.T) []byte {
	t.Helper()

	key := make([]byte, 32)
	_, err := io.ReadFull(rand.Reader, key)
	require.NoError(t, err)

	return key
}

type freeboxFixture struct {
	svc     *integrations.Service
	handler *integrations.Handler
	router  *httpx.Router
	dbSvc   db.Service
	org     *models.Organization
}

func newFreeboxFixture(t *testing.T) *freeboxFixture {
	t.Helper()

	return newFreeboxFixtureWithConfig(t, nil)
}

// newFreeboxFixtureWithConfig is newFreeboxFixture with the Service built
// against appConfig instead of nil — used to exercise the SaaS-mode baseUrl
// restriction (spec 2026-09-25-31), which reads appConfig.Deployment.Mode.
func newFreeboxFixtureWithConfig(t *testing.T, appConfig *config.Config) *freeboxFixture {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	org := models.NewOrganization("freebox-test", "Freebox Test Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	svc := integrations.NewService(dbSvc, creds, nil, appConfig)
	handler := integrations.NewHandler(svc, &config.Config{})

	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org")
	group.POST("/integrations/freebox/pair", handler.StartFreeboxPairing)
	group.GET("/integrations/freebox/pair/:uid/status", handler.GetFreeboxPairingStatus)

	return &freeboxFixture{
		svc:     svc,
		handler: handler,
		router:  router,
		dbSvc:   dbSvc,
		org:     org,
	}
}

func (f *freeboxFixture) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		require.NoError(t, err)
	}

	req := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewBuffer(bodyBytes))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)

	return rec
}

// startFakeFreebox returns an httptest server impersonating the
// /api/v4/login/authorize/ endpoint pair.
func startFakeFreebox(t *testing.T, appToken string, trackID int, status string) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/login/authorize/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			raw, _ := json.Marshal(freebox.AuthorizeResult{AppToken: appToken, TrackID: trackID})

			if err := json.NewEncoder(w).Encode(freebox.APIResponse{Success: true, Result: raw}); err != nil {
				t.Logf("encode envelope: %v", err)
			}
		case http.MethodGet:
			raw, _ := json.Marshal(freebox.PairingStatus{Status: status})

			if err := json.NewEncoder(w).Encode(freebox.APIResponse{Success: true, Result: raw}); err != nil {
				t.Logf("encode envelope: %v", err)
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func TestStartFreeboxPairingCreatesChannelWithEncryptedToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFreeboxFixture(t)
	srv := startFakeFreebox(t, "permanent-token-xyz", 17, freebox.StatusPending)
	f.svc.AllowFreeboxTestBaseURL(srv.URL)

	rec := f.do(t, http.MethodPost,
		"/api/v1/orgs/"+f.org.Slug+"/integrations/freebox/pair",
		integrations.StartFreeboxPairingRequest{Name: "Living Room", BaseURL: srv.URL},
	)
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var resp integrations.FreeboxPairingResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.NotEmpty(resp.ConnectionUID)
	r.Equal(17, resp.TrackID)
	r.Equal(models.FreeboxStatusPairing, resp.Status)

	// Inspect persisted channel: app_token must be encrypted (not in
	// the public Settings JSONB).
	conn, err := f.dbSvc.GetChannel(t.Context(), resp.ConnectionUID)
	r.NoError(err)
	r.Equal(models.ConnectionTypeFreebox, conn.Type)
	r.Equal("Living Room", conn.Name)
	r.NotContains(conn.Settings, "appToken", "app_token must not be in public settings")
	r.NotNil(conn.SettingsPrivate, "encrypted envelope must be present")
	r.NotNil(conn.SettingsPrivateKeys)

	// Public settings still carry the trackId and status.
	track, ok := conn.Settings["trackId"].(float64)
	r.True(ok, "trackId must be a JSON number")
	r.Equal(17, int(track))
	r.Equal(models.FreeboxStatusPairing, conn.Settings["status"])
}

func TestGetFreeboxPairingStatusTransitionsToGranted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFreeboxFixture(t)
	srv := startFakeFreebox(t, "permanent-token-xyz", 17, freebox.StatusGranted)
	f.svc.AllowFreeboxTestBaseURL(srv.URL)

	// Bootstrap the channel via the start endpoint.
	rec := f.do(t, http.MethodPost,
		"/api/v1/orgs/"+f.org.Slug+"/integrations/freebox/pair",
		integrations.StartFreeboxPairingRequest{BaseURL: srv.URL},
	)
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var start integrations.FreeboxPairingResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &start))

	// Now poll for status; the fake server reports granted.
	rec = f.do(t, http.MethodGet,
		"/api/v1/orgs/"+f.org.Slug+"/integrations/freebox/pair/"+start.ConnectionUID+"/status",
		nil,
	)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var poll integrations.FreeboxPairingStatusResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &poll))
	r.Equal(models.FreeboxStatusGranted, poll.Status)

	// Persisted state should reflect the grant: trackId cleared,
	// status flipped.
	conn, err := f.dbSvc.GetChannel(t.Context(), start.ConnectionUID)
	r.NoError(err)
	r.Equal(models.FreeboxStatusGranted, conn.Settings["status"])
	// trackId should be cleared (omitempty drops 0).
	_, hasTrack := conn.Settings["trackId"]
	r.False(hasTrack, "trackId must be cleared after grant")
	// Encrypted app_token must still be present.
	r.NotNil(conn.SettingsPrivate)
}

func TestGetFreeboxPairingStatusDeniedKeepsRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFreeboxFixture(t)
	srv := startFakeFreebox(t, "tok", 9, freebox.StatusDenied)
	f.svc.AllowFreeboxTestBaseURL(srv.URL)

	rec := f.do(t, http.MethodPost,
		"/api/v1/orgs/"+f.org.Slug+"/integrations/freebox/pair",
		integrations.StartFreeboxPairingRequest{BaseURL: srv.URL},
	)
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var start integrations.FreeboxPairingResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &start))

	rec = f.do(t, http.MethodGet,
		"/api/v1/orgs/"+f.org.Slug+"/integrations/freebox/pair/"+start.ConnectionUID+"/status",
		nil,
	)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var poll integrations.FreeboxPairingStatusResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &poll))
	r.Equal(models.FreeboxStatusDenied, poll.Status)

	conn, err := f.dbSvc.GetChannel(t.Context(), start.ConnectionUID)
	r.NoError(err)
	r.Equal(models.FreeboxStatusDenied, conn.Settings["status"])
}

// TestGetFreeboxPairingStatusRevalidatesStoredBaseURL covers a connection
// persisted before spec 2026-09-25-31's baseUrl contract existed (or paired
// under a since-tightened policy): the status endpoint re-validates the
// stored baseUrl and returns the same clean VALIDATION_ERROR a fresh pairing
// attempt would get today, instead of actually dialing an address that would
// now be rejected (here, the cloud metadata address — never where a real
// Freebox lives).
func TestGetFreeboxPairingStatusRevalidatesStoredBaseURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFreeboxFixture(t)

	settings := &models.FreeboxSettings{
		BaseURL: "http://169.254.169.254/",
		AppID:   freebox.DefaultAppID,
		Status:  models.FreeboxStatusPairing,
		TrackID: 7,
	}
	settingsMap, err := settings.ToJSONMap()
	r.NoError(err)

	conn := models.NewIntegration(f.org.UID, models.ConnectionTypeFreebox, "Legacy Freebox")
	conn.Settings = settingsMap
	r.NoError(f.dbSvc.CreateChannel(t.Context(), conn))

	rec := f.do(t, http.MethodGet,
		"/api/v1/orgs/"+f.org.Slug+"/integrations/freebox/pair/"+conn.UID+"/status",
		nil,
	)
	r.Equal(http.StatusBadRequest, rec.Code, rec.Body.String())
	r.Contains(rec.Body.String(), "VALIDATION_ERROR")
	r.Contains(rec.Body.String(), "baseUrl must be a valid Freebox API endpoint")

	// The row is untouched — no dial was attempted, so status stays exactly
	// what it was before this poll.
	reloaded, err := f.dbSvc.GetChannel(t.Context(), conn.UID)
	r.NoError(err)
	r.Equal(models.FreeboxStatusPairing, reloaded.Settings["status"])
}

func TestGetFreeboxPairingStatusRejectsNonFreeboxChannel(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFreeboxFixture(t)

	// Create a non-freebox channel directly.
	wrong := models.NewIntegration(f.org.UID, models.ConnectionTypeDiscord, "Discord")
	r.NoError(f.dbSvc.CreateChannel(t.Context(), wrong))

	rec := f.do(t, http.MethodGet,
		"/api/v1/orgs/"+f.org.Slug+"/integrations/freebox/pair/"+wrong.UID+"/status",
		nil,
	)
	r.Equal(http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestStartFreeboxPairingValidatesOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFreeboxFixture(t)

	rec := f.do(t, http.MethodPost,
		"/api/v1/orgs/does-not-exist/integrations/freebox/pair",
		integrations.StartFreeboxPairingRequest{},
	)
	r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestStartFreeboxPairingRejectsInvalidBaseURL covers the URL-contract half
// of spec 2026-09-25-31: an arbitrary internal host is rejected as a clean
// 400 VALIDATION_ERROR before any request is made — never a raw fetch
// error/timeout from actually dialing it.
func TestStartFreeboxPairingRejectsInvalidBaseURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFreeboxFixture(t)

	cases := []string{
		"https://evil.example",             // not an IP, not *.freebox.fr
		"http://user:pass@192.168.1.254",   // userinfo
		"http://mafreebox.freebox.fr:2222", // port outside 80/443/8443
		"http://203.0.113.10",              // public IP over http
		"ftp://192.168.1.254",              // bad scheme
		"http://192.168.1.254:6379",        // weird port, even on a private IP
		// None of these is where a Freebox lives — accepting them would keep
		// exactly the internal scan/POST primitive this validator exists to
		// close.
		"http://127.0.0.1",        // loopback
		"http://169.254.169.254/", // cloud metadata
		"http://[::1]",            // IPv6 loopback
		"http://0.0.0.0",          // unspecified
		"http://[fe80::1]",        // IPv6 link-local
	}

	for _, baseURL := range cases {
		rec := f.do(t, http.MethodPost,
			"/api/v1/orgs/"+f.org.Slug+"/integrations/freebox/pair",
			integrations.StartFreeboxPairingRequest{BaseURL: baseURL},
		)
		r.Equal(http.StatusBadRequest, rec.Code, "baseUrl=%s: %s", baseURL, rec.Body.String())
		r.Contains(rec.Body.String(), "VALIDATION_ERROR", baseURL)
		r.Contains(rec.Body.String(), "baseUrl must be a valid Freebox API endpoint", baseURL)
	}
}

// TestStartFreeboxPairingAcceptsValidBaseURL covers the self-hosted-accepted
// side of the same contract: a private-range IP and the documented default
// both pass validation and reach the (fake) Freebox.
func TestStartFreeboxPairingAcceptsValidBaseURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFreeboxFixture(t)

	srv := startFakeFreebox(t, "app-token", 42, freebox.StatusPending)
	f.svc.AllowFreeboxTestBaseURL(srv.URL)

	rec := f.do(t, http.MethodPost,
		"/api/v1/orgs/"+f.org.Slug+"/integrations/freebox/pair",
		integrations.StartFreeboxPairingRequest{BaseURL: srv.URL},
	)
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())
}

// TestStartFreeboxPairingRejectsOverrideInSaaSMode covers the SaaS-mode half
// of spec 2026-09-25-31: a shared worker never runs pairing against anything
// but the documented default, since it is never the member's own network
// (the same reasoning as spec 2026-09-25-22's docker gate).
func TestStartFreeboxPairingRejectsOverrideInSaaSMode(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFreeboxFixtureWithConfig(t, &config.Config{
		Deployment: config.DeploymentConfig{Mode: config.DeploymentModeSaaS},
	})

	srv := startFakeFreebox(t, "app-token", 42, freebox.StatusPending)
	f.svc.AllowFreeboxTestBaseURL(srv.URL)

	rec := f.do(t, http.MethodPost,
		"/api/v1/orgs/"+f.org.Slug+"/integrations/freebox/pair",
		integrations.StartFreeboxPairingRequest{BaseURL: srv.URL},
	)
	r.Equal(http.StatusBadRequest, rec.Code, rec.Body.String())
	r.Contains(rec.Body.String(), "not available on this deployment")
}

// CreateIntegration through the regular CRUD must accept the new freebox
// connection type — guards against forgetting to add it to the
// validation switch.
func TestCreateChannelAcceptsFreeboxType(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFreeboxFixture(t)

	_, err := f.svc.CreateIntegration(t.Context(), f.org.Slug, integrations.CreateIntegrationRequest{
		Type: string(models.ConnectionTypeFreebox),
		Name: "Manual Freebox",
	})
	r.NoError(err)
}
