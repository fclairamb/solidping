package rewraptask_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/rewraptask"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// captureReporter records what an executor finished with.
type captureReporter struct {
	body json.RawMessage
}

func (*captureReporter) Heartbeat(context.Context) error { return nil }

func (*captureReporter) Step(context.Context, workertasks.StepRequest) error { return nil }

func (c *captureReporter) Finish(_ context.Context, body json.RawMessage) error {
	c.body = body

	return nil
}

func (*captureReporter) Fail(context.Context, string) error { return nil }

func claimOf(t *testing.T, input any) *workertasks.Claim {
	t.Helper()

	raw, err := json.Marshal(input)
	require.NoError(t, err)

	return &workertasks.Claim{UID: "task", CheckUID: "check", Input: raw}
}

func TestExecutor_KindAndCapability(t *testing.T) {
	t.Parallel()

	none := func() []string { return nil }

	require.Equal(t, rewraptask.Kind, rewraptask.NewExecutor(none).Kind())
	require.Equal(t, rewraptask.Capability, rewraptask.NewExecutor(none).Capability())
	require.Equal(t, rewraptask.VerifyKind, rewraptask.NewVerifyExecutor(none).Kind())
	require.Equal(t, rewraptask.VerifyCapability, rewraptask.NewVerifyExecutor(none).Capability())
}

// TestExecutor_Refusals: every way an agent can refuse a re-wrap finishes
// the task with its own code and never with a plaintext.
func TestExecutor_Refusals(t *testing.T) {
	t.Parallel()

	identity, recipient, err := credentials.GenerateX25519Keypair()
	require.NoError(t, err)
	otherIdentity, _, err := credentials.GenerateX25519Keypair()
	require.NoError(t, err)
	_, newRecipient, err := credentials.GenerateX25519Keypair()
	require.NoError(t, err)

	envelope, err := credentials.SealForRecipients([]string{recipient}, map[string]any{"password": "s3cret"})
	require.NoError(t, err)

	leakyPlain := boundTo("https://acme.com")
	leakyPlain["url"] = "https://evil.example"

	leaky, err := credentials.SealForRecipients([]string{recipient}, leakyPlain)
	require.NoError(t, err)

	// A server-sealed blob carrying a public key (spec 2026-11-12-03).
	serverLeaky, err := credentials.SealForRecipients([]string{recipient},
		map[string]any{"password": "s3cret", "url": "https://evil.example"})
	require.NoError(t, err)

	challenge, _, err := credentials.NewSealedChallenge(recipient)
	require.NoError(t, err)

	cases := []struct {
		name       string
		identities []string
		input      any
		want       string
	}{
		{
			"public field in a server blob",
			[]string{identity},
			rewraptask.Input{
				Envelope: serverLeaky, Recipients: []string{newRecipient}, CheckType: "http",
				Config: map[string]any{"url": "https://acme.com"},
			},
			rewraptask.CodeNotSecret,
		},
		{"no input", []string{identity}, map[string]any{}, rewraptask.CodeInvalidInput},
		{"no recipients", []string{identity}, rewraptask.Input{Envelope: envelope}, rewraptask.CodeInvalidInput},
		{
			"challenge not openable",
			[]string{otherIdentity},
			rewraptask.Input{Envelope: envelope, Recipients: []string{newRecipient}, Challenge: challenge},
			rewraptask.CodeCannotOpen,
		},
		{
			"blob not openable",
			[]string{otherIdentity},
			rewraptask.Input{Envelope: envelope, Recipients: []string{newRecipient}},
			rewraptask.CodeCannotOpen,
		},
		{
			"public field in the blob",
			[]string{identity},
			rewraptask.Input{
				Envelope: leaky, Recipients: []string{newRecipient}, CheckType: "http",
				Config: map[string]any{"url": "https://acme.com"},
			},
			rewraptask.CodeNotSecret,
		},
		{
			"recipient is not a key",
			[]string{identity},
			rewraptask.Input{Envelope: envelope, Recipients: []string{"not-a-recipient"}},
			rewraptask.CodeSealFailed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rep := &captureReporter{}
			exec := rewraptask.NewExecutor(func() []string { return tc.identities })
			require.NoError(t, exec.Run(t.Context(), claimOf(t, tc.input), rep))

			var result rewraptask.Result
			require.NoError(t, json.Unmarshal(rep.body, &result))
			require.Equal(t, tc.want, result.ErrorCode)
			require.Empty(t, result.CT)
		})
	}
}

