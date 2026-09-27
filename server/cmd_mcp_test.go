package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/defaults"
)

// mcpStdioHelperEnv switches the test binary into acting as the solidping
// binary: TestMain runs `solidping <args>` with the JSON array of args it
// holds, exactly as main() would (stdout logger first), and exits.
const mcpStdioHelperEnv = "MCP_STDIO_TEST_HELPER_ARGS"

// mcpStdioTimeout bounds one child run: a boot, a few requests, a shutdown.
const mcpStdioTimeout = 2 * time.Minute

// TestMain lets a child process of the test binary be `solidping mcp --stdio`
// before the testing package prints anything on stdout: a test function
// cannot, since os.Exit(0) inside one is reported as a failure.
func TestMain(m *testing.M) {
	if raw := os.Getenv(mcpStdioHelperEnv); raw != "" {
		os.Exit(runMCPStdioHelper(raw))
	}

	os.Exit(m.Run())
}

// runMCPStdioHelper mirrors main(): the stdout logger main installs before
// anything else, then the real command tree.
func runMCPStdioHelper(raw string) int {
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "bad helper args:", err)

		return 2
	}

	setupLogger(config.ParseLogLevel(os.Getenv("LOG_LEVEL")), config.ParseLogFormat(os.Getenv("SP_LOG_FORMAT")))

	return run(context.Background(), buildRootCommand(), append([]string{"solidping"}, args...))
}

// mcpStdioChild prepares `solidping <args>` as a child process on the SQLite
// database in dataDir.
//
// It runs in its own empty directory with every SP_* / SOLIDPING_* variable of
// the developer's environment removed: config.yml, config.local.yml and a
// shell's tokens would otherwise point the boot at real mailboxes, bots and
// databases.
func mcpStdioChild(ctx context.Context, t *testing.T, dataDir string, extraEnv []string, args ...string) *exec.Cmd {
	t.Helper()

	rawArgs, err := json.Marshal(args)
	require.NoError(t, err)

	workDir := t.TempDir()

	env := make([]string, 0, len(os.Environ()))

	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "SP_") || strings.HasPrefix(name, "SOLIDPING_") || name == "LOG_LEVEL" ||
			name == "HOME" {
			continue
		}

		env = append(env, kv)
	}

	env = append(env,
		mcpStdioHelperEnv+"="+string(rawArgs),
		"HOME="+workDir,
		"LOG_LEVEL=info",
		"SP_LOG_FORMAT=text",
		"SP_DB_TYPE=sqlite",
		"SP_DB_DIR="+dataDir,
		"SP_SHUTDOWN_TIMEOUT=5s",
	)
	env = append(env, extraEnv...)

	cmd := exec.CommandContext(ctx, os.Args[0])
	cmd.Dir = workDir
	cmd.Env = env

	return cmd
}

// requireOnlyJSONRPC asserts every stdout line is one JSON-RPC 2.0 message
// carrying an id, and returns them in order.
func requireOnlyJSONRPC(t *testing.T, stdout string) []map[string]json.RawMessage {
	t.Helper()
	r := require.New(t)

	var messages []map[string]json.RawMessage

	for _, line := range strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") {
		if line == "" {
			continue
		}

		var msg map[string]json.RawMessage
		r.NoError(json.Unmarshal([]byte(line), &msg), "stdout carried a non-JSON line: %q", line)
		r.JSONEq(`"2.0"`, string(msg["jsonrpc"]), "stdout carried a non-JSON-RPC line: %q", line)
		r.Contains(msg, "id", "stdout carried a message that answers nothing: %q", line)

		messages = append(messages, msg)
	}

	return messages
}

