package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/urfave/cli/v3"
)

// TestAuthSwitchOrgWithPATPrintsGuidance: on a PAT login, `sp auth switch-org`
// does not call the server's switch-org (which answers 403 PAT_FORBIDDEN, spec
// 2026-10-28-01) and tells the user what to do instead.
func TestAuthSwitchOrgWithPATPrintsGuidance(t *testing.T) {
	var switchCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/api/v1/auth/me":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user":         map[string]any{"uid": smokeUID, "email": "alice@acme.com"},
				"organization": map[string]any{"uid": smokeUID, "slug": "acme", "name": "Acme"},
			})
		case "/api/v1/auth/switch-org":
			switchCalls.Add(1)
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "PAT_FORBIDDEN"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	t.Chdir(t.TempDir())
	t.Setenv("HOME", home)

	cfgDir := filepath.Join(home, ".config", "solidping")
	if err := os.MkdirAll(cfgDir, 0o750); err != nil {
		t.Fatal(err)
	}

	cfg := `{"url":"` + srv.URL + `","org":"acme","auth":{"pat":"pat_test"}}`
	if err := os.WriteFile(filepath.Join(cfgDir, "settings.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(t.TempDir(), "stdout")

	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}

	origOut := os.Stdout
	os.Stdout = out

	root := &cli.Command{
		Name: "sp", Flags: GetGlobalFlags(), Commands: GetCommands(),
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	runErr := root.Run(ctx, []string{"sp", "auth", "switch-org", "globex"})

	cancel()

	os.Stdout = origOut
	_ = out.Close()

	if runErr == nil {
		t.Fatal("switch-org with a PAT must fail")
	}

	printed, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"this token is bound to org acme",
		"create a token in org globex",
		"sp auth login",
	} {
		if !strings.Contains(string(printed), want) {
			t.Errorf("output %q does not contain %q", printed, want)
		}
	}

	if n := switchCalls.Load(); n != 0 {
		t.Errorf("switch-org was called %d times with a PAT", n)
	}
}