// TestExecutor_LegacyServerSendsNoChallenge: a server older than the
// possession challenge sends none; the re-wrap still goes through, unanswered.
func TestExecutor_LegacyServerSendsNoChallenge(t *testing.T) {
	t.Parallel()

	identity, recipient, err := credentials.GenerateX25519Keypair()
	require.NoError(t, err)
	_, newRecipient, err := credentials.GenerateX25519Keypair()
	require.NoError(t, err)

	envelope, err := credentials.SealForRecipients([]string{recipient}, map[string]any{"password": "s3cret"})
	require.NoError(t, err)

	rep := &captureReporter{}
	exec := rewraptask.NewExecutor(func() []string { return []string{identity} })
	require.NoError(t, exec.Run(t.Context(),
		claimOf(t, rewraptask.Input{Envelope: envelope, Recipients: []string{newRecipient}}), rep))

	var result rewraptask.Result
	require.NoError(t, json.Unmarshal(rep.body, &result))
	require.Empty(t, result.ErrorCode)
	require.Empty(t, result.Challenge)
	require.NotEmpty(t, result.CT)
	require.Equal(t, []string{credentials.RecipientFingerprint(newRecipient)}, result.Fingerprints)
}

func TestVerifyExecutor(t *testing.T) {
	t.Parallel()

	identity, recipient, err := credentials.GenerateX25519Keypair()
	require.NoError(t, err)
	otherIdentity, _, err := credentials.GenerateX25519Keypair()
	require.NoError(t, err)

	secrets := map[string]any{"password": "s3cret"}

	seal := func(plain map[string]any) string {
		t.Helper()

		envelope, sealErr := credentials.SealForRecipients([]string{recipient}, plain)
		require.NoError(t, sealErr)

		return envelope
	}

	pending := seal(secrets)
	same := seal(secrets)
	different := seal(map[string]any{"password": "other"})
	leakyPlain := boundTo("https://acme.com")
	leakyPlain["url"] = "https://evil.example"
	leaky := seal(leakyPlain)
	// Unbound, as the server seals (spec 2026-11-12-03).
	serverLeaky := seal(map[string]any{"password": "s3cret", "url": "https://evil.example"})

	challenge, sum, err := credentials.NewSealedChallenge(recipient)
	require.NoError(t, err)

	cases := []struct {
		name       string
		identities []string
		input      rewraptask.VerifyInput
		wantCode   string
	}{
		{"no input", []string{identity}, rewraptask.VerifyInput{}, rewraptask.CodeInvalidInput},
		{
			"challenge not openable",
			[]string{otherIdentity},
			rewraptask.VerifyInput{Envelope: pending, Challenge: challenge},
			rewraptask.CodeCannotOpen,
		},
		{
			"public field in the blob",
			[]string{identity},
			rewraptask.VerifyInput{
				Envelope: leaky, Challenge: challenge, CheckType: "http",
				Config: map[string]any{"url": "https://acme.com"},
			},
			rewraptask.CodeNotSecret,
		},
		{
			"public field in a server blob",
			[]string{identity},
			rewraptask.VerifyInput{
				Envelope: serverLeaky, Challenge: challenge, CheckType: "http",
				Config: map[string]any{"url": "https://acme.com"},
			},
			rewraptask.CodeNotSecret,
		},
		{
			"server blob with secret fields only",
			[]string{identity},
			rewraptask.VerifyInput{
				Envelope: pending, Challenge: challenge, CheckType: "http",
				Config: map[string]any{"url": "https://acme.com"},
			},
			"",
		},
		{
			"differs from the old blob",
			[]string{identity},
			rewraptask.VerifyInput{Envelope: pending, Previous: different, Challenge: challenge},
			rewraptask.CodeVerifyMismatch,
		},
		{
			"same as the old blob",
			[]string{identity},
			rewraptask.VerifyInput{Envelope: pending, Previous: same, Challenge: challenge},
			"",
		},
		{
			"old blob not openable by this agent",
			[]string{identity},
			rewraptask.VerifyInput{Envelope: pending, Previous: "garbage", Challenge: challenge},
			"",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rep := &captureReporter{}
			exec := rewraptask.NewVerifyExecutor(func() []string { return tc.identities })
			require.NoError(t, exec.Run(t.Context(), claimOf(t, tc.input), rep))

			var result rewraptask.VerifyResult
			require.NoError(t, json.Unmarshal(rep.body, &result))
			require.Equal(t, tc.wantCode, result.ErrorCode)

			if tc.wantCode == "" {
				require.True(t, credentials.ChallengeAnswered(result.Challenge, sum))
			} else {
				require.Empty(t, result.Challenge)
			}
		})
	}
}

