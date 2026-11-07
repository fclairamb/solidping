package checkworker

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/checkworker/tasks"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

const wiringKind = "wiring-test"

// wiringExecutor records the guard its Run context carries, then finishes.
type wiringExecutor struct {
	guard *atomic.Pointer[egress.Guard]
}

func (wiringExecutor) Kind() string       { return wiringKind }
func (wiringExecutor) Capability() string { return wiringKind }

func (e wiringExecutor) Run(ctx context.Context, _ *workertasks.Claim, rep tasks.Reporter) error {
	e.guard.Store(egress.FromContext(ctx))

	return rep.Finish(ctx, json.RawMessage(`{}`))
}

// A cloud worker runs a registered executor through the in-process client:
// it claims in its own region, and Run sees the worker's egress guard.
func TestCloudWorkerRunsWorkerTasks(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "site", "http")
	check.Enabled = false
	r.NoError(dbSvc.CreateCheck(ctx, check))

	svc := workertasks.NewService(dbSvc.DB())
	svc.RegisterKind(workertasks.TaskKind{Name: wiringKind})

	task := models.NewWorkerTask(org.UID, wiringKind, "eu-west")
	task.CheckUID = &check.UID
	r.NoError(svc.Create(ctx, task))

	var seen atomic.Pointer[egress.Guard]

	tasks.Register(wiringExecutor{guard: &seen})

	region := "eu-west"
	worker := &CheckWorker{egressGuard: egress.New(false), config: &config.Config{}}
	worker.setWorker(&models.Worker{UID: uuid.NewString(), Region: &region})
	worker.EnableTasks(backend.NewDirectTaskClient(svc, worker.taskIdentity), 2)

	runCtx, cancel := context.WithCancel(ctx)
	worker.startTasks(runCtx)

	r.Eventually(func() bool {
		got, getErr := svc.Get(ctx, task.UID)

		return getErr == nil && got.Status == models.WorkerTaskStatusDone
	}, 5*time.Second, 10*time.Millisecond)

	cancel()
	worker.wg.Wait()

	r.Same(worker.egressGuard, seen.Load())
}

// gatedClient is a tasks.Gated client that counts its claims.
type gatedClient struct {
	tasks.Client

	allowed bool
	claims  atomic.Int32
}

func (c *gatedClient) TasksAllowed() bool { return c.allowed }

func (c *gatedClient) Claim(context.Context, []string) (*workertasks.Claim, error) {
	c.claims.Add(1)

	return nil, nil //nolint:nilnil // no task
}

// A gated client the server refuses tasks (a system agent) never gets a claim:
// the loop is not started. The allowed client is the positive control.
func TestWorkerSkipsTaskLoopWhenRefused(t *testing.T) {
	t.Parallel()

	tasks.Register(wiringExecutor{guard: &atomic.Pointer[egress.Guard]{}})

	for _, allowed := range []bool{true, false} {
		r := require.New(t)

		client := &gatedClient{allowed: allowed}
		worker := &CheckWorker{config: &config.Config{}, logger: slog.Default()}
		worker.EnableTasks(client, 1)

		runCtx, cancel := context.WithCancel(t.Context())
		worker.startTasks(runCtx)

		claimed := func() bool { return client.claims.Load() > 0 }
		if allowed {
			r.Eventually(claimed, 5*time.Second, 10*time.Millisecond)
		} else {
			// A started loop claims at once, well inside this window.
			r.Never(claimed, 300*time.Millisecond, 10*time.Millisecond)
		}

		cancel()
		worker.wg.Wait()
	}
}
