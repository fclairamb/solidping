package jobtypes_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	cfg, _ := json.Marshal(jobtypes.FreeboxLanDiscoveryConfig{ChannelUID: f.channelUID})

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
				{Addr: "192.168.1.254", Af: freebox.LanAfIPv4, Active: true, Reachable: true},
			},
		},
		{
			ID:          "nas-id",
			PrimaryName: "nas",
			HostType:    "nas",
			Active:      true,
			Reachable:   true,
			L3Connectivities: []freebox.RawLanL3Conn{
				{Addr: "192.168.1.10", Af: freebox.LanAfIPv4, Active: true, Reachable: true, LastActivity: 100},
			},
		},
		{
			ID:          "phone-id",
			PrimaryName: "phone",
			HostType:    "smartphone",
			Active:      true,
			Reachable:   false,
			L3Connectivities: []freebox.RawLanL3Conn{
				{Addr: "192.168.1.20", Af: freebox.LanAfIPv4, Active: true, Reachable: false, LastActivity: 50},
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

	r.Equal("192.168.1.10", hosts[0].IP)
	r.Equal("nas", hosts[0].Hostname)
	r.True(hosts[0].ICMPReachable)
	r.JSONEq("[]", string(hosts[0].OpenPorts))
	r.JSONEq("[]", string(hosts[0].SuggestedChecks))

	r.Equal("192.168.1.20", hosts[1].IP)
	r.False(hosts[1].ICMPReachable)
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
				{Addr: "192.168.1.10", Af: freebox.LanAfIPv4, Active: true, Reachable: true, LastActivity: 100},
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
