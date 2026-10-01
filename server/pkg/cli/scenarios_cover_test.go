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

type coverRoute struct {
	method string // empty matches any
	path   string // substring of the URL path
	status int
	body   string
}

// coverRun executes `sp <args>` against a fake server answering with routes.
// It returns the command error. Stdio is silenced.
//
//nolint:thelper // process-global stdio and HOME
func coverRun(t *testing.T, routes []coverRoute, args ...string) error {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for _, rt := range routes {
			if (rt.method == "" || rt.method == r.Method) && strings.Contains(r.URL.Path, rt.path) {
				w.WriteHeader(rt.status)
				_, _ = w.Write([]byte(rt.body))

				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

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
	defer func() { _ = devNull.Close() }()
	origOut, origErr, origIn := os.Stdout, os.Stderr, os.Stdin
	os.Stdout, os.Stderr, os.Stdin = devNull, devNull, devNull
	defer func() { os.Stdout, os.Stderr, os.Stdin = origOut, origErr, origIn }()

	root := &cli.Command{
		Name: "sp", Flags: GetGlobalFlags(), Commands: GetCommands(),
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return root.Run(ctx, append([]string{"sp"}, args...))
}

const (
	cuid  = smokeUID
	cuid2 = "00000000-0000-0000-0000-000000000002"
	cts   = "2026-01-01T00:00:00Z"
)

//nolint:paralleltest,lll // coverRun swaps process-global stdio and HOME; table rows mirror CLI argv
func TestCLIScenariosCover(t *testing.T) {
	edge := `{"uid":"` + cuid + `","kind":"hard","description":"d","parentCheck":{"uid":"` + cuid2 +
		`","slug":"parent","name":"Parent"},"childCheck":{"uid":"` + cuid + `","slug":"child","name":"Child"}}`
	deps := `{"data":{"dependsOn":[` + edge + `],"dependedOnBy":[]}}`
	graph := `{"data":{"nodes":[{"uid":"` + cuid + `","slug":"child"}],"edges":[{"uid":"` + cuid +
		`","childCheckUid":"` + cuid + `","parentCheckUid":"` + cuid2 + `","kind":"soft"}]}}`
	check := `{"data":{"uid":"` + cuid + `","slug":"child","name":"Child","type":"http","period":"00:01:00",` +
		`"enabled":true,"config":{"url":"https://acme.com"}}}`
	window := `{"uid":"` + cuid + `","title":"t","startAt":"` + cts + `","endAt":"` + cts +
		`","createdAt":"` + cts + `","updatedAt":"` + cts + `"}`
	schedule := `{"uid":"` + cuid + `","name":"n","timezone":"UTC","rotationType":"weekly","handoffTime":"09:00",` +
		`"startAt":"` + cts + `","createdAt":"` + cts + `","updatedAt":"` + cts + `"}`
	override := `{"uid":"` + cuid + `","userUid":"` + cuid + `","startAt":"` + cts + `","endAt":"` + cts +
		`","createdAt":"` + cts + `"}`
	resource := `{"uid":"` + cuid + `","publicName":"x","position":1,"createdAt":"` + cts + `"}`

	dir := t.TempDir()
	setYAML := filepath.Join(dir, "deps.yaml")
	setJSON := filepath.Join(dir, "deps.json")
	bad := filepath.Join(dir, "bad.json")
	for p, c := range map[string]string{
		setYAML: "dependsOn:\n  - parentSlug: parent\n    kind: hard\n",
		setJSON: `{"dependsOn":[{"parentSlug":"parent","kind":"soft","description":"x"}]}`,
		bad:     `{not json`,
	} {
		if err := os.WriteFile(p, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	singleCheck := filepath.Join(dir, "check.yaml")
	if err := os.WriteFile(singleCheck, []byte("type: http\nslug: a\nconfig:\n  url: https://acme.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := filepath.Join(dir, "doc.yaml")
	if err := os.WriteFile(doc, []byte("version: 1\nchecks:\n  - slug: a\n    type: http\n    config:\n      url: https://acme.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ok := func(body string) []coverRoute { return []coverRoute{{"", "", 200, body}} }
	created := func(body string) []coverRoute { return []coverRoute{{"", "", 201, body}} }
	depRoutes := []coverRoute{
		{"GET", "/dependencies", 200, deps},
		{"PATCH", "/dependencies/", 200, edge},
		{"DELETE", "/dependencies/", 204, ""},
		{"GET", "/checks/child", 200, check},
		{"PUT", "/checks/child", 200, `{}`},
	}

	tests := []struct {
		name   string
		routes []coverRoute
		args   []string
	}{
		{"deps list text", depRoutes, []string{"checks", "deps", "list", "child"}},
		{"deps list json", depRoutes, []string{"-o", "json", "checks", "deps", "list", "child"}},
		{"deps list empty", ok(`{"data":{"dependsOn":[]}}`), []string{"checks", "deps", "list", "child"}},
		{"deps list err", ok(`{`), []string{"checks", "deps", "list", "child"}},
		{"deps list noarg", nil, []string{"checks", "deps", "list"}},
		{"deps remove", depRoutes, []string{"checks", "deps", "remove", "child", "parent"}},
		{"deps remove notfound", depRoutes, []string{"checks", "deps", "remove", "child", "zzz"}},
		{"deps remove noarg", depRoutes, []string{"checks", "deps", "remove", "child"}},
		{"deps update kind", depRoutes, []string{"checks", "deps", "update", "child", "parent", "--kind", "soft"}},
		{"deps update json", depRoutes, []string{"-o", "json", "checks", "deps", "update", "child", "parent", "--description", "x"}},
		{"deps update bad kind", depRoutes, []string{"checks", "deps", "update", "child", "parent", "--kind", "meh"}},
		{"deps update none", depRoutes, []string{"checks", "deps", "update", "child", "parent"}},
		{"deps update missing", depRoutes, []string{"checks", "deps", "update", "child", "zzz", "--kind", "soft"}},
		{"deps update noarg", depRoutes, []string{"checks", "deps", "update", "child"}},
		{
			"deps update 500",
			[]coverRoute{{"GET", "/dependencies", 200, deps}, {"PATCH", "", 500, `{"title":"x"}`}},
			[]string{"checks", "deps", "update", "child", "parent", "--kind", "soft"},
		},
		{"deps graph", ok(graph), []string{"checks", "deps", "graph"}},
		{"deps graph json", ok(graph), []string{"-o", "json", "checks", "deps", "graph"}},
		{"deps graph empty", ok(`{"data":{"edges":[]}}`), []string{"checks", "deps", "graph"}},
		{"deps graph 500", []coverRoute{{"", "", 500, `{}`}}, []string{"checks", "deps", "graph"}},
		{"deps set yaml", depRoutes, []string{"checks", "deps", "set", "child", "--from", setYAML}},
		{"deps set json", depRoutes, []string{"checks", "deps", "set", "child", "--from", setJSON}},
		{"deps set bad", depRoutes, []string{"checks", "deps", "set", "child", "--from", bad}},
		{"deps set missing file", depRoutes, []string{"checks", "deps", "set", "child", "--from", filepath.Join(dir, "none")}},
		{
			"deps set put 500",
			[]coverRoute{{"GET", "/checks/child", 200, check}, {"PUT", "", 500, `{}`}},
			[]string{"checks", "deps", "set", "child", "--from", setYAML},
		},
		{
			"deps set put 400",
			[]coverRoute{{"GET", "/checks/child", 200, check}, {"PUT", "", 204, ``}},
			[]string{"checks", "deps", "set", "child", "--from", setYAML},
		},
		{
			"deps set nocheck",
			[]coverRoute{{"GET", "/checks/child", 404, `{}`}},
			[]string{"checks", "deps", "set", "child", "--from", setYAML},
		},
		{"deps set noarg", nil, []string{"checks", "deps", "set", "--from", setYAML}},

		{"validate single", ok(`{"valid":true}`), []string{"checks", "validate", singleCheck}},
		{"validate single json", ok(`{"valid":true}`), []string{"-o", "json", "checks", "validate", singleCheck}},
		{"validate invalid", ok(`{"valid":false,"fields":[{"name":"a","message":"b"}]}`), []string{"checks", "validate", singleCheck}},
		{"validate bad resp", ok(`nope`), []string{"checks", "validate", singleCheck}},
		{"validate 500", []coverRoute{{"", "", 500, `{}`}}, []string{"checks", "validate", singleCheck}},
		{"validate doc", nil, []string{"checks", "validate", doc}},
		{"validate doc json", nil, []string{"-o", "json", "checks", "validate", doc}},
		{"validate missing", nil, []string{"checks", "validate", filepath.Join(dir, "none")}},

		{"maint create", created(window), []string{
			"maintenance-windows", "create", "--title", "t", "--start", cts, "--end", "2026-01-01T01:00:00Z",
			"--description", "d", "--recurrence", "weekly", "--recurrence-end", "2026-06-01T00:00:00Z",
		}},
		{"maint create json", created(window), []string{"-o", "json", "maintenance-windows", "create", "--title", "t", "--start", cts, "--end", cts}},
		{"maint create missing", nil, []string{"maintenance-windows", "create", "--title", "t"}},
		{"maint create bad time", nil, []string{"maintenance-windows", "create", "--title", "t", "--start", "zz", "--end", cts}},
		{"maint create 500", []coverRoute{{"", "", 500, `{}`}}, []string{"maintenance-windows", "create", "--title", "t", "--start", cts, "--end", cts}},

		{"oncall create", created(schedule), []string{
			"oncall", "create", "--name", "n", "--timezone", "UTC", "--rotation-type", "weekly",
			"--handoff-time", "09:00", "--user", cuid, "--start", cts, "--description", "d", "--handoff-weekday", "1",
		}},
		{"oncall create json", created(schedule), []string{"-o", "json", "oncall", "create", "--name", "n", "--timezone", "UTC", "--rotation-type", "daily", "--handoff-time", "09:00"}},
		{"oncall create missing", nil, []string{"oncall", "create", "--name", "n"}},
		{"oncall create bad user", nil, []string{"oncall", "create", "--name", "n", "--timezone", "UTC", "--rotation-type", "daily", "--handoff-time", "09:00", "--user", "zz"}},
		{"oncall create bad start", nil, []string{"oncall", "create", "--name", "n", "--timezone", "UTC", "--rotation-type", "daily", "--handoff-time", "09:00", "--start", "zz"}},
		{"oncall create 500", []coverRoute{{"", "", 500, `{}`}}, []string{"oncall", "create", "--name", "n", "--timezone", "UTC", "--rotation-type", "daily", "--handoff-time", "09:00"}},
		{"override create", created(override), []string{"oncall", "overrides", "create", cuid, "--user", cuid2, "--start", cts, "--end", cts, "--reason", "r"}},
		{"override create json", created(override), []string{"-o", "json", "oncall", "overrides", "create", cuid, "--user", cuid2, "--start", cts, "--end", cts}},
		{"override create missing", nil, []string{"oncall", "overrides", "create", cuid}},
		{"override create bad user", nil, []string{"oncall", "overrides", "create", cuid, "--user", "zz", "--start", cts, "--end", cts}},
		{"override create bad start", nil, []string{"oncall", "overrides", "create", cuid, "--user", cuid2, "--start", "zz", "--end", cts}},
		{"override create bad end", nil, []string{"oncall", "overrides", "create", cuid, "--user", cuid2, "--start", cts, "--end", "zz"}},
		{"override create 500", []coverRoute{{"", "", 500, `{}`}}, []string{"oncall", "overrides", "create", cuid, "--user", cuid2, "--start", cts, "--end", cts}},

		{"resource create check", created(resource), []string{"status-pages", "resources", "create", cuid, cuid2, "--check", cuid, "--public-name", "p", "--explanation", "e", "--position", "2"}},
		{"resource create group json", created(resource), []string{"-o", "json", "status-pages", "resources", "create", cuid, cuid2, "--check-group", cuid}},
		{"resource create none", nil, []string{"status-pages", "resources", "create", cuid, cuid2}},
		{"resource create 500", []coverRoute{{"", "", 500, `{}`}}, []string{"status-pages", "resources", "create", cuid, cuid2, "--check", cuid}},

		{
			"auth login token",
			[]coverRoute{{"", "/auth/me", 200, `{"user":{"uid":"` + cuid + `","email":"a@acme.com"},"organization":{"slug":"acme"}}`}},
			[]string{"auth", "login", "--token", "sp_pat_x"},
		},
		{
			"auth login token json",
			[]coverRoute{{"", "/auth/me", 200, `{"user":{"uid":"` + cuid + `","email":"a@acme.com"},"organization":{"slug":"acme"}}`}},
			[]string{"-o", "json", "auth", "login", "--token", "sp_pat_x"},
		},
		{"auth login token bad", []coverRoute{{"", "/auth/me", 401, `{"title":"no"}`}}, []string{"auth", "login", "--token", "sp_pat_x"}},
		{
			"auth login password",
			[]coverRoute{{"", "/auth/login", 200, `{"accessToken":"a","refreshToken":"r","user":{"uid":"` + cuid + `","email":"a@acme.com"}}`}},
			[]string{"auth", "login", "--email", "a@acme.com", "--password", "p"},
		},
		{
			"auth login password json",
			[]coverRoute{{"", "/auth/login", 200, `{"accessToken":"a","refreshToken":"r","user":{"uid":"` + cuid + `","email":"a@acme.com"}}`}},
			[]string{"-o", "json", "auth", "login", "--email", "a@acme.com", "--password", "p"},
		},
		{"auth login password bad", []coverRoute{{"", "/auth/login", 401, `{"title":"no"}`}}, []string{"auth", "login", "--email", "a@acme.com", "--password", "p"}},
		{"auth login password bad json", []coverRoute{{"", "/auth/login", 401, `{"title":"no"}`}}, []string{"-o", "json", "auth", "login", "--email", "a@acme.com", "--password", "p"}},
		{"auth me", []coverRoute{{"", "/auth/me", 200, `{"user":{"uid":"` + cuid + `","email":"a@acme.com"},"organization":{"slug":"acme"}}`}}, []string{"auth", "me"}},
		{"auth me json", []coverRoute{{"", "/auth/me", 200, `{"user":{"uid":"` + cuid + `","email":"a@acme.com"},"organization":{"slug":"acme"}}`}}, []string{"-o", "json", "auth", "me"}},

		{"jobs stats", ok(`{"data":{"jobs":{"pending":1,"running":2,"failed24h":3},"checks":{"total":4,"dueNow":1,"inFlight":2,"stalled":0,"crashLooping":0}}}`), []string{"jobs", "stats"}},
		{"jobs stats json", ok(`{"data":{}}`), []string{"-o", "json", "jobs", "stats"}},
		{"jobs stats empty", ok(`{}`), []string{"jobs", "stats"}},
		{"jobs stats 500", []coverRoute{{"", "", 500, `{}`}}, []string{"jobs", "stats"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = coverRun(t, tt.routes, tt.args...)
		})
	}
}
