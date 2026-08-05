package agentmode

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/config"
)

// newTestIdentity returns a freshly generated identity plus the base64 of its
// serialized form — the exact string that must never appear in any output.
func newTestIdentity(t *testing.T) (*backend.Identity, string) {
	t.Helper()

	keys, err := agents.GenerateAgentKeys()
	require.NoError(t, err)

	identity := &backend.Identity{AgentKeys: *keys, AgentUID: "agent-uid-1", Region: "test-1"}

	raw, err := json.MarshalIndent(identity, "", "  ")
	require.NoError(t, err)

	return identity, base64.StdEncoding.EncodeToString(raw)
}

// requireNoKeyMaterial asserts that none of the agent's secrets — the base64 of
// the identity JSON, the Ed25519 private key, the age identity, or even the
// AGE-SECRET-KEY-1 prefix — appear anywhere in the captured output.
func requireNoKeyMaterial(t *testing.T, identity *backend.Identity, encoded, output, what string) {
	t.Helper()

	r := require.New(t)
	r.NotContains(output, encoded, "%s must not contain the base64 identity", what)
	r.NotContains(output, identity.Ed25519PrivateKey, "%s must not contain the ed25519 private key", what)
	r.NotContains(output, identity.X25519Identity, "%s must not contain the x25519 identity", what)
	r.NotContains(output, "AGE-SECRET-KEY-1", "%s must not contain an age secret key", what)
}

// bufferLogger returns a logger writing structured text into buf.
func bufferLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// unwritableKeysPath makes every persistence target fail, for root as much as
// for a normal user: the configured path hangs off a regular file (so MkdirAll
// can never create its parent), and the working directory is switched to a
// deleted directory so the ./agent-keys.json fallback cannot be created either.
// Callers must not be parallel — it uses t.Chdir.
func unwritableKeysPath(t *testing.T) string {
	t.Helper()

	r := require.New(t)

	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	r.NoError(os.WriteFile(blocker, []byte("x"), 0o600))

	dead, err := os.MkdirTemp("", "agentmode-dead-*")
	r.NoError(err)
	t.Chdir(dead)
	r.NoError(os.RemoveAll(dead))

	return filepath.Join(blocker, "agent-keys.json")
}

// TestPersistIdentityDoesNotLogKeys is the core regression test for the leak:
// with the keys file writable and SP_AGENT_PRINT_KEYS unset, neither the logs
// nor stdout may carry key material.
func TestPersistIdentityDoesNotLogKeys(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	identity, encoded := newTestIdentity(t)
	keysFile := filepath.Join(t.TempDir(), "sub", "agent-keys.json")
	cfg := &config.Config{Agent: config.AgentConfig{KeysFile: keysFile}}

	var logs, stdout bytes.Buffer

	persistIdentity(context.Background(), cfg, identity, false, bufferLogger(&logs), &stdout)

	requireNoKeyMaterial(t, identity, encoded, logs.String(), "logs")
	requireNoKeyMaterial(t, identity, encoded, stdout.String(), "stdout")
	r.Empty(stdout.String(), "nothing at all should be printed without SP_AGENT_PRINT_KEYS")
	r.Contains(logs.String(), "Agent identity persisted")
	r.NotContains(logs.String(), "SP_AGENT_KEYS=")

	// The file itself must still hold the identity, at 0600.
	onDisk, err := os.ReadFile(keysFile)
	r.NoError(err)
	r.Contains(string(onDisk), identity.Ed25519PrivateKey)

	info, err := os.Stat(keysFile)
	r.NoError(err)
	r.Equal(os.FileMode(0o600), info.Mode().Perm())
}

// TestPersistIdentityUnwritableDoesNotLogKeys covers the branch that
// historically justified the log line: no file could be written anywhere. It
// must warn — pointing at the opt-in — without emitting key material.
// Not parallel: t.Chdir is incompatible with t.Parallel.
func TestPersistIdentityUnwritableDoesNotLogKeys(t *testing.T) { //nolint:paralleltest // t.Chdir
	r := require.New(t)
	identity, encoded := newTestIdentity(t)

	cfg := &config.Config{Agent: config.AgentConfig{KeysFile: unwritableKeysPath(t)}}

	var logs, stdout bytes.Buffer

	persistIdentity(context.Background(), cfg, identity, false, bufferLogger(&logs), &stdout)

	requireNoKeyMaterial(t, identity, encoded, logs.String(), "logs")
	requireNoKeyMaterial(t, identity, encoded, stdout.String(), "stdout")
	r.Empty(stdout.String())
	r.Contains(logs.String(), "Could not write the agent keys file anywhere")
	r.Contains(logs.String(), "SP_AGENT_PRINT_KEYS")
}

