package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

// sealServer serves the two endpoints `seal` reads, with raw bodies.
func sealServer(t *testing.T, checkTypes, recipients string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/check-types") {
			_, _ = writer.Write([]byte(checkTypes))

			return
		}

		_, _ = writer.Write([]byte(recipients))
	}))
	t.Cleanup(server.Close)

	return server
}

func TestSealCommand_CheckTypeLookup(t *testing.T) {
	t.Parallel()

	_, recipient, err := credentials.GenerateX25519Keypair()
	require.NoError(t, err)

	good := `{"data":[{"recipient":"` + recipient + `"}]}`

	cases := []struct {
		name       string
		checkTypes string
		wantErr    error
		wantFields []string
	}{
		{"unknown type", `{"data":[{"type":"dns","targetFields":["host"]}]}`, ErrSealUnknownType, nil},
		{"type without declared fields", `{"data":[{"type":"http"}]}`, nil, credentials.TargetFieldsFor(nil)},
		{"garbage check types", `not json`, nil, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := sealServer(t, tc.checkTypes, good)
			out, runErr := runSealCommand(t, `{"password":"x"}`, append(sealArgs(server.URL), "--region", "@dc1")...)

			switch {
			case tc.wantErr != nil:
				require.ErrorIs(t, runErr, tc.wantErr)
			case tc.wantFields == nil:
				require.ErrorContains(t, runErr, "decode check types")
			default:
				require.NoError(t, runErr)
				require.Contains(t, out, `"bound": true`)
			}
		})
	}
}

func TestSealCommand_GarbageRecipients(t *testing.T) {
	t.Parallel()

	server := sealServer(t, `{"data":[{"type":"http","targetFields":["url"]}]}`, `not json`)
	_, err := runSealCommand(t, `{"password":"x"}`, append(sealArgs(server.URL), "--region", "@dc1")...)
	require.ErrorContains(t, err, "decode recipients")
}

func TestSealCommand_ServerUnreachable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()

	_, err := runSealCommand(t, `{"password":"x"}`, append(sealArgs(url), "--region", "@dc1")...)
	require.ErrorContains(t, err, "request")
}

func TestSealCommand_StdinTooLarge(t *testing.T) {
	t.Parallel()

	big := `{"password":"` + strings.Repeat("x", maxSealInputBytes) + `"}`
	_, err := runSealCommand(t, big, append(sealArgs("http://127.0.0.1:1"), "--region", "@dc1")...)
	require.ErrorIs(t, err, ErrSealNoSecrets)
}

func TestSealCommand_ConfigFromFile(t *testing.T) {
	t.Parallel()

	_, recipient, err := credentials.GenerateX25519Keypair()
	require.NoError(t, err)

	server := sealServer(t, `{"data":[{"type":"http","targetFields":["url"]}]}`,
		`{"data":[{"recipient":"`+recipient+`"}]}`)

	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"url":"https://a.acme.com"}`), 0o600))

	args := []string{
		"--server", server.URL, "--token", "p", "--type", "http", "--config-json", "@" + path, "--region", "@dc1",
	}

	out, err := runSealCommand(t, `{"password":"x"}`, args...)
	require.NoError(t, err)

	var sealed SealedSecrets
	require.NoError(t, json.Unmarshal([]byte(out), &sealed))
	require.True(t, sealed.Bound)

	args[7] = "@" + filepath.Join(t.TempDir(), "missing.json")
	_, err = runSealCommand(t, `{"password":"x"}`, args...)
	require.ErrorContains(t, err, "read --config-json file")
}

func TestSealSecrets_Refusals(t *testing.T) {
	t.Parallel()

	_, recipient, err := credentials.GenerateX25519Keypair()
	require.NoError(t, err)

	_, err = SealSecrets([]string{recipient}, nil, nil)
	require.ErrorIs(t, err, ErrSealNoSecrets)

	_, err = SealSecrets([]string{recipient}, map[string]any{credentials.BindingKey: "x"}, nil)
	require.ErrorIs(t, err, ErrSealNoSecrets)

	_, err = SealSecrets([]string{"not-a-recipient"}, map[string]any{"password": "x"}, nil)
	require.Error(t, err)

	sealed, err := SealSecrets([]string{recipient}, map[string]any{"password": "x"}, nil)
	require.NoError(t, err)
	require.False(t, sealed.Bound)
}
