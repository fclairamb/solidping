package agentws_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/handlers/agentws"
)

var errNonceStoreDown = errors.New("nonce store unreachable")

// failingNonceGuard fails every CheckAndStore like a database outage would.
type failingNonceGuard struct{}

func (failingNonceGuard) CheckAndStore(context.Context, string, string, time.Time, time.Duration) error {
	return errNonceStoreDown
}

// signedReconnectHeaders signs a reconnect for agentUID with keys.
func signedReconnectHeaders(
	t *testing.T, keys *agentcrypto.AgentKeys, agentUID, nonce string,
) http.Header {
	t.Helper()

	timestamp := time.Now().UTC().Format(time.RFC3339)
	signature, err := keys.Sign(http.MethodGet, "/api/v1/agent/ws", timestamp, nonce)
	require.NoError(t, err)

	headers := http.Header{}
	headers.Set("X-Sp-Agent-Uid", agentUID)
	headers.Set("X-Sp-Timestamp", timestamp)
	headers.Set("X-Sp-Nonce", nonce)
	headers.Set("X-Sp-Signature", signature)

	return headers
}

// TestReconnectNonceStoreFailureIs503: a nonce store error is not a replay, so
// the agent gets a retryable 503 instead of "Nonce already used" (401).
func TestReconnectNonceStoreFailureIs503(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Enrollment never consults the nonce guard, so it can fail from the start.
	e := newEnvConfigured(t, nil, func(h *agentws.Handler) { h.SetNonceGuard(failingNonceGuard{}) })

	conn, keys, enrolled := e.enroll(e.mintToken(), "dc1-agent")
	_ = conn.Close(websocket.StatusNormalClosure, "bye")

	_, status, err := e.dial(signedReconnectHeaders(t, keys, enrolled.AgentUID, "nonce-down"))
	r.Error(err)
	r.Equal(http.StatusServiceUnavailable, status)
}

// TestAgentAuthDatabaseDownIs503: with the database unreachable, a reconnect
// and an enrollment both get 503, while unknown or malformed agent UIDs keep
// their 401.
func TestAgentAuthDatabaseDownIs503(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)

	token := e.mintToken()

	conn, keys, enrolled := e.enroll(e.mintToken(), "dc1-agent")
	_ = conn.Close(websocket.StatusNormalClosure, "bye")

	// Unknown (well-formed) and malformed UIDs: 401.
	_, status, err := e.dial(signedReconnectHeaders(t, keys, "00000000-0000-4000-8000-000000000000", "n-1"))
	r.Error(err)
	r.Equal(http.StatusUnauthorized, status)

	_, status, err = e.dial(signedReconnectHeaders(t, keys, "not-a-uuid", "n-2"))
	r.Error(err)
	r.Equal(http.StatusUnauthorized, status)

	r.NoError(e.dbSvc.Close())

	_, status, err = e.dial(signedReconnectHeaders(t, keys, enrolled.AgentUID, "n-3"))
	r.Error(err)
	r.Equal(http.StatusServiceUnavailable, status)

	_, status, err = e.dial(http.Header{"Authorization": []string{"Bearer " + token}})
	r.Error(err)
	r.Equal(http.StatusServiceUnavailable, status)
}
