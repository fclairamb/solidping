package discovery_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/discovery/scantypes"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/discovery"
	"github.com/fclairamb/solidping/server/internal/integrations/freebox"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/regions"
)

type discoveryFixture struct {
	svc   *discovery.Service
	dbSvc db.Service
	org   *models.Organization
}

func newDiscoveryFixture(t *testing.T) *discoveryFixture {
	t.Helper()

	return newDiscoveryFixtureWith(t, nil, false)
}

// newDiscoveryFixtureWith builds the fixture with the given process egress
// guard and docker gate (a SaaS deployment is egress.New(false) + true).
func newDiscoveryFixtureWith(t *testing.T, guard *egress.Guard, dockerBlocked bool) *discoveryFixture {
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

	jobSvc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	checksSvc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), creds, nil)
	svc := discovery.NewService(dbSvc.DB(), dbSvc, checksSvc, jobSvc, creds, guard, dockerBlocked)

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

func (f *discoveryFixture) newFreeboxChannel(t *testing.T, baseURL, status string) *models.Integration {
	t.Helper()

	r := require.New(t)

	settings := &models.FreeboxSettings{BaseURL: baseURL, AppID: freebox.DefaultAppID, Status: status}
	m, err := settings.ToJSONMap()
	r.NoError(err)
	m["appToken"] = "permanent-token"

	conn := models.NewIntegration(f.org.UID, models.ConnectionTypeFreebox, "Freebox")
	conn.Settings = m
	r.NoError(f.dbSvc.CreateChannel(t.Context(), conn))

	return conn
}

// insertCheck inserts a discovered check with the given source/group under a
// freshly created job (to satisfy the scan rollup), returning its UID.
func (f *discoveryFixture) insertCheck(
	t *testing.T, jobUID, group, slug, checkType string, source models.DiscoverySource, cfg json.RawMessage,
) string {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dc := models.NewDiscoveredCheck(
		f.org.UID, jobUID, source, group, group, group+" · "+slug, slug, checkType, cfg, nil, "",
	)
	_, err := f.dbSvc.DB().NewInsert().Model(dc).Exec(ctx)
	r.NoError(err)

	return dc.UID
}

// newScanJob creates a network_discovery job row and returns its UID (used as the
// scan UID for discovered checks).
func (f *discoveryFixture) newScanJob(t *testing.T) string {
	t.Helper()

	r := require.New(t)
	job := models.NewJob(&f.org.UID, string(jobdef.JobTypeNetworkDiscovery))
	r.NoError(f.dbSvc.CreateJob(t.Context(), job))

	return job.UID
}

func TestStartScanUnknownType(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)

	_, err := f.svc.StartScan(t.Context(), f.org.UID, "nope", "", json.RawMessage(`{}`))
	r.Error(err)

	var de *scantypes.DiscoveryError
	r.ErrorAs(err, &de)
	r.Equal(scantypes.CodeUnknownType, de.Code)
}

func TestStartScanLANCreatesPlanJob(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)

	job, err := f.svc.StartScan(t.Context(), f.org.UID, "lan", "", json.RawMessage(`{"cidrs":["10.0.0.0/18"]}`))
	r.NoError(err)
	r.NotNil(job)
	r.Equal(string(jobdef.JobTypeNetworkDiscoveryPlan), job.Type, "LAN scan must create a plan job")
}

func TestStartScanLANBadParams(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)

	_, err := f.svc.StartScan(t.Context(), f.org.UID, "lan", "", json.RawMessage(`{}`))
	r.Error(err)

	var de *scantypes.DiscoveryError
	r.ErrorAs(err, &de)
	r.Equal(scantypes.CodeInvalidParameters, de.Code)
}

func TestStartScanLANRejectsRangeOverCeiling(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)

	_, err := f.svc.StartScan(t.Context(), f.org.UID, "lan", "", json.RawMessage(`{"cidrs":["0.0.0.0/7"]}`))
	r.Error(err)

	var de *scantypes.DiscoveryError
	r.ErrorAs(err, &de)
	r.Equal("DISCOVERY_RANGE_TOO_LARGE", de.Code)
}

