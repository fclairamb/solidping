package backend_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

// TestUnsealIdentitiesOwnKeyFirst: the identities an agent opens blobs with are
// its own key, then the shared region keys it holds, in that order.
func TestUnsealIdentitiesOwnKeyFirst(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	keys, err := agents.GenerateAgentKeys()
	r.NoError(err)

	regionA, _, err := credentials.GenerateX25519Keypair()
	r.NoError(err)
	regionB, _, err := credentials.GenerateX25519Keypair()
	r.NoError(err)

	wsBackend := backend.NewWSBackend("http://127.0.0.1:1", "", "agent",
		&backend.Identity{AgentKeys: *keys}, nil, backend.WithRegionIdentities([]string{regionA, regionB}))

	r.Equal([]string{keys.X25519Identity, regionA, regionB}, wsBackend.UnsealIdentities())

	alone := backend.NewWSBackend("http://127.0.0.1:1", "", "agent", &backend.Identity{AgentKeys: *keys}, nil)
	r.Equal([]string{keys.X25519Identity}, alone.UnsealIdentities())
}
