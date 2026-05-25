package jobtypes_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/integrations/freebox"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
)

// listenOnDefaultPort opens a loopback TCP listener on the first free port from
// the scanner's default set, so the active scan finds a genuinely open port.
// The candidate list mirrors discovery.defaultPorts (which is package-private),
// since the Freebox job uses the scanner's default port list.
func listenOnDefaultPort(t *testing.T) int {
	t.Helper()

	defaultScannerPorts := []int{22, 25, 53, 80, 110, 143, 443, 465, 587, 993, 995, 3306, 5432, 6379, 8080, 8443}

	var lc net.ListenConfig

	for _, port := range defaultScannerPorts {
		ln, err := lc.Listen(t.Context(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			t.Cleanup(func() { _ = ln.Close() })

			return port
		}
	}

	t.Skip("no default scanner port free to listen on")

	return 0
}

// fbDiscoveryFixture stands up an in-memory db + disabled credentials + a fake
// Freebox server, plus an org, a granted Freebox channel, and a job row to
// satisfy the discovered_hosts foreign keys.
type fbDiscoveryFixture struct {
	dbSvc      db.Service
	creds      credentials.Service
	org        *models.Organization
	channelUID string
	jobUID     string
}

func startFakeFreeboxLAN(t *testing.T, hosts []freebox.RawLanHost) *httptest.Server {
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

func newFbDiscoveryFixture(t *testing.T, baseURL, status string) *fbDiscoveryFixture {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(nil, nil)
	r.NoError(err)

	org := models.NewOrganization("fb-disc", "Freebox Discovery Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	settings := &models.FreeboxSettings{BaseURL: baseURL, AppID: freebox.DefaultAppID, Status: status}
	m, err := settings.ToJSONMap()
	r.NoError(err)
	m["appToken"] = "permanent-token"

	conn := models.NewChannel(org.UID, models.ConnectionTypeFreebox, "Freebox")
	conn.Settings = m
	r.NoError(dbSvc.CreateChannel(ctx, conn))

	job := models.NewJob(&org.UID, string(jobdef.JobTypeFreeboxLanDiscovery))
	r.NoError(dbSvc.CreateJob(ctx, job))

	return &fbDiscoveryFixture{
		dbSvc:      dbSvc,
		creds:      creds,
		org:        org,
		channelUID: conn.UID,
		jobUID:     job.UID,
	}
}

func (f *fbDiscoveryFixture) jctx(t *testing.T) *jobdef.JobContext {
	t.Helper()

	return &jobdef.JobContext{
		OrganizationUID: &f.org.UID,
		Job:             &models.Job{UID: f.jobUID},
		DB:              f.dbSvc.DB(),
		DBService:       f.dbSvc,
		Services:        &services.Registry{Credentials: f.creds},
		Logger:          slog.Default(),
	}
}

func runFreeboxJob(t *testing.T, f *fbDiscoveryFixture) error {
	t.Helper()

	def := &jobtypes.FreeboxLanDiscoveryJobDefinition{}
	cfg, err := json.Marshal(jobtypes.FreeboxLanDiscoveryConfig{ChannelUID: f.channelUID})
	require.NoError(t, err)

	runner, err := def.CreateJobRun(cfg)
	require.NoError(t, err)

	return runner.Run(context.Background(), f.jctx(t))
}

func (f *fbDiscoveryFixture) listHosts(t *testing.T) []*models.DiscoveredHost {
	t.Helper()

	var hosts []*models.DiscoveredHost
	err := f.dbSvc.DB().NewSelect().
		Model(&hosts).
		Where("organization_uid = ?", f.org.UID).
		Where("deleted_at IS NULL").
		Order("ip ASC").
		Scan(t.Context())
	require.NoError(t, err)

	return hosts
}

func TestFreeboxLanDiscoveryPersistsHosts(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := startFakeFreeboxLAN(t, []freebox.RawLanHost{
		{
			ID:        "router",
			HostType:  freebox.LanHostTypeRouter,
			Active:    true,
			Reachable: true,
			L3Connectivities: []freebox.RawLanL3Conn{
				{Addr: "192.0.2.254", Af: freebox.LanAfIPv4, Active: true, Reachable: true},
			},
		},
		{
			ID:          "nas-id",
			PrimaryName: "nas",
			HostType:    "nas",
			Active:      true,
			Reachable:   true,
			L3Connectivities: []freebox.RawLanL3Conn{
				{Addr: "192.0.2.10", Af: freebox.LanAfIPv4, Active: true, Reachable: true, LastActivity: 100},
			},
		},
		{
			ID:          "phone-id",
			PrimaryName: "phone",
			HostType:    "smartphone",
			Active:      true,
			Reachable:   false,
			L3Connectivities: []freebox.RawLanL3Conn{
				{Addr: "192.0.2.20", Af: freebox.LanAfIPv4, Active: true, Reachable: false, LastActivity: 50},
			},
		},
	})

	f := newFbDiscoveryFixture(t, srv.URL, models.FreeboxStatusGranted)

	r.NoError(runFreeboxJob(t, f))

	hosts := f.listHosts(t)
	r.Len(hosts, 2, "router must be filtered out by ListLanHosts")

	for _, h := range hosts {
		r.Equal(models.DiscoverySourceFreebox, h.Source)
		r.Equal(f.jobUID, h.JobUID)
	}

	// These hosts use TEST-NET-1 addresses (192.0.2.0/24, RFC 5737) which are
	// guaranteed unroutable, so the active scan finds nothing and the rows fall
	// back to the Freebox-provided name + reachability with empty ports/checks.
	r.Equal("192.0.2.10", hosts[0].IP)
	r.Equal("nas", hosts[0].Hostname)
	r.True(hosts[0].ICMPReachable)
	r.JSONEq("[]", string(hosts[0].OpenPorts))
	r.JSONEq("[]", string(hosts[0].SuggestedChecks))

	r.Equal("192.0.2.20", hosts[1].IP)
	r.False(hosts[1].ICMPReachable)
}

func TestFreeboxLanDiscoveryActivelyScansHosts(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// A real listener on a default scanner port confirms the active scan ran:
	// the corresponding Freebox host must come back with that port open and a
	// non-empty suggested-checks list.
	openPort := listenOnDefaultPort(t)

	srv := startFakeFreeboxLAN(t, []freebox.RawLanHost{
		{
			ID:          "live-id",
			PrimaryName: "live-host",
			HostType:    "nas",
			Active:      true,
			Reachable:   false, // Freebox says unreachable; the active scan should override.
			L3Connectivities: []freebox.RawLanL3Conn{
				{Addr: "127.0.0.1", Af: freebox.LanAfIPv4, Active: true, Reachable: true, LastActivity: 100},
			},
		},
		{
			ID:          "dead-id",
			PrimaryName: "dead-host",
			HostType:    "smartphone",
			Active:      true,
			Reachable:   true, // not scannable; falls back to the Freebox flag.
			L3Connectivities: []freebox.RawLanL3Conn{
				{Addr: "192.0.2.50", Af: freebox.LanAfIPv4, Active: true, Reachable: true, LastActivity: 50},
			},
		},
	})

	f := newFbDiscoveryFixture(t, srv.URL, models.FreeboxStatusGranted)

	r.NoError(runFreeboxJob(t, f))

	hosts := f.listHosts(t)
	r.Len(hosts, 2)

	byIP := map[string]*models.DiscoveredHost{}
	for _, h := range hosts {
		byIP[h.IP] = h
	}

	// The live host: the active scan found the open port + suggested checks.
	live := byIP["127.0.0.1"]
	r.NotNil(live)
	r.Equal(models.DiscoverySourceFreebox, live.Source)
	r.Equal("live-host", live.Hostname, "the Freebox device name must be preserved over reverse DNS")
	r.Contains(string(live.OpenPorts), strconv.Itoa(openPort))
	r.NotEqual("[]", string(live.OpenPorts), "active scan must populate open_ports")
	r.NotEqual("[]", string(live.SuggestedChecks), "active scan must populate suggested_checks")

	var suggested []map[string]any
	r.NoError(json.Unmarshal(live.SuggestedChecks, &suggested))
	r.NotEmpty(suggested)

	// The unscannable host: falls back to the Freebox name + reachability,
	// empty ports/checks. It is never dropped.
	dead := byIP["192.0.2.50"]
	r.NotNil(dead)
	r.Equal("dead-host", dead.Hostname)
	r.JSONEq("[]", string(dead.OpenPorts))
	r.JSONEq("[]", string(dead.SuggestedChecks))
}

func TestFreeboxLanDiscoveryUpsertIsIdempotent(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := startFakeFreeboxLAN(t, []freebox.RawLanHost{
		{
			ID:          "nas-id",
			PrimaryName: "nas",
			HostType:    "nas",
			Active:      true,
			Reachable:   true,
			L3Connectivities: []freebox.RawLanL3Conn{
				{Addr: "192.0.2.10", Af: freebox.LanAfIPv4, Active: true, Reachable: true, LastActivity: 100},
			},
		},
	})

	f := newFbDiscoveryFixture(t, srv.URL, models.FreeboxStatusGranted)

	r.NoError(runFreeboxJob(t, f))
	r.NoError(runFreeboxJob(t, f))

	hosts := f.listHosts(t)
	r.Len(hosts, 1, "re-running must update in place, not duplicate")
}

func TestFreeboxLanDiscoveryNotGranted(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := startFakeFreeboxLAN(t, nil)
	f := newFbDiscoveryFixture(t, srv.URL, models.FreeboxStatusPairing)

	err := runFreeboxJob(t, f)
	r.ErrorIs(err, freebox.ErrFreeboxNotGranted)
}

func TestFreeboxLanDiscoveryRequiresChannelUID(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	def := &jobtypes.FreeboxLanDiscoveryJobDefinition{}
	_, err := def.CreateJobRun(json.RawMessage(`{}`))
	r.Error(err)
}