func TestStartScanFreeboxGranted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	srv := startFakeFreebox(t)
	conn := f.newFreeboxChannel(t, srv.URL, models.FreeboxStatusGranted)

	params := json.RawMessage(`{"channelUid":"` + conn.UID + `"}`)
	job, err := f.svc.StartScan(t.Context(), f.org.UID, "freebox", "", params)
	r.NoError(err)
	r.NotNil(job)
	r.Equal(string(jobdef.JobTypeFreeboxLanDiscovery), job.Type)
}

func TestStartScanFreeboxNotGranted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	srv := startFakeFreebox(t)
	conn := f.newFreeboxChannel(t, srv.URL, models.FreeboxStatusPairing)

	params := json.RawMessage(`{"channelUid":"` + conn.UID + `"}`)
	_, err := f.svc.StartScan(t.Context(), f.org.UID, "freebox", "", params)
	r.Error(err)

	var de *scantypes.DiscoveryError
	r.ErrorAs(err, &de)
	r.Equal(scantypes.CodeFreeboxNotGranted, de.Code)
}

func TestStartScanFreeboxGuardsDuplicate(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	srv := startFakeFreebox(t)
	conn := f.newFreeboxChannel(t, srv.URL, models.FreeboxStatusGranted)

	// Simulate a running freebox discovery job.
	running := models.NewJob(&f.org.UID, string(jobdef.JobTypeFreeboxLanDiscovery))
	running.Status = models.JobStatusRunning
	r.NoError(f.dbSvc.CreateJob(t.Context(), running))

	params := json.RawMessage(`{"channelUid":"` + conn.UID + `"}`)
	_, err := f.svc.StartScan(t.Context(), f.org.UID, "freebox", "", params)
	r.ErrorIs(err, discovery.ErrAlreadyRunning)
}

func TestStartScanContainerCreatesJob(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)

	params := json.RawMessage(`{"hosts":["unix:///var/run/docker.sock"]}`)
	job, err := f.svc.StartScan(t.Context(), f.org.UID, "container", "", params)
	r.NoError(err)
	r.NotNil(job)
	r.Equal(string(jobdef.JobTypeContainerDiscovery), job.Type, "container scan must create a container_discovery job")
}

func TestStartScanContainerBadEndpoint(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)

	_, err := f.svc.StartScan(t.Context(), f.org.UID, "container", "", json.RawMessage(`{"hosts":["http://10.0.0.5:2375"]}`))
	r.Error(err)

	var de *scantypes.DiscoveryError
	r.ErrorAs(err, &de)
	r.Equal(scantypes.CodeInvalidParameters, de.Code)
}

func TestStartScanContainerGuardsDuplicate(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)

	// Simulate a running container discovery job.
	running := models.NewJob(&f.org.UID, string(jobdef.JobTypeContainerDiscovery))
	running.Status = models.JobStatusRunning
	r.NoError(f.dbSvc.CreateJob(t.Context(), running))

	params := json.RawMessage(`{"hosts":["unix:///var/run/docker.sock"]}`)
	_, err := f.svc.StartScan(t.Context(), f.org.UID, "container", "", params)
	r.ErrorIs(err, discovery.ErrAlreadyRunning)
}

