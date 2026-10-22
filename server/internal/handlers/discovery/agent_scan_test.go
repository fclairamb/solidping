package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/discovery/scantypes"
	"github.com/fclairamb/solidping/server/internal/discoverytask"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/discovery"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// agentScanFixture is a SaaS-shaped deployment (enforcing egress guard) whose
// org has the private region @dc1 served by a live agent reporting discovery,
// @dc2 served by an agent that does not, and an admin member.
type agentScanFixture struct {
	*discoveryFixture
	router *httpx.Router
	admin  *models.User
	tasks  *workertasks.Service
}

func newAgentScanFixture(t *testing.T, mode string) *agentScanFixture {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	f := newDiscoveryFixtureWith(t, egress.New(false), true)
	f.setRegions(t, "dc1", "dc2")

	tasks := workertasks.NewService(f.dbSvc.DB())
	f.svc.SetWorkerTasks(tasks)
	f.svc.SetEntitlements(entitlements.NewService(f.dbSvc, entitlements.DefaultsFor(mode), 0))

	addAgent := func(region, name string, caps []string) {
		agent := models.NewAgent(f.org.UID, region, name, "ed-"+name, "x-"+name, "fp-"+name)
		_, err := f.dbSvc.DB().NewInsert().Model(agent).Exec(ctx)
		r.NoError(err)
		r.NoError(f.dbSvc.UpdateAgentLastSeen(ctx, agent.UID, time.Now()))

		_, err = f.dbSvc.RegisterOrUpdateWorker(ctx, &models.Worker{
			UID: agent.UID, Slug: agents.WorkerSlug(agent.UID), Name: "agent:" + name, Capabilities: caps,
		})
		r.NoError(err)
	}

	addAgent("@dc1", "dc1", []string{"ipv4", discoverytask.Capability})
	addAgent("@dc2", "dc2", []string{"ipv4"})

	admin := f.addMember(t, "alice@acme.com", models.MemberRoleAdmin)

	return &agentScanFixture{discoveryFixture: f, router: f.newRouter(t), admin: admin, tasks: tasks}
}

func (f *discoveryFixture) addMember(t *testing.T, email string, role models.MemberRole) *models.User {
	t.Helper()

	user := models.NewUser(email)
	require.NoError(t, f.dbSvc.CreateUser(t.Context(), user))
	require.NoError(t, f.dbSvc.CreateOrganizationMember(t.Context(),
		models.NewOrganizationMember(f.org.UID, user.UID, role)))

	return user
}

// do sends a request as userUID holding an admin token (claims.Role), with
// the audit actor RequireAuth would set.
func (f *agentScanFixture) do(t *testing.T, userUID, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var raw []byte

	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		require.NoError(t, err)
	}

	ctx := context.WithValue(t.Context(), base.ContextKeyClaims, &auth.Claims{
		UserUID: userUID, OrgSlug: f.org.Slug, Role: "admin",
	})
	ctx = context.WithValue(ctx, base.ContextKeyOrganization, f.org)
	ctx = audit.WithUser(ctx, userUID, models.ActorTypeUser)

	req := httptest.NewRequestWithContext(ctx, method, path, bytes.NewBuffer(raw))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)

	return rec
}

func lanScan(region, cidr string) discovery.StartScanRequest {
	return discovery.StartScanRequest{
		Type: "lan", Region: region, Parameters: json.RawMessage(`{"cidrs":["` + cidr + `"],"ports":[22,80]}`),
	}
}

func errorCodeOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var body struct {
		Code string `json:"code"`
	}

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())

	return body.Code
}

func scanUIDOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var body struct {
		Data struct {
			UID  string `json:"uid"`
			Type string `json:"type"`
		} `json:"data"`
	}

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	require.Equal(t, string(jobdef.JobTypeNetworkDiscoveryAgentPlan), body.Data.Type)

	return body.Data.UID
}

