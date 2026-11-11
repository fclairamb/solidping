package agentws_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/sealing"
)

// sendEnroll dials with token and sends an enroll frame carrying recipient as
// the agent's X25519 key. It returns the server's answer.
func (e *env) sendEnroll(token, recipient string) agentcrypto.ServerFrame {
	e.t.Helper()

	r := require.New(e.t)
	ctx := e.t.Context()

	keys, err := agentcrypto.GenerateAgentKeys()
	r.NoError(err)

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+token)

	conn, _, err := e.dial(headers)
	r.NoError(err)
	e.t.Cleanup(func() { _ = conn.CloseNow() })

	var hello agentcrypto.ServerFrame
	r.NoError(wsjson.Read(ctx, conn, &hello))

	r.NoError(wsjson.Write(ctx, conn, agentcrypto.ClientFrame{
		Type:             agentcrypto.MsgTypeEnroll,
		ID:               "enroll-dup",
		Name:             "copycat",
		Ed25519PublicKey: keys.Ed25519PublicKey,
		X25519PublicKey:  recipient,
	}))

	var answer agentcrypto.ServerFrame
	r.NoError(wsjson.Read(ctx, conn, &answer))

	return answer
}

// TestEnrollRefusesADuplicateKey: an agent cannot register another live
// agent's X25519 key, nor a region key of the org (current or retired), as
// its own (spec 2026-11-11-02). A fresh key is accepted.
func TestEnrollRefusesADuplicateKey(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	e := newEnv(t)

	_, firstKeys, _ := e.enroll(e.mintToken(), "first")

	_, regionRecipient, err := credentials.GenerateX25519Keypair()
	r.NoError(err)
	_, retiredRecipient, err := credentials.GenerateX25519Keypair()
	r.NoError(err)

	current, err := sealing.NewRegionKey(regionRecipient, time.Now())
	r.NoError(err)
	r.NoError(sealing.SetRegionKeys(ctx, e.dbSvc, e.org.UID, map[string]sealing.RegionKey{testRegion: current}))

	retired, err := sealing.NewRegionKey(retiredRecipient, time.Now())
	r.NoError(err)
	r.NoError(sealing.RetireRegionKey(ctx, e.dbSvc, e.org.UID, retired, time.Now()))

	for name, recipient := range map[string]string{
		"another agent's key":  firstKeys.X25519Recipient,
		"the region key":       regionRecipient,
		"a retired region key": retiredRecipient,
	} {
		token := e.mintToken()

		answer := e.sendEnroll(token, recipient)
		r.Equal(agentcrypto.MsgTypeError, answer.Type, name)
		r.Contains(answer.Title, "already the key of another agent", name)

		// The token was not consumed: the operator retries with a new key.
		_, _, enrolled := e.enroll(token, "retry")
		r.NotEmpty(enrolled.AgentUID, name)
	}

	rows, err := e.dbSvc.ListAgents(ctx, e.org.UID)
	r.NoError(err)
	r.Len(rows, 4, "the first agent and the three retries, no copy")

	holders := 0

	for _, row := range rows {
		if row.X25519PublicKey == firstKeys.X25519Recipient {
			holders++
		}
	}

	r.Equal(1, holders, "only the first agent has its key")
}