func TestPromoteChecksMultiple(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	ctx := t.Context()

	jobUID := f.newScanJob(t)
	httpUID := f.insertCheck(t, jobUID, "192.168.1.6", "http-192-168-1-6", "http",
		models.DiscoverySourceLAN, json.RawMessage(`{"url":"http://192.168.1.6"}`))
	icmpUID := f.insertCheck(t, jobUID, "192.168.1.6", "icmp-192-168-1-6", "ping",
		models.DiscoverySourceLAN, json.RawMessage(`{"host":"192.168.1.6"}`))

	resp, err := f.svc.PromoteChecks(ctx, f.org.UID, f.org.Slug, discovery.PromoteRequest{
		UIDs: []string{httpUID, icmpUID},
	})
	r.NoError(err)
	r.Len(resp, 2)

	// Each created check is labeled auto-discovery and has a distinct slug.
	r.NotEqual(*resp[0].Slug, *resp[1].Slug)
	for _, cr := range resp {
		r.Equal("true", cr.Labels["auto-discovery"])
		r.Equal(jobUID, cr.Labels["discovery-job"])
	}

	// The "ping" suggestion must have been normalized to a valid "icmp" check.
	types := map[string]bool{}
	for _, cr := range resp {
		types[*cr.Type] = true
	}
	r.True(types["http"])
	r.True(types["icmp"], "ping must normalize to icmp")

	// Both rows are now promoted.
	for _, uid := range []string{httpUID, icmpUID} {
		row, gErr := f.svc.GetCheck(ctx, f.org.UID, uid)
		r.NoError(gErr)
		r.NotNil(row.PromotedToCheckUID)
	}
}

func TestPromoteChecksSingle(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	ctx := t.Context()

	jobUID := f.newScanJob(t)
	uid := f.insertCheck(t, jobUID, "192.168.1.5", "tcp-192-168-1-5-22", "tcp",
		models.DiscoverySourceLAN, json.RawMessage(`{"host":"192.168.1.5","port":22}`))

	resp, err := f.svc.PromoteChecks(ctx, f.org.UID, f.org.Slug, discovery.PromoteRequest{UIDs: []string{uid}})
	r.NoError(err)
	r.Len(resp, 1)
	r.Equal("tcp", *resp[0].Type)

	row, err := f.svc.GetCheck(ctx, f.org.UID, uid)
	r.NoError(err)
	r.NotNil(row.PromotedToCheckUID)
	r.Equal(resp[0].UID, *row.PromotedToCheckUID)
}

func TestPromoteChecksNameOverride(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	ctx := t.Context()

	jobUID := f.newScanJob(t)
	uid := f.insertCheck(t, jobUID, "192.168.1.8", "tcp-192-168-1-8-22", "tcp",
		models.DiscoverySourceLAN, json.RawMessage(`{"host":"192.168.1.8","port":22}`))

	period := "PT2M"
	resp, err := f.svc.PromoteChecks(ctx, f.org.UID, f.org.Slug, discovery.PromoteRequest{
		UIDs:      []string{uid},
		Overrides: discovery.PromoteOverrides{Name: "Custom Name", Period: &period},
	})
	r.NoError(err)
	r.Len(resp, 1)
	r.Equal("Custom Name", *resp[0].Name)
}

func TestPromoteChecksAlreadyPromoted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	ctx := t.Context()

	jobUID := f.newScanJob(t)
	uid := f.insertCheck(t, jobUID, "192.168.1.7", "tcp-192-168-1-7-22", "tcp",
		models.DiscoverySourceLAN, json.RawMessage(`{"host":"192.168.1.7","port":22}`))

	_, err := f.svc.PromoteChecks(ctx, f.org.UID, f.org.Slug, discovery.PromoteRequest{UIDs: []string{uid}})
	r.NoError(err)

	_, err = f.svc.PromoteChecks(ctx, f.org.UID, f.org.Slug, discovery.PromoteRequest{UIDs: []string{uid}})
	r.ErrorIs(err, discovery.ErrAlreadyPromoted)
}

func TestPromoteChecksNotFound(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)

	_, err := f.svc.PromoteChecks(t.Context(), f.org.UID, f.org.Slug, discovery.PromoteRequest{
		UIDs: []string{"00000000-0000-0000-0000-000000000000"},
	})
	r.ErrorIs(err, discovery.ErrCheckNotFound)
}

