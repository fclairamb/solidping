package agentmode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

// TestLoadRegionIdentities: SP_AGENT_REGION_KEYS and SP_AGENT_REGION_KEYS_FILE
// are merged (old and new key during a rotation), comments are skipped, and an
// invalid identity fails the start.
func TestLoadRegionIdentities(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	oldID, oldRec, err := credentials.GenerateX25519Keypair()
	r.NoError(err)
	newID, newRec, err := credentials.GenerateX25519Keypair()
	r.NoError(err)

	file := filepath.Join(t.TempDir(), "region-keys.txt")
	r.NoError(os.WriteFile(file, []byte("# created: today\n"+newID+"\n\n"), 0o600))

	cfg := &config.Config{}
	cfg.Agent.RegionKeys = []string{oldID}
	cfg.Agent.RegionKeysFile = file

	ids, err := loadRegionIdentities(cfg)
	r.NoError(err)
	r.Equal([]string{oldID, newID}, ids)

	// Blobs sealed to either key open with the loaded list.
	for _, recipient := range []string{oldRec, newRec} {
		blob, sealErr := credentials.SealForRecipients([]string{recipient}, map[string]any{"k": "v"})
		r.NoError(sealErr)

		_, openErr := credentials.UnsealWithIdentities(blob, ids...)
		r.NoError(openErr)
	}

	cfg.Agent.RegionKeys = []string{"not-a-key"}
	_, err = loadRegionIdentities(cfg)
	r.Error(err)
}