// TestPersistIdentityFromEnvDoesNotLogKeys covers the SP_AGENT_KEYS path, where
// no file is written at all.
func TestPersistIdentityFromEnvDoesNotLogKeys(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	identity, encoded := newTestIdentity(t)
	cfg := &config.Config{Agent: config.AgentConfig{KeysFile: filepath.Join(t.TempDir(), "keys.json")}}

	var logs, stdout bytes.Buffer

	persistIdentity(context.Background(), cfg, identity, true, bufferLogger(&logs), &stdout)

	requireNoKeyMaterial(t, identity, encoded, logs.String(), "logs")
	requireNoKeyMaterial(t, identity, encoded, stdout.String(), "stdout")
	r.Empty(stdout.String())
	r.NoFileExists(cfg.Agent.KeysFile)
}

// TestPersistIdentityPrintKeysOptIn is the POSITIVE CONTROL: with
// SP_AGENT_PRINT_KEYS the base64 must reach stdout (proving the capture
// plumbing in the negative tests actually works), inside the banner, while the
// logger still carries no key material.
func TestPersistIdentityPrintKeysOptIn(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	identity, encoded := newTestIdentity(t)
	cfg := &config.Config{
		Agent: config.AgentConfig{KeysFile: filepath.Join(t.TempDir(), "keys.json"), PrintKeys: true},
	}

	var logs, stdout bytes.Buffer

	persistIdentity(context.Background(), cfg, identity, false, bufferLogger(&logs), &stdout)

	r.Contains(stdout.String(), encoded, "positive control: the base64 must be printed when opted in")
	r.Contains(stdout.String(), "SP_AGENT_KEYS="+encoded)
	r.Equal(2, strings.Count(stdout.String(), keyMaterialBanner), "the value must be wrapped in the banner")

	// The logger warns, but carries no key material of its own.
	r.Contains(logs.String(), "SP_AGENT_PRINT_KEYS is set")
	requireNoKeyMaterial(t, identity, encoded, logs.String(), "logs")
}

// TestPersistIdentityUnwritablePrintKeysOptIn is the positive control for the
// unwritable branch, so its negative twin above cannot pass trivially.
// Not parallel: t.Chdir is incompatible with t.Parallel.
func TestPersistIdentityUnwritablePrintKeysOptIn(t *testing.T) { //nolint:paralleltest // t.Chdir
	r := require.New(t)
	identity, encoded := newTestIdentity(t)

	cfg := &config.Config{Agent: config.AgentConfig{KeysFile: unwritableKeysPath(t), PrintKeys: true}}

	var logs, stdout bytes.Buffer

	persistIdentity(context.Background(), cfg, identity, false, bufferLogger(&logs), &stdout)

	r.Contains(stdout.String(), encoded)
	requireNoKeyMaterial(t, identity, encoded, logs.String(), "logs")
}

// TestPrintIdentityKeysHonoredOnEveryStart pins Open question 1: an
// already-persisted identity (no enrollment) prints only when opted in — the
// path Run takes on a restart.
func TestPrintIdentityKeysHonoredOnEveryStart(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	identity, encoded := newTestIdentity(t)

	var offLogs, offOut bytes.Buffer

	printIdentityKeys(context.Background(), &config.Config{}, identity, bufferLogger(&offLogs), &offOut)
	r.Empty(offOut.String())
	requireNoKeyMaterial(t, identity, encoded, offLogs.String(), "logs")

	var onLogs, onOut bytes.Buffer

	cfg := &config.Config{Agent: config.AgentConfig{PrintKeys: true}}
	printIdentityKeys(context.Background(), cfg, identity, bufferLogger(&onLogs), &onOut)
	r.Contains(onOut.String(), encoded)
	requireNoKeyMaterial(t, identity, encoded, onLogs.String(), "logs")
}

// TestPersistIdentityWritesNothingToRealStdout guards against a stray
// fmt.Print* that would bypass the injected writer entirely: it runs the real
// wiring (out = os.Stdout) with the real process stdout redirected to a pipe.
// Not parallel: it swaps the process-wide os.Stdout.
func TestPersistIdentityWritesNothingToRealStdout(t *testing.T) { //nolint:paralleltest // swaps os.Stdout
	r := require.New(t)
	identity, encoded := newTestIdentity(t)
	cfg := &config.Config{Agent: config.AgentConfig{KeysFile: filepath.Join(t.TempDir(), "keys.json")}}

	readEnd, writeEnd, err := os.Pipe()
	r.NoError(err)

	orig := os.Stdout
	os.Stdout = writeEnd

	var logs bytes.Buffer

	persistIdentity(context.Background(), cfg, identity, false, bufferLogger(&logs), os.Stdout)

	os.Stdout = orig
	r.NoError(writeEnd.Close())

	captured, err := io.ReadAll(readEnd)
	r.NoError(err)
	r.NoError(readEnd.Close())

	requireNoKeyMaterial(t, identity, encoded, string(captured), "process stdout")
	r.Empty(string(captured))
	requireNoKeyMaterial(t, identity, encoded, logs.String(), "logs")
}