func TestListDiscoveredChecksFilters(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	jobUID := f.newScanJob(t)

	f.insertCheck(t, jobUID, "192.168.1.10", "a-tcp", "tcp",
		models.DiscoverySourceLAN, json.RawMessage(`{"host":"192.168.1.10","port":22}`))
	f.insertCheck(t, jobUID, "192.168.1.20", "b-icmp", "ping",
		models.DiscoverySourceFreebox, json.RawMessage(`{"host":"192.168.1.20"}`))

	all, err := f.svc.ListDiscoveredChecks(t.Context(), f.org.UID, &discovery.ListChecksOptions{})
	r.NoError(err)
	r.Len(all, 2)

	fbOnly, err := f.svc.ListDiscoveredChecks(t.Context(), f.org.UID, &discovery.ListChecksOptions{
		Sources: []models.DiscoverySource{models.DiscoverySourceFreebox},
	})
	r.NoError(err)
	r.Len(fbOnly, 1)
	r.Equal(models.DiscoverySourceFreebox, fbOnly[0].Source)
	r.Equal("192.168.1.20", fbOnly[0].GroupKey)

	grouped, err := f.svc.ListDiscoveredChecks(t.Context(), f.org.UID, &discovery.ListChecksOptions{
		Group: "192.168.1.10",
	})
	r.NoError(err)
	r.Len(grouped, 1)
	r.Equal("192.168.1.10", grouped[0].GroupKey)
}

func TestDismissCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	jobUID := f.newScanJob(t)

	uid := f.insertCheck(t, jobUID, "192.168.1.30", "x-tcp", "tcp",
		models.DiscoverySourceLAN, json.RawMessage(`{"host":"192.168.1.30","port":22}`))

	r.NoError(f.svc.SoftDeleteCheck(t.Context(), f.org.UID, uid))

	all, err := f.svc.ListDiscoveredChecks(t.Context(), f.org.UID, &discovery.ListChecksOptions{})
	r.NoError(err)
	r.Empty(all, "a dismissed check must not appear in the list")

	r.ErrorIs(f.svc.SoftDeleteCheck(t.Context(), f.org.UID, uid), discovery.ErrCheckNotFound)
}

func TestDismissGroup(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	jobUID := f.newScanJob(t)

	f.insertCheck(t, jobUID, "192.168.1.40", "g-http", "http",
		models.DiscoverySourceLAN, json.RawMessage(`{"url":"http://192.168.1.40"}`))
	f.insertCheck(t, jobUID, "192.168.1.40", "g-icmp", "ping",
		models.DiscoverySourceLAN, json.RawMessage(`{"host":"192.168.1.40"}`))
	// A different group must survive.
	f.insertCheck(t, jobUID, "192.168.1.41", "h-tcp", "tcp",
		models.DiscoverySourceLAN, json.RawMessage(`{"host":"192.168.1.41","port":22}`))

	n, err := f.svc.SoftDeleteGroup(t.Context(), f.org.UID, jobUID, "192.168.1.40", "")
	r.NoError(err)
	r.Equal(2, n)

	remaining, err := f.svc.ListDiscoveredChecks(t.Context(), f.org.UID, &discovery.ListChecksOptions{})
	r.NoError(err)
	r.Len(remaining, 1)
	r.Equal("192.168.1.41", remaining[0].GroupKey)
}

// newChildJob inserts a network_discovery child job carrying the given parent UID
// and status, returning it.
func (f *discoveryFixture) newChildJob(
	t *testing.T, parentUID string, status models.JobStatus,
) *models.Job {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	job := models.NewJob(&f.org.UID, string(jobdef.JobTypeNetworkDiscovery))
	job.Status = status
	job.Config = models.JSONMap{
		"cidrs":        []string{"10.0.0.0/24"},
		"parentJobUid": parentUID,
	}
	r.NoError(f.dbSvc.CreateJob(ctx, job))

	return job
}

func TestStartScanGuardsOnPendingChild(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	ctx := t.Context()

	plan := models.NewJob(&f.org.UID, string(jobdef.JobTypeNetworkDiscoveryPlan))
	plan.Status = models.JobStatusSuccess
	r.NoError(f.dbSvc.CreateJob(ctx, plan))
	f.newChildJob(t, plan.UID, models.JobStatusPending)

	_, err := f.svc.StartScan(ctx, f.org.UID, "lan", "", json.RawMessage(`{"cidrs":["192.168.1.0/24"]}`))
	r.ErrorIs(err, discovery.ErrAlreadyRunning)
}

