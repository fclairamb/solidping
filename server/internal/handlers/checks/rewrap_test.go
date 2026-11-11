package checks_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/rewraptask"
	"github.com/fclairamb/solidping/server/internal/workertasks"
	spcli "github.com/fclairamb/solidping/server/pkg/cli"
)

// Peer re-wrap and `solidping seal` against the check write API (spec
// 2026-11-10-03).

// makeLiveRewrapAgent marks an agent online and running the re-wrap executor.
func makeLiveRewrapAgent(t *testing.T, dbSvc db.Service, agent *models.Agent) {
	t.Helper()

	r := require.New(t)
	r.NoError(dbSvc.UpdateAgentLastSeen(t.Context(), agent.UID, time.Now().UTC()))

	worker := models.NewWorker(agentcrypto.WorkerSlug(agent.UID), agent.Name)
	worker.Capabilities = []string{rewraptask.Capability}
	r.NoError(dbSvc.CreateWorker(t.Context(), worker))
}

// TestSealCommandOutputAcceptedByWriteAPI: what `solidping seal` prints is a
// sealedSecrets object the create endpoint stores as a browser blob.
func TestSealCommandOutputAcceptedByWriteAPI(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org, keys := setupBrowserSealing(t, sqliteSealingEnv)
	ctx := t.Context()

	sealed, err := spcli.SealSecrets([]string{keys.X25519Recipient}, map[string]any{"basicAuth": "alice:hunter2"}, nil)
	r.NoError(err)

	// Through JSON, as a script would pipe it.
	raw, err := json.Marshal(sealed)
	r.NoError(err)

	var req checks.SealedSecretsRequest
	r.NoError(json.Unmarshal(raw, &req))

	created, err := svc.CreateCheck(ctx, org.Slug, sealedCheckRequest("cli-sealed", &req, sealTestRegion))
	r.NoError(err)
	r.Equal("browser", *created.SecretsEncryption)

	row, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)

	secrets, err := credentials.UnsealWithIdentity(keys.X25519Identity, *row.ConfigSealed)
	r.NoError(err)
	r.Equal("alice:hunter2", secrets["basicAuth"])
}

// TestStartRewrapClearsNeedsReseal runs the whole loop through the checks
// service: a new agent flags the check, a re-wrap on the old agent clears it.
func TestStartRewrapClearsNeedsReseal(t *testing.T) {
	t.Parallel()
	testStartRewrapClearsNeedsReseal(t, sqliteSealingEnv)
}

func testStartRewrapClearsNeedsReseal(t *testing.T, env sealingEnv) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	svc, dbSvc, org := env(t)
	defineSealRegions(t, dbSvc, org.UID)

	_, err := svc.StartRewrap(ctx, org.Slug, &checks.RewrapRequest{})
	r.ErrorIs(err, checks.ErrRewrapUnavailable)

	tasks := workertasks.NewService(dbSvc.DB())
	svc.SetRewrapper(rewraptask.Register(rewraptask.Deps{DB: dbSvc, Tasks: tasks}))

	oldAgent, oldKeys := enrollAgentIn(t, dbSvc, org.UID, sealTestRegion)
	makeLiveRewrapAgent(t, dbSvc, oldAgent)

	created, err := svc.CreateCheck(ctx, org.Slug, sealedCheckRequest("rewrap-me",
		clientSeal(t, map[string]any{"basicAuth": "alice:hunter2"}, oldKeys.X25519Recipient), sealTestRegion))
	r.NoError(err)

	_, newKeys := enrollAgentIn(t, dbSvc, org.UID, sealTestRegion)

	detail, err := svc.GetCheck(ctx, org.Slug, created.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.NotNil(detail.NeedsReseal)
	r.True(*detail.NeedsReseal)

	_, err = svc.StartRewrap(ctx, org.Slug, &checks.RewrapRequest{Region: "@nope"})
	r.ErrorIs(err, checks.ErrSealedSecretsRequirePrivateRegions)

	_, err = svc.StartRewrap(ctx, org.Slug, &checks.RewrapRequest{CheckUIDs: []string{"no-such-check"}})
	r.ErrorIs(err, checks.ErrCheckNotFound)

	outcomes, err := svc.StartRewrap(ctx, org.Slug, &checks.RewrapRequest{Region: sealTestRegion})
	r.NoError(err)
	r.Len(outcomes, 1)
	r.Equal(created.UID, outcomes[0].CheckUID)
	r.Equal("rewrap-me", outcomes[0].CheckName)
	r.Equal("pending", outcomes[0].Status)
	r.Equal(oldAgent.UID, outcomes[0].AgentUID)

	claim, err := tasks.Claim(ctx, &workertasks.WorkerIdentity{
		UID: oldAgent.UID, Region: sealTestRegion, OrgUID: org.UID, IsAgent: true, Kinds: []string{rewraptask.Kind},
	})
	r.NoError(err)
	r.NotNil(claim)

	exec := rewraptask.NewExecutor(func() []string { return []string{oldKeys.X25519Identity} })
	r.NoError(exec.Run(ctx, claim, &finishReporter{tasks: tasks, uid: claim.UID, worker: oldAgent.UID}))

	detail, err = svc.GetCheck(ctx, org.Slug, created.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.NotNil(detail.NeedsReseal)
	r.False(*detail.NeedsReseal, "the re-wrap cleared the flag")
	r.Equal("browser", *detail.SecretsEncryption, "the blob keeps its origin")

	row, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)

	secrets, err := credentials.UnsealWithIdentity(newKeys.X25519Identity, *row.ConfigSealed)
	r.NoError(err)
	r.Equal("alice:hunter2", secrets["basicAuth"])

	statuses, err := svc.RewrapStatus(ctx, org.Slug, []string{created.UID})
	r.NoError(err)
	r.Len(statuses, 1)
	r.Equal("done", statuses[0].Status)
	r.Equal("rewrap-me", statuses[0].CheckName)

	again, err := svc.StartRewrap(ctx, org.Slug, &checks.RewrapRequest{Region: sealTestRegion})
	r.NoError(err)
	r.Empty(again, "nothing is flagged any more")
}

// finishReporter finishes the claimed task on the service.
type finishReporter struct {
	tasks  *workertasks.Service
	uid    string
	worker string
}

func (f *finishReporter) Heartbeat(context.Context) error { return nil }

func (f *finishReporter) Step(context.Context, workertasks.StepRequest) error { return nil }

func (f *finishReporter) Finish(ctx context.Context, body json.RawMessage) error {
	return f.tasks.Finish(ctx, f.uid, f.worker, body)
}

func (f *finishReporter) Fail(ctx context.Context, code string) error {
	return f.tasks.FailSlice(ctx, f.uid, f.worker, code)
}
