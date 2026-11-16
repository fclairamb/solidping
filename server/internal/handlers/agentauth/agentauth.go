// Package agentauth verifies the Ed25519 signed headers a deported agent puts
// on its plain HTTP calls (attachment uploads, worker tasks). It is the same
// scheme, skew and replay guard as the agent WebSocket reconnect, so an agent
// needs no second credential. The current (v2) challenge binds the full
// request URI and the exact body bytes; the legacy v1 challenge (path only) is
// still accepted for agents that have not upgraded yet.
package agentauth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Signed-request headers, identical to the agent WebSocket reconnect headers.
const (
	HeaderAgentUID  = "X-Sp-Agent-Uid"
	HeaderTimestamp = "X-Sp-Timestamp"
	HeaderNonce     = "X-Sp-Nonce"
	HeaderSignature = "X-Sp-Signature"
)

// nonceRetention mirrors agentws: twice the accepted clock skew, so a captured
// signature cannot be replayed anywhere inside the window it is valid in.
const nonceRetention = 2 * agents.DefaultClockSkew

// ErrUnauthenticated is the single error every authentication failure
// produces. Distinguishing "unknown agent" from "bad signature" would leak
// which agent uids exist.
var ErrUnauthenticated = errors.New("invalid agent signature")

// AgentStore loads an agent by uid.
type AgentStore interface {
	GetAgent(ctx context.Context, uid string) (*models.Agent, error)
}

// Authenticator verifies signed agent requests.
type Authenticator struct {
	store  AgentStore
	nonces agents.NonceGuard
}

// New builds an authenticator over the agent store and the replay guard.
func New(store AgentStore, nonces agents.NonceGuard) *Authenticator {
	return &Authenticator{store: store, nonces: nonces}
}

// SetNonceGuard swaps the replay guard (tests, embeddings without the
// agent_nonces table).
func (a *Authenticator) SetNonceGuard(guard agents.NonceGuard) {
	a.nonces = guard
}

// Authenticate verifies the signed headers exactly the way the WS reconnect
// does: known active agent (a revoked one fails), timestamp inside the skew,
// nonce not seen before (cluster-wide, DB-backed), then the Ed25519 signature
// — v2 over method|requestURI|timestamp|nonce|sha256(body) when the agent
// advertises it, legacy v1 over method|path|timestamp|nonce otherwise.
func (a *Authenticator) Authenticate(req *http.Request) (*models.Agent, error) {
	agentUID := req.Header.Get(HeaderAgentUID)
	timestamp := req.Header.Get(HeaderTimestamp)
	nonce := req.Header.Get(HeaderNonce)
	signature := req.Header.Get(HeaderSignature)

	if agentUID == "" || timestamp == "" || nonce == "" || signature == "" {
		return nil, ErrUnauthenticated
	}

	agent, err := a.store.GetAgent(req.Context(), agentUID)
	if err != nil || agent == nil {
		return nil, ErrUnauthenticated
	}

	if agent.Status != models.AgentStatusActive {
		return nil, ErrUnauthenticated
	}

	if tsErr := agents.ValidateTimestamp(timestamp, time.Now(), agents.DefaultClockSkew); tsErr != nil {
		return nil, ErrUnauthenticated
	}

	// Replay guard BEFORE the signature check, same order as agentws: a valid
	// signature presented twice must fail the second time. Fails closed: a
	// store error rejects the request.
	if nonceErr := a.nonces.CheckAndStore(
		req.Context(), agent.UID, nonce, time.Now(), nonceRetention,
	); nonceErr != nil {
		return nil, ErrUnauthenticated
	}

	if sigErr := a.verifySignature(req, agent, timestamp, nonce, signature); sigErr != nil {
		return nil, ErrUnauthenticated
	}

	return agent, nil
}

// maxSignedBodyBytes caps the body Authenticate buffers for the v2 body hash.
// Mirrors attachments.MaxAttachmentBytes (the largest signed payload agents
// send); the value is duplicated rather than imported because the attachments
// handler imports this package.
const maxSignedBodyBytes = 4 << 20

// verifySignature dispatches on the challenge version the agent advertises.
// v2 binds the full request URI (path AND query) and the exact body bytes;
// the legacy v1 challenge (path only) is still accepted for agents that have
// not upgraded, with a deprecation warning so operators can watch the legacy
// channel go quiet before it is removed. Any other version is refused.
func (a *Authenticator) verifySignature(
	req *http.Request, agent *models.Agent, timestamp, nonce, signature string,
) error {
	switch req.Header.Get(agents.HeaderSignatureVersion) {
	case agents.SignatureVersionV2:
		body, err := readAndRestoreBody(req)
		if err != nil {
			return err
		}

		return agents.VerifySignatureV2(
			agent.Ed25519PublicKey, req.Method, req.URL.RequestURI(), timestamp, nonce, body, signature,
		)
	case "":
		slog.WarnContext(req.Context(),
			"DEPRECATED: agent request authenticated with the legacy v1 signature (path only); "+
				"the agent should upgrade to the v2 challenge binding query and body",
			"agent", agent.UID, "path", req.URL.Path)

		return agents.VerifySignature(
			agent.Ed25519PublicKey, req.Method, req.URL.Path, timestamp, nonce, signature,
		)
	default:
		return ErrUnauthenticated
	}
}

// readAndRestoreBody buffers the request body so it can be hashed, then puts
// it back for the handler to decode. The read is capped and happens BEFORE the
// signature is verified, so it must not trust the caller's body size: an
// oversized body is refused rather than buffered.
func readAndRestoreBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}

	body, err := io.ReadAll(io.LimitReader(req.Body, maxSignedBodyBytes+1))
	_ = req.Body.Close()

	if err != nil {
		return nil, err
	}

	if int64(len(body)) > maxSignedBodyBytes {
		return nil, ErrUnauthenticated
	}

	req.Body = io.NopCloser(bytes.NewReader(body))

	return body, nil
}