func TestStartScanIgnoresStaleRunningChild(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	ctx := t.Context()

	plan := models.NewJob(&f.org.UID, string(jobdef.JobTypeNetworkDiscoveryPlan))
	plan.Status = models.JobStatusSuccess
	r.NoError(f.dbSvc.CreateJob(ctx, plan))

	child := f.newChildJob(t, plan.UID, models.JobStatusRunning)
	stale := time.Now().Add(-2 * time.Hour)
	_, err := f.dbSvc.DB().NewUpdate().
		Model((*models.Job)(nil)).
		Set("updated_at = ?", stale).
		Where("uid = ?", child.UID).
		Exec(ctx)
	r.NoError(err)

	job, err := f.svc.StartScan(ctx, f.org.UID, "lan", "", json.RawMessage(`{"cidrs":["192.168.1.0/24"]}`))
	r.NoError(err, "a stale running child must not block a new scan")
	r.NotNil(job)
}

// TestCancelScanDropsLiveChildren: cancel soft-deletes pending AND running
// children. Deleting a running child is what un-blocks the already-running
// guard immediately (a stopped scan previously kept the org blocked from
// starting a new scan for the remaining chunk duration — ~1min measured — the
// root cause of the flaky scan-start E2E) and what the worker's cancellation
// watcher keys on to abort the chunk. A new scan must be accepted right after
// the cancel, with no waiting.
func TestCancelScanDropsLiveChildren(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	ctx := t.Context()

	plan := models.NewJob(&f.org.UID, string(jobdef.JobTypeNetworkDiscoveryPlan))
	plan.Status = models.JobStatusSuccess
	r.NoError(f.dbSvc.CreateJob(ctx, plan))

	pending := f.newChildJob(t, plan.UID, models.JobStatusPending)
	running := f.newChildJob(t, plan.UID, models.JobStatusRunning)

	// The live children block a new scan before the cancel…
	_, err := f.svc.StartScan(ctx, f.org.UID, "lan", "", json.RawMessage(`{"cidrs":["192.168.1.0/24"]}`))
	r.ErrorIs(err, discovery.ErrAlreadyRunning)

	r.NoError(f.svc.CancelScan(ctx, f.org.UID, plan.UID))

	var pendingJob models.Job
	err = f.dbSvc.DB().NewSelect().Model(&pendingJob).Where("uid = ?", pending.UID).Scan(ctx)
	r.NoError(err)
	r.NotNil(pendingJob.DeletedAt, "pending child must be soft-deleted")

	var runningJob models.Job
	err = f.dbSvc.DB().NewSelect().Model(&runningJob).Where("uid = ?", running.UID).Scan(ctx)
	r.NoError(err)
	r.NotNil(runningJob.DeletedAt, "running child must be soft-deleted so the guard clears and the watcher aborts it")

	// …and immediately after the cancel a new scan is accepted.
	job, err := f.svc.StartScan(ctx, f.org.UID, "lan", "", json.RawMessage(`{"cidrs":["192.168.1.0/24"]}`))
	r.NoError(err, "a new scan must start immediately after cancel")
	r.NotNil(job)
}

func TestCancelScanUnknownScan(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)

	err := f.svc.CancelScan(t.Context(), f.org.UID, "00000000-0000-0000-0000-000000000000")
	r.ErrorIs(err, discovery.ErrScanNotFound)
}

