package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v3"
)

// smokeSkip lists leaf commands that legitimately block (interactive login,
// stdio bridges, long-running watchers) and cannot be driven by a smoke run.
var smokeSkip = map[string]bool{
	"auth login":  true,
	"mcp":         true,
	"mcp bridge":  true,
	"server":      true,
	"agent":       true,
	"completion":  true,
	"checks edit": true,
}

const smokeUID = "00000000-0000-0000-0000-000000000001"

type smokeLeaf struct {
	path []string
	cmd  *cli.Command
}

func collectLeaves(prefix []string, cmds []*cli.Command, out *[]smokeLeaf) {
	for _, c := range cmds {
		path := append(append([]string{}, prefix...), c.Name)
		if len(c.Commands) > 0 {
			collectLeaves(path, c.Commands, out)

			continue
		}

		if c.Action != nil {
			*out = append(*out, smokeLeaf{path: path, cmd: c})
		}
	}
}

// smokeArgs builds a plausible argument vector for a leaf: every required
// flag gets a value, and a few positional args are supplied.
func smokeArgs(leaf smokeLeaf) []string {
	args := append([]string{}, leaf.path...)

	for _, f := range leaf.cmd.Flags {
		names := f.Names()
		if len(names) == 0 {
			continue
		}

		switch ff := f.(type) {
		case *cli.StringFlag:
			if ff.Required {
				args = append(args, "--"+names[0], smokeUID)
			}
		case *cli.IntFlag:
			if ff.Required {
				args = append(args, "--"+names[0], "1")
			}
		case *cli.StringSliceFlag:
			if ff.Required {
				args = append(args, "--"+names[0], smokeUID)
			}
		case *cli.BoolFlag:
			if ff.Required {
				args = append(args, "--"+names[0])
			}
		}
	}

	return append(args, smokeUID, smokeUID, smokeUID)
}

func runSmoke(t *testing.T, status int, body string, format string) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".config", "solidping")
	if err := os.MkdirAll(cfgDir, 0o750); err != nil {
		t.Fatal(err)
	}

	cfg := `{"url":"` + srv.URL + `","org":"acme","auth":{"pat":"sp_pat_test"}}`
	if err := os.WriteFile(filepath.Join(cfgDir, "settings.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()

	origOut, origErr, origIn := os.Stdout, os.Stderr, os.Stdin
	os.Stdout, os.Stderr, os.Stdin = devNull, devNull, devNull
	defer func() { os.Stdout, os.Stderr, os.Stdin = origOut, origErr, origIn }()

	var leaves []smokeLeaf
	collectLeaves(nil, GetCommands(), &leaves)

	for _, leaf := range leaves {
		if smokeSkip[strings.Join(leaf.path, " ")] || smokeSkip[leaf.path[0]] {
			continue
		}

		root := &cli.Command{
			Name:           "sp",
			Flags:          GetGlobalFlags(),
			Commands:       GetCommands(),
			ExitErrHandler: func(context.Context, *cli.Command, error) {},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		args := append([]string{"sp", "-o", format}, smokeArgs(leaf)...)
		// Errors are expected (dummy args, error statuses); the point is to
		// exercise every action end to end without panicking or hanging.
		_ = root.Run(ctx, args)

		cancel()
	}
}

// TestCommandsSmoke drives every leaf command of the real tree against a
// fake server, on the success path (text and JSON output) and on the
// server-error path, and requires that none of them panics or hangs.
func TestCommandsSmoke(t *testing.T) {
	okBody := `{"data":[],"uid":"` + smokeUID + `","slug":"x","name":"x","items":[],"checks":[]}`

	t.Run("ok text", func(t *testing.T) { runSmoke(t, http.StatusOK, okBody, "text") })
	item := `{"uid":"` + smokeUID + `","slug":"x","name":"x","type":"http","state":"up","enabled":true,` +
		`"period":"00:01:00","status":"up","createdAt":"2026-01-01T00:00:00Z","email":"a@acme.com",` +
		`"role":"admin","title":"t","url":"https://acme.com","config":{},"labels":{"a":"b"}}`
	populated := `{"data":[` + item + `,` + item + `],"pagination":{"cursor":"c","total":2},` + item[:len(item)-1] + `}`

	t.Run("populated text", func(t *testing.T) { runSmoke(t, http.StatusOK, populated, "text") })
	t.Run("populated json", func(t *testing.T) { runSmoke(t, http.StatusOK, populated, "jsonl") })
	t.Run("ok json", func(t *testing.T) { runSmoke(t, http.StatusOK, okBody, "json") })
	t.Run("created json", func(t *testing.T) { runSmoke(t, http.StatusCreated, okBody, "json") })
	t.Run("server error text", func(t *testing.T) {
		runSmoke(t, http.StatusInternalServerError, `{"title":"boom","code":"INTERNAL_ERROR"}`, "text")
	})
	t.Run("server error json", func(t *testing.T) {
		runSmoke(t, http.StatusNotFound, `{"title":"nope","code":"NOT_FOUND"}`, "json")
	})
}
