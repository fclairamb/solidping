package jobtypes_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/discoverytask"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
)

func (f *planFixture) runAgentPlan(t *testing.T, cfg discoverytask.PlanConfig) error {
	t.Helper()

	r := require.New(t)

	cfgBytes, err := json.Marshal(cfg)
	r.NoError(err)

	runner, err := (&jobtypes.NetworkDiscoveryAgentPlanJobDefinition{}).CreateJobRun(cfgBytes)
	r.NoError(err)

	return runner.Run(context.Background(), &jobdef.JobContext{
		OrganizationUID: &f.org.UID,
		Job:             f.plan,
		DB:              f.dbSvc.DB(),
		DBService:       f.dbSvc,
		Services:        &services.Registry{Jobs: f.jobSvc, EgressGuard: f.guard},
		Logger:          slog.Default(),
	})
}

func (f *planFixture) discoveryTasks(t *testing.T) []*models.WorkerTask {
	t.Helper()

	var tasks []*models.WorkerTask

	require.NoError(t, f.dbSvc.DB().NewSelect().Model(&tasks).
		Where("job_uid = ?", f.plan.UID).
		Scan(t.Context()))

	return tasks
}

// countingPort listens on 127.0.0.1 and counts accepted connections.
func countingPort(t *testing.T) (int, *atomic.Int64) {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	var accepted atomic.Int64

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			accepted.Add(1)

			_ = conn.Close()
		}
	}()

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)

	return tcpAddr.Port, &accepted
}

// The agent plan inserts one pending discovery task per chunk, in the scan's
// region, under the plan, and records the chunk total.
func TestNetworkDiscoveryAgentPlanCreatesTasks(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newPlanFixture(t)

	r.NoError(f.runAgentPlan(t, discoverytask.PlanConfig{
		CIDRs: []string{"10.0.0.0/18"}, Ports: []int{22, 80}, Region: "@dc1",
	}))

	tasks := f.discoveryTasks(t)
	r.Len(tasks, 4)

	for _, task := range tasks {
		r.Equal(discoverytask.Kind, task.Kind)
		r.Equal("@dc1", task.Region)
		r.Equal(models.WorkerTaskStatusPending, task.Status)
		r.Equal(f.org.UID, task.OrganizationUID)

		cidrs, ok := task.Payload["cidrs"].([]any)
		r.True(ok)
		r.Len(cidrs, 1)
		r.Equal([]any{float64(22), float64(80)}, task.Payload["ports"])
	}

	r.Equal(4, toInt(f.plan.Output["totalChunks"]))
	r.Empty(f.childJobs(t), "an agent plan schedules no server chunk")
}

// The default port list is resolved server-side, so the result can be checked
// against it.
func TestNetworkDiscoveryAgentPlanDefaultPorts(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newPlanFixture(t)

	r.NoError(f.runAgentPlan(t, discoverytask.PlanConfig{CIDRs: []string{"10.0.0.0/24"}, Region: "@dc1"}))

	tasks := f.discoveryTasks(t)
	r.Len(tasks, 1)

	ports, ok := tasks[0].Payload["ports"].([]any)
	r.True(ok)
	r.Len(ports, len(disc.DefaultPortList()))
}

func TestNetworkDiscoveryAgentPlanRequiresRegion(t *testing.T) {
	t.Parallel()

	_, err := (&jobtypes.NetworkDiscoveryAgentPlanJobDefinition{}).CreateJobRun(
		json.RawMessage(`{"cidrs":["10.0.0.0/24"]}`))
	require.Error(t, err)
}

// The agent plan dials nothing, even under a permissive guard; a
// network_discovery chunk on the same CIDR does (positive control). It also
// runs under an enforcing guard: the server is not the one scanning.
func TestNetworkDiscoveryAgentPlanNeverDials(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	port, accepted := countingPort(t)

	f := newPlanFixture(t)
	f.guard = egress.New(false)
	r.NoError(f.runAgentPlan(t, discoverytask.PlanConfig{
		CIDRs: []string{"127.0.0.1/32"}, Ports: []int{port}, Region: "@dc1",
	}))
	r.Len(f.discoveryTasks(t), 1)

	time.Sleep(100 * time.Millisecond)
	r.Zero(accepted.Load(), "the agent plan must not dial")

	runner, err := (&jobtypes.NetworkDiscoveryJobDefinition{}).CreateJobRun(
		json.RawMessage(`{"cidrs":["127.0.0.1/32"],"ports":[` + strconv.Itoa(port) + `]}`))
	r.NoError(err)

	control := newPlanFixture(t)
	r.NoError(runner.Run(t.Context(), &jobdef.JobContext{
		OrganizationUID: &control.org.UID,
		Job:             control.plan,
		DB:              control.dbSvc.DB(),
		DBService:       control.dbSvc,
		Services:        &services.Registry{EgressGuard: egress.New(true)},
		Logger:          slog.Default(),
	}))
	r.Eventually(func() bool { return accepted.Load() > 0 }, 5*time.Second, 10*time.Millisecond)
}