func TestGetScanProgressFanOutAggregates(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	ctx := t.Context()

	plan := models.NewJob(&f.org.UID, string(jobdef.JobTypeNetworkDiscoveryPlan))
	plan.Status = models.JobStatusSuccess
	plan.Output = models.JSONMap{"totalChunks": 4}
	r.NoError(f.dbSvc.CreateJob(ctx, plan))

	f.newChildJob(t, plan.UID, models.JobStatusSuccess)
	f.newChildJob(t, plan.UID, models.JobStatusSuccess)
	f.newChildJob(t, plan.UID, models.JobStatusRunning)
	f.newChildJob(t, plan.UID, models.JobStatusPending)

	// Two discovered checks across two groups rolled up under the plan.
	f.insertCheck(t, plan.UID, "10.0.0.1", "c1", "tcp",
		models.DiscoverySourceLAN, json.RawMessage(`{"host":"10.0.0.1","port":22}`))
	f.insertCheck(t, plan.UID, "10.0.0.2", "c2", "tcp",
		models.DiscoverySourceLAN, json.RawMessage(`{"host":"10.0.0.2","port":22}`))

	prog, err := f.svc.GetScanProgress(ctx, f.org.UID, plan.UID)
	r.NoError(err)
	r.Equal(4, prog.TotalChunks)
	r.Equal(2, prog.CompletedChunks)
	r.Equal(1, prog.RunningChunks)
	r.Equal(1, prog.PendingChunks)
	r.Equal(0, prog.FailedChunks)
	r.Equal(string(models.JobStatusRunning), prog.DerivedStatus)
	r.Equal(2, prog.GroupCount)
	r.Equal(2, prog.CheckCount)
}

func TestGetScanProgressNonChunkedSingleChunk(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	ctx := t.Context()

	// A Freebox scan is a single, non-chunked job: progress reports totalChunks=1.
	job := models.NewJob(&f.org.UID, string(jobdef.JobTypeFreeboxLanDiscovery))
	job.Status = models.JobStatusSuccess
	r.NoError(f.dbSvc.CreateJob(ctx, job))

	f.insertCheck(t, job.UID, "192.0.2.10", "fb-icmp", "ping",
		models.DiscoverySourceFreebox, json.RawMessage(`{"host":"192.0.2.10"}`))

	prog, err := f.svc.GetScanProgress(ctx, f.org.UID, job.UID)
	r.NoError(err)
	r.Equal(1, prog.TotalChunks)
	r.Equal(1, prog.CompletedChunks)
	r.Equal(string(models.JobStatusSuccess), prog.DerivedStatus)
	r.Equal(1, prog.GroupCount)
	r.Equal(1, prog.CheckCount)
}

func TestUpsertDiscoveredChecksRescanUpdatesInPlace(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	ctx := t.Context()

	jobUID := f.newScanJob(t)

	rows := []disc.SuggestedCheck{
		{
			GroupKey: "192.168.1.50", GroupLabel: "192.168.1.50",
			Name: "192.168.1.50 · TCP/22", Slug: "tcp-192-168-1-50-22", Type: "tcp",
			Config: json.RawMessage(`{"host":"192.168.1.50","port":22}`),
		},
	}

	db := f.dbSvc.DB()
	r.NoError(disc.UpsertDiscoveredChecks(ctx, db, f.org.UID, jobUID, "", models.DiscoverySourceLAN, rows, nil))
	r.NoError(disc.UpsertDiscoveredChecks(ctx, db, f.org.UID, jobUID, "", models.DiscoverySourceLAN, rows, nil))

	all, err := f.svc.ListDiscoveredChecks(ctx, f.org.UID, &discovery.ListChecksOptions{})
	r.NoError(err)
	r.Len(all, 1, "re-scan of the same (org, source, group, slug) must update in place")
}

// countJobs counts the org's jobs (any type, any status).
func (f *discoveryFixture) countJobs(t *testing.T) int {
	t.Helper()

	count, err := f.dbSvc.DB().NewSelect().TableExpr("jobs").
		Where("organization_uid = ?", f.org.UID).Count(t.Context())
	require.NoError(t, err)

	return count
}