// TestMCPStdioCommandFreshDatabase runs the real `solidping mcp --stdio` on a
// fresh database, as an MCP directory would: the session acts as the seeded
// admin (who carries must_change_password), a create_check is stamped with it,
// the notification gets no reply, and while the services log plenty, stdout
// holds nothing but the three replies.
func TestMCPStdioCommandFreshDatabase(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, cancel := context.WithTimeout(t.Context(), mcpStdioTimeout)
	defer cancel()

	dataDir := t.TempDir()

	// jobs only: the startup job seeds the org and its sample checks, and no
	// check worker takes them to the internet from a unit test.
	cmd := mcpStdioChild(ctx, t, dataDir, []string{"SP_NODE_ROLE=jobs"}, "mcp", "--stdio")
	cmd.Stdin = strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26",` +
			`"clientInfo":{"name":"acme-client","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"create_check","arguments":` +
			`{"name":"Acme","slug":"acme-web","type":"http","config":{"url":"https://acme.com"}}}}`,
	}, "\n") + "\n")

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	r.NoError(cmd.Run(), "stderr: %s", stderr.String())

	msgs := requireOnlyJSONRPC(t, stdout.String())
	r.Len(msgs, 3, "one line per request, none for the notification; stdout: %s", stdout.String())
	r.JSONEq(`1`, string(msgs[0]["id"]))
	r.Contains(string(msgs[0]["result"]), `"serverInfo":{"name":"solidping"`)
	r.JSONEq(`2`, string(msgs[1]["id"]))
	r.Contains(string(msgs[1]["result"]), `"create_check"`)
	r.JSONEq(`3`, string(msgs[2]["id"]))
	r.NotContains(msgs[2], "error")
	r.NotContains(string(msgs[2]["result"]), `"isError":true`)

	// The services did log, all of it on stderr.
	logs := stderr.String()
	r.Contains(logs, "Migrations completed successfully")
	r.Contains(logs, "Startup job completed successfully")
	r.Contains(logs, "MCP stdio session ready")
	r.Contains(logs, "headless=true")
	r.NotContains(logs, "Starting HTTP server")

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{DataDir: dataDir})
	r.NoError(err)

	defer func() { _ = dbSvc.Close() }()

	admin, err := dbSvc.GetUserByEmail(t.Context(), defaults.Email)
	r.NoError(err)
	r.True(admin.MustChangePassword, "the seeded admin must still be flagged: stdio ignores the rotation")

	org, err := dbSvc.GetOrganizationBySlug(t.Context(), defaults.Organization)
	r.NoError(err)

	check, err := dbSvc.GetCheckByUidOrSlug(t.Context(), org.UID, "acme-web")
	r.NoError(err)
	r.NotNil(check.CreatedBy)
	r.Equal(admin.UID, *check.CreatedBy, "created_by must be the stdio principal")
}

// seedAcmeDatabase migrates a SQLite database in dataDir and gives it one org
// with an owner and a stranger. With an org already present, the startup job
// seeds neither the default org nor any sample check.
func seedAcmeDatabase(t *testing.T, dataDir string) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{DataDir: dataDir})
	r.NoError(err)

	defer func() { r.NoError(dbSvc.Close()) }()

	r.NoError(dbSvc.Initialize(ctx))

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	owner := models.NewUser("alice@acme.com")
	r.NoError(dbSvc.CreateUser(ctx, owner))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, owner.UID, models.MemberRoleOwner)))

	r.NoError(dbSvc.CreateUser(ctx, models.NewUser("dave@acme.com")))
}

// mcpSession drives a running child one request at a time.
type mcpSession struct {
	t      *testing.T
	stdin  io.Writer
	lines  chan string
	nextID int
}

func (s *mcpSession) call(method string, params any) map[string]json.RawMessage {
	s.t.Helper()
	r := require.New(s.t)

	s.nextID++

	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": s.nextID, "method": method, "params": params})
	r.NoError(err)

	_, err = s.stdin.Write(append(payload, '\n'))
	r.NoError(err)

	select {
	case line, ok := <-s.lines:
		r.True(ok, "stdout closed before the reply to %s", method)

		msgs := requireOnlyJSONRPC(s.t, line)
		r.Len(msgs, 1)
		r.JSONEq(strconv.Itoa(s.nextID), string(msgs[0]["id"]))

		return msgs[0]
	case <-time.After(mcpStdioTimeout):
		r.FailNow("no reply to " + method)

		return nil
	}
}

