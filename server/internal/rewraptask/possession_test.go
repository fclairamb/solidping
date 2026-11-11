package rewraptask_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/agents"
	"github.com/fclairamb/solidping/server/internal/rewraptask"
	"github.com/fclairamb/solidping/server/internal/sealing"
)

// enrollWithKey enrolls a live agent of @dc1 that registered recipient as its
// X25519 key (it holds no matching identity), running both executors.
func (h *harness) enrollWithKey(name, recipient string, regionCaps ...string) *models.Agent {
	h.t.Helper()

	r := require.New(h.t)
	ctx := h.t.Context()

	minted, err := h.admin.MintEnrollmentToken(ctx, h.org.Slug, &agents.MintEnrollmentTokenRequest{RegionSlug: "dc1"}, nil)
	r.NoError(err)

	keys, err := agentcrypto.GenerateAgentKeys()
	r.NoError(err)

	agent, err := h.db.EnrollAgent(ctx, agentcrypto.HashEnrollmentToken(minted.Token), name,
		keys.Ed25519PublicKey, recipient, agentcrypto.KeyFingerprint(keys.Ed25519PublicKey))
	r.NoError(err)

	now := time.Now().UTC()
	r.NoError(h.db.UpdateAgentLastSeen(ctx, agent.UID, now))
	agent.LastSeenAt = &now

	worker := models.NewWorker(agentcrypto.WorkerSlug(agent.UID), name)
	worker.Capabilities = append([]string{rewraptask.Capability, rewraptask.VerifyCapability}, regionCaps...)
	r.NoError(h.db.CreateWorker(ctx, worker))

	return agent
}

// forge is what an agent that cannot open a blob sends back: any plaintext
// it likes, sealed to the current recipients, with the right fingerprints.
func forge(t *testing.T, challenge string, recipients ...string) json.RawMessage {
	t.Helper()

	fps, ct, err := credentials.SealForClient(recipients, map[string]any{"url": "https://always-up.example"})
	require.NoError(t, err)

	body, err := json.Marshal(rewraptask.Result{CT: ct, Fingerprints: fps, Challenge: challenge})
	require.NoError(t, err)

	return body
}

// TestRewrap_ImpostorIsRefused: an agent picked for a key it does not hold
// (a copy of another agent's public key, or a region key fingerprint it
// advertises without the key) cannot answer the challenge. Its valid-looking
// blob is refused, the old blob is kept and the check stays flagged. The
// positive control is TestRewrap_NewAgentGetsTheBlob.
func TestRewrap_ImpostorIsRefused(t *testing.T) {
	t.Parallel()

	cases := map[string]func(h *harness, holder enrolled, regionRecipient string) *models.Agent{
		"copied agent key": func(h *harness, holder enrolled, _ string) *models.Agent {
			return h.enrollWithKey("impostor", holder.keys.X25519Recipient)
		},
		"advertised region key": func(h *harness, _ enrolled, regionRecipient string) *models.Agent {
			_, own, err := credentials.GenerateX25519Keypair()
			require.NoError(t, err)

			return h.enrollWithKey("impostor", own, "region-key-"+credentials.RecipientFingerprint(regionRecipient))
		},
	}

	for name, impostorOf := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx := t.Context()
			h := newHarness(t)

			holder := h.enroll("holder", false)

			_, regionRecipient, err := credentials.GenerateX25519Keypair()
			r.NoError(err)

			recipients := []string{holder.keys.X25519Recipient}
			if name == "advertised region key" {
				_, err = h.admin.SetRegionKey(ctx, h.org.Slug, "dc1",
					&agents.SetRegionKeyRequest{Recipient: regionRecipient})
				r.NoError(err)

				recipients = []string{regionRecipient}
			}

			check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret"}, recipients...)
			before := *check.ConfigSealed

			if name == "advertised region key" {
				// Rotate, so the check needs a re-seal.
				_, newRecipient, keyErr := credentials.GenerateX25519Keypair()
				r.NoError(keyErr)
				_, err = h.admin.SetRegionKey(ctx, h.org.Slug, "dc1",
					&agents.SetRegionKeyRequest{Recipient: newRecipient})
				r.NoError(err)
			} else {
				h.enroll("new", false)
			}

			impostor := impostorOf(h, holder, regionRecipient)

			outcome := startOne(t, h, check)
			r.Equal("pending", outcome.Status, outcome.ErrorCode)
			r.Equal(impostor.UID, outcome.AgentUID, "the server picks the impostor on its word")

			claim := h.claim(impostor)
			r.NotNil(claim)

			// It cannot open the challenge: it answers with anything.
			current, err := sealing.RecipientsForRegions(ctx, h.db, h.org.UID, []string{testRegion})
			r.NoError(err)
			r.NoError(h.tasks.Finish(ctx, claim.UID, impostor.UID,
				forge(t, "00", sealing.RecipientKeys(current)...)))

			task := h.task(outcome.TaskUID)
			r.Equal(models.WorkerTaskStatusFailed, task.Status)
			r.Equal(rewraptask.CodeInvalidResult, *task.ErrorCode)
			r.Equal(before, *h.reload(check).ConfigSealed, "the old blob is kept")
			r.True(needsReseal(t, h, check, sealing.RecipientKeys(current)...), "the check stays flagged")

			// Its executor, run honestly, cannot open the challenge either.
			r.Nil(h.claimKind(impostor, rewraptask.VerifyKind))
		})
	}
}

