package discoverytask

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

const testRegion = "@dc1"

// kindHarness is one database with the discovery kind registered, an org and
// a plan job. Its agent is "agent-<org slug>": an agent holds one lease at a
// time, so parallel harnesses need their own.
type kindHarness struct {
	t     *testing.T
	db    db.Service
	tasks *workertasks.Service
	srv   *Server
	org   *models.Organization
	plan  *models.Job
}

func newSQLite(t *testing.T) db.Service {
	t.Helper()

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(t.Context()))
	t.Cleanup(func() { _ = dbSvc.Close() })

	return dbSvc
}

func newKindHarness(t *testing.T, dbSvc db.Service, slug string) *kindHarness {
	t.Helper()

	r := require.New(t)

	org := models.NewOrganization(slug, slug)
	r.NoError(dbSvc.CreateOrganization(t.Context(), org))

	plan := models.NewJob(&org.UID, "network_discovery_agent_plan")
	r.NoError(dbSvc.CreateJob(t.Context(), plan))

	tasks := workertasks.NewService(dbSvc.DB())

	return &kindHarness{
		t: t, db: dbSvc, tasks: tasks, org: org, plan: plan,
		srv: Register(Deps{DB: dbSvc, Tasks: tasks}),
	}
}

// finishChunk creates a chunk task, claims it as agent and finishes it with
// body. It returns the task as stored afterwards.
func (h *kindHarness) finishChunk(input Input, body string) *models.WorkerTask {
	h.t.Helper()

	r := require.New(h.t)
	ctx := h.t.Context()
	agent := "agent-" + h.org.Slug

	raw, err := json.Marshal(input)
	r.NoError(err)

	payload := models.JSONMap{}
	r.NoError(json.Unmarshal(raw, &payload))

	task := models.NewWorkerTask(h.org.UID, Kind, testRegion)
	task.JobUID = &h.plan.UID
	task.Payload = payload
	r.NoError(h.tasks.Create(ctx, task))

	claim, err := h.tasks.Claim(ctx, &workertasks.WorkerIdentity{
		UID: agent, Region: testRegion, OrgUID: h.org.UID, IsAgent: true, Kinds: []string{Kind},
	})
	r.NoError(err)
	r.NotNil(claim)
	r.Equal(task.UID, claim.UID)

	r.NoError(h.tasks.Finish(ctx, claim.UID, agent, json.RawMessage(body)))

	stored, err := h.tasks.Get(ctx, task.UID)
	r.NoError(err)

	return stored
}

func (h *kindHarness) rows() []*models.DiscoveredCheck {
	h.t.Helper()

	var rows []*models.DiscoveredCheck

	require.NoError(h.t, h.db.DB().NewSelect().Model(&rows).
		Where("organization_uid = ?", h.org.UID).
		Where("deleted_at IS NULL").
		Order("slug ASC").
		Scan(h.t.Context()))

	return rows
}

