package agentws_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/agentws"
)

var errInjected = errors.New("injected database failure")

// failingEnrollDB lets the first token lookup through (the pre-upgrade check
// of serveEnrollment) and fails the next ones, and can refuse EnrollAgent.
type failingEnrollDB struct {
	db.Service

	lookups    atomic.Int32
	failEnroll atomic.Bool
}

func (f *failingEnrollDB) GetAgentEnrollmentTokenByHash(
	ctx context.Context, hash string,
) (*models.AgentEnrollmentToken, error) {
	if f.lookups.Add(1) > 1 {
		return nil, errInjected
	}

	return f.Service.GetAgentEnrollmentTokenByHash(ctx, hash)
}

func (f *failingEnrollDB) EnrollAgent(
	ctx context.Context, tokenHash, name, ed, x, fingerprint string,
) (*models.Agent, error) {
	if f.failEnroll.Load() {
		return nil, errInjected
	}

	return f.Service.EnrollAgent(ctx, tokenHash, name, ed, x, fingerprint)
}

// dialEnrollment dials with token, reads the hello and sends frame; it returns
// the connection so the test can watch how the server ends it.
func dialEnrollment(t *testing.T, e *env, token string, frame agentcrypto.ClientFrame) *websocket.Conn {
	t.Helper()

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+token)

	conn, _, err := e.dial(headers)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })

	var hello agentcrypto.ServerFrame
	require.NoError(t, wsjson.Read(t.Context(), conn, &hello))
	require.NoError(t, wsjson.Write(t.Context(), conn, frame))

	return conn
}

// requireRefusedEnrollment checks the server closed the socket with the
// auth-failed code and created no agent.
func requireRefusedEnrollment(t *testing.T, e *env, conn *websocket.Conn) {
	t.Helper()

	var frame agentcrypto.ServerFrame

	err := wsjson.Read(t.Context(), conn, &frame)
	require.Error(t, err)
	require.Equal(t, agentws.CloseAuthFailed, websocket.CloseStatus(err), "got frame %+v", frame)

	agents, err := e.dbSvc.ListAgents(t.Context(), e.org.UID)
	require.NoError(t, err)
	require.Empty(t, agents)
}

func TestMalformedEnrollFramesAreRefused(t *testing.T) {
	t.Parallel()

	keys, err := agentcrypto.GenerateAgentKeys()
	require.NoError(t, err)

	cases := map[string]agentcrypto.ClientFrame{
		"not an enroll frame": {Type: agentcrypto.MsgTypeClaim, ID: "x"},
		"bad ed25519 key": {
			Type: agentcrypto.MsgTypeEnroll, ID: "x", Ed25519PublicKey: "not-a-key",
			X25519PublicKey: keys.X25519Recipient,
		},
		"missing x25519 key": {
			Type: agentcrypto.MsgTypeEnroll, ID: "x", Ed25519PublicKey: keys.Ed25519PublicKey,
		},
		"x25519 key is not an age recipient": {
			Type: agentcrypto.MsgTypeEnroll, ID: "x", Ed25519PublicKey: keys.Ed25519PublicKey,
			X25519PublicKey: "ssh-ed25519 AAAA",
		},
	}

	for name, frame := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t)
			conn := dialEnrollment(t, e, e.mintToken(), frame)
			requireRefusedEnrollment(t, e, conn)
		})
	}
}

func TestEnrollWithoutNameGetsTheDefaultName(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)

	_, _, enrolled := e.enroll(e.mintToken(), "")

	agents, err := e.dbSvc.ListAgents(t.Context(), e.org.UID)
	r.NoError(err)
	r.Len(agents, 1)
	r.Equal(enrolled.AgentUID, agents[0].UID)
	r.NotEmpty(agents[0].Name, "an agent that sent no name still gets one")
}

func TestEnrollmentFailsWhenTheDatabaseDoes(t *testing.T) {
	t.Parallel()

	keys, err := agentcrypto.GenerateAgentKeys()
	require.NoError(t, err)

	frame := agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeEnroll, ID: "x", Name: "agent",
		Ed25519PublicKey: keys.Ed25519PublicKey, X25519PublicKey: keys.X25519Recipient,
	}

	t.Run("token lookup", func(t *testing.T) {
		t.Parallel()

		wrapper := &failingEnrollDB{}
		e := newEnvWrapped(t, nil, nil, func(inner db.Service) db.Service {
			wrapper.Service = inner

			return wrapper
		})

		conn := dialEnrollment(t, e, e.mintToken(), frame)
		requireRefusedEnrollment(t, e, conn)
	})

	t.Run("agent creation", func(t *testing.T) {
		t.Parallel()

		wrapper := &failingEnrollDB{}
		wrapper.failEnroll.Store(true)
		wrapper.lookups.Store(-1000) // the lookups all succeed

		e := newEnvWrapped(t, nil, nil, func(inner db.Service) db.Service {
			wrapper.Service = inner

			return wrapper
		})

		conn := dialEnrollment(t, e, e.mintToken(), frame)
		requireRefusedEnrollment(t, e, conn)
	})
}
