package rewraptask_test

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
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/agents"
	"github.com/fclairamb/solidping/server/internal/rewraptask"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

const testRegion = "@dc1"

// harness is one database with the re-wrap kind registered, an org with the
// private region @dc1 and helpers to enroll agents and seal checks.
type harness struct {
	t     *testing.T
	db    db.Service
	tasks *workertasks.Service
	srv   *rewraptask.Server
	admin *agents.Service
	org   *models.Organization
}

type enrolled struct {
	agent *models.Agent
	keys  *agentcrypto.AgentKeys
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	return newHarnessOn(t, dbSvc, "acme")
}

// newHarnessOn builds the harness on a given database, with its own org.
func newHarnessOn(t *testing.T, dbSvc db.Service, slug string) *harness {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	org := models.NewOrganization(slug, slug)
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	creds, err := credentials.NewService(nil, credentials.ParamStore{})
	r.NoError(err)

	admin := agents.NewService(dbSvc, creds, nil)
	_, err = admin.CreatePrivateRegion(ctx, org.Slug, &agents.CreatePrivateRegionRequest{Slug: "dc1"})
	r.NoError(err)

	tasks := workertasks.NewService(dbSvc.DB())

	return &harness{
		t: t, db: dbSvc, tasks: tasks, admin: admin, org: org,
		srv: rewraptask.Register(rewraptask.Deps{DB: dbSvc, Tasks: tasks}),
	}
}

// enroll enrolls an agent in @dc1. A live agent was seen now and runs the
// re-wrap executor (its worker row reports the capability, plus regionCaps).
func (h *harness) enroll(name string, live bool, regionCaps ...string) enrolled {
	h.t.Helper()

	r := require.New(h.t)
	ctx := h.t.Context()

	minted, err := h.admin.MintEnrollmentToken(ctx, h.org.Slug, &agents.MintEnrollmentTokenRequest{RegionSlug: "dc1"}, nil)
	r.NoError(err)

	keys, err := agentcrypto.GenerateAgentKeys()
	r.NoError(err)

	agent, err := h.db.EnrollAgent(ctx, agentcrypto.HashEnrollmentToken(minted.Token), name,
		keys.Ed25519PublicKey, keys.X25519Recipient, agentcrypto.KeyFingerprint(keys.Ed25519PublicKey))
	r.NoError(err)

	if live {
		now := time.Now().UTC()
		r.NoError(h.db.UpdateAgentLastSeen(ctx, agent.UID, now))
		agent.LastSeenAt = &now

		worker := models.NewWorker(agentcrypto.WorkerSlug(agent.UID), name)
		worker.Capabilities = append([]string{rewraptask.Capability}, regionCaps...)
		r.NoError(h.db.CreateWorker(ctx, worker))
	}

	return enrolled{agent: agent, keys: keys}
}

// sealedCheck creates a check in @dc1 whose secrets are sealed (by the
// browser) to recipients, with one job.
func (h *harness) sealedCheck(slug string, secrets map[string]any, recipients ...string) *models.Check {
	h.t.Helper()

	r := require.New(h.t)
	ctx := h.t.Context()

	fps, ct, err := credentials.SealForClient(recipients, secrets)
	r.NoError(err)

	envelope, err := credentials.BuildClientSealedEnvelope(fps, ct)
	r.NoError(err)

	keys := `["password"]`
	check := models.NewCheck(h.org.UID, slug, "http")
	check.Config = models.JSONMap{"url": "https://acme.example.com"}
	check.Regions = []string{testRegion}
	check.ConfigSealed = &envelope
	check.ConfigPrivateKeys = &keys
	r.NoError(h.db.CreateCheck(ctx, check))

	// CreateCheck makes the check's job; it must carry the blob too.
	_, err = h.db.DB().NewUpdate().Model((*models.CheckJob)(nil)).
		Set("config_sealed = ?", envelope).Where("check_uid = ?", check.UID).Exec(ctx)
	r.NoError(err)
	r.Equal(envelope, h.jobSealed(check))

	return check
}

func (h *harness) reload(check *models.Check) *models.Check {
	h.t.Helper()

	stored, err := h.db.GetCheck(h.t.Context(), h.org.UID, check.UID)
	require.NoError(h.t, err)

	return stored
}

