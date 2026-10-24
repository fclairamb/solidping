package agentmode

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

func TestRunWithoutIdentityNorToken(t *testing.T) {
	t.Chdir(t.TempDir())

	cfg := &config.Config{}
	cfg.Agent.KeysFile = filepath.Join(t.TempDir(), "missing.json")

	require.ErrorIs(t, Run(context.Background(), cfg), ErrNoIdentityNoToken)
}

func TestRunRejectsBrokenIdentity(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.Agent.Keys = "!!!not base64!!!"
	require.ErrorContains(t, Run(context.Background(), cfg), "decode SP_AGENT_KEYS")

	cfg.Agent.Keys = base64.StdEncoding.EncodeToString([]byte("{not json"))
	require.ErrorContains(t, Run(context.Background(), cfg), "parse SP_AGENT_KEYS")
}

func TestLoadIdentityFromFile(t *testing.T) {
	t.Chdir(t.TempDir())

	identity, _ := newTestIdentity(t)
	path := filepath.Join(t.TempDir(), "keys.json")

	cfg := &config.Config{}
	cfg.Agent.KeysFile = path

	got, fromEnv, err := loadIdentity(cfg)
	require.NoError(t, err)
	require.Nil(t, got)
	require.False(t, fromEnv)

	persistIdentity(context.Background(), cfg, identity, false, bufferLogger(&bytes.Buffer{}), nil)

	got, fromEnv, err = loadIdentity(cfg)
	require.NoError(t, err)
	require.False(t, fromEnv)
	require.Equal(t, identity.AgentUID, got.AgentUID)

	require.NoError(t, os.WriteFile(path, []byte("{broken"), 0o600))

	_, _, err = loadIdentity(cfg)
	require.ErrorContains(t, err, "parse")
}

func TestRunFailsWhenMasterUnreachable(t *testing.T) {
	t.Chdir(t.TempDir())

	_, encoded := newTestIdentity(t)

	cfg := &config.Config{}
	cfg.Agent.Keys = encoded
	cfg.Agent.PrintKeys = true
	cfg.Agent.Name = "acme-agent"
	cfg.Agent.ServerURL = "http://127.0.0.1:1"

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)

	go func() { done <- Run(ctx, cfg) }()

	select {
	case err := <-done:
		// The master is unreachable: registration fails and Run reports it.
		require.ErrorContains(t, err, "failed to register worker")
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return")
	}
}
