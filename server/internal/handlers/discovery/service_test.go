package discovery_test

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
	disc "github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/discovery"
	"github.com/fclairamb/solidping/server/internal/integrations/freebox"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

type discoveryFixture struct {
	svc   *discovery.Service
	dbSvc db.Service
	org   *models.Organization
}

func newDiscoveryFixture(t *testing.T) *discoveryFixture {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(nil, nil)
	r.NoError(err)

	org := models.NewOrganization("disc", "Discovery Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	jobSvc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier())
	checksSvc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), creds)
	svc := discovery.NewService(dbSvc.DB(), dbSvc, checksSvc, jobSvc, creds)

	return &discoveryFixture{svc: svc, dbSvc: dbSvc, org: org}
}

func startFakeFreebox(t *testing.T) *httptest.Server {
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
		body, _ := json.Marshal([]freebox.RawLanHost{})
		_ = json.NewEncoder(w).Encode(freebox.APIResponse{Success: true, Result: body})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func (f *discoveryFixture) newFreeboxChannel(t *testing.T, baseURL, status string) *models.Channel {
	t.Helper()

	r := require.New(t)

	settings := &models.FreeboxSettings{BaseURL: baseURL, AppID: freebox.DefaultAppID, Status: status}
	m, err := settings.ToJSONMap()
	r.NoError(err)
	m["appToken"] = "permanent-token"

	conn := models.NewChannel(f.org.UID, models.ConnectionTypeFreebox, "Freebox")
	conn.Settings = m
	r.NoError(f.dbSvc.CreateChannel(t.Context(), conn))

	return conn
}

// insertHost inserts a discovered host with the given source under a freshly
// created job (to satisfy the FK), returning nothing useful — the test reads
// back via ListHosts.
func (f *discoveryFixture) insertHost(t *testing.T, ip string, source models.DiscoverySource) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	job := models.NewJob(&f.org.UID, string(jobdef.JobTypeNetworkDiscovery))
	r.NoError(f.dbSvc.CreateJob(ctx, job))

	host := models.NewDiscoveredHost(f.org.UID, job.UID, ip, source)
	_, err := f.dbSvc.DB().NewInsert().Model(host).Exec(ctx)
	r.NoError(err)
}

// insertPromotableHost inserts a host with the given suggested checks and
// returns its UID and org slug for promotion tests.
func (f *discoveryFixture) insertPromotableHost(
	t *testing.T, ip string, suggested []disc.SuggestedCheck,
) string {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	job := models.NewJob(&f.org.UID, string(jobdef.JobTypeNetworkDiscovery))
	r.NoError(f.dbSvc.CreateJob(ctx, job))

	host := models.NewDiscoveredHost(f.org.UID, job.UID, ip, models.DiscoverySourceLAN)
	raw, err := json.Marshal(suggested)
	r.NoError(err)
	host.SuggestedChecks = raw
	_, err = f.dbSvc.DB().NewInsert().Model(host).Exec(ctx)
	r.NoError(err)

	return host.UID
}

func TestPromoteHostSingleCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	ctx := t.Context()

	hostUID := f.insertPromotableHost(t, "192.168.1.5", []disc.SuggestedCheck{
		{Type: "tcp", Config: map[string]any{"host": "192.168.1.5", "port": 22}},
	})

	resp, err := f.svc.PromoteHost(ctx, f.org.UID, f.org.Slug, hostUID, discovery.PromoteRequest{
		Checks: []discovery.PromoteCheckSpec{
			{CheckType: "tcp", Name: "host5", Slug: "host5"},
		},
	})
	r.NoError(err)
	r.Len(resp, 1)
	r.NotNil(resp[0].Type)
	r.Equal("tcp", *resp[0].Type)

	// Host is now promoted, pointing at the created check.
	host, err := f.svc.GetHost(ctx, f.org.UID, hostUID)
	r.NoError(err)
	r.NotNil(host.PromotedToCheckUID)
	r.Equal(resp[0].UID, *host.PromotedToCheckUID)
}

func TestPromoteHostMultiCheckDistinctSlugs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	ctx := t.Context()

	hostUID := f.insertPromotableHost(t, "192.168.1.6", []disc.SuggestedCheck{
		{Type: "http", Config: map[string]any{"url": "http://192.168.1.6"}},
		{Type: "ping", Config: map[string]any{"host": "192.168.1.6"}},
	})

	// "ping" mirrors the suggest engine output and must be normalized to a
	// valid "icmp" check at promotion time.
	resp, err := f.svc.PromoteHost(ctx, f.org.UID, f.org.Slug, hostUID, discovery.PromoteRequest{
		Checks: []discovery.PromoteCheckSpec{
			{CheckType: "http", Name: "host6 (http)", Slug: "host6-http"},
			{CheckType: "ping", Name: "host6 (ping)", Slug: "host6-ping"},
		},
	})
	r.NoError(err)
	r.Len(resp, 2)
	r.NotNil(resp[0].Slug)
	r.NotNil(resp[1].Slug)
	r.NotEqual(*resp[0].Slug, *resp[1].Slug)

	// promoted_to_check_uid points at the first created check.
	host, err := f.svc.GetHost(ctx, f.org.UID, hostUID)
	r.NoError(err)
	r.NotNil(host.PromotedToCheckUID)
	r.Equal(resp[0].UID, *host.PromotedToCheckUID)
}