func (h *harness) jobSealed(check *models.Check) string {
	h.t.Helper()

	var job models.CheckJob
	require.NoError(h.t, h.db.DB().NewSelect().Model(&job).Where("check_uid = ?", check.UID).Scan(h.t.Context()))
	require.NotNil(h.t, job.ConfigSealed)

	return *job.ConfigSealed
}

func (h *harness) claim(agent *models.Agent) *workertasks.Claim {
	h.t.Helper()

	claim, err := h.tasks.Claim(h.t.Context(), &workertasks.WorkerIdentity{
		UID: agent.UID, Region: testRegion, OrgUID: h.org.UID, IsAgent: true, Kinds: []string{rewraptask.Kind},
	})
	require.NoError(h.t, err)

	return claim
}

func (h *harness) task(uid string) *models.WorkerTask {
	h.t.Helper()

	task, err := h.tasks.Get(h.t.Context(), uid)
	require.NoError(h.t, err)

	return task
}

// reporter finishes the claimed task on the service, as the agent loop does.
type reporter struct {
	tasks  *workertasks.Service
	uid    string
	worker string
	body   json.RawMessage
}

func (r *reporter) Heartbeat(context.Context) error { return nil }

func (r *reporter) Step(context.Context, workertasks.StepRequest) error { return nil }

func (r *reporter) Finish(ctx context.Context, body json.RawMessage) error {
	r.body = body

	return r.tasks.Finish(ctx, r.uid, r.worker, body)
}

func (r *reporter) Fail(ctx context.Context, code string) error {
	return r.tasks.FailSlice(ctx, r.uid, r.worker, code)
}

// run executes the claim on the agent's executor (its own key plus the
// region identities given) and returns what it reported.
func (h *harness) run(claim *workertasks.Claim, agent *models.Agent, identities ...string) json.RawMessage {
	h.t.Helper()

	exec := rewraptask.NewExecutor(func() []string { return identities })
	rep := &reporter{tasks: h.tasks, uid: claim.UID, worker: agent.UID}
	require.NoError(h.t, exec.Run(h.t.Context(), claim, rep))

	return rep.body
}

func startOne(t *testing.T, h *harness, check *models.Check) rewraptask.Outcome {
	t.Helper()

	outcomes, err := h.srv.Start(t.Context(), h.org.UID, []*models.Check{check})
	require.NoError(t, err)
	require.Len(t, outcomes, 1)

	return outcomes[0]
}

func needsReseal(t *testing.T, h *harness, check *models.Check, recipients ...string) bool {
	t.Helper()

	fps := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		fps = append(fps, credentials.RecipientFingerprint(recipient))
	}

	need, err := credentials.NeedsReseal(*h.reload(check).ConfigSealed, fps)
	require.NoError(t, err)

	return need
}

// TestRewrap_NewAgentGetsTheBlob is the main path: a new agent joins a
// per-agent region, the old one re-seals the blob for both, and the flag
// clears. The task goes to the old agent only, and the origin is kept.
func TestRewrap_NewAgentGetsTheBlob(t *testing.T) {
	t.Parallel()

	runNewAgentGetsTheBlob(t, newHarness(t))
}

func runNewAgentGetsTheBlob(t *testing.T, h *harness) {
	t.Helper()

	r := require.New(t)

	oldAgent := h.enroll("old", true)
	check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret"}, oldAgent.keys.X25519Recipient)
	newAgent := h.enroll("new", true)

	r.True(needsReseal(t, h, check, oldAgent.keys.X25519Recipient, newAgent.keys.X25519Recipient))

	outcome := startOne(t, h, check)
	r.Equal("pending", outcome.Status)
	r.Equal(oldAgent.agent.UID, outcome.AgentUID, "only the agent the blob was sealed to can open it")

	r.Nil(h.claim(newAgent.agent), "a targeted task is never handed to another agent")

	claim := h.claim(oldAgent.agent)
	r.NotNil(claim)

	var input rewraptask.Input
	r.NoError(json.Unmarshal(claim.Input, &input))
	r.ElementsMatch([]string{oldAgent.keys.X25519Recipient, newAgent.keys.X25519Recipient}, input.Recipients)

	h.run(claim, oldAgent.agent, oldAgent.keys.X25519Identity)
	r.Equal(models.WorkerTaskStatusDone, h.task(claim.UID).Status)

	stored := h.reload(check)
	r.False(needsReseal(t, h, check, oldAgent.keys.X25519Recipient, newAgent.keys.X25519Recipient))
	r.Equal(credentials.SealOriginBrowser, credentials.SealedOrigin(*stored.ConfigSealed))
	r.Equal(*stored.ConfigSealed, h.jobSealed(check), "the job carries the new blob too")

	secrets, err := credentials.UnsealWithIdentity(newAgent.keys.X25519Identity, *stored.ConfigSealed)
	r.NoError(err)
	r.Equal("s3cret", secrets["password"])

	statuses, err := h.srv.Status(t.Context(), h.org.UID, []string{check.UID})
	r.NoError(err)
	r.Len(statuses, 1)
	r.Equal("done", statuses[0].Status)
}