// Under an enforcing guard a region-bound lan scan is accepted and becomes an
// agent plan; without a region the same scan stays refused.
func TestAgentScanAcceptedUnderEgressPolicy(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newAgentScanFixture(t, config.DeploymentModeSaaS)

	rec := f.do(t, f.admin.UID, http.MethodPost, f.scansPath(), lanScan("", "10.0.0.0/24"))
	r.Equal(http.StatusForbidden, rec.Code)
	r.Equal(scantypes.CodeNotAvailable, errorCodeOf(t, rec))

	rec = f.do(t, f.admin.UID, http.MethodPost, f.scansPath(), lanScan("@dc1", "10.0.0.0/24"))
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())
	scanUID := scanUIDOf(t, rec)

	// discovery.scan_started carries the actor and the region.
	events, err := f.dbSvc.ListEvents(t.Context(), &models.ListEventsFilter{
		OrganizationUID: f.org.UID,
		EventTypes:      []models.EventType{models.EventTypeDiscoveryScanStarted},
		Limit:           10,
	})
	r.NoError(err)
	r.Len(events, 1)
	r.NotNil(events[0].ActorUID)
	r.Equal(f.admin.UID, *events[0].ActorUID)
	r.Equal("@dc1", events[0].Payload["region"])
	r.Equal(scanUID, events[0].Payload["target_uid"])
	r.InDelta(256, events[0].Payload["address_count"], 0)
	r.InDelta(2, events[0].Payload["port_count"], 0)
	r.InDelta(1, events[0].Payload["cidr_count"], 0)

	// GET /types keeps lan for an org with private regions, flagged.
	rec = f.do(t, f.admin.UID, http.MethodGet, "/api/v1/orgs/"+f.org.Slug+"/discovery/types", nil)
	r.Equal(http.StatusOK, rec.Code)
	r.Contains(rec.Body.String(), `{"type":"lan","source":"lan","serverAvailable":false}`)
	r.Contains(rec.Body.String(), `{"type":"freebox","source":"freebox","serverAvailable":true}`)
}

func TestAgentScanRefusals(t *testing.T) {
	t.Parallel()

	f := newAgentScanFixture(t, config.DeploymentModeSaaS)
	demoted := f.addMember(t, "bob@acme.com", models.MemberRoleUser)
	outsider := models.NewUser("eve@acme.com")
	require.NoError(t, f.dbSvc.CreateUser(t.Context(), outsider))

	freebox := discovery.StartScanRequest{
		Type: "freebox", Region: "@dc1", Parameters: json.RawMessage(`{"channelUid":"x"}`),
	}

	tests := []struct {
		name   string
		user   string
		body   discovery.StartScanRequest
		status int
		code   string
	}{
		{"region on a non-lan type", f.admin.UID, freebox, http.StatusUnprocessableEntity,
			string(base.ErrorCodeValidationError)},
		{"foreign region", f.admin.UID, lanScan("@dc9", "10.0.0.0/24"), http.StatusUnprocessableEntity,
			scantypes.CodeInvalidParameters},
		{"cloud slug", f.admin.UID, lanScan("eu-west", "10.0.0.0/24"), http.StatusUnprocessableEntity,
			scantypes.CodeInvalidParameters},
		{"legacy spelling", f.admin.UID, lanScan("@acmetech/dc1", "10.0.0.0/24"), http.StatusUnprocessableEntity,
			scantypes.CodeInvalidParameters},
		{"no live discovery agent", f.admin.UID, lanScan("@dc2", "10.0.0.0/24"), http.StatusUnprocessableEntity,
			scantypes.CodeRegionUnavailable},
		{"demoted admin with a valid token", demoted.UID, lanScan("@dc1", "10.0.0.0/24"), http.StatusForbidden,
			string(base.ErrorCodeForbidden)},
		{"no membership (super admin)", outsider.UID, lanScan("@dc1", "10.0.0.0/24"), http.StatusForbidden,
			string(base.ErrorCodeForbidden)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			rec := f.do(t, tc.user, http.MethodPost, f.scansPath(), tc.body)
			r.Equal(tc.status, rec.Code, rec.Body.String())
			r.Equal(tc.code, errorCodeOf(t, rec))
		})
	}

	ports := make([]int, 0, 33)
	for i := 1; i <= 33; i++ {
		ports = append(ports, i)
	}

	params, err := json.Marshal(map[string]any{"cidrs": []string{"10.0.0.0/24"}, "ports": ports})
	require.NoError(t, err)

	rec := f.do(t, f.admin.UID, http.MethodPost, f.scansPath(),
		discovery.StartScanRequest{Type: "lan", Region: "@dc1", Parameters: params})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, 0, f.countJobs(t), "no refused scan created a job")
}