func runKindContract(t *testing.T, dbSvc db.Service) {
	t.Helper()

	t.Run("filters hosts, ports and names; suggestions rebuilt", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		h := newKindHarness(t, dbSvc, "acme-filter")

		task := h.finishChunk(Input{CIDRs: []string{"10.0.0.0/30"}, Ports: []int{22, 80}}, `{
			"icmpAvailable": true,
			"hosts": [
				{"ip": "10.0.0.1", "hostname": "Web.Local", "openPorts": [22, 443],
				 "suggestedChecks": [{"type": "http", "slug": "evil", "config": {"url": "http://169.254.169.254"}}]},
				{"ip": "10.0.0.1", "hostname": "dup", "openPorts": [80]},
				{"ip": "10.9.9.9", "hostname": "outside", "openPorts": [22]},
				{"ip": "10.0.0.2", "hostname": "bad host!", "openPorts": [80, 80]}
			]
		}`)

		r.Equal(models.WorkerTaskStatusDone, task.Status)
		r.InDelta(2, task.Progress["hostCount"], 0)

		want := disc.SuggestChecks("10.0.0.1", "web.local", false, []int{22})
		want = append(want, disc.SuggestChecks("10.0.0.2", "", false, []int{80})...)

		rows := h.rows()
		r.Len(rows, len(want))

		wantSlugs := map[string]string{}
		for _, row := range want {
			wantSlugs[row.Slug] = string(row.Config)
		}

		for _, row := range rows {
			r.Equal(testRegion, row.Region, "rows carry the task's region")
			r.Equal(h.plan.UID, row.JobUID, "rows roll up under the plan")
			r.NotEqual("evil", row.Slug, "an agent-sent suggestion is ignored")
			r.Contains(wantSlugs, row.Slug)
			r.JSONEq(wantSlugs[row.Slug], string(row.Config))
			r.NotContains(string(row.Config), "10.9.9.9")
			r.NotContains(string(row.Config), "443")
		}
	})

	t.Run("more hosts than addresses rejects the body", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		h := newKindHarness(t, dbSvc, "acme-flood")

		task := h.finishChunk(Input{CIDRs: []string{"10.0.0.0/31"}, Ports: []int{22}}, `{"hosts": [
			{"ip": "10.0.0.0", "openPorts": [22]},
			{"ip": "10.0.0.1", "openPorts": [22]},
			{"ip": "10.0.0.2", "openPorts": [22]}
		]}`)

		r.Equal(models.WorkerTaskStatusFailed, task.Status)
		r.Equal(codeInvalidResult, *task.ErrorCode)
		r.Empty(h.rows())
	})

	t.Run("org inventory cap", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		h := newKindHarness(t, dbSvc, "acme-full")
		h.srv.SetInventoryCap(1)

		task := h.finishChunk(Input{CIDRs: []string{"10.0.0.0/30"}, Ports: []int{22, 80}},
			`{"hosts": [{"ip": "10.0.0.1", "openPorts": [22, 80]}]}`)

		r.Equal(models.WorkerTaskStatusFailed, task.Status)
		r.Equal(codeInventoryFull, *task.ErrorCode)
		r.Empty(h.rows())
	})

	t.Run("partial keeps its hosts", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		h := newKindHarness(t, dbSvc, "acme-partial")

		task := h.finishChunk(Input{CIDRs: []string{"10.0.0.0/30"}, Ports: []int{22}},
			`{"partial": true, "hosts": [{"ip": "10.0.0.1", "openPorts": [22]}]}`)

		r.Equal(models.WorkerTaskStatusFailed, task.Status)
		r.Equal(codePartial, *task.ErrorCode)
		r.NotEmpty(h.rows())
	})

	t.Run("refusal is audited once per plan and agent", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		h := newKindHarness(t, dbSvc, "acme-refused")
		input := Input{CIDRs: []string{"10.0.0.0/30"}, Ports: []int{22}}

		for range 2 {
			task := h.finishChunk(input, `{"errorCode": "DISCOVERY_NOT_ALLOWED", "hosts": []}`)
			r.Equal(models.WorkerTaskStatusFailed, task.Status)
			r.Equal(CodeNotAllowed, *task.ErrorCode)
		}

		task := h.finishChunk(input, `{"errorCode": "SOMETHING_ELSE"}`)
		r.Equal(codeInvalidResult, *task.ErrorCode)

		events, err := h.db.ListEvents(t.Context(), &models.ListEventsFilter{
			OrganizationUID: h.org.UID,
			EventTypes:      []models.EventType{models.EventTypeDiscoveryChunkRefused},
			Limit:           10,
		})
		r.NoError(err)
		r.Len(events, 1)
		r.Equal(models.ActorTypeSystem, events[0].ActorType)
		r.Equal("agent-"+h.org.Slug, events[0].Payload["target_uid"])
		r.Equal(h.plan.UID, events[0].Payload[payloadScanUID])
		r.Equal(testRegion, events[0].Payload[payloadRegion])
	})
}

func TestKindContract(t *testing.T) {
	t.Parallel()

	runKindContract(t, newSQLite(t))
}