// On a SaaS-shaped service (enforcing egress guard, docker blocked) `lan` and
// `container` scans are refused with DISCOVERY_NOT_AVAILABLE and no job is
// created. The self-hosted service (permissive guard) is the control: it still
// creates the plan job and the container job.
func TestStartScanRefusedUnderEgressPolicy(t *testing.T) {
	t.Parallel()

	scans := []struct {
		typ     string
		params  string
		jobType string
	}{
		{typ: "lan", params: `{"cidrs":["10.0.0.0/24"]}`, jobType: string(jobdef.JobTypeNetworkDiscoveryPlan)},
		{typ: "lan", params: `{"cidrs":["169.254.169.254/32"]}`, jobType: string(jobdef.JobTypeNetworkDiscoveryPlan)},
		{
			typ: "container", params: `{"hosts":["unix:///var/run/docker.sock"]}`,
			jobType: string(jobdef.JobTypeContainerDiscovery),
		},
		{
			typ: "container", params: `{"hosts":["tcp://10.0.0.5:2375"]}`,
			jobType: string(jobdef.JobTypeContainerDiscovery),
		},
	}

	for _, sc := range scans {
		t.Run(sc.typ+" "+sc.params, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			saas := newDiscoveryFixtureWith(t, egress.New(false), true)
			job, err := saas.svc.StartScan(t.Context(), saas.org.UID, sc.typ, "", json.RawMessage(sc.params))
			r.Nil(job)

			var de *scantypes.DiscoveryError
			r.ErrorAs(err, &de)
			r.Equal(scantypes.CodeNotAvailable, de.Code)
			r.Zero(saas.countJobs(t), "a refused scan must create no job")

			selfHosted := newDiscoveryFixtureWith(t, egress.New(true), false)
			job, err = selfHosted.svc.StartScan(t.Context(), selfHosted.org.UID, sc.typ, "", json.RawMessage(sc.params))
			r.NoError(err)
			r.Equal(sc.jobType, job.Type)
			r.Equal(1, selfHosted.countJobs(t))
		})
	}
}

// AvailableTypes hides the types the deployment refuses.
func TestAvailableTypesHidesRefusedTypes(t *testing.T) {
	t.Parallel()

	typesOf := func(f *discoveryFixture) []string {
		defs := f.svc.AvailableTypes()
		out := make([]string, 0, len(defs))

		for _, d := range defs {
			out = append(out, d.Type())
		}

		return out
	}

	r := require.New(t)
	r.Equal([]string{"freebox", "kubernetes"}, typesOf(newDiscoveryFixtureWith(t, egress.New(false), true)))
	r.Equal([]string{"container", "freebox", "kubernetes"}, typesOf(newDiscoveryFixtureWith(t, egress.New(false), false)))
	r.Equal([]string{"container", "freebox", "kubernetes", "lan"}, typesOf(newDiscoveryFixture(t)))
}

// insertRegionCheck inserts a discovered check found from the given region.
func (f *discoveryFixture) insertRegionCheck(t *testing.T, jobUID, group, slug, region string) string {
	t.Helper()

	dc := models.NewDiscoveredCheck(
		f.org.UID, jobUID, models.DiscoverySourceLAN, group, group, group+" · "+slug, slug, "tcp",
		json.RawMessage(`{"host":"`+group+`","port":22}`), nil, region,
	)
	_, err := f.dbSvc.DB().NewInsert().Model(dc).Exec(t.Context())
	require.NoError(t, err)

	return dc.UID
}

func (f *discoveryFixture) setRegions(t *testing.T, slugs ...string) {
	t.Helper()

	defs := make([]regions.RegionDefinition, 0, len(slugs))
	for _, s := range slugs {
		defs = append(defs, regions.RegionDefinition{Slug: s, Name: s})
	}

	require.NoError(t, regions.NewService(f.dbSvc).SetOrgCustomRegions(t.Context(), f.org.UID, defs))
}