// TestRewrap_SecondHolderCannotOpen: a re-wrap the confirming agent cannot
// open is never stored; the check stays flagged with the reason.
func TestRewrap_SecondHolderCannotOpen(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	h := newHarness(t)

	oldAgent := h.enroll("old", true)
	check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret"}, oldAgent.keys.X25519Recipient)
	before := *check.ConfigSealed
	newAgent := h.enroll("new", true)

	outcome := startOne(t, h, check)
	claim := h.claim(oldAgent.agent)
	r.NotNil(claim)

	// The key holder seals to itself and to a stranger, and claims the new
	// agent's fingerprint: the stanza count passes.
	_, stranger, err := credentials.GenerateX25519Keypair()
	r.NoError(err)

	_, ct, err := credentials.SealForClient([]string{oldAgent.keys.X25519Recipient, stranger},
		map[string]any{"password": "s3cret"})
	r.NoError(err)

	body, err := json.Marshal(rewraptask.Result{
		CT: ct, Challenge: answer(t, claim, oldAgent.keys.X25519Identity),
		Fingerprints: []string{
			credentials.RecipientFingerprint(oldAgent.keys.X25519Recipient),
			credentials.RecipientFingerprint(newAgent.keys.X25519Recipient),
		},
	})
	r.NoError(err)
	r.NoError(h.tasks.Finish(ctx, claim.UID, oldAgent.agent.UID, body))
	r.Equal(models.WorkerTaskStatusDone, h.task(outcome.TaskUID).Status)

	verify, reply := h.confirm(newAgent.agent, newAgent.keys.X25519Identity)
	r.JSONEq(`{"errorCode":"REWRAP_VERIFY_CANNOT_OPEN"}`, string(reply))
	r.Equal(models.WorkerTaskStatusFailed, h.task(verify.UID).Status)

	r.Equal(before, *h.reload(check).ConfigSealed, "never stored")
	r.True(needsReseal(t, h, check, oldAgent.keys.X25519Recipient, newAgent.keys.X25519Recipient))

	final := status(t, h, check)
	r.Equal("failed", final.Status)
	r.Equal(rewraptask.CodeVerifyCannotOpen, final.ErrorCode)
	r.Equal(outcome.TaskUID, final.TaskUID)
}