func (f *agentScanFixture) insertTask(t *testing.T, planUID string, status models.WorkerTaskStatus,
	code string, updatedAgo, leaseIn time.Duration,
) *models.WorkerTask {
	t.Helper()

	task := models.NewWorkerTask(f.org.UID, discoverytask.Kind, "@dc1")
	task.JobUID = &planUID
	task.Status = status
	task.UpdatedAt = time.Now().UTC().Add(-updatedAgo)

	if code != "" {
		task.ErrorCode = &code
	}

	if status == models.WorkerTaskStatusClaimed {
		lease := time.Now().UTC().Add(leaseIn)
		worker := "agent-1"
		task.LeaseUntil = &lease
		task.WorkerUID = &worker
	}

	_, err := f.dbSvc.DB().NewInsert().Model(task).Exec(t.Context())
	require.NoError(t, err)

	return task
}

func (f *agentScanFixture) finishPlan(t *testing.T, planUID string) {
	t.Helper()

	_, err := f.dbSvc.DB().NewUpdate().Model((*models.Job)(nil)).
		Set("status = ?", models.JobStatusSuccess).
		Set("output = ?", models.JSONMap{"totalChunks": 6}).
		Where("uid = ?", planUID).
		Exec(t.Context())
	require.NoError(t, err)
}

