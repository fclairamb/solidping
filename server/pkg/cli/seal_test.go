package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

// runSealCommand runs `seal` under a root carrying the global flags, with
// stdin and a captured stdout.
func runSealCommand(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer

	seal := SealCommand()
	seal.Action = func(ctx context.Context, cmd *cli.Command) error {
		return runSeal(ctx, cmd, strings.NewReader(stdin), &out)
	}

	root := &cli.Command{Name: "solidping", Flags: GetGlobalFlags(), Commands: []*cli.Command{seal}}
	argv := make([]string, 0, 6+len(args))
	argv = append(argv, "solidping", "--config", filepath.Join(t.TempDir(), "settings.json"), "--org", "acme", "seal")

	err := root.Run(t.Context(), append(argv, args...))

	return out.String(), err
}

func TestSealCommand_SealsForTheServerRecipients(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	identity, recipient, err := credentials.GenerateX25519Keypair()
	r.NoError(err)

	var gotPath, gotQuery, gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		gotPath, gotQuery, gotAuth = req.URL.Path, req.URL.Query().Get("region"), req.Header.Get("Authorization")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"data": []map[string]string{{"recipient": recipient, "fingerprint": credentials.RecipientFingerprint(recipient)}},
		})
	}))
	t.Cleanup(server.Close)

	out, err := runSealCommand(t, `{"password":"hunter2","token":"t0k"}`,
		"--server", server.URL, "--region", "@dc1", "--token", "pat-1")
	r.NoError(err)

	r.Equal("/api/v1/orgs/acme/sealing-recipients", gotPath)
	r.Equal("@dc1", gotQuery)
	r.Equal("Bearer pat-1", gotAuth)

	var sealed SealedSecrets
	r.NoError(json.Unmarshal([]byte(out), &sealed))
	r.Equal([]string{"password", "token"}, sealed.Keys)
	r.Equal([]string{credentials.RecipientFingerprint(recipient)}, sealed.Fingerprints)
	r.NoError(credentials.ValidateSealedCiphertext(sealed.CT, 1))

	envelope, err := credentials.BuildClientSealedEnvelope(sealed.Fingerprints, sealed.CT)
	r.NoError(err)

	secrets, err := credentials.UnsealWithIdentity(identity, envelope)
	r.NoError(err)
	r.Equal("hunter2", secrets["password"])
}

func TestSealCommand_Refusals(t *testing.T) {
	t.Parallel()

	empty := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(empty.Close)

	refused := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(refused.Close)

	cases := []struct {
		name  string
		stdin string
		args  []string
		want  error
	}{
		{"no region", `{"password":"x"}`, []string{"--server", empty.URL, "--token", "p"}, ErrSealNoRegion},
		{"not an object", `"x"`, []string{"--server", empty.URL, "--token", "p", "--region", "@dc1"}, ErrSealNoSecrets},
		{"empty object", `{}`, []string{"--server", empty.URL, "--token", "p", "--region", "@dc1"}, ErrSealNoSecrets},
		{
			"no recipients", `{"password":"x"}`,
			[]string{"--server", empty.URL, "--token", "p", "--region", "@dc1"},
			ErrSealNoRecipients,
		},
		{
			"server refusal", `{"password":"x"}`,
			[]string{"--server", refused.URL, "--token", "p", "--region", "@x"},
			ErrSealServerRefused,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, err := runSealCommand(t, tc.stdin, tc.args...)
			require.ErrorIs(t, err, tc.want)
			require.Empty(t, out)
		})
	}
}

func TestRegionKeyGenerate_PrintsMatchingRecipientAndFingerprint(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var out bytes.Buffer
	r.NoError(writeRegionKey(&out))

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	r.Len(lines, 3)

	recipient, ok := strings.CutPrefix(lines[0], "# public key: ")
	r.True(ok)
	fingerprint, ok := strings.CutPrefix(lines[1], "# fingerprint: ")
	r.True(ok)
	identity := lines[2]

	r.True(strings.HasPrefix(identity, "AGE-SECRET-KEY-1"))

	derived, err := credentials.X25519RecipientOf(identity)
	r.NoError(err)
	r.Equal(recipient, derived)
	r.Equal(credentials.RecipientFingerprint(recipient), fingerprint)

	// Two runs never print the same key.
	var again bytes.Buffer
	r.NoError(writeRegionKey(&again))
	r.NotEqual(out.String(), again.String())
}

func recipientServer(t *testing.T, recipients ...string) *httptest.Server {
	t.Helper()

	entries := make([]map[string]string, 0, len(recipients))
	for _, recipient := range recipients {
		entries = append(entries, map[string]string{"recipient": recipient})
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{"data": entries})
	}))
	t.Cleanup(server.Close)

	return server
}

