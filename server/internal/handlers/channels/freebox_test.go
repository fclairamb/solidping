package channels_test

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
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/channels"
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
	svc     *channels.Service
	handler *channels.Handler
	router  *bunrouter.Router
	dbSvc   db.Service
	org     *models.Organization
}

func newFreeboxFixture(t *testing.T) *freeboxFixture {
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

	svc := channels.NewService(dbSvc, creds)
	handler := channels.NewHandler(svc, &config.Config{})

	router := bunrouter.New()
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
			_ = json.NewEncoder(w).Encode(freebox.APIResponse{Success: true, Result: raw})
		case http.MethodGet:
			raw, _ := json.Marshal(freebox.PairingStatus{Status: status})
			_ = json.NewEncoder(w).Encode(freebox.APIResponse{Success: true, Result: raw})
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

	rec := f.do(t, http.MethodPost,
		"/api/v1/orgs/"+f.org.Slug+"/integrations/freebox/pair",
		channels.StartFreeboxPairingRequest{Name: "Living Room", BaseURL: srv.URL},
	)
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var resp channels.FreeboxPairingResponse
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
	r.EqualValues(17, int(conn.Settings["trackId"].(float64))) //nolint:forcetypeassert // test asserts json.Number
	r.Equal(models.FreeboxStatusPairing, conn.Settings["status"])
}

func TestGetFreeboxPairingStatusTransitionsToGranted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFreeboxFixture(t)
	srv := startFakeFreebox(t, "permanent-token-xyz", 17, freebox.StatusGranted)

	// Bootstrap the channel via the start endpoint.
	rec := f.do(t, http.MethodPost,
		"/api/v1/orgs/"+f.org.Slug+"/integrations/freebox/pair",
		channels.StartFreeboxPairingRequest{BaseURL: srv.URL},
	)
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var start channels.FreeboxPairingResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &start))

	// Now poll for status; the fake server reports granted.
	rec = f.do(t, http.MethodGet,
		"/api/v1/orgs/"+f.org.Slug+"/integrations/freebox/pair/"+start.ConnectionUID+"/status",
		nil,
	)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var poll channels.FreeboxPairingStatusResponse
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

	rec := f.do(t, http.MethodPost,
		"/api/v1/orgs/"+f.org.Slug+"/integrations/freebox/pair",
		channels.StartFreeboxPairingRequest{BaseURL: srv.URL},
	)
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var start channels.FreeboxPairingResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &start))

	rec = f.do(t, http.MethodGet,
		"/api/v1/orgs/"+f.org.Slug+"/integrations/freebox/pair/"+start.ConnectionUID+"/status",
		nil,
	)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var poll channels.FreeboxPairingStatusResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &poll))
	r.Equal(models.FreeboxStatusDenied, poll.Status)

	conn, err := f.dbSvc.GetChannel(t.Context(), start.ConnectionUID)
	r.NoError(err)
	r.Equal(models.FreeboxStatusDenied, conn.Settings["status"])
}

func TestGetFreeboxPairingStatusRejectsNonFreeboxChannel(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFreeboxFixture(t)

	// Create a non-freebox channel directly.
	wrong := models.NewChannel(f.org.UID, models.ConnectionTypeDiscord, "Discord")
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
		channels.StartFreeboxPairingRequest{},
	)
	r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())
}

// CreateChannel through the regular CRUD must accept the new freebox
// connection type — guards against forgetting to add it to the
// validation switch.
func TestCreateChannelAcceptsFreeboxType(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFreeboxFixture(t)

	_, err := f.svc.CreateChannel(t.Context(), f.org.Slug, channels.CreateChannelRequest{
		Type: string(models.ConnectionTypeFreebox),
		Name: "Manual Freebox",
	})
	r.NoError(err)
}