// TestVerifyExecutor_BlobNotOpenable: the agent proves possession of the
// challenge key but cannot open the re-sealed blob.
func TestVerifyExecutor_BlobNotOpenable(t *testing.T) {
	t.Parallel()

	identity, recipient, err := credentials.GenerateX25519Keypair()
	require.NoError(t, err)
	_, other, err := credentials.GenerateX25519Keypair()
	require.NoError(t, err)

	blob, err := credentials.SealForRecipients([]string{other}, map[string]any{"password": "s3cret"})
	require.NoError(t, err)

	challenge, _, err := credentials.NewSealedChallenge(recipient)
	require.NoError(t, err)

	rep := &captureReporter{}
	exec := rewraptask.NewVerifyExecutor(func() []string { return []string{identity} })
	require.NoError(t, exec.Run(t.Context(),
		claimOf(t, rewraptask.VerifyInput{Envelope: blob, Challenge: challenge}), rep))

	var result rewraptask.VerifyResult
	require.NoError(t, json.Unmarshal(rep.body, &result))
	require.Equal(t, rewraptask.CodeVerifyCannotOpen, result.ErrorCode)
}

// TestExecutor_ServerBlobWithSecretsOnly is the positive control of spec
// 2026-11-12-03: a server blob that only holds secret fields re-wraps.
func TestExecutor_ServerBlobWithSecretsOnly(t *testing.T) {
	t.Parallel()

	identity, recipient, err := credentials.GenerateX25519Keypair()
	require.NoError(t, err)
	newIdentity, newRecipient, err := credentials.GenerateX25519Keypair()
	require.NoError(t, err)

	envelope, err := credentials.SealForRecipients([]string{recipient}, map[string]any{"password": "s3cret"})
	require.NoError(t, err)

	rep := &captureReporter{}
	exec := rewraptask.NewExecutor(func() []string { return []string{identity} })
	require.NoError(t, exec.Run(t.Context(), claimOf(t, rewraptask.Input{
		Envelope: envelope, Recipients: []string{newRecipient}, CheckType: "http",
		Config: map[string]any{"url": "https://acme.com"},
	}), rep))

	var result rewraptask.Result
	require.NoError(t, json.Unmarshal(rep.body, &result))
	require.Empty(t, result.ErrorCode)

	stored, err := credentials.RewrapSealedEnvelope(envelope, result.Fingerprints, result.CT)
	require.NoError(t, err)
	require.Equal(t, credentials.SealOriginAgent, credentials.SealedOrigin(stored))

	secrets, err := credentials.UnsealWithIdentity(newIdentity, stored)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"password": "s3cret"}, secrets)
}
