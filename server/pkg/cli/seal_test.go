package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
		if req.URL.Path == "/api/v1/orgs/acme/check-types" {
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"data": []map[string]any{{"type": "http", "targetFields": []string{"tunnelCheckUid", "url"}}},
			})

			return
		}

		gotPath, gotQuery, gotAuth = req.URL.Path, req.URL.Query().Get("region"), req.Header.Get("Authorization")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"data": []map[string]string{{"recipient": recipient, "fingerprint": credentials.RecipientFingerprint(recipient)}},
		})
	}))
	t.Cleanup(server.Close)

	out, err := runSealCommand(t, `{"password":"hunter2","token":"t0k"}`,
		"--server", server.URL, "--region", "@dc1", "--token", "pat-1",
		"--type", "http", "--config-json", `{"url":"https://DB.acme.com/x","timeout":"5s"}`)
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

	// The binding (spec 2026-11-11-01): the type and the digest of the target.
	r.True(sealed.Bound)
	r.Equal(map[string]any{
		"type": "http",
		"digest": credentials.TargetDigest(
			[]string{"tunnelCheckUid", "url"}, map[string]any{"url": "https://db.acme.com:443/x"}),
	}, secrets[credentials.BindingKey])
}

// sealArgs are the flags every refusal case shares but --region.
func sealArgs(server string) []string {
	return []string{"--server", server, "--token", "p", "--type", "http", "--config-json", `{"url":"https://a.acme.com"}`}
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
		{
			"no type", `{"password":"x"}`,
			[]string{"--server", empty.URL, "--token", "p", "--region", "@dc1", "--config-json", "{}"},
			ErrSealNoType,
		},
		{
			"no config", `{"password":"x"}`,
			[]string{"--server", empty.URL, "--token", "p", "--region", "@dc1", "--type", "http"},
			ErrSealBadConfig,
		},
		{
			"config not an object", `{"password":"x"}`,
			[]string{"--server", empty.URL, "--token", "p", "--region", "@dc1", "--type", "http", "--config-json", "[1]"},
			ErrSealBadConfig,
		},
		{"not an object", `"x"`, append(sealArgs(empty.URL), "--region", "@dc1"), ErrSealNoSecrets},
		{"empty object", `{}`, append(sealArgs(empty.URL), "--region", "@dc1"), ErrSealNoSecrets},
		{"reserved key", `{"_binding":{}}`, append(sealArgs(empty.URL), "--region", "@dc1"), ErrSealNoSecrets},
		{"no recipients", `{"password":"x"}`, append(sealArgs(empty.URL), "--region", "@dc1"), ErrSealNoRecipients},
		{"server refusal", `{"password":"x"}`, append(sealArgs(refused.URL), "--region", "@x"), ErrSealServerRefused},
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
