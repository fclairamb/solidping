package agentws_test

import (
	"encoding/json"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/plugins/protocol"
	"github.com/fclairamb/solidping/server/internal/plugintypes"
)

func agentManifest(description string) []protocol.TypeManifest {
	return []protocol.TypeManifest{{
		Type: "x-agent-ws", Plugin: "acme", Version: "1.0.0", Protocol: "exec", Description: description,
		Schema: json.RawMessage(`{"type":"object","required":["host"],"properties":{"host":{"type":"string"}}}`),
	}}
}

// The plugin types an agent sends on its claim frame are stored under the
// agent's org, once per connection (spec 2026-10-17-05 §18).
func TestClaimStoresTheAgentPluginTypesUnderItsOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	ctx := t.Context()
	store := plugintypes.New(e.dbSvc.DB())

	other := models.NewOrganization("agentws-other", "Other")
	r.NoError(e.dbSvc.CreateOrganization(ctx, other))

	conn, keys, enrolled := e.enroll(e.mintToken(), "plugins-agent")

	resp := roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 5,
		Capabilities: []string{"x-agent-ws"}, Plugins: agentManifest("first"),
	})
	r.Equal(agentcrypto.MsgTypeJobs, resp.Type)

	remote, ok, err := store.Lookup(ctx, e.org.UID, "x-agent-ws")
	r.NoError(err)
	r.True(ok)
	r.Equal("first", remote.Meta().Description)

	_, ok, err = store.Lookup(ctx, other.UID, "x-agent-ws")
	r.NoError(err)
	r.False(ok, "another org never gets the type")

	// A second report on the same connection is ignored.
	resp = roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c2", MaxJobs: 5, Plugins: agentManifest("second"),
	})
	r.Equal(agentcrypto.MsgTypeJobs, resp.Type)

	remote, _, err = store.Lookup(ctx, e.org.UID, "x-agent-ws")
	r.NoError(err)
	r.Equal("first", remote.Meta().Description)

	_ = conn.Close(websocket.StatusNormalClosure, "bye")

	// A new connection reports again, and the latest report wins.
	conn2 := reconnect(t, e, keys, enrolled.AgentUID, "nonce-plugins")

	resp = roundTrip(t, conn2, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c3", MaxJobs: 5, Plugins: agentManifest("third"),
	})
	r.Equal(agentcrypto.MsgTypeJobs, resp.Type)

	remote, _, err = store.Lookup(ctx, e.org.UID, "x-agent-ws")
	r.NoError(err)
	r.Equal("third", remote.Meta().Description)
}

// A system agent serves every org, so what it reports is stored for none.
func TestSystemAgentPluginTypesAreNotStored(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newSysEnv(t)
	ctx := t.Context()
	store := plugintypes.New(e.dbSvc.DB())

	conn, _, _ := e.enroll(e.mintSystemToken(systemRegion), "fly-machine-plugins")

	resp := roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 5, Plugins: agentManifest("system"),
	})
	r.Equal(agentcrypto.MsgTypeJobs, resp.Type)

	for _, org := range []*models.Organization{e.org, e.orgB} {
		list, err := store.List(ctx, org.UID)
		r.NoError(err)
		r.Empty(list)
	}
}
