package checks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkfilter"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/rewraptask"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
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
	worker.Capabilities = []string{rewraptask.Capability, rewraptask.VerifyCapability}
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

	newAgent, newKeys := enrollAgentIn(t, dbSvc, org.UID, sealTestRegion)
	makeLiveRewrapAgent(t, dbSvc, newAgent)
	// The old agent was seen last: it re-wraps, the new one confirms.
	r.NoError(dbSvc.UpdateAgentLastSeen(ctx, oldAgent.UID, time.Now().UTC().Add(time.Second)))

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

	pending, err := svc.RewrapStatus(ctx, org.Slug, []string{created.UID})
	r.NoError(err)
	r.Len(pending, 1)
	r.Equal("pending", pending[0].Status, "waiting for the second agent")

	verify, err := tasks.Claim(ctx, &workertasks.WorkerIdentity{
		UID: newAgent.UID, Region: sealTestRegion, OrgUID: org.UID, IsAgent: true,
		Kinds: []string{rewraptask.VerifyKind},
	})
	r.NoError(err)
	r.NotNil(verify)

	verifyExec := rewraptask.NewVerifyExecutor(func() []string { return []string{newKeys.X25519Identity} })
	r.NoError(verifyExec.Run(ctx, verify, &finishReporter{tasks: tasks, uid: verify.UID, worker: newAgent.UID}))

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

// TestStartRewrapWithoutSecondAgentStaysFlagged: with no second agent online
// to confirm it, the re-wrap is stored but the check stays on the Re-seal
// page with the reason "unconfirmed" (spec 2026-11-11-02).
func TestStartRewrapWithoutSecondAgentStaysFlagged(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	svc, dbSvc, org := sqliteSealingEnv(t)
	defineSealRegions(t, dbSvc, org.UID)

	tasks := workertasks.NewService(dbSvc.DB())
	svc.SetRewrapper(rewraptask.Register(rewraptask.Deps{DB: dbSvc, Tasks: tasks}))

	oldAgent, oldKeys := enrollAgentIn(t, dbSvc, org.UID, sealTestRegion)
	makeLiveRewrapAgent(t, dbSvc, oldAgent)

	created, err := svc.CreateCheck(ctx, org.Slug, sealedCheckRequest("rewrap-alone",
		clientSeal(t, map[string]any{"basicAuth": "alice:hunter2"}, oldKeys.X25519Recipient), sealTestRegion))
	r.NoError(err)

	_, newKeys := enrollAgentIn(t, dbSvc, org.UID, sealTestRegion) // offline

	outcomes, err := svc.StartRewrap(ctx, org.Slug, &checks.RewrapRequest{Region: sealTestRegion})
	r.NoError(err)
	r.Len(outcomes, 1)

	claim, err := tasks.Claim(ctx, &workertasks.WorkerIdentity{
		UID: oldAgent.UID, Region: sealTestRegion, OrgUID: org.UID, IsAgent: true, Kinds: []string{rewraptask.Kind},
	})
	r.NoError(err)
	r.NotNil(claim)

	exec := rewraptask.NewExecutor(func() []string { return []string{oldKeys.X25519Identity} })
	r.NoError(exec.Run(ctx, claim, &finishReporter{tasks: tasks, uid: claim.UID, worker: oldAgent.UID}))

	row, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)

	secrets, err := credentials.UnsealWithIdentity(newKeys.X25519Identity, *row.ConfigSealed)
	r.NoError(err, "stored for the current agents")
	r.Equal("alice:hunter2", secrets["basicAuth"])

	detail, err := svc.GetCheck(ctx, org.Slug, created.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.NotNil(detail.NeedsReseal)
	r.True(*detail.NeedsReseal, "not confirmed by a second agent")

	listed, err := svc.ListResealChecks(ctx, org.Slug)
	r.NoError(err)
	r.Len(listed, 1)
	r.Equal(checks.ResealReasonUnconfirmed, listed[0].Reason)

	statuses, err := svc.RewrapStatus(ctx, org.Slug, []string{created.UID})
	r.NoError(err)
	r.Len(statuses, 1)
	r.Equal("done", statuses[0].Status)
	r.Equal(rewraptask.CodeNotConfirmed, statuses[0].ErrorCode)
}

// recordingRewrapper records what the service hands to the re-wrap kind.
type recordingRewrapper struct {
	started    [][]string
	statusRefs [][]string
}