// TestRewrap_RegionKeyRotation re-seals from the old region key to the new
// one: the agent holds both, and afterwards the old key no longer opens it.
func TestRewrap_RegionKeyRotation(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	h := newHarness(t)

	oldID, oldRecipient, err := credentials.GenerateX25519Keypair()
	r.NoError(err)
	newID, newRecipient, err := credentials.GenerateX25519Keypair()
	r.NoError(err)

	check := h.sealedCheck("acme-db", map[string]any{"password": "pw"}, oldRecipient)

	_, err = h.admin.SetRegionKey(ctx, h.org.Slug, "dc1", &agents.SetRegionKeyRequest{Recipient: newRecipient})
	r.NoError(err)

	agent := h.enroll("holder", true, "region-key-"+credentials.RecipientFingerprint(oldRecipient))

	outcome := startOne(t, h, check)
	r.Equal("pending", outcome.Status, outcome.ErrorCode)

	claim := h.claim(agent.agent)
	r.NotNil(claim)
	h.run(claim, agent.agent, agent.keys.X25519Identity, oldID, newID)

	stored := h.reload(check)
	r.False(needsReseal(t, h, check, newRecipient))

	_, err = credentials.UnsealWithIdentity(oldID, *stored.ConfigSealed)
	r.Error(err, "the old region key no longer opens the re-sealed blob")

	secrets, err := credentials.UnsealWithIdentity(newID, *stored.ConfigSealed)
	r.NoError(err)
	r.Equal("pw", secrets["password"])
}

// TestRewrap_AgentCannotOpen reports a blob the agent cannot open, and keeps
// the check's blob and flag.
func TestRewrap_AgentCannotOpen(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t)

	agent := h.enroll("holder", true)
	check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret"}, agent.keys.X25519Recipient)
	before := *check.ConfigSealed
	h.enroll("new", true)

	outcome := startOne(t, h, check)
	claim := h.claim(agent.agent)
	r.NotNil(claim)

	// The agent lost its key (a fresh identity): it cannot open the blob.
	otherID, _, err := credentials.GenerateX25519Keypair()
	r.NoError(err)

	body := h.run(claim, agent.agent, otherID)
	r.JSONEq(`{"errorCode":"REWRAP_CANNOT_OPEN"}`, string(body))

	task := h.task(outcome.TaskUID)
	r.Equal(models.WorkerTaskStatusFailed, task.Status)
	r.Equal(rewraptask.CodeCannotOpen, *task.ErrorCode)
	r.Equal(before, *h.reload(check).ConfigSealed)

	statuses, err := h.srv.Status(t.Context(), h.org.UID, nil)
	r.NoError(err)
	r.Len(statuses, 1)
	r.Equal(rewraptask.CodeCannotOpen, statuses[0].ErrorCode)
}

