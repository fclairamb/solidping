package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
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
