package rewraptask_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/rewraptask"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

var errInjected = errors.New("injected failure")

// faultyDB wraps the real database and fails the calls a test switches on.
type faultyDB struct {
	db.Service

	failParam   atomic.Bool
	failListAt  atomic.Int32 // fails the nth ListActiveAgentsByRegion call, 0 = never
	listCalls   atomic.Int32
	staleCheck  atomic.Pointer[models.Check]
	failGetWork atomic.Bool
}

func (f *faultyDB) GetOrgParameter(ctx context.Context, orgUID, key string) (*models.Parameter, error) {
	if f.failParam.Load() {
		return nil, errInjected
	}

	return f.Service.GetOrgParameter(ctx, orgUID, key)
}

func (f *faultyDB) ListActiveAgentsByRegion(ctx context.Context, orgUID, region string) ([]*models.Agent, error) {
	if at := f.failListAt.Load(); at != 0 && f.listCalls.Add(1) == at {
		return nil, errInjected
	}

	return f.Service.ListActiveAgentsByRegion(ctx, orgUID, region)
}

func (f *faultyDB) GetCheck(ctx context.Context, orgUID, checkUID string) (*models.Check, error) {
	if stale := f.staleCheck.Load(); stale != nil {
		return stale, nil
	}

	return f.Service.GetCheck(ctx, orgUID, checkUID)
}

func (f *faultyDB) GetWorkerBySlug(ctx context.Context, slug string) (*models.Worker, error) {
	if f.failGetWork.Load() {
		return nil, errInjected
	}

	return f.Service.GetWorkerBySlug(ctx, slug)
}

// faultyHarness is a harness whose server reads the database through faultyDB.
func faultyHarness(t *testing.T) (*harness, *faultyDB) {
	t.Helper()

	h := newHarness(t)
	faulty := &faultyDB{Service: h.db}

	// A fresh task service: the kind of h.tasks is bound to the healthy server.
	tasks := workertasks.NewService(h.db.DB())
	h.tasks = tasks
	h.srv = rewraptask.Register(rewraptask.Deps{DB: faulty, Tasks: tasks})

	return h, faulty
}

func (h *harness) exec(query string) {
	h.t.Helper()

	_, err := h.db.DB().ExecContext(h.t.Context(), query)
	require.NoError(h.t, err)
}

// pendingRewrap sets up an agent holding a sealed check, a new agent, and a
// started re-wrap claimed by the holder.
func pendingRewrap(t *testing.T, h *harness) (enrolled, enrolled, *models.Check, *workertasks.Claim) {
	t.Helper()

	r := require.New(t)

	holder := h.enroll("old", true)
	check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret"}, holder.keys.X25519Recipient)
	fresh := h.enroll("new", true)

	r.Equal("pending", startOne(t, h, check).Status)

	claim := h.claim(holder.agent)
	r.NotNil(claim)

	return holder, fresh, check, claim
}

func validBody(t *testing.T, recipients ...string) json.RawMessage {
	t.Helper()

	fps, ct, err := credentials.SealForClient(recipients, map[string]any{"password": "x"})
	require.NoError(t, err)

	body, err := json.Marshal(rewraptask.Result{CT: ct, Fingerprints: fps})
	require.NoError(t, err)

	return body
}

