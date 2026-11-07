package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/internal/version"
)

// runVersion runs the sp tree with args after a --config pointing at a
// missing file, so the developer's own settings.json never leaks in, and
// returns what the command printed.
func runVersion(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer

	root := &cli.Command{
		Name:           "sp",
		Flags:          GetGlobalFlags(),
		Commands:       GetCommands(),
		Writer:         &out,
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
	}

	argv := append([]string{"sp", "--config", filepath.Join(t.TempDir(), "settings.json")}, args...)
	err := root.Run(context.Background(), argv)

	return out.String(), err
}

func TestVersionClientOnly(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// An unreachable URL proves --client never contacts the server.
	out, err := runVersion(t, "--url", "http://127.0.0.1:1", "version", "--client")
	r.NoError(err)
	r.Contains(out, "Client:")
	r.Contains(out, "Version:   "+version.Get().Version)
	r.NotContains(out, "Server")
}

func TestVersionReportsClientAndServer(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/mgmt/version" {
			http.NotFound(w, req)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"0.43.0","commit":"abc1234","gitTime":"2026-10-31T10:30:01Z","uptimeSeconds":5}`))
	}))
	t.Cleanup(srv.Close)

	out, err := runVersion(t, "--url", srv.URL, "-o", "json", "version")
	r.NoError(err)

	var report versionReport
	r.NoError(json.Unmarshal([]byte(out), &report))
	r.Equal(localBuildInfo(), report.Client)
	r.Equal(&serverVersion{
		URL:     srv.URL,
		Version: "0.43.0",
		Commit:  "abc1234",
		GitTime: "2026-10-31T10:30:01Z",
	}, report.Server)

	text, err := runVersion(t, "--url", srv.URL, "version")
	r.NoError(err)
	r.Contains(text, "Server ("+srv.URL+"):\n  Version:   0.43.0\n  Commit:    abc1234\n")
}

// An unreachable server still prints the client half, then fails the command.
func TestVersionUnreachableServer(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	out, err := runVersion(t, "--url", "http://127.0.0.1:1", "version")
	r.Error(err)
	r.Contains(out, "Client:\n  Version:   "+version.Get().Version)
	r.Contains(out, "Failed to get server version")

	out, err = runVersion(t, "--url", "http://127.0.0.1:1", "--json", "version")
	r.Error(err)

	var report versionReport
	r.NoError(json.Unmarshal([]byte(out), &report))
	r.Equal(localBuildInfo(), report.Client)
	r.NotNil(report.Server)
	r.Equal("http://127.0.0.1:1", report.Server.URL)
	r.NotEmpty(report.Server.Error)
	r.Empty(report.Server.Version)
}
