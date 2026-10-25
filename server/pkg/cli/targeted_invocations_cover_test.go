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

// targetedInvocations are realistic argument lists for commands whose
// success paths the generic smoke sweep cannot reach: a UID is not an email,
// a duration or a JSON settings blob, so the sweep stops at validation.
func targetedInvocations() [][]string {
	uid := smokeUID

	return [][]string{
		{"members", "add", "--role", "admin", "alice@acme.com"},
		{"members", "add"},
		{"invitations", "create", "--email", "bob@acme.com", "--role", "user", "--expires-in", "7d", "--app", "dash"},
		{"tokens", "create", "--name", "ci", "--expires", "7d"},
		{"tokens", "create", "--name", "ci", "--expires", "30d"},
		{"tokens", "create", "--name", "ci", "--expires", "90d"},
		{"tokens", "create", "--name", "ci", "--expires", "1y"},
		{"tokens", "create", "--name", "ci", "--expires", "2w"},
		{"tokens", "create", "--name", "ci", "--expires", "never"},
		{"tokens", "create"},
		{"incidents", "snooze", "--duration", "1h", "--until", "2026-01-01T00:00:00Z", "--reason", "deploy", uid},
		{"incidents", "snooze", "--until", "tomorrow", uid},
		{
			"channels", "create", "--type", "webhook", "--name", "ops", "--settings", `{"url":"https://acme.com/hook"}`,
			"--disabled", "--default",
		},
		{"channels", "create", "--type", "webhook"},
		{"channels", "create", "--type", "webhook", "--name", "ops", "--settings", "{"},
		{"channels", "update", "--name", "ops", "--enabled", "--default", "--settings", `{"url":"https://acme.com"}`, uid},
		{"channels", "update", "--disabled", "--no-default", uid},
		{"channels", "update", "--settings", "{", uid},
		{
			"checks", "add", "--number", "3", "--type", "http", "--name", "api", "--slug", "api", "--interval", "90s",
			"https://acme.com",
		},
		{"checks", "add", "--number", "2", "--interval", "00:05:00", "https://acme.com"},
		{"checks", "add", "--number", "0", "https://acme.com"},
		{"checks", "add"},
		{"notifications", "list", "--connection", uid},
		{"notifications", "list", "--user", uid, "--status", "sent", "--connection", uid},
		{"notifications", "list", "--incident", uid},
		{"notifications", "list", "--incident", "not-a-uid"},
	}
}

// runInvocations runs each argument list against a server answering handler.
func runInvocations(t *testing.T, format string, handler http.Handler, invocations [][]string) {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	home := t.TempDir()
	t.Chdir(t.TempDir())
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
	defer func() { _ = devNull.Close() }()
	origOut, origErr, origIn := os.Stdout, os.Stderr, os.Stdin
	os.Stdout, os.Stderr, os.Stdin = devNull, devNull, devNull
	defer func() { os.Stdout, os.Stderr, os.Stdin = origOut, origErr, origIn }()

	for _, args := range invocations {
		root := &cli.Command{
			Name: "sp", Flags: GetGlobalFlags(), Commands: GetCommands(),
			ExitErrHandler: func(context.Context, *cli.Command, error) {},
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = root.Run(ctx, append([]string{"sp", "-o", format}, args...))
		cancel()
	}
}

//nolint:paralleltest // process-global stdio and HOME
func TestTargetedInvocationsCover(t *testing.T) {
	routed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := coverListBody()
		if coverUIDSuffix.MatchString(r.URL.Path) || r.Method != http.MethodGet {
			body = coverObjBody(true)
		}
		status := http.StatusOK
		if r.Method == http.MethodPost {
			status = http.StatusCreated
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	failing := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"title":"invalid","code":"VALIDATION_ERROR","detail":"acme"}`))
	})

	for _, format := range []string{"text", "json"} {
		//nolint:paralleltest // process-global stdio and HOME
		t.Run(format, func(t *testing.T) { runInvocations(t, format, routed, targetedInvocations()) })
		//nolint:paralleltest // process-global stdio and HOME
		t.Run(format+"-failing", func(t *testing.T) { runInvocations(t, format, failing, targetedInvocations()) })
	}
}

// runLeavesWithHome drives every leaf command with HOME set to home and, when
// cfg is not empty, a settings file holding it.
func runLeavesWithHome(t *testing.T, home, cfg string) {
	t.Helper()

	t.Chdir(t.TempDir())
	t.Setenv("HOME", home)
	if cfg != "" {
		cfgDir := filepath.Join(home, ".config", "solidping")
		if err := os.MkdirAll(cfgDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cfgDir, "settings.json"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = devNull.Close() }()
	origOut, origErr, origIn := os.Stdout, os.Stderr, os.Stdin
	os.Stdout, os.Stderr, os.Stdin = devNull, devNull, devNull
	defer func() { os.Stdout, os.Stderr, os.Stdin = origOut, origErr, origIn }()

	var leaves []smokeLeaf
	collectLeaves(nil, GetCommands(), &leaves)
	skip := smokeSkip()
	for _, leaf := range leaves {
		if skip[strings.Join(leaf.path, " ")] {
			continue
		}
		for _, all := range []bool{false, true} {
			root := &cli.Command{
				Name: "sp", Flags: GetGlobalFlags(), Commands: GetCommands(),
				ExitErrHandler: func(context.Context, *cli.Command, error) {},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = root.Run(ctx, append([]string{"sp"}, coverArgs(leaf, all)...))
			cancel()
		}
	}
}

// TestCommandsWithoutHomeOrCredentials checks every command fails cleanly when
// the CLI cannot even locate its config (no HOME), and when it has a server
// but no credential at all.
//
//nolint:paralleltest // process-global stdio and HOME
func TestCommandsWithoutHomeOrCredentials(t *testing.T) {
	//nolint:paralleltest // process-global stdio and HOME
	t.Run("no-home", func(t *testing.T) { runLeavesWithHome(t, "", "") })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	//nolint:paralleltest // process-global stdio and HOME
	t.Run("no-credentials", func(t *testing.T) {
		runLeavesWithHome(t, t.TempDir(), `{"url":"`+srv.URL+`","org":"acme","auth":{}}`)
	})
}