// TestRewrap_ResultValidation refuses a returned blob with a wrong stanza
// count or stale fingerprints; the check keeps its blob.
func TestRewrap_ResultValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		code string
		body func(oldRecipient, newRecipient string) rewraptask.Result
	}{
		{
			name: "wrong stanza count",
			code: rewraptask.CodeInvalidResult,
			body: func(oldRecipient, newRecipient string) rewraptask.Result {
				// Sealed to one recipient, but claims both fingerprints.
				_, ct, err := credentials.SealForClient([]string{oldRecipient}, map[string]any{"password": "x"})
				require.NoError(t, err)

				return rewraptask.Result{CT: ct, Fingerprints: []string{
					credentials.RecipientFingerprint(oldRecipient), credentials.RecipientFingerprint(newRecipient),
				}}
			},
		},
		{
			name: "stale fingerprints",
			code: rewraptask.CodeRecipientsChanged,
			body: func(oldRecipient, _ string) rewraptask.Result {
				fps, ct, err := credentials.SealForClient([]string{oldRecipient}, map[string]any{"password": "x"})
				require.NoError(t, err)

				return rewraptask.Result{CT: ct, Fingerprints: fps}
			},
		},
		{
			name: "unknown refusal code",
			code: rewraptask.CodeInvalidResult,
			body: func(string, string) rewraptask.Result { return rewraptask.Result{ErrorCode: "SOMETHING"} },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			h := newHarness(t)

			oldAgent := h.enroll("old", true)
			check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret"}, oldAgent.keys.X25519Recipient)
			before := *check.ConfigSealed
			newAgent := h.enroll("new", true)

			outcome := startOne(t, h, check)
			claim := h.claim(oldAgent.agent)
			r.NotNil(claim)

			body, err := json.Marshal(tc.body(oldAgent.keys.X25519Recipient, newAgent.keys.X25519Recipient))
			r.NoError(err)
			r.NoError(h.tasks.Finish(t.Context(), claim.UID, oldAgent.agent.UID, body))

			task := h.task(outcome.TaskUID)
			r.Equal(models.WorkerTaskStatusFailed, task.Status)
			r.Equal(tc.code, *task.ErrorCode)
			r.Equal(before, *h.reload(check).ConfigSealed, "a refused blob is never stored")
			r.True(needsReseal(t, h, check, oldAgent.keys.X25519Recipient, newAgent.keys.X25519Recipient))
		})
	}
}

// TestRewrap_NoOnlineRecipient refuses to start when no online agent holding
// a key of the blob runs the executor.
func TestRewrap_NoOnlineRecipient(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t)

	offline := h.enroll("offline", false)
	check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret"}, offline.keys.X25519Recipient)
	// Online, but the blob was not sealed to it: it cannot open it.
	h.enroll("new", true)

	outcome := startOne(t, h, check)
	r.Equal("refused", outcome.Status)
	r.Equal(rewraptask.CodeNoOnlineRecipient, outcome.ErrorCode)
	r.Empty(outcome.TaskUID)
}

// TestRewrap_KeyHolderWithoutRewrap names the reason when the online agent
// that holds the key does not run the re-wrap: its operator set
// SP_AGENT_REWRAP_ENABLED=false, or it predates 0.47 (spec 2026-11-10-04).
func TestRewrap_KeyHolderWithoutRewrap(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t)
	ctx := t.Context()

	holder := h.enroll("holder", false)
	check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret"}, holder.keys.X25519Recipient)
	// A new agent the blob is not sealed to, so the check needs a re-seal.
	h.enroll("new", true)

	// The holder is online, but its worker does not report the capability.
	now := time.Now().UTC()
	r.NoError(h.db.UpdateAgentLastSeen(ctx, holder.agent.UID, now))
	r.NoError(h.db.CreateWorker(ctx, models.NewWorker(agentcrypto.WorkerSlug(holder.agent.UID), "holder")))

	outcome := startOne(t, h, check)
	r.Equal("refused", outcome.Status)
	r.Equal(rewraptask.CodeRewrapNotOffered, outcome.ErrorCode)
	r.Empty(outcome.TaskUID)
}

// TestRewrap_UpToDateAndTimeout covers a check that needs nothing, and a
// re-wrap no agent finished within the timeout.
func TestRewrap_UpToDateAndTimeout(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	h := newHarness(t)

	agent := h.enroll("old", true)
	check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret"}, agent.keys.X25519Recipient)

	outcome := startOne(t, h, check)
	r.Equal(rewraptask.CodeUpToDate, outcome.ErrorCode)

	h.enroll("new", true)

	outcome = startOne(t, h, check)
	r.Equal("pending", outcome.Status)

	again := startOne(t, h, check)
	r.Equal(rewraptask.CodeAlreadyRunning, again.ErrorCode)

	_, err := h.db.DB().NewUpdate().Model((*models.WorkerTask)(nil)).
		Set("created_at = ?", time.Now().UTC().Add(-2*rewraptask.Timeout)).
		Where("uid = ?", outcome.TaskUID).Exec(ctx)
	r.NoError(err)

	statuses, err := h.srv.Status(ctx, h.org.UID, []string{check.UID})
	r.NoError(err)
	r.Len(statuses, 1)
	r.Equal("failed", statuses[0].Status)
	r.Equal(rewraptask.CodeTimedOut, statuses[0].ErrorCode)

	r.Nil(h.claim(agent.agent), "a timed-out re-wrap is not claimed any more")
}
