package channels_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/channels"
	"github.com/fclairamb/solidping/server/internal/integrations/freebox"
)

// startFakeFreeboxWithLAN returns a fake Freebox server that answers the
// pairing exchange plus the LAN-browser endpoint with the supplied
// hosts. Reuses the freebox-pairing setup so we get realistic
// challenge/session flow.
func startFakeFreeboxWithLAN(t *testing.T, hosts []freebox.RawLanHost) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/login/authorize/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			raw, _ := json.Marshal(freebox.AuthorizeResult{AppToken: "permanent-token", TrackID: 1})
			_ = json.NewEncoder(w).Encode(freebox.APIResponse{Success: true, Result: raw})
		case http.MethodGet:
			raw, _ := json.Marshal(freebox.PairingStatus{Status: freebox.StatusGranted})
			_ = json.NewEncoder(w).Encode(freebox.APIResponse{Success: true, Result: raw})
		default:
			http.Error(w, "nope", http.StatusMethodNotAllowed)
		}
	})
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

func TestLanHostsHandlerReturnsFilteredList(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFreeboxFixture(t)
	srv := startFakeFreeboxWithLAN(t, []freebox.RawLanHost{
		{
			ID:          "router",
			PrimaryName: "freebox",
			HostType:    freebox.LanHostTypeRouter,
			Active:      true,
			Reachable:   true,
			L3Connectivities: []freebox.RawLanL3Conn{
				{Addr: "192.168.1.254", Af: freebox.LanAfIPv4, Active: true, Reachable: true},
			},
		},
		{
			ID:                "ether-aa",
			PrimaryName:       "macbook",
			HostType:          "laptop",
			Active:            true,
			Reachable:         true,
			LastTimeReachable: 1700,
			L3Connectivities: []freebox.RawLanL3Conn{
				{Addr: "192.168.1.10", Af: freebox.LanAfIPv4, Active: true, Reachable: true, LastActivity: 1700},
			},
		},
	})

	// Register the LAN-hosts route on the fixture router.
	f.router.GET("/api/v1/orgs/:org/integrations/freebox/:uid/lan-hosts", f.handler.LanHostsHandler)

	// Bootstrap the channel via the pair endpoint, then flip its status to
	// granted by polling our fake server.
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

	// The channel's persisted BaseURL already points at our fake Freebox
	// (set at pairing time), so the LAN-hosts handler reaches it directly.
	rec = f.do(t, http.MethodGet,
		"/api/v1/orgs/"+f.org.Slug+"/integrations/freebox/"+start.ConnectionUID+"/lan-hosts",
		nil,
	)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var resp channels.ListLanHostsResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Len(resp.Data, 1, "router must be filtered out")
	r.Equal("ether-aa", resp.Data[0].ID)
	r.Equal("192.168.1.10", resp.Data[0].IP)
	r.Equal("macbook", resp.Data[0].Name)
	r.Equal("laptop", resp.Data[0].HostType)
	r.True(resp.Data[0].Reachable)
}

func TestLanHostsHandlerRejectsChannelStillPairing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFreeboxFixture(t)
	srv := startFakeFreeboxWithLAN(t, nil)

	f.router.GET("/api/v1/orgs/:org/integrations/freebox/:uid/lan-hosts", f.handler.LanHostsHandler)

	rec := f.do(t, http.MethodPost,
		"/api/v1/orgs/"+f.org.Slug+"/integrations/freebox/pair",
		channels.StartFreeboxPairingRequest{BaseURL: srv.URL},
	)
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var start channels.FreeboxPairingResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &start))

	// Channel is still in `pairing` — the LAN-hosts endpoint must
	// 409 rather than blindly reaching out to a Freebox we can't auth
	// against.
	rec = f.do(t, http.MethodGet,
		"/api/v1/orgs/"+f.org.Slug+"/integrations/freebox/"+start.ConnectionUID+"/lan-hosts",
		nil,
	)
	r.Equal(http.StatusConflict, rec.Code, rec.Body.String())
}

func TestLanHostsHandlerRejectsNonFreeboxChannel(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFreeboxFixture(t)

	f.router.GET("/api/v1/orgs/:org/integrations/freebox/:uid/lan-hosts", f.handler.LanHostsHandler)

	// Discord channel — not a Freebox.
	other := models.NewChannel(f.org.UID, models.ConnectionTypeDiscord, "Discord")
	r.NoError(f.dbSvc.CreateChannel(t.Context(), other))

	rec := f.do(t, http.MethodGet,
		"/api/v1/orgs/"+f.org.Slug+"/integrations/freebox/"+other.UID+"/lan-hosts",
		nil,
	)
	r.Equal(http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestLanHostsHandlerNotFound(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFreeboxFixture(t)

	f.router.GET("/api/v1/orgs/:org/integrations/freebox/:uid/lan-hosts", f.handler.LanHostsHandler)

	rec := f.do(t, http.MethodGet,
		"/api/v1/orgs/"+f.org.Slug+"/integrations/freebox/00000000-0000-0000-0000-000000000000/lan-hosts",
		nil,
	)
	r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())
}