// TestRewrap_SecondHolderRefusesChangedSecrets: the confirming agent refuses
// a re-wrap whose plaintext differs from the old blob's (it holds an old key
// too), or carries a key that is not a secret field of the type.
func TestRewrap_SecondHolderRefusesChangedSecrets(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		plain map[string]any
		code  string
	}{
		"other secrets":   {plain: map[string]any{"password": "chosen"}, code: rewraptask.CodeVerifyMismatch},
		"a public field":  {plain: map[string]any{"url": "https://always-up.example"}, code: rewraptask.CodeNotSecret},
		"bound elsewhere": {plain: boundTo("https://evil.example"), code: rewraptask.CodeNotSecret},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx := t.Context()
			h := newHarness(t)

			first := h.enroll("first", true)
			second := h.enroll("second", true)
			check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret"},
				first.keys.X25519Recipient, second.keys.X25519Recipient)
			before := *check.ConfigSealed
			third := h.enroll("third", true)

			// The most recently seen holder re-wraps; the next one confirms,
			// and it can open the old blob too.
			now := time.Now().UTC()
			r.NoError(h.db.UpdateAgentLastSeen(ctx, first.agent.UID, now.Add(2*time.Second)))
			r.NoError(h.db.UpdateAgentLastSeen(ctx, second.agent.UID, now.Add(time.Second)))

			outcome := startOne(t, h, check)
			r.Equal(first.agent.UID, outcome.AgentUID)

			claim := h.claim(first.agent)
			r.NotNil(claim)

			fps, ct, err := credentials.SealForClient([]string{
				first.keys.X25519Recipient, second.keys.X25519Recipient, third.keys.X25519Recipient,
			}, tc.plain)
			r.NoError(err)

			body, err := json.Marshal(rewraptask.Result{
				CT: ct, Fingerprints: fps, Challenge: answer(t, claim, first.keys.X25519Identity),
			})
			r.NoError(err)
			r.NoError(h.tasks.Finish(ctx, claim.UID, first.agent.UID, body))

			r.Nil(h.claimKind(third.agent, rewraptask.VerifyKind), "the confirmation goes to one agent")

			verifier := second
			_, reply := h.confirm(verifier.agent, verifier.keys.X25519Identity)
			r.JSONEq(`{"errorCode":"`+tc.code+`"}`, string(reply))
			r.Equal(before, *h.reload(check).ConfigSealed)
		})
	}
}

func boundTo(url string) map[string]any {
	return map[string]any{
		"password": "s3cret",
		credentials.BindingKey: credentials.NewBinding("http", []string{"tunnelCheckUid", "url"},
			map[string]any{"url": url}),
	}
}

// TestRewrap_ExecutorRefusesAPublicField: an honest agent never re-seals a
// plaintext carrying a key that is not a secret field of the check type.
func TestRewrap_ExecutorRefusesAPublicField(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t)

	oldAgent := h.enroll("old", true)
	check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret", "url": "https://evil.example"},
		oldAgent.keys.X25519Recipient)
	before := *check.ConfigSealed
	h.enroll("new", true)

	outcome := startOne(t, h, check)
	claim := h.claim(oldAgent.agent)
	r.NotNil(claim)

	body := h.run(claim, oldAgent.agent, oldAgent.keys.X25519Identity)
	r.JSONEq(`{"errorCode":"REWRAP_NOT_SECRET"}`, string(body))
	r.Equal(rewraptask.CodeNotSecret, *h.task(outcome.TaskUID).ErrorCode)
	r.Equal(before, *h.reload(check).ConfigSealed)
}

// TestRewrap_NoProvableKey: an agent that holds the blob's key only by a
// region key the server has no public key for (rotated away before the
// server kept retired keys) cannot be challenged, so it gets no task.
func TestRewrap_NoProvableKey(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	h := newHarness(t)

	_, oldRecipient, err := credentials.GenerateX25519Keypair()
	r.NoError(err)
	_, newRecipient, err := credentials.GenerateX25519Keypair()
	r.NoError(err)

	check := h.sealedCheck("acme-db", map[string]any{"password": "pw"}, oldRecipient)

	_, err = h.admin.SetRegionKey(ctx, h.org.Slug, "dc1", &agents.SetRegionKeyRequest{Recipient: newRecipient})
	r.NoError(err)

	h.enroll("holder", true, "region-key-"+credentials.RecipientFingerprint(oldRecipient))

	outcome := startOne(t, h, check)
	r.Equal("refused", outcome.Status)
	r.Equal(rewraptask.CodeNoProvableKey, outcome.ErrorCode)
}

// TestRewrap_AgentWithoutTheChallengeIsNotPicked: an agent older than the
// possession challenge reports the re-wrap but not the confirmation. It would
// always be refused, so it gets no task: the reason says so.
func TestRewrap_AgentWithoutTheChallengeIsNotPicked(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	h := newHarness(t)

	holder := h.enroll("holder", false)
	check := h.sealedCheck("acme-api", map[string]any{"password": "s3cret"}, holder.keys.X25519Recipient)
	h.enroll("new", true)

	now := time.Now().UTC()
	r.NoError(h.db.UpdateAgentLastSeen(ctx, holder.agent.UID, now))

	worker := models.NewWorker(agentcrypto.WorkerSlug(holder.agent.UID), "holder")
	worker.Capabilities = []string{rewraptask.Capability}
	r.NoError(h.db.CreateWorker(ctx, worker))

	outcome := startOne(t, h, check)
	r.Equal("refused", outcome.Status)
	r.Equal(rewraptask.CodeRewrapNotOffered, outcome.ErrorCode)
}