func TestPromoteHostAlreadyPromoted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	ctx := t.Context()

	hostUID := f.insertPromotableHost(t, "192.168.1.7", []disc.SuggestedCheck{
		{Type: "tcp", Config: map[string]any{"host": "192.168.1.7", "port": 22}},
	})

	_, err := f.svc.PromoteHost(ctx, f.org.UID, f.org.Slug, hostUID, discovery.PromoteRequest{
		Checks: []discovery.PromoteCheckSpec{{CheckType: "tcp", Slug: "host7"}},
	})
	r.NoError(err)

	_, err = f.svc.PromoteHost(ctx, f.org.UID, f.org.Slug, hostUID, discovery.PromoteRequest{
		Checks: []discovery.PromoteCheckSpec{{CheckType: "tcp", Slug: "host7-again"}},
	})
	r.ErrorIs(err, discovery.ErrAlreadyPromoted)
}

func TestPromoteHostDefaultsNameToIP(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	ctx := t.Context()

	hostUID := f.insertPromotableHost(t, "192.168.1.8", nil)

	// No suggested checks → manual fallback uses host IP as config base; supply
	// the port that the tcp checker requires via ExtraConfig.
	resp, err := f.svc.PromoteHost(ctx, f.org.UID, f.org.Slug, hostUID, discovery.PromoteRequest{
		Checks: []discovery.PromoteCheckSpec{
			{CheckType: "tcp", Slug: "host8", ExtraConfig: map[string]any{"port": 22}},
		},
	})
	r.NoError(err)
	r.Len(resp, 1)
	r.NotNil(resp[0].Name)
	r.Equal("192.168.1.8", *resp[0].Name)
}

func TestStartFreeboxScanGranted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	srv := startFakeFreebox(t)
	conn := f.newFreeboxChannel(t, srv.URL, models.FreeboxStatusGranted)

	job, err := f.svc.StartFreeboxScan(t.Context(), f.org.UID, conn.UID)
	r.NoError(err)
	r.NotNil(job)
	r.Equal(string(jobdef.JobTypeFreeboxLanDiscovery), job.Type)
}

func TestStartFreeboxScanNotGranted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	srv := startFakeFreebox(t)
	conn := f.newFreeboxChannel(t, srv.URL, models.FreeboxStatusPairing)

	_, err := f.svc.StartFreeboxScan(t.Context(), f.org.UID, conn.UID)
	r.ErrorIs(err, discovery.ErrFreeboxNotGranted)
}

func TestStartFreeboxScanMissingChannel(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)

	_, err := f.svc.StartFreeboxScan(t.Context(), f.org.UID, "00000000-0000-0000-0000-000000000000")
	r.ErrorIs(err, discovery.ErrFreeboxChannelNotFound)
}

func TestStartFreeboxScanGuardsDuplicate(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	srv := startFakeFreebox(t)
	conn := f.newFreeboxChannel(t, srv.URL, models.FreeboxStatusGranted)

	// Simulate a running freebox discovery job.
	running := models.NewJob(&f.org.UID, string(jobdef.JobTypeFreeboxLanDiscovery))
	running.Status = models.JobStatusRunning
	r.NoError(f.dbSvc.CreateJob(t.Context(), running))

	_, err := f.svc.StartFreeboxScan(t.Context(), f.org.UID, conn.UID)
	r.ErrorIs(err, discovery.ErrAlreadyRunning)
}

func TestListHostsSourceFilter(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)

	f.insertHost(t, "192.168.1.10", models.DiscoverySourceLAN)
	f.insertHost(t, "192.168.1.20", models.DiscoverySourceFreebox)

	all, err := f.svc.ListHosts(t.Context(), f.org.UID, discovery.ListHostsOptions{})
	r.NoError(err)
	r.Len(all, 2)

	fbOnly, err := f.svc.ListHosts(t.Context(), f.org.UID, discovery.ListHostsOptions{
		Sources: []models.DiscoverySource{models.DiscoverySourceFreebox},
	})
	r.NoError(err)
	r.Len(fbOnly, 1)
	r.Equal(models.DiscoverySourceFreebox, fbOnly[0].Source)
	r.Equal("192.168.1.20", fbOnly[0].IP)
}

func TestCrossSourceUpsertCoexists(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	ctx := t.Context()

	job := models.NewJob(&f.org.UID, string(jobdef.JobTypeNetworkDiscovery))
	r.NoError(f.dbSvc.CreateJob(ctx, job))

	// Same IP, two sources → two rows after upsert.
	lan := models.NewDiscoveredHost(f.org.UID, job.UID, "192.168.1.50", models.DiscoverySourceLAN)
	r.NoError(f.svc.UpsertDiscoveredHost(ctx, lan))

	fb := models.NewDiscoveredHost(f.org.UID, job.UID, "192.168.1.50", models.DiscoverySourceFreebox)
	r.NoError(f.svc.UpsertDiscoveredHost(ctx, fb))

	hosts, err := f.svc.ListHosts(ctx, f.org.UID, discovery.ListHostsOptions{})
	r.NoError(err)
	r.Len(hosts, 2, "same ip from two sources must coexist as separate rows")

	// Same (org, ip, source) again → updates in place, no duplicate.
	fbAgain := models.NewDiscoveredHost(f.org.UID, job.UID, "192.168.1.50", models.DiscoverySourceFreebox)
	r.NoError(f.svc.UpsertDiscoveredHost(ctx, fbAgain))

	hosts, err = f.svc.ListHosts(ctx, f.org.UID, discovery.ListHostsOptions{})
	r.NoError(err)
	r.Len(hosts, 2, "re-upsert of same source must update in place")
}