func (s *mcpSession) tool(name string, args map[string]any) map[string]any {
	s.t.Helper()
	r := require.New(s.t)

	msg := s.call("tools/call", map[string]any{"name": name, "arguments": args})
	r.NotContains(msg, "error", "%s failed: %s", name, msg["error"])

	var result struct {
		StructuredContent map[string]any `json:"structuredContent"`
		IsError           bool           `json:"isError"`
		Content           []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	r.NoError(json.Unmarshal(msg["result"], &result))
	r.False(result.IsError, "%s failed: %v", name, result.Content)

	return result.StructuredContent
}

// TestMCPStdioCommandRunsChecks proves the check workers run in the same
// process: a check created over stdio actually probes its target, and its
// result is readable over stdio. It also picks the principal with --org.
func TestMCPStdioCommandRunsChecks(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, cancel := context.WithTimeout(t.Context(), mcpStdioTimeout)
	defer cancel()

	var probes atomic.Int64

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probes.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	dataDir := t.TempDir()
	seedAcmeDatabase(t, dataDir)

	cmd := mcpStdioChild(ctx, t, dataDir, nil, "--org", "acme", "mcp", "--stdio")

	stdin, err := cmd.StdinPipe()
	r.NoError(err)

	stdout, err := cmd.StdoutPipe()
	r.NoError(err)

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	r.NoError(cmd.Start())

	lines := make(chan string)

	go func() {
		defer close(lines)

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()

	session := &mcpSession{t: t, stdin: stdin, lines: lines}

	session.call("initialize", map[string]any{"protocolVersion": "2025-03-26"})

	created := session.tool("create_check", map[string]any{
		"name": "Local", "slug": "local", "type": "http", "period": "00:00:10",
		"config": map[string]any{"url": target.URL},
	})
	checkUID, ok := created["uid"].(string)
	r.True(ok, "create_check returned no uid: %v", created)

	deadline := time.Now().Add(time.Minute)

	var gotResult bool

	for time.Now().Before(deadline) {
		listed := session.tool("list_results", map[string]any{"checkUid": checkUID, "periodType": "raw"})
		if data, isList := listed["data"].([]any); isList && len(data) > 0 {
			gotResult = true

			break
		}

		time.Sleep(500 * time.Millisecond)
	}

	r.True(gotResult, "no result within a minute; stderr: %s", stderr.String())
	r.Positive(probes.Load(), "the in-process check worker never probed the target")

	r.NoError(stdin.Close())

	// Drain until the child closes stdout.
	for line := range lines {
		t.Logf("unexpected stdout after the last request: %s", line)
	}

	r.NoError(cmd.Wait(), "stderr: %s", stderr.String())
	r.Contains(stderr.String(), "user=alice@acme.com")
}

// TestMCPStdioCommandPrincipalErrors: an unknown org, an unknown user and a
// non-member each stop the command with a non-zero exit and a clear message on
// stderr, and not one byte on stdout.
func TestMCPStdioCommandPrincipalErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "unknown org",
			args: []string{"mcp", "--stdio", "--org", "nosuch"},
			want: `organization not found: \"nosuch\"`,
		},
		{
			name: "unknown user",
			args: []string{"mcp", "--stdio", "--org", "acme", "--user", "nobody@acme.com"},
			want: `user not found: \"nobody@acme.com\"`,
		},
		{
			name: "not a member",
			args: []string{"mcp", "--stdio", "--org", "acme", "--user", "dave@acme.com"},
			want: "user is not a member of the organization: dave@acme.com",
		},
		{name: "default org missing", args: []string{"mcp", "--stdio"}, want: `organization not found: \"default\"`},
		{name: "no transport", args: []string{"mcp"}, want: "stdio is the only transport"},
		{name: "bad flag", args: []string{"mcp", "--stdio", "--acme"}, want: "flag provided but not defined"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			ctx, cancel := context.WithTimeout(t.Context(), mcpStdioTimeout)
			defer cancel()

			// One database per child: concurrent boots on one SQLite file
			// would race each other's migrations.
			dataDir := t.TempDir()
			seedAcmeDatabase(t, dataDir)

			// No check worker: the seeded org has no checks, but the errors
			// under test must not depend on one either.
			cmd := mcpStdioChild(ctx, t, dataDir, []string{"SP_NODE_ROLE=jobs"}, tc.args...)
			cmd.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")

			var stdout, stderr bytes.Buffer

			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			err := cmd.Run()

			var exitErr *exec.ExitError
			r.ErrorAs(err, &exitErr, "must exit non-zero; stderr: %s", stderr.String())
			r.Equal(1, exitErr.ExitCode())
			r.Empty(stdout.String(), "nothing but JSON-RPC may reach stdout")
			r.Contains(stderr.String(), tc.want)
		})
	}
}

// TestMCPCommandFlags pins the flag wiring on the real tree: --org is the
// global flag, valid before or after "mcp", and --user / --stdio belong to
// "mcp".
func TestMCPCommandFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		args     []string
		wantOrg  string
		wantUser string
	}{
		{args: []string{"mcp", "--stdio"}, wantOrg: defaults.Organization},
		{args: []string{"--org", "acme", "mcp", "--stdio"}, wantOrg: "acme"},
		{
			args:     []string{"mcp", "--stdio", "--org", "acme", "--user", "alice@acme.com"},
			wantOrg:  "acme",
			wantUser: "alice@acme.com",
		},
	}

	for _, tc := range tests {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			root := buildRootCommand()

			mcpCmd := findSubcommand(root, "mcp")
			r.NotNil(mcpCmd, `the root must have an "mcp" node`)

			var gotOrg, gotUser string

			var gotStdio bool

			mcpCmd.Action = func(_ context.Context, c *cli.Command) error {
				gotOrg, gotUser, gotStdio = c.String(flagOrg), c.String(flagMCPUser), c.Bool(flagMCPStdio)

				return nil
			}

			r.NoError(root.Run(t.Context(), append([]string{"solidping"}, tc.args...)))
			r.Equal(tc.wantOrg, gotOrg)
			r.Equal(tc.wantUser, gotUser)
			r.True(gotStdio)
		})
	}
}
