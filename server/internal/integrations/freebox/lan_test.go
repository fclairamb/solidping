package freebox_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/integrations/freebox"
)

// newFreeboxStub returns a fresh httptest server that answers the
// minimum subset of the Freebox API needed to exercise the
// authenticated endpoints: the login challenge, session open, and the
// LAN-browser endpoint (whose body the caller supplies). The returned
// Client is preconfigured to talk to it.
func newFreeboxStub(t *testing.T, lanHandler http.HandlerFunc) *freebox.Client {
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

	if lanHandler != nil {
		mux.HandleFunc("/api/v4/lan/browser/pub/", lanHandler)
	}

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return freebox.NewClient(srv.URL, "appToken")
}

func TestListLanHostsFiltersAndSorts(t *testing.T) {
	t.Parallel()

	// Raw payload covers every filter rule:
	//   - router → dropped
	//   - host with only IPv6 → dropped
	//   - host with all IPv4 inactive → dropped
	//   - reachable workstation → kept, comes first
	//   - unreachable laptop → kept, second
	//   - multi-IPv4 host → keeps the most-recent IPv4
	raw := []freebox.RawLanHost{
		{
			ID:          "router-id",
			PrimaryName: "freebox",
			HostType:    freebox.LanHostTypeRouter,
			Active:      true,
			Reachable:   true,
			L3Connectivities: []freebox.RawLanL3Conn{
				{Addr: "192.168.1.254", Af: freebox.LanAfIPv4, Active: true, Reachable: true, LastActivity: 1000},
			},
		},
		{
			ID:          "ipv6-only",
			PrimaryName: "ip6host",
			HostType:    "other",
			Active:      true,
			Reachable:   true,
			L3Connectivities: []freebox.RawLanL3Conn{
				{Addr: "fe80::1", Af: freebox.LanAfIPv6, Active: true, Reachable: true, LastActivity: 1000},
			},
		},
		{
			ID:          "inactive",
			PrimaryName: "old-pi",
			HostType:    "other",
			Active:      false,
			Reachable:   false,
			L3Connectivities: []freebox.RawLanL3Conn{
				{Addr: "192.168.1.50", Af: freebox.LanAfIPv4, Active: false, Reachable: false, LastActivity: 1},
			},
		},
		{
			ID:                "ether-aa",
			PrimaryName:       "Workstation",
			HostType:          "workstation",
			Active:            true,
			Reachable:         true,
			LastTimeReachable: 1700,
			L3Connectivities: []freebox.RawLanL3Conn{
				{Addr: "192.168.1.10", Af: freebox.LanAfIPv4, Active: true, Reachable: true, LastActivity: 1700},
				{Addr: "192.168.1.11", Af: freebox.LanAfIPv4, Active: true, Reachable: true, LastActivity: 1500},
			},
		},
		{
			ID:                "ether-bb",
			PrimaryName:       "laptop",
			HostType:          "laptop",
			Active:            true,
			Reachable:         false,
			LastTimeReachable: 500,
			L3Connectivities: []freebox.RawLanL3Conn{
				{Addr: "192.168.1.20", Af: freebox.LanAfIPv4, Active: true, Reachable: false, LastActivity: 500},
			},
		},
	}

	client := newFreeboxStub(t, func(w http.ResponseWriter, _ *http.Request) {
		body, _ := json.Marshal(raw)
		_ = json.NewEncoder(w).Encode(freebox.APIResponse{Success: true, Result: body})
	})

	r := require.New(t)

	hosts, err := freebox.ListLanHosts(t.Context(), client)
	r.NoError(err)
	r.Len(hosts, 2, "router/ipv6-only/inactive must be filtered out")

	// Reachable first, then by name (case-insensitive).
	r.Equal("ether-aa", hosts[0].ID)
	r.Equal("ether-bb", hosts[1].ID)
	r.True(hosts[0].Reachable)
	r.False(hosts[1].Reachable)

	// Multi-IPv4 host: most-recent activity wins.
	r.Equal("192.168.1.10", hosts[0].IP)

	// Simplified shape: name pulled from primary_name, lastSeen is set.
	r.Equal("Workstation", hosts[0].Name)
	r.Equal("workstation", hosts[0].HostType)
	r.NotEmpty(hosts[0].LastSeen)
}

func TestListLanHostsEmpty(t *testing.T) {
	t.Parallel()

	client := newFreeboxStub(t, func(w http.ResponseWriter, _ *http.Request) {
		body, _ := json.Marshal([]freebox.RawLanHost{})
		_ = json.NewEncoder(w).Encode(freebox.APIResponse{Success: true, Result: body})
	})

	r := require.New(t)

	hosts, err := freebox.ListLanHosts(t.Context(), client)
	r.NoError(err)
	r.Empty(hosts)
}

func TestListLanHostsNilClient(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	_, err := freebox.ListLanHosts(t.Context(), nil)
	r.Error(err)
	r.ErrorIs(err, freebox.ErrNilSettings)
}

// Reachable-then-name ordering with mixed-case names — confirms the
// case-insensitive comparison.
func TestListLanHostsSortIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	raw := []freebox.RawLanHost{
		mkHost("a", "Zeta", true),
		mkHost("b", "alpha", true),
		mkHost("c", "Beta", true),
	}

	client := newFreeboxStub(t, func(w http.ResponseWriter, _ *http.Request) {
		body, _ := json.Marshal(raw)
		_ = json.NewEncoder(w).Encode(freebox.APIResponse{Success: true, Result: body})
	})

	r := require.New(t)

	hosts, err := freebox.ListLanHosts(t.Context(), client)
	r.NoError(err)
	r.Len(hosts, 3)

	r.Equal("alpha", hosts[0].Name)
	r.Equal("Beta", hosts[1].Name)
	r.Equal("Zeta", hosts[2].Name)
}

func mkHost(id, name string, reachable bool) freebox.RawLanHost {
	return freebox.RawLanHost{
		ID:          id,
		PrimaryName: name,
		HostType:    "workstation",
		Active:      true,
		Reachable:   reachable,
		L3Connectivities: []freebox.RawLanL3Conn{
			{Addr: "10.0.0." + id, Af: freebox.LanAfIPv4, Active: true, Reachable: reachable, LastActivity: 1000},
		},
	}
}