func TestRewrap_FinishRefusals(t *testing.T) {
	t.Parallel()

	t.Run("malformed body", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		holder, _, check, claim := pendingRewrap(t, h)

		require.NoError(t, h.tasks.Finish(t.Context(), claim.UID, holder.agent.UID, json.RawMessage(`"nope"`)))
		require.Equal(t, rewraptask.CodeInvalidResult, *h.task(claim.UID).ErrorCode)
		require.NotNil(t, h.reload(check).ConfigSealed)
	})

	t.Run("check changed after the claim", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		holder, fresh, check, claim := pendingRewrap(t, h)

		other := h.sealedCheck("other", map[string]any{"password": "y"}, holder.keys.X25519Recipient)
		h.exec("UPDATE checks SET config_sealed = '" + *other.ConfigSealed + "' WHERE uid = '" + check.UID + "'")

		body := validBody(t, holder.keys.X25519Recipient, fresh.keys.X25519Recipient)
		require.NoError(t, h.tasks.Finish(t.Context(), claim.UID, holder.agent.UID, body))
		require.Equal(t, rewraptask.CodeCheckChanged, *h.task(claim.UID).ErrorCode)
	})

	t.Run("check swapped while storing", func(t *testing.T) {
		t.Parallel()

		h, faulty := faultyHarness(t)
		holder, fresh, check, claim := pendingRewrap(t, h)

		// The server reads the check as it was when the task was created, while
		// the stored blob has already moved on.
		faulty.staleCheck.Store(h.reload(check))
		h.exec("UPDATE checks SET config_sealed = '" + *h.sealedCheck("other-check", map[string]any{"password": "y"},
			holder.keys.X25519Recipient).ConfigSealed + "' WHERE uid = '" + check.UID + "'")

		body := validBody(t, holder.keys.X25519Recipient, fresh.keys.X25519Recipient)
		require.NoError(t, h.tasks.Finish(t.Context(), claim.UID, holder.agent.UID, body))
		require.Equal(t, rewraptask.CodeCheckChanged, *h.task(claim.UID).ErrorCode)
	})

	t.Run("unparsable stored blob", func(t *testing.T) {
		t.Parallel()

		h, faulty := faultyHarness(t)
		holder, fresh, check, claim := pendingRewrap(t, h)

		garbled := h.reload(check)
		bad := "garbage"
		garbled.ConfigSealed = &bad
		// The task payload is bound to the real blob: make the hash match.
		h.exec("UPDATE worker_tasks SET payload = '" + `{"envelopeSha256":"` + hashOf(bad) + `"}` +
			"' WHERE uid = '" + claim.UID + "'")
		faulty.staleCheck.Store(garbled)

		body := validBody(t, holder.keys.X25519Recipient, fresh.keys.X25519Recipient)
		require.NoError(t, h.tasks.Finish(t.Context(), claim.UID, holder.agent.UID, body))
		require.Equal(t, rewraptask.CodeInvalidResult, *h.task(claim.UID).ErrorCode)
	})
}

func TestRewrap_FinishFailures(t *testing.T) {
	t.Parallel()

	t.Run("recipients unreadable", func(t *testing.T) {
		t.Parallel()

		h, faulty := faultyHarness(t)
		holder, fresh, _, claim := pendingRewrap(t, h)

		faulty.failParam.Store(true)

		body := validBody(t, holder.keys.X25519Recipient, fresh.keys.X25519Recipient)
		require.Error(t, h.tasks.Finish(t.Context(), claim.UID, holder.agent.UID, body))
	})

	t.Run("check update fails", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		holder, fresh, check, claim := pendingRewrap(t, h)

		h.exec("CREATE TRIGGER no_check_update BEFORE UPDATE ON checks BEGIN SELECT RAISE(ABORT, 'nope'); END")

		body := validBody(t, holder.keys.X25519Recipient, fresh.keys.X25519Recipient)
		require.Error(t, h.tasks.Finish(t.Context(), claim.UID, holder.agent.UID, body))
		require.NotNil(t, h.reload(check).ConfigSealed)
	})

	t.Run("job update fails and rolls back", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		holder, fresh, check, claim := pendingRewrap(t, h)
		before := *h.reload(check).ConfigSealed

		h.exec("CREATE TRIGGER no_job_update BEFORE UPDATE ON check_jobs BEGIN SELECT RAISE(ABORT, 'nope'); END")

		body := validBody(t, holder.keys.X25519Recipient, fresh.keys.X25519Recipient)
		require.Error(t, h.tasks.Finish(t.Context(), claim.UID, holder.agent.UID, body))
		require.Equal(t, before, *h.reload(check).ConfigSealed, "the check keeps its blob when its jobs cannot follow")
	})
}

func TestRewrap_ClaimCancelledWhenNothingToDo(t *testing.T) {
	t.Parallel()

	setup := func(t *testing.T) (*harness, enrolled, *models.Check) {
		t.Helper()

		h := newHarness(t)
		holder := h.enroll("old", true)
		check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret"}, holder.keys.X25519Recipient)
		h.enroll("new", true)
		require.Equal(t, "pending", startOne(t, h, check).Status)

		return h, holder, check
	}

	t.Run("blob removed", func(t *testing.T) {
		t.Parallel()

		h, holder, check := setup(t)
		h.exec("UPDATE checks SET config_sealed = NULL WHERE uid = '" + check.UID + "'")

		require.Nil(t, h.claim(holder.agent))
	})

	t.Run("check deleted", func(t *testing.T) {
		t.Parallel()

		h, holder, check := setup(t)
		h.exec("PRAGMA foreign_keys = OFF")
		h.exec("DELETE FROM checks WHERE uid = '" + check.UID + "'")

		require.Nil(t, h.claim(holder.agent))
	})

	t.Run("no private region left", func(t *testing.T) {
		t.Parallel()

		h, holder, check := setup(t)
		h.exec("UPDATE checks SET regions = '[]' WHERE uid = '" + check.UID + "'")

		require.Nil(t, h.claim(holder.agent))
	})
}

