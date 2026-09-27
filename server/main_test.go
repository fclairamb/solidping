package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/internal/config"
)

// findSubcommand returns the direct child of cmd with the given name, or
// nil.
func findSubcommand(cmd *cli.Command, name string) *cli.Command {
	for _, c := range cmd.Commands {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// orgCapture records what a leaf Action observed for the "org" flag.
type orgCapture struct {
	org   string
	isSet bool
}

// captureOrgFlag returns the REAL "solidping" command tree - exactly what
// main() builds via buildRootCommand(), Flags/Commands/DefaultCommand and
// all - with the real "client checks list" leaf's Action swapped for a stub
// that records the resolved --org flag instead of making a network call.
// Every node up to and including "list" keeps its production Flags field
// untouched, so a future edit to buildRootCommand() that reintroduces a
// flag on "client" or a sibling (the exact regression the completeness
// audit flagged - a synthetic mirror tree wouldn't catch that) makes these
// tests exercise the actual bug.
func captureOrgFlag(t *testing.T) (*cli.Command, *orgCapture) {
	t.Helper()

	root := buildRootCommand()

	client := findSubcommand(root, "client")
	require.NotNil(t, client, `root command tree must have a "client" node`)

	checks := findSubcommand(client, "checks")
	require.NotNil(t, checks, `"client" must have a "checks" node`)

	list := findSubcommand(checks, "list")
	require.NotNil(t, list, `"checks" must have a "list" node`)

	capture := &orgCapture{}
	list.Action = func(_ context.Context, c *cli.Command) error {
		capture.org = c.String("org")
		capture.isSet = c.IsSet("org")
		return nil
	}

	return root, capture
}

// TestClientOrgFlag_BeforeClient covers the gap the completeness audit
// found: with DefaultCommand set on the "solidping" root, an --org
// positioned before "client" must still reach a leaf Action under it.
// Before the fix, GetGlobalFlags() was declared only on "client" itself,
// which the root (having no Flags of its own) could not recognize: v3
// passes an unrecognized flag ahead of the first subcommand through as a
// positional arg when DefaultCommand is set, so --org silently never
// reached "client" at all.
func TestClientOrgFlag_BeforeClient(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	root, capture := captureOrgFlag(t)

	err := root.Run(context.Background(), []string{"solidping", "--org", "test", "client", "checks", "list"})
	r.NoError(err)
	r.True(capture.isSet)
	r.Equal("test", capture.org)
}

// TestClientOrgFlag_AfterClient covers the position that already worked
// pre-fix ("client" itself was declaring the flags), kept working post-fix
// since v3 flags are inherited by default.
func TestClientOrgFlag_AfterClient(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	root, capture := captureOrgFlag(t)

	err := root.Run(context.Background(), []string{"solidping", "client", "--org", "test", "checks", "list"})
	r.NoError(err)
	r.True(capture.isSet)
	r.Equal("test", capture.org)
}

// TestClientOrgFlag_AfterLeafGroup covers --org positioned at the deepest
// point, right before the leaf command name.
func TestClientOrgFlag_AfterLeafGroup(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	root, capture := captureOrgFlag(t)

	err := root.Run(context.Background(), []string{"solidping", "client", "checks", "--org", "test", "list"})
	r.NoError(err)
	r.True(capture.isSet)
	r.Equal("test", capture.org)
}

// TestClientOrgFlag_NotSetFallsBackToDefault confirms the baseline: with no
// --org anywhere, IsSet is false and String reports the flag's own default
// ("default", from defaults.Organization) - i.e. the mechanism under test
// isn't accidentally reporting every flag as always-set.
func TestClientOrgFlag_NotSetFallsBackToDefault(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	root, capture := captureOrgFlag(t)

	err := root.Run(context.Background(), []string{"solidping", "client", "checks", "list"})
	r.NoError(err)
	r.False(capture.isSet)
	r.Equal("default", capture.org)
}

// TestClientMCPCommand pins `solidping client mcp`, the stdio bridge to a
// remote MCP endpoint (spec 2026-09-26-04). It lives under "client", so the
// server binary's own top-level `mcp` command (the in-process variant, spec
// 2026-09-26-05) can never shadow it, and --url set before "client" reaches it.
func TestClientMCPCommand(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	root := buildRootCommand()

	client := findSubcommand(root, "client")
	r.NotNil(client)

	mcpCmd := findSubcommand(client, "mcp")
	r.NotNil(mcpCmd, `"client" must have an "mcp" node`)

	var gotURL string

	mcpCmd.Action = func(_ context.Context, c *cli.Command) error {
		gotURL = c.String("url")
		return nil
	}

	err := root.Run(context.Background(),
		[]string{"solidping", "--url", "https://solidping.acme.com", "client", "mcp"})
	r.NoError(err)
	r.Equal("https://solidping.acme.com", gotURL)
}

// clientMCPHelperEnv switches the test binary into acting as the solidping
// binary for TestClientMCPFailureKeepsStdoutClean.
const clientMCPHelperEnv = "SP_TEST_CLIENT_MCP_HELPER"

// errClientMCPStub is what the stubbed `client mcp` action fails with.
var errClientMCPStub = errors.New("stubbed client mcp failure")

// TestClientMCPFailureKeepsStdoutClean runs `solidping client mcp` in a child
// process with main's own stdout logger installed and makes it fail, then
// checks stdout stayed empty: an MCP client parses every byte there as
// JSON-RPC. Two ways to fail:
//   - the real action, with HOME unset so no token path can be resolved: it
//     must log to stderr and exit without returning the error to main;
//   - a stubbed action returning a plain error, which does reach main's
//     "Application failed" log: the client subtree must have pointed slog at
//     stderr.
func TestClientMCPFailureKeepsStdoutClean(t *testing.T) {
	t.Parallel()

	if mode := os.Getenv(clientMCPHelperEnv); mode != "" {
		runClientMCPHelper(mode)

		return
	}

	for _, mode := range []string{"real", "stub"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()

			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestClientMCPFailureKeepsStdoutClean$")
			cmd.Env = append(os.Environ(), clientMCPHelperEnv+"="+mode, "HOME=", "LOG_LEVEL=info")

			var stdout, stderr bytes.Buffer

			cmd.Stdin = bytes.NewReader(nil)
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			err := cmd.Run()

			var exitErr *exec.ExitError
			r.ErrorAs(err, &exitErr, "the failure must exit non-zero; stderr: %s", stderr.String())
			r.Equal(1, exitErr.ExitCode())
			r.Empty(stdout.String(), "nothing but JSON-RPC may reach stdout")
			r.NotEmpty(stderr.String(), "the failure must still be reported, on stderr")
		})
	}
}

// runClientMCPHelper is the child side: it mirrors main() (stdout logger,
// then run) and exits with run's code.
func runClientMCPHelper(mode string) {
	setupLogger(config.ParseLogLevel(os.Getenv("LOG_LEVEL")), config.LogFormatText)

	root := buildRootCommand()

	if mode == "stub" {
		mcpCmd := findSubcommand(findSubcommand(root, "client"), "mcp")
		mcpCmd.Action = func(context.Context, *cli.Command) error { return errClientMCPStub }
	}

	os.Exit(run(context.Background(), root, []string{"solidping", "client", "--url", "http://127.0.0.1:1", "mcp"}))
}