func TestSealSecrets_Refusals(t *testing.T) {
	t.Parallel()

	_, err := SealSecrets([]string{"age1x"}, nil)
	require.ErrorIs(t, err, ErrSealNoSecrets)

	_, err = SealSecrets([]string{"not-a-recipient"}, map[string]any{"password": "x"})
	require.Error(t, err)
}

func TestSealCommand_ActionWithoutRegion(t *testing.T) {
	t.Parallel()

	root := &cli.Command{Name: "solidping", Flags: GetGlobalFlags(), Commands: []*cli.Command{SealCommand()}}
	err := root.Run(t.Context(), []string{"solidping", "seal"})
	require.ErrorIs(t, err, ErrSealNoRegion)
}

func TestSealCommand_ServerFromTheGlobalURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	_, recipient, err := credentials.GenerateX25519Keypair()
	r.NoError(err)

	server := recipientServer(t, recipient)

	out, err := runSealCommand(t, `{"password":"x"}`, "--url", server.URL, "--region", "@dc1", "--token", "p")
	r.NoError(err)
	r.Contains(out, `"fingerprints"`)
}

func TestSealCommand_NoToken(t *testing.T) {
	// Not parallel: the saved login lives under $HOME.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SP_TOKEN", "")

	denied := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(denied.Close)

	_, err := runSealCommand(t, `{"password":"x"}`, "--url", denied.URL, "--region", "@dc1")
	require.ErrorIs(t, err, ErrSealNoToken)
}

func TestSealCommand_SealFailsOnBadRecipient(t *testing.T) {
	t.Parallel()

	server := recipientServer(t, "not-a-recipient")

	out, err := runSealCommand(t, `{"password":"x"}`, "--server", server.URL, "--region", "@dc1", "--token", "p")
	require.Error(t, err)
	require.Empty(t, out)
}

type failingReader struct{}

var errReadFailed = errors.New("read failed")

func (failingReader) Read([]byte) (int, error) { return 0, errReadFailed }

func TestReadSecrets_Errors(t *testing.T) {
	t.Parallel()

	_, err := readSecrets(failingReader{})
	require.ErrorIs(t, err, errReadFailed)

	_, err = readSecrets(strings.NewReader(`{"a":"` + strings.Repeat("x", maxSealInputBytes) + `"}`))
	require.ErrorIs(t, err, ErrSealNoSecrets)
	require.Contains(t, err.Error(), "64 KiB")
}

func TestFetchRecipients_TransportErrors(t *testing.T) {
	t.Parallel()

	// A URL that cannot be built.
	_, err := fetchRecipients(t.Context(), "http://bad host", "acme", "p", []string{"@dc1"})
	require.ErrorContains(t, err, "build recipients request")

	// A server that is gone.
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()

	_, err = fetchRecipients(t.Context(), gone.URL, "acme", "p", []string{"@dc1"})
	require.ErrorContains(t, err, "fetch recipients")

	// A body that stops before its announced length.
	truncated := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Length", "100")
		_, _ = writer.Write([]byte(`{"data":`))

		hijacker, ok := writer.(http.Hijacker)
		if !ok {
			return
		}

		if conn, _, hijackErr := hijacker.Hijack(); hijackErr == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(truncated.Close)

	_, err = fetchRecipients(t.Context(), truncated.URL, "acme", "p", []string{"@dc1"})
	require.ErrorContains(t, err, "read recipients")

	// A body that is not the expected JSON.
	garbage := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`<html>`))
	}))
	t.Cleanup(garbage.Close)

	_, err = fetchRecipients(t.Context(), garbage.URL, "acme", "p", []string{"@dc1"})
	require.ErrorContains(t, err, "decode recipients")
}

func TestAgentCommand_RegionKeyGenerate(t *testing.T) {
	t.Parallel()

	cmd := AgentCommand()
	require.Equal(t, "agent", cmd.Name)

	generate := cmd.Commands[0].Commands[0]
	require.Equal(t, "generate", generate.Name)
	require.NoError(t, generate.Action(t.Context(), generate))
}

func TestSealCommand_NoHomeDirectory(t *testing.T) {
	// Not parallel: it unsets $HOME, where the token file lives.
	t.Setenv("HOME", "")

	_, err := runSealCommand(t, `{"password":"x"}`, "--server", "http://127.0.0.1:1", "--region", "@dc1")
	require.ErrorContains(t, err, "token path")
}