func TestRewrap_ClaimFailsWhenRecipientsUnreadable(t *testing.T) {
	t.Parallel()

	h, faulty := faultyHarness(t)
	holder := h.enroll("old", true)
	check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret"}, holder.keys.X25519Recipient)
	h.enroll("new", true)
	require.Equal(t, "pending", startOne(t, h, check).Status)

	faulty.failParam.Store(true)

	_, err := h.tasks.Claim(t.Context(), &workertasks.WorkerIdentity{
		UID: holder.agent.UID, Region: testRegion, OrgUID: h.org.UID, IsAgent: true, Kinds: []string{rewraptask.Kind},
	})
	require.Error(t, err)
}

func TestRewrap_StartErrors(t *testing.T) {
	t.Parallel()

	t.Run("recipients unreadable", func(t *testing.T) {
		t.Parallel()

		h, faulty := faultyHarness(t)
		holder := h.enroll("old", true)
		check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret"}, holder.keys.X25519Recipient)

		faulty.failParam.Store(true)

		_, err := h.srv.Start(t.Context(), h.org.UID, []*models.Check{check})
		require.Error(t, err)
	})

	t.Run("agent listing fails when picking", func(t *testing.T) {
		t.Parallel()

		h, faulty := faultyHarness(t)
		holder := h.enroll("old", true)
		check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret"}, holder.keys.X25519Recipient)
		h.enroll("new", true)

		// The first listing is the recipients', the second the picker's.
		faulty.failListAt.Store(2)

		_, err := h.srv.Start(t.Context(), h.org.UID, []*models.Check{check})
		require.Error(t, err)
	})

	t.Run("task creation fails", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		holder := h.enroll("old", true)
		check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret"}, holder.keys.X25519Recipient)
		h.enroll("new", true)

		h.exec("CREATE TRIGGER no_task BEFORE INSERT ON worker_tasks BEGIN SELECT RAISE(ABORT, 'nope'); END")

		_, err := h.srv.Start(t.Context(), h.org.UID, []*models.Check{check})
		require.Error(t, err)
	})

	t.Run("refusals", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		holder := h.enroll("old", true)
		check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret"}, holder.keys.X25519Recipient)

		unsealed := *check
		unsealed.ConfigSealed = nil
		require.Equal(t, rewraptask.CodeNotSealed, startOne(t, h, &unsealed).ErrorCode)

		garbled := *check
		garbage := "garbage"
		garbled.ConfigSealed = &garbage
		require.Equal(t, rewraptask.CodeNotSealed, startOne(t, h, &garbled).ErrorCode)

		// No agent in the region: nothing to seal to.
		empty := newHarness(t)
		blob := *check.ConfigSealed
		lonely := empty.sealedCheck("lonely", map[string]any{"password": "s3cret"}, holder.keys.X25519Recipient)
		lonely.ConfigSealed = &blob
		outcomes, err := empty.srv.Start(t.Context(), empty.org.UID, []*models.Check{lonely})
		require.NoError(t, err)
		require.Equal(t, rewraptask.CodeNoRecipients, outcomes[0].ErrorCode)
	})

	t.Run("closed database", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		require.NoError(t, h.db.Close())

		_, err := h.srv.Start(t.Context(), h.org.UID, nil)
		require.Error(t, err)

		_, err = h.srv.Status(t.Context(), h.org.UID, nil)
		require.Error(t, err)
	})
}

func TestRewrap_PickAgentWithoutWorkerRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t)

	holder := h.enroll("holder", false)
	check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret"}, holder.keys.X25519Recipient)
	h.enroll("new", true)

	// Online, holds the key, but never registered a worker row: no capability.
	r.NoError(h.db.UpdateAgentLastSeen(t.Context(), holder.agent.UID, time.Now().UTC()))

	r.Equal(rewraptask.CodeRewrapNotOffered, startOne(t, h, check).ErrorCode)
}

func TestRewrap_PickAgentWorkerLookupFails(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h, faulty := faultyHarness(t)

	holder := h.enroll("holder", true)
	check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret"}, holder.keys.X25519Recipient)
	h.enroll("new", true)

	faulty.failGetWork.Store(true)

	r.Equal(rewraptask.CodeRewrapNotOffered, startOne(t, h, check).ErrorCode)
}