func (f *recordingRewrapper) Start(
	_ context.Context, _ string, list []*models.Check,
) ([]rewraptask.Outcome, error) {
	uids := make([]string, 0, len(list))
	outcomes := make([]rewraptask.Outcome, 0, len(list))

	for _, check := range list {
		uids = append(uids, check.UID)
		outcomes = append(outcomes, rewraptask.Outcome{CheckUID: check.UID, Status: "pending"})
	}

	f.started = append(f.started, uids)

	return outcomes, nil
}

func (f *recordingRewrapper) Status(_ context.Context, _ string, uids []string) ([]rewraptask.Outcome, error) {
	f.statusRefs = append(f.statusRefs, uids)
	outcomes := make([]rewraptask.Outcome, 0, len(uids))

	for _, uid := range uids {
		outcomes = append(outcomes, rewraptask.Outcome{CheckUID: uid, Status: "pending"})
	}

	return outcomes, nil
}

func TestRewrapCheckListIsCapped(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	svc, dbSvc, org := sqliteSealingEnv(t)
	rec := &recordingRewrapper{}
	svc.SetRewrapper(rec)

	var first, second *models.Check

	for i, slug := range []string{"rw-a", "rw-b"} {
		check := &models.Check{
			UID: uuid.NewString(), OrganizationUID: org.UID, Slug: &slug, Name: &slug, Type: "http",
			Config: models.JSONMap{"url": "https://example.com"}, Period: timeutils.Duration(time.Minute), Enabled: true,
		}
		r.NoError(dbSvc.CreateCheck(ctx, check))

		if i == 0 {
			first = check
		} else {
			second = check
		}
	}

	// Over the cap: refused before any lookup (an unknown ref would be 404).
	tooMany := make([]string, checkfilter.MaxIdentifiers+1)
	for i := range tooMany {
		tooMany[i] = "unknown-check"
	}

	_, err := svc.StartRewrap(ctx, org.Slug, &checks.RewrapRequest{CheckUIDs: tooMany})
	r.ErrorIs(err, checkfilter.ErrTooManyIdentifiers)
	r.Empty(rec.started)

	_, err = svc.RewrapStatus(ctx, org.Slug, tooMany)
	r.ErrorIs(err, checkfilter.ErrTooManyIdentifiers)
	r.Empty(rec.statusRefs)

	// Duplicates collapse to one re-wrap.
	dup := make([]string, 50)
	for i := range dup {
		dup[i] = first.UID
	}

	outcomes, err := svc.StartRewrap(ctx, org.Slug, &checks.RewrapRequest{CheckUIDs: dup})
	r.NoError(err)
	r.Len(outcomes, 1)
	r.Equal([][]string{{first.UID}}, rec.started)

	statuses, err := svc.RewrapStatus(ctx, org.Slug, dup)
	r.NoError(err)
	r.Len(statuses, 1)
	r.Equal([]string{first.UID}, rec.statusRefs[0])

	// Positive control: two distinct checks start two re-wraps.
	outcomes, err = svc.StartRewrap(ctx, org.Slug, &checks.RewrapRequest{CheckUIDs: []string{first.UID, second.UID}})
	r.NoError(err)
	r.Len(outcomes, 2)
}

func TestRewrapStatusHandlerCheckUIDParam(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, _, org := sqliteSealingEnv(t)
	rec := &recordingRewrapper{}
	svc.SetRewrapper(rec)

	handler := checks.NewHandler(svc, &config.Config{})

	call := func(query string) *httptest.ResponseRecorder {
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("org", org.Slug)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/sealing/rewrap?"+query, nil)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
		resp := httptest.NewRecorder()
		r.NoError(handler.RewrapStatus(resp, req))

		return resp
	}

	// Over the cap: 400 VALIDATION_ERROR, nothing queried.
	resp := call("checkUid=" + strings.Repeat("x,", checkfilter.MaxIdentifiers))
	r.Equal(http.StatusUnprocessableEntity, resp.Code)
	r.Contains(resp.Body.String(), "VALIDATION_ERROR")
	r.Empty(rec.statusRefs)

	// The same UID repeated is looked up once.
	resp = call("checkUid=" + strings.Repeat("a,", 50) + "a")
	r.Equal(http.StatusOK, resp.Code)
	r.Equal([][]string{{"a"}}, rec.statusRefs)

	// Legacy alias still answers.
	resp = call("check=b,c")
	r.Equal(http.StatusOK, resp.Code)
	r.Equal([]string{"b", "c"}, rec.statusRefs[1])
}
