package rewraptask_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/rewraptask"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// rewrapped is a check whose re-wrap by the old agent passed its checks and
// waits for the confirmation of the new agent.
type rewrapped struct {
	h        *harness
	check    *models.Check
	oldAgent enrolled
	newAgent enrolled
	before   string
	rewrap   string
}

func newRewrapped(t *testing.T) *rewrapped {
	t.Helper()

	r := require.New(t)
	h := newHarness(t)

	oldAgent := h.enroll("old", true)
	check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret"}, oldAgent.keys.X25519Recipient)
	before := *check.ConfigSealed
	newAgent := h.enroll("new", true)

	outcome := startOne(t, h, check)
	claim := h.claim(oldAgent.agent)
	r.NotNil(claim)

	h.run(claim, oldAgent.agent, oldAgent.keys.X25519Identity)
	r.Equal(models.WorkerTaskStatusDone, h.task(outcome.TaskUID).Status)

	return &rewrapped{
		h: h, check: check, oldAgent: oldAgent, newAgent: newAgent, before: before, rewrap: outcome.TaskUID,
	}
}

// claimVerify claims the confirmation as the new agent and returns the
// challenge answer an honest agent would send.
func (rw *rewrapped) claimVerify(t *testing.T) (*workertasks.Claim, string) {
	t.Helper()

	claim := rw.h.claimKind(rw.newAgent.agent, rewraptask.VerifyKind)
	require.NotNil(t, claim)

	var input rewraptask.VerifyInput
	require.NoError(t, json.Unmarshal(claim.Input, &input))
	require.NotEmpty(t, input.Envelope)
	require.Equal(t, rw.before, input.Previous, "the confirming agent gets the blob it replaces")
	require.Equal(t, "http", input.CheckType)

	return claim, answerChallenge(t, input.Challenge, rw.newAgent.keys.X25519Identity)
}

func answerChallenge(t *testing.T, challenge string, identities ...string) string {
	t.Helper()

	value, err := credentials.OpenSealedChallenge(challenge, identities...)
	require.NoError(t, err)

	return value
}

func (rw *rewrapped) finishVerify(t *testing.T, claim *workertasks.Claim, body any) {
	t.Helper()

	var raw []byte

	if str, ok := body.(string); ok {
		raw = []byte(str)
	} else {
		var err error

		raw, err = json.Marshal(body)
		require.NoError(t, err)
	}

	require.NoError(t, rw.h.tasks.Finish(t.Context(), claim.UID, rw.newAgent.agent.UID, raw))
}

func (rw *rewrapped) assertRefused(t *testing.T, verifyUID, code string) {
	t.Helper()

	task := rw.h.task(verifyUID)
	require.Equal(t, models.WorkerTaskStatusFailed, task.Status)
	require.NotNil(t, task.ErrorCode)
	require.Equal(t, code, *task.ErrorCode)
	require.Equal(t, rw.before, *rw.h.reload(rw.check).ConfigSealed, "a refused confirmation stores nothing")

	final := status(t, rw.h, rw.check)
	require.Equal(t, "failed", final.Status)
	require.Equal(t, code, final.ErrorCode)
}

// TestConfirmation_RefusedResults: whatever the confirming agent sends back,
// only the right challenge with no error code stores the blob.
func TestConfirmation_RefusedResults(t *testing.T) {
	t.Parallel()

	cases := map[string]func(answer string) any{
		"malformed body":     func(string) any { return "not json" },
		"unknown error code": func(string) any { return rewraptask.VerifyResult{ErrorCode: "SOMETHING"} },
		"no challenge":       func(string) any { return rewraptask.VerifyResult{} },
		"wrong challenge":    func(answer string) any { return rewraptask.VerifyResult{Challenge: answer + "0"} },
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rw := newRewrapped(t)
			claim, answer := rw.claimVerify(t)
			rw.finishVerify(t, claim, body(answer))
			rw.assertRefused(t, claim.UID, rewraptask.CodeInvalidResult)
		})
	}
}

// TestConfirmation_CheckChanged: the check's blob changed after the re-wrap,
// so the confirmation is never handed out; or while it was out, so its
// answer is refused.
func TestConfirmation_CheckChanged(t *testing.T) {
	t.Parallel()

	change := func(t *testing.T, rw *rewrapped) {
		t.Helper()

		other := rw.h.sealedCheck("other", map[string]any{"password": "other"}, rw.oldAgent.keys.X25519Recipient)

		_, err := rw.h.db.DB().NewUpdate().Model((*models.Check)(nil)).
			Set("config_sealed = ?", *other.ConfigSealed).Where("uid = ?", rw.check.UID).Exec(t.Context())
		require.NoError(t, err)
	}

	t.Run("before the claim", func(t *testing.T) {
		t.Parallel()

		rw := newRewrapped(t)
		change(t, rw)

		require.Nil(t, rw.h.claimKind(rw.newAgent.agent, rewraptask.VerifyKind))
	})

	t.Run("after the claim", func(t *testing.T) {
		t.Parallel()

		rw := newRewrapped(t)
		claim, answer := rw.claimVerify(t)
		change(t, rw)
		rw.finishVerify(t, claim, rewraptask.VerifyResult{Challenge: answer})

		task := rw.h.task(claim.UID)
		require.Equal(t, models.WorkerTaskStatusFailed, task.Status)
		require.Equal(t, rewraptask.CodeCheckChanged, *task.ErrorCode)
	})
}

// TestConfirmation_RecipientsChanged: an agent enrolled while the
// confirmation was out changes what the blob must be sealed to.
func TestConfirmation_RecipientsChanged(t *testing.T) {
	t.Parallel()

	rw := newRewrapped(t)
	claim, answer := rw.claimVerify(t)

	rw.h.enroll("third", true)
	rw.finishVerify(t, claim, rewraptask.VerifyResult{Challenge: answer})
	rw.assertRefused(t, claim.UID, rewraptask.CodeRecipientsChanged)
}

// TestConfirmation_NeverCreated: a re-wrap whose confirmation task does not
// exist reads as pending, then as timed out.
func TestConfirmation_NeverCreated(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	rw := newRewrapped(t)

	_, err := rw.h.db.DB().NewDelete().Model((*models.WorkerTask)(nil)).
		Where("kind = ?", rewraptask.VerifyKind).Exec(ctx)
	r.NoError(err)

	r.Equal("pending", status(t, rw.h, rw.check).Status)

	_, err = rw.h.db.DB().NewUpdate().Model((*models.WorkerTask)(nil)).
		Set("created_at = ?", time.Now().UTC().Add(-2*rewraptask.Timeout)).
		Where("uid = ?", rw.rewrap).Exec(ctx)
	r.NoError(err)

	final := status(t, rw.h, rw.check)
	r.Equal("failed", final.Status)
	r.Equal(rewraptask.CodeTimedOut, final.ErrorCode)
}
