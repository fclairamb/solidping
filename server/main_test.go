package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	spCli "github.com/fclairamb/solidping/server/pkg/cli"
)

// buildClientFlagTestRoot mirrors the production `solidping` root's shape
// for the pkg/cli-reusing "client" subtree: DefaultCommand set on the root,
// pkg/cli's global flags (config/url/org/output/json/verbose) declared only
// once - at the root - and "client" (and everything under it) declaring
// none of its own, relying on urfave/cli v3's default flag persistence to
// carry them down. This is the exact shape main.go now uses; a synthetic
// tree keeps the test independent of pkg/cli's real leaf actions (which
// hit the network).
func buildClientFlagTestRoot(leafAction cli.ActionFunc) *cli.Command {
	leaf := &cli.Command{Name: "leaf", Action: leafAction}
	group := &cli.Command{Name: "group", Commands: []*cli.Command{leaf}}
	client := &cli.Command{Name: "client", Commands: []*cli.Command{group}}

	return &cli.Command{
		Name:           "solidping",
		DefaultCommand: "serve",
		Flags:          spCli.GetGlobalFlags(),
		Commands: []*cli.Command{
			{
				Name:   "serve",
				Action: func(context.Context, *cli.Command) error { return nil },
			},
			client,
		},
	}
}

// TestClientOrgFlag_BeforeClient covers the gap the completeness audit
// found: with DefaultCommand set on the "solidping" root and no Flags
// declared on "client" (the fix - see buildClientFlagTestRoot doc), an
// --org positioned before "client" must still reach a leaf Action under it.
// Before this fix, GetGlobalFlags() was declared only on "client" itself,
// which the root (having no Flags of its own) could not recognize: v3
// passes an unrecognized flag ahead of the first subcommand through as a
// positional arg when DefaultCommand is set, so --org silently never
// reached "client" at all.
func TestClientOrgFlag_BeforeClient(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var org string
	var isSet bool

	root := buildClientFlagTestRoot(func(_ context.Context, c *cli.Command) error {
		org = c.String("org")
		isSet = c.IsSet("org")
		return nil
	})

	err := root.Run(context.Background(), []string{"solidping", "--org", "test", "client", "group", "leaf"})
	r.NoError(err)
	r.True(isSet)
	r.Equal("test", org)
}

// TestClientOrgFlag_AfterClient covers the position that already worked
// pre-fix ("client" itself was declaring the flags), kept working post-fix
// since v3 flags are inherited by default.
func TestClientOrgFlag_AfterClient(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var org string
	var isSet bool

	root := buildClientFlagTestRoot(func(_ context.Context, c *cli.Command) error {
		org = c.String("org")
		isSet = c.IsSet("org")
		return nil
	})

	err := root.Run(context.Background(), []string{"solidping", "client", "--org", "test", "group", "leaf"})
	r.NoError(err)
	r.True(isSet)
	r.Equal("test", org)
}

// TestClientOrgFlag_AfterLeafGroup covers --org positioned at the deepest
// point, right before the leaf command name.
func TestClientOrgFlag_AfterLeafGroup(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var org string
	var isSet bool

	root := buildClientFlagTestRoot(func(_ context.Context, c *cli.Command) error {
		org = c.String("org")
		isSet = c.IsSet("org")
		return nil
	})

	err := root.Run(context.Background(), []string{"solidping", "client", "group", "--org", "test", "leaf"})
	r.NoError(err)
	r.True(isSet)
	r.Equal("test", org)
}

// TestClientOrgFlag_NotSetFallsBackToDefault confirms the baseline: with no
// --org anywhere, IsSet is false and String reports the flag's own default
// ("default", from defaults.Organization) - i.e. the mechanism under test
// isn't accidentally reporting every flag as always-set.
func TestClientOrgFlag_NotSetFallsBackToDefault(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var org string
	var isSet bool

	root := buildClientFlagTestRoot(func(_ context.Context, c *cli.Command) error {
		org = c.String("org")
		isSet = c.IsSet("org")
		return nil
	})

	err := root.Run(context.Background(), []string{"solidping", "client", "group", "leaf"})
	r.NoError(err)
	r.False(isSet)
	r.Equal("default", org)
}
