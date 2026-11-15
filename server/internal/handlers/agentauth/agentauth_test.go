package agentauth

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// fakeAgentStore serves one enrolled agent (or nothing for an unknown uid).
type fakeAgentStore struct {
	agent *models.Agent
}

func (f *fakeAgentStore) GetAgent(_ context.Context, uid string) (*models.Agent, error) {
	if f.agent != nil && f.agent.UID == uid {
		return f.agent, nil
	}

	return nil, sql.ErrNoRows
}

// authEnv is an Authenticator over one active agent and an in-memory nonce
// guard (single replica, which is all a unit test needs).
type authEnv struct {
	auth *Authenticator
	keys *agents.AgentKeys
}

func newAuthEnv(t *testing.T) *authEnv {
	t.Helper()
	r := require.New(t)

	keys, err := agents.GenerateAgentKeys()
	r.NoError(err)

	agent := &models.Agent{
		UID:              "agent-1",
		Status:           models.AgentStatusActive,
		Ed25519PublicKey: keys.Ed25519PublicKey,
	}

	return &authEnv{
		auth: New(&fakeAgentStore{agent: agent}, agents.NewNonceCache()),
		keys: keys,
	}
}

// signedRequest builds a POST to the attachments path (screenshot topic) with
// the given body, signed with the v2 challenge unless version is empty
// (legacy v1).
func (e *authEnv) signedRequest(t *testing.T, body []byte, version, nonce string) *http.Request {
	t.Helper()
	r := require.New(t)

	timestamp := time.Now().UTC().Format(time.RFC3339)
	path := "/api/v1/agent/attachments"
	requestURI := path + "?topic=screenshot"

	var signature string
	var err error

	if version == agents.SignatureVersionV2 {
		signature, err = e.keys.SignV2(http.MethodPost, requestURI, timestamp, nonce, body)
	} else {
		signature, err = e.keys.Sign(http.MethodPost, path, timestamp, nonce)
	}

	r.NoError(err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, requestURI, bytes.NewReader(body))
	req.Header.Set(HeaderAgentUID, "agent-1")
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderNonce, nonce)
	req.Header.Set(HeaderSignature, signature)

	if version != "" {
		req.Header.Set(agents.HeaderSignatureVersion, version)
	}

	return req
}

// TestAuthenticateV2BindsQueryAndBody is the core regression: a valid v2
// request authenticates AND the handler reads back the exact signed bytes;
// rewriting the body or the topic query in transit fails authentication.
func TestAuthenticateV2BindsQueryAndBody(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newAuthEnv(t)
	body := []byte("capture-bytes")

	// Happy path: the agent is resolved and the body is handed back intact.
	req := env.signedRequest(t, body, agents.SignatureVersionV2, "nonce-1")
	agent, err := env.auth.Authenticate(req)
	r.NoError(err)
	r.Equal("agent-1", agent.UID)

	restored, err := io.ReadAll(req.Body)
	r.NoError(err)
	r.Equal(body, restored, "the handler must read back the exact signed bytes")

	// Tampered body (same signature, nonce fresh) -> refused.
	tampered := env.signedRequest(t, body, agents.SignatureVersionV2, "nonce-2")
	tampered.Body = io.NopCloser(bytes.NewReader([]byte("forged-bytes")))
	_, err = env.auth.Authenticate(tampered)
	r.ErrorIs(err, ErrUnauthenticated)

	// Tampered query (same path, same body) -> refused. Signed for the
	// original topic, rewritten in transit the way an on-path attacker would.
	rewritten := env.signedRequest(t, body, agents.SignatureVersionV2, "nonce-3")
	rewritten.URL.RawQuery = "topic=trace"
	_, err = env.auth.Authenticate(rewritten)
	r.ErrorIs(err, ErrUnauthenticated)
}

// TestAuthenticateV2OversizedBodyRefused: the body is buffered BEFORE the
// signature is verified, so the read must be capped — an oversized payload is
// refused, not allocated.
func TestAuthenticateV2OversizedBodyRefused(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newAuthEnv(t)
	body := bytes.Repeat([]byte("a"), maxSignedBodyBytes+1)

	req := env.signedRequest(t, body, agents.SignatureVersionV2, "nonce-1")
	_, err := env.auth.Authenticate(req)
	r.ErrorIs(err, ErrUnauthenticated)
}

// TestAuthenticateLegacyV1StillAccepted: an agent that has not upgraded yet
// (no version header, path-only challenge) keeps working — the rollout is a
// fleet migration, not a lockstep restart.
func TestAuthenticateLegacyV1StillAccepted(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newAuthEnv(t)

	req := env.signedRequest(t, []byte("capture-bytes"), "", "nonce-1")
	agent, err := env.auth.Authenticate(req)
	r.NoError(err)
	r.Equal("agent-1", agent.UID)
}

// TestAuthenticateVersionDispatch: an unknown version is refused, and a v2
// signature cannot be downgraded to the legacy path-only verifier by dropping
// the version header.
func TestAuthenticateVersionDispatch(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newAuthEnv(t)
	body := []byte("capture-bytes")

	// Unknown version -> refused.
	unknown := env.signedRequest(t, body, "99", "nonce-1")
	_, err := env.auth.Authenticate(unknown)
	r.ErrorIs(err, ErrUnauthenticated)

	// A v2 signature presented without the version header is verified against
	// the v1 challenge -> refused (no downgrade).
	v2 := env.signedRequest(t, body, agents.SignatureVersionV2, "nonce-2")
	v2.Header.Del(agents.HeaderSignatureVersion)
	_, err = env.auth.Authenticate(v2)
	r.ErrorIs(err, ErrUnauthenticated)
}

// TestAuthenticateReplayedNonceRefused: the replay guard runs before the
// signature check, so a captured v2 request presented twice fails the second
// time even though the signature itself is valid.
func TestAuthenticateReplayedNonceRefused(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newAuthEnv(t)
	body := []byte("capture-bytes")

	first := env.signedRequest(t, body, agents.SignatureVersionV2, "nonce-1")
	_, err := env.auth.Authenticate(first)
	r.NoError(err)

	replay := env.signedRequest(t, body, agents.SignatureVersionV2, "nonce-1")
	_, err = env.auth.Authenticate(replay)
	r.ErrorIs(err, ErrUnauthenticated)
}

// TestAuthenticateRevokedAgentRefused: a revoked agent fails before any
// signature work, whatever the challenge version.
func TestAuthenticateRevokedAgentRefused(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newAuthEnv(t)
	store, ok := env.auth.store.(*fakeAgentStore)
	r.True(ok)
	store.agent.Status = models.AgentStatusRevoked

	req := env.signedRequest(t, []byte("capture-bytes"), agents.SignatureVersionV2, "nonce-1")
	_, err := env.auth.Authenticate(req)
	r.ErrorIs(err, ErrUnauthenticated)
}
