package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v3"
)

var coverUIDSuffix = regexp.MustCompile(`/[0-9a-f]{8}-[0-9a-f-]{27}$`)

// coverItem is a rich object carrying the fields most API resources expose.
const coverItem = `{"uid":"` + smokeUID + `","slug":"acme","name":"acme","type":"http","state":"up",` +
	`"status":"running","enabled":true,"period":"00:01:00","createdAt":"2026-01-01T00:00:00Z",` +
	`"updatedAt":"2026-01-02T00:00:00Z","scheduledAt":"2026-01-02T00:00:00Z","startedAt":"2026-01-02T00:00:00Z",` +
	`"completedAt":"2026-01-02T00:00:00Z","email":"alice@acme.com","role":"admin","title":"acme",` +
	`"description":"acme","url":"https://acme.com","labels":{"a":"b"},"retryCount":2,` +
	`"organizationUid":"` + smokeUID + `","checkUid":"` + smokeUID + `","checkName":"acme","region":"eu",` +
	`"periodSeconds":60,"leaseStarts":1,"encrypted":true,"duration":120,"relapseCount":1,` +
	`"startsAt":"2026-01-01T00:00:00Z","endsAt":"2026-01-01T01:00:00Z","firstSeenAt":"2026-01-01T00:00:00Z",` +
	`"resolvedAt":"2026-01-01T02:00:00Z","failureCount":3,"message":"acme","visibility":"public"}`

func coverListBody() string {
	return `{"status":"ok","version":"1.0.0","data":[` + coverItem + `,` + coverItem + `],"pagination":{"cursor":"c","total":2},"total":2}`
}

func coverObjBody(dataAsObject bool) string {
	if dataAsObject {
		return `{"data":` + coverItem + `}`
	}

	return coverItem[:len(coverItem)-1] + `,"data":[` + coverItem + `]}`
}

// runRouted drives every leaf command against a fake server whose body shape
// depends on whether the request path targets a single resource.
//
//nolint:paralleltest // process-global stdio and HOME
func runRouted(t *testing.T, format string, allFlags bool, dataAsObject bool) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := coverListBody()
		if coverUIDSuffix.MatchString(r.URL.Path) || r.Method == http.MethodPost || r.Method == http.MethodPut ||
			r.Method == http.MethodPatch {
			body = coverObjBody(dataAsObject)
		}
		status := http.StatusOK
		if r.Method == http.MethodPost {
			status = http.StatusCreated
		}
		if r.Method == http.MethodDelete {
			status = http.StatusNoContent
			body = ""
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
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

	var leaves []smokeLeaf
	collectLeaves(nil, GetCommands(), &leaves)
	skip := smokeSkip()
	delete(skip, "server")
	for _, leaf := range leaves {
		if skip[strings.Join(leaf.path, " ")] {
			continue
		}
		root := &cli.Command{
			Name: "sp", Flags: GetGlobalFlags(), Commands: GetCommands(),
			ExitErrHandler: func(context.Context, *cli.Command, error) {},
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		args := append([]string{"sp", "-o", format}, coverArgs(leaf, allFlags)...)
		_ = root.Run(ctx, args)
		cancel()
	}
}

// coverArgs is like smokeArgs but also feeds string-slice and (safe) bool flags.
func coverArgs(leaf smokeLeaf, allFlags bool) []string {
	args := smokeArgs(leaf, allFlags)
	if !allFlags {
		return args
	}
	extra := []string{}
	for _, f := range leaf.cmd.Flags {
		names := f.Names()
		if len(names) == 0 {
			continue
		}
		switch ff := f.(type) {
		case *cli.StringSliceFlag:
			if !ff.Required {
				extra = append(extra, "--"+names[0], smokeUID)
			}
		case *cli.BoolFlag:
			switch names[0] {
			case "watch", "follow", "wait", "interactive", "yes", "dry-run", "help":
			default:
				if !ff.Required {
					extra = append(extra, "--"+names[0])
				}
			}
		}
	}
	// flags must precede positionals: insert right after the command path.
	n := len(leaf.path)
	out := append([]string{}, args[:n]...)
	out = append(out, extra...)
	return append(out, args[n:]...)
}

//nolint:paralleltest // process-global stdio and HOME
func TestCommandsRoutedCover(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		for _, asObj := range []bool{true, false} {
			for _, all := range []bool{false, true} {
				name := format
				if asObj {
					name += "-obj"
				}
				if all {
					name += "-all"
				}
				//nolint:paralleltest // process-global stdio and HOME
				t.Run(name, func(t *testing.T) { runRouted(t, format, all, asObj) })
			}
		}
	}
}
