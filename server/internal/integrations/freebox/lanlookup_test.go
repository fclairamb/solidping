package freebox_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/integrations/freebox"
)

// lanlookupFixture wires an in-memory db + disabled-credentials service so the
// shared resolver can be exercised without Postgres or a master key (app_token
// lives plaintext on Settings, matching the V1 fallback).
type lanlookupFixture struct {
	dbSvc db.Service
	creds credentials.Service
	org   *models.Organization
}

func newLanlookupFixture(t *testing.T) *lanlookupFixture {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(nil, nil) // disabled mode → plaintext fallback
	r.NoError(err)

	org := models.NewOrganization("lan-lookup", "Lan Lookup Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	return &lanlookupFixture{dbSvc: dbSvc, creds: creds, org: org}
}

// newGrantedFreeboxChannel creates a granted Freebox channel whose BaseURL
// points at baseURL and whose plaintext appToken is set on Settings.
func (f *lanlookupFixture) newGrantedFreeboxChannel(t *testing.T, baseURL, status string) *models.Channel {
	t.Helper()

	r := require.New(t)

	settings := &models.FreeboxSettings{
		BaseURL: baseURL,
		AppID:   freebox.DefaultAppID,
		Status:  status,
	}
	m, err := settings.ToJSONMap()
	r.NoError(err)
	m["appToken"] = "permanent-token"

	conn := models.NewChannel(f.org.UID, models.ConnectionTypeFreebox, "Freebox")
	conn.Settings = m
	r.NoError(f.dbSvc.CreateChannel(t.Context(), conn))

	return conn
}

func startFakeFreebox(t *testing.T, hosts []freebox.RawLanHost) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/login/", func(w http.ResponseWriter, _ *http.Request) {
		body, _ := json.Marshal(freebox.LoginChallenge{Challenge: "ch"})
		_ = json.NewEncoder(w).Encode(freebox.APIResponse{Success: true, Result: body})
	})
	mux.HandleFunc("/api/v4/login/session/", func(w http.ResponseWriter, _ *http.Request) {
		body, _ := json.Marshal(freebox.SessionResult{SessionToken: "tok"})
		_ = json.NewEncoder(w).Encode(freebox.APIResponse{Success: true, Result: body})
	})
	mux.HandleFunc("/api/v4/lan/browser/pub/", func(w http.ResponseWriter, _ *http.Request) {
		body, _ := json.Marshal(hosts)
		_ = json.NewEncoder(w).Encode(freebox.APIResponse{Success: true, Result: body})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func TestListLanHostsForChannelGranted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newLanlookupFixture(t)
	srv := startFakeFreebox(t, []freebox.RawLanHost{
		{
			ID:        "router",
			HostType:  freebox.LanHostTypeRouter,
			Active:    true,
			Reachable: true,
			L3Connectivities: []freebox.RawLanL3Conn{
				{Addr: "192.168.1.254", Af: freebox.LanAfIPv4, Active: true, Reachable: true},
			},
		},
		{
			ID:          "ether-aa",
			PrimaryName: "nas",
			HostType:    "nas",
			Active:      true,
			Reachable:   true,
			L3Connectivities: []freebox.RawLanL3Conn{
				{Addr: "192.168.1.10", Af: freebox.LanAfIPv4, Active: true, Reachable: true, LastActivity: 100},
			},
		},
	})

	conn := f.newGrantedFreeboxChannel(t, srv.URL, models.FreeboxStatusGranted)

	hosts, err := freebox.ListLanHostsForChannel(t.Context(), f.dbSvc, f.creds, f.org.UID, conn.UID)
	r.NoError(err)
	r.Len(hosts, 1, "router must be filtered out")
	r.Equal("192.168.1.10", hosts[0].IP)
	r.Equal("nas", hosts[0].Name)
}

func TestListLanHostsForChannelNotGranted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newLanlookupFixture(t)
	srv := startFakeFreebox(t, nil)

	conn := f.newGrantedFreeboxChannel(t, srv.URL, models.FreeboxStatusPairing)

	_, err := freebox.ListLanHostsForChannel(t.Context(), f.dbSvc, f.creds, f.org.UID, conn.UID)
	r.ErrorIs(err, freebox.ErrFreeboxNotGranted)
}

func TestListLanHostsForChannelWrongOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newLanlookupFixture(t)
	srv := startFakeFreebox(t, nil)

	conn := f.newGrantedFreeboxChannel(t, srv.URL, models.FreeboxStatusGranted)

	_, err := freebox.ListLanHostsForChannel(t.Context(), f.dbSvc, f.creds, "some-other-org", conn.UID)
	r.ErrorIs(err, freebox.ErrFreeboxChannelNotFound)
}

func TestListLanHostsForChannelMissing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newLanlookupFixture(t)

	_, err := freebox.ListLanHostsForChannel(
		t.Context(), f.dbSvc, f.creds, f.org.UID, "00000000-0000-0000-0000-000000000000",
	)
	r.ErrorIs(err, freebox.ErrFreeboxChannelNotFound)
}

func TestListLanHostsForChannelNonFreebox(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newLanlookupFixture(t)

	other := models.NewChannel(f.org.UID, models.ConnectionTypeDiscord, "Discord")
	r.NoError(f.dbSvc.CreateChannel(t.Context(), other))

	_, err := freebox.ListLanHostsForChannel(t.Context(), f.dbSvc, f.creds, f.org.UID, other.UID)
	r.ErrorIs(err, freebox.ErrFreeboxChannelNotFound)
}