func TestRewrap_StatusLatestPerCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	h := newHarness(t)

	holder := h.enroll("old", true)
	check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret"}, holder.keys.X25519Recipient)
	h.enroll("new", true)

	first := startOne(t, h, check)
	r.Equal("pending", first.Status)

	_, err := h.db.DB().NewUpdate().Model((*models.WorkerTask)(nil)).
		Set("created_at = ?", time.Now().UTC().Add(-2*rewraptask.Timeout)).
		Where("uid = ?", first.TaskUID).Exec(ctx)
	r.NoError(err)

	second := startOne(t, h, check)
	r.Equal("pending", second.Status)
	r.NotEqual(first.TaskUID, second.TaskUID)

	// A task that belongs to a job, not a check, has no check to report on.
	jobUID := "job-without-check"
	orphan := models.NewWorkerTask(h.org.UID, rewraptask.Kind, testRegion)
	orphan.JobUID = &jobUID
	h.exec("PRAGMA foreign_keys = OFF")
	_, err = h.tasks.CreateIfNone(ctx, h.db.DB(), orphan)
	r.NoError(err)

	statuses, err := h.srv.Status(ctx, h.org.UID, nil)
	r.NoError(err)
	r.Len(statuses, 1, "one row per check, the latest one")
	r.Equal(second.TaskUID, statuses[0].TaskUID)
}

func TestRewrap_StatusQueryFails(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	// The expiry update still works, the listing of full rows does not.
	h.exec("ALTER TABLE worker_tasks DROP COLUMN payload")

	_, err := h.srv.Status(t.Context(), h.org.UID, nil)
	require.Error(t, err)
}

func TestRewrap_ExecutorRefusals(t *testing.T) {
	t.Parallel()

	exec := rewraptask.NewExecutor(func() []string { return nil })
	require.Equal(t, rewraptask.Kind, exec.Kind())
	require.Equal(t, rewraptask.Capability, exec.Capability())

	h := newHarness(t)
	holder := h.enroll("old", true)
	envelope := *h.sealedCheck("acme-api", map[string]any{"password": "s3cret"}, holder.keys.X25519Recipient).ConfigSealed

	input := func(in rewraptask.Input) json.RawMessage {
		raw, err := json.Marshal(in)
		require.NoError(t, err)

		return raw
	}

	cases := []struct {
		name  string
		input json.RawMessage
		code  string
	}{
		{"not json", json.RawMessage(`not json`), rewraptask.CodeInvalidInput},
		{"empty input", input(rewraptask.Input{}), rewraptask.CodeInvalidInput},
		{
			"seal failure", input(rewraptask.Input{Envelope: envelope, Recipients: []string{"not-a-recipient"}}),
			rewraptask.CodeSealFailed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			exec := rewraptask.NewExecutor(func() []string { return []string{holder.keys.X25519Identity} })
			rep := &captureReporter{}

			require.NoError(t, exec.Run(t.Context(), &workertasks.Claim{UID: "t", Input: tc.input}, rep))
			require.JSONEq(t, `{"errorCode":"`+tc.code+`"}`, string(rep.body))
		})
	}
}

// captureReporter records the body a task finished with.
type captureReporter struct{ body json.RawMessage }

func (*captureReporter) Heartbeat(context.Context) error { return nil }

func (*captureReporter) Step(context.Context, workertasks.StepRequest) error { return nil }

func (c *captureReporter) Finish(_ context.Context, body json.RawMessage) error {
	c.body = body

	return nil
}

func (*captureReporter) Fail(context.Context, string) error { return nil }

func hashOf(envelope string) string {
	sum := sha256.Sum256([]byte(envelope))

	return hex.EncodeToString(sum[:])
}

// TestRewrap_ClaimCancelledForJobTask: a re-wrap task that belongs to a job and
// not to a check has nothing to re-wrap and is canceled on claim.
func TestRewrap_ClaimCancelledForJobTask(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t)
	agent := h.enroll("old", true)

	h.exec("PRAGMA foreign_keys = OFF")

	jobUID := "job-without-check"
	task := models.NewWorkerTask(h.org.UID, rewraptask.Kind, testRegion)
	task.JobUID = &jobUID
	task.TargetWorkerUID = &agent.agent.UID

	created, err := h.tasks.CreateIfNone(t.Context(), h.db.DB(), task)
	r.NoError(err)
	r.True(created)

	r.Nil(h.claim(agent.agent))
	r.Equal(models.WorkerTaskStatusCancelled, h.task(task.UID).Status)
}