func TestAgentScanProgressAndCancel(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newAgentScanFixture(t, config.DeploymentModeSaaS)

	rec := f.do(t, f.admin.UID, http.MethodPost, f.scansPath(), lanScan("@dc1", "10.0.0.0/24"))
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())
	planUID := scanUIDOf(t, rec)
	f.finishPlan(t, planUID)

	day := 25 * time.Hour
	f.insertTask(t, planUID, models.WorkerTaskStatusDone, "", time.Minute, 0)
	f.insertTask(t, planUID, models.WorkerTaskStatusFailed, "older", 2*time.Minute, 0)
	f.insertTask(t, planUID, models.WorkerTaskStatusFailed, discoverytask.CodeNotAllowed, time.Second, 0)
	f.insertTask(t, planUID, models.WorkerTaskStatusPending, "", day, 0)
	claimed := f.insertTask(t, planUID, models.WorkerTaskStatusClaimed, "", time.Second, time.Minute)
	pending := f.insertTask(t, planUID, models.WorkerTaskStatusPending, "", time.Second, 0)

	rec = f.do(t, f.admin.UID, http.MethodGet, f.scansPath()+"/"+planUID, nil)
	r.Equal(http.StatusOK, rec.Code)

	var resp struct {
		Progress discovery.ScanProgress `json:"progress"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Equal(discovery.ScanProgress{
		TotalChunks: 6, CompletedChunks: 1, FailedChunks: 3, RunningChunks: 1, PendingChunks: 1,
		DerivedStatus: string(models.JobStatusRunning), Region: "@dc1", LastErrorCode: discoverytask.CodeNotAllowed,
	}, resp.Progress)

	// The list shows the agent plan.
	rec = f.do(t, f.admin.UID, http.MethodGet, f.scansPath(), nil)
	r.Contains(rec.Body.String(), planUID)

	rec = f.do(t, f.admin.UID, http.MethodPost, f.scansPath()+"/"+planUID+"/cancel", nil)
	r.Equal(http.StatusNoContent, rec.Code)

	for _, uid := range []string{claimed.UID, pending.UID} {
		task, err := f.tasks.Get(t.Context(), uid)
		r.NoError(err)
		r.Equal(models.WorkerTaskStatusCancelled, task.Status)
	}

	// The agent holding the canceled chunk loses it.
	_, err := f.tasks.Heartbeat(t.Context(), claimed.UID, "agent-1")
	r.ErrorIs(err, workertasks.ErrTaskGone)

	events, err := f.dbSvc.ListEvents(t.Context(), &models.ListEventsFilter{
		OrganizationUID: f.org.UID,
		EventTypes:      []models.EventType{models.EventTypeDiscoveryScanCanceled},
		Limit:           10,
	})
	r.NoError(err)
	r.Len(events, 1)
}

// The already-running guard sees an agent plan's live chunks, not its expired
// ones.
func TestAgentScanAlreadyRunningGuard(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newAgentScanFixture(t, config.DeploymentModeSaaS)

	rec := f.do(t, f.admin.UID, http.MethodPost, f.scansPath(), lanScan("@dc1", "10.0.0.0/24"))
	r.Equal(http.StatusCreated, rec.Code)
	planUID := scanUIDOf(t, rec)

	// The pending plan itself guards.
	rec = f.do(t, f.admin.UID, http.MethodPost, f.scansPath(), lanScan("@dc1", "10.0.1.0/24"))
	r.Equal(http.StatusConflict, rec.Code)

	f.finishPlan(t, planUID)
	live := f.insertTask(t, planUID, models.WorkerTaskStatusPending, "", time.Second, 0)

	rec = f.do(t, f.admin.UID, http.MethodPost, f.scansPath(), lanScan("@dc1", "10.0.1.0/24"))
	r.Equal(http.StatusConflict, rec.Code, "a live pending chunk guards")

	_, err := f.dbSvc.DB().NewUpdate().Model((*models.WorkerTask)(nil)).
		Set("updated_at = ?", time.Now().UTC().Add(-25*time.Hour)).
		Where("uid = ?", live.UID).Exec(t.Context())
	r.NoError(err)
	f.insertTask(t, planUID, models.WorkerTaskStatusClaimed, "", time.Minute, -time.Minute)

	rec = f.do(t, f.admin.UID, http.MethodPost, f.scansPath(), lanScan("@dc1", "10.0.1.0/24"))
	r.Equal(http.StatusCreated, rec.Code, "expired chunks do not guard: %s", rec.Body.String())
}

// The daily agent address quota refuses a scan over what is left; a
// self-hosted org is never refused; server-run scans are not counted.
func TestAgentScanQuota(t *testing.T) {
	t.Parallel()

	usedToday := func(t *testing.T, f *agentScanFixture, region string, addresses int) {
		t.Helper()

		event := models.NewEvent(f.org.UID, models.EventTypeDiscoveryScanStarted, models.ActorTypeUser)
		event.Payload["region"] = region
		event.Payload["address_count"] = addresses
		require.NoError(t, f.dbSvc.CreateEvent(t.Context(), event))
	}

	t.Run("saas", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		f := newAgentScanFixture(t, config.DeploymentModeSaaS)
		usedToday(t, f, "@dc1", 65_400)
		usedToday(t, f, "", 1_000_000)

		rec := f.do(t, f.admin.UID, http.MethodPost, f.scansPath(), lanScan("@dc1", "10.0.0.0/24"))
		r.Equal(http.StatusUnprocessableEntity, rec.Code)
		r.Equal(scantypes.CodeQuotaExceeded, errorCodeOf(t, rec))

		rec = f.do(t, f.admin.UID, http.MethodPost, f.scansPath(), lanScan("@dc1", "10.0.0.0/25"))
		r.Equal(http.StatusCreated, rec.Code, "the remaining 136 addresses cover a /25: %s", rec.Body.String())
	})

	t.Run("self-hosted", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		f := newAgentScanFixture(t, config.DeploymentModeSelfHosted)
		usedToday(t, f, "@dc1", 10_000_000)

		rec := f.do(t, f.admin.UID, http.MethodPost, f.scansPath(), lanScan("@dc1", "10.0.0.0/16"))
		r.Equal(http.StatusCreated, rec.Code, rec.Body.String())
	})
}
