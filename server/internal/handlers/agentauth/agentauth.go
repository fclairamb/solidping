// Package agentauth verifies the Ed25519 signed headers a deported agent puts
// on its plain HTTP calls (attachment uploads, worker tasks). It is the same
// scheme, skew and replay guard as the agent WebSocket reconnect, so an agent
// needs no second credential.
package agentauth

import (
	"context"
	"errors"
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
// nonce not seen before (cluster-wide, DB-backed), Ed25519 signature over
// method|path|timestamp|nonce.
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

	if sigErr := agents.VerifySignature(
		agent.Ed25519PublicKey, req.Method, req.URL.Path, timestamp, nonce, signature,
	); sigErr != nil {
		return nil, ErrUnauthenticated
	}

	return agent, nil
}