func TestUpsertDiscoveredChecksRegionIdentity(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	ctx := t.Context()
	db := f.dbSvc.DB()
	jobUID := f.newScanJob(t)

	rows := []disc.SuggestedCheck{{
		GroupKey: "10.0.0.5", GroupLabel: "10.0.0.5", Name: "10.0.0.5 · TCP/22", Slug: "tcp-10-0-0-5-22",
		Type: "tcp", Config: json.RawMessage(`{"host":"10.0.0.5","port":22}`),
	}}

	for _, region := range []string{"", "@dc1", "", "@dc1"} {
		r.NoError(disc.UpsertDiscoveredChecks(ctx, db, f.org.UID, jobUID, region, models.DiscoverySourceLAN, rows, nil))
	}

	all, err := f.svc.ListDiscoveredChecks(ctx, f.org.UID, &discovery.ListChecksOptions{})
	r.NoError(err)
	r.Len(all, 2, "same IP from the server and from @dc1 is two rows, re-scans update in place")

	got := map[string]bool{}
	for _, row := range all {
		got[row.Region] = true
	}

	r.True(got[""])
	r.True(got["@dc1"])
}

func TestPromoteChecksRegion(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	ctx := t.Context()
	jobUID := f.newScanJob(t)
	f.setRegions(t, "dc1")

	uid := f.insertRegionCheck(t, jobUID, "10.0.0.5", "tcp-10-0-0-5-22", "@dc1")

	resp, err := f.svc.PromoteChecks(ctx, f.org.UID, f.org.Slug, discovery.PromoteRequest{UIDs: []string{uid}})
	r.NoError(err)
	r.Len(resp, 1)
	r.Equal([]string{"@dc1"}, resp[0].Regions)
	r.Equal(models.PlacementPinned, resp[0].Placement)
}

func TestPromoteChecksRegionUnavailable(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	ctx := t.Context()
	jobUID := f.newScanJob(t)
	f.setRegions(t, "dc2")

	serverUID := f.insertRegionCheck(t, jobUID, "10.0.0.6", "tcp-10-0-0-6-22", "")
	goneUID := f.insertRegionCheck(t, jobUID, "10.0.0.5", "tcp-10-0-0-5-22", "@dc1")

	_, err := f.svc.PromoteChecks(ctx, f.org.UID, f.org.Slug, discovery.PromoteRequest{
		UIDs: []string{serverUID, goneUID},
	})
	r.ErrorIs(err, discovery.ErrRegionUnavailable)

	// Nothing was created: the server-side row stays unpromoted too.
	row, err := f.svc.GetCheck(ctx, f.org.UID, serverUID)
	r.NoError(err)
	r.Nil(row.PromotedToCheckUID)

	n, err := f.dbSvc.DB().NewSelect().TableExpr("checks").Where("organization_uid = ?", f.org.UID).Count(ctx)
	r.NoError(err)
	r.Zero(n)
}

func TestPromoteChecksNoRegionKeepsDefaultPlacement(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	uid := f.insertRegionCheck(t, f.newScanJob(t), "10.0.0.7", "tcp-10-0-0-7-22", "")

	resp, err := f.svc.PromoteChecks(t.Context(), f.org.UID, f.org.Slug, discovery.PromoteRequest{UIDs: []string{uid}})
	r.NoError(err)
	r.Len(resp, 1)
	r.NotContains(resp[0].Regions, "")
	r.NotContains(resp[0].Regions, "@dc1")
}

func TestDismissGroupScopedByRegion(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	ctx := t.Context()
	jobUID := f.newScanJob(t)

	f.insertRegionCheck(t, jobUID, "10.0.0.5", "tcp-10-0-0-5-22", "")
	f.insertRegionCheck(t, jobUID, "10.0.0.5", "tcp-10-0-0-5-80", "@dc1")

	n, err := f.svc.SoftDeleteGroup(ctx, f.org.UID, "", "10.0.0.5", "@dc1")
	r.NoError(err)
	r.Equal(1, n)

	remaining, err := f.svc.ListDiscoveredChecks(ctx, f.org.UID, &discovery.ListChecksOptions{})
	r.NoError(err)
	r.Len(remaining, 1)
	r.Empty(remaining[0].Region, "the server-side row with the same IP is untouched")
}
