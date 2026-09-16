package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/internal/defaults"
)

// writeTestConfig writes a minimal CLI settings.json to a temp dir and
// returns its path. When org is non-empty, it is included as the config
// file's "org" key; otherwise the key is omitted entirely.
func writeTestConfig(t *testing.T, org string) string {
	t.Helper()

	data := map[string]any{"url": "http://localhost:4000"}
	if org != "" {
		data["org"] = org
	}

	raw, err := json.Marshal(data)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "settings.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))

	return path
}

// runNewCLIContext parses args through a fresh root command carrying the
// real GetGlobalFlags() set and returns whatever NewCLIContext produced
// inside the command's Action - i.e. it exercises the actual flag-parsing
// path, not just NewCLIContext in isolation.
func runNewCLIContext(t *testing.T, args []string) *Context {
	t.Helper()

	var got *Context

	cmd := &cli.Command{
		Name:  "sp",
		Flags: GetGlobalFlags(),
		Action: func(_ context.Context, c *cli.Command) error {
			ctx, err := NewCLIContext(c)
			if err != nil {
				return err
			}
			got = ctx
			return nil
		},
	}

	require.NoError(t, cmd.Run(context.Background(), args))

	return got
}

// TestNewCLIContext_ConfigOrgUsedWhenFlagNotPassed covers: no --org flag,
// config file Org set to a non-default value -> that org is used.
func TestNewCLIContext_ConfigOrgUsedWhenFlagNotPassed(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	path := writeTestConfig(t, "acme")

	ctx := runNewCLIContext(t, []string{"sp", "--config", path})
	r.Equal("acme", ctx.Config.Org)
}

// TestNewCLIContext_DefaultOrgWhenNothingSet covers: no --org flag, no
// config file Org -> falls back to "default".
func TestNewCLIContext_DefaultOrgWhenNothingSet(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	path := writeTestConfig(t, "")

	ctx := runNewCLIContext(t, []string{"sp", "--config", path})
	r.Equal(defaults.Organization, ctx.Config.Org)
}

// TestNewCLIContext_ExplicitFlagOverridesConfig covers: explicit --org test
// with a config file Org set to something else -> --org wins.
func TestNewCLIContext_ExplicitFlagOverridesConfig(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	path := writeTestConfig(t, "acme")

	ctx := runNewCLIContext(t, []string{"sp", "--config", path, "--org", "test"})
	r.Equal("test", ctx.Config.Org)
}

// TestNewCLIContext_ExplicitFlagAfterSubcommandOverridesConfig is the same
// as above but with --org positioned after a subcommand, the shape actually
// used throughout GetCommands() (e.g. "sp checks list --org test" is not
// idiomatic, but "sp --org test checks list" and flags declared on a leaf's
// own Flags both need to keep working; this exercises a leaf command with no
// Flags of its own picking up the persistent, root-declared "org" flag
// regardless of where in a real, deeper tree it sits).
func TestNewCLIContext_ExplicitFlagAfterSubcommandOverridesConfig(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	path := writeTestConfig(t, "acme")

	var got *Context

	leaf := &cli.Command{
		Name: "list",
		Action: func(_ context.Context, c *cli.Command) error {
			ctx, err := NewCLIContext(c)
			if err != nil {
				return err
			}
			got = ctx
			return nil
		},
	}
	group := &cli.Command{
		Name:     "checks",
		Commands: []*cli.Command{leaf},
	}
	root := &cli.Command{
		Name:     "sp",
		Flags:    GetGlobalFlags(),
		Commands: []*cli.Command{group},
	}

	require.NoError(t, root.Run(context.Background(), []string{"sp", "--config", path, "--org", "test", "checks", "list"}))
	r.Equal("test", got.Config.Org)
}

// TestCLI_OrgFlagPropagatesThroughCommandTree is the end-to-end regression
// test for the actual bug: it builds a root -> group -> leaf command tree
// mirroring GetCommands()'s real shape (global flags declared only on
// root, nothing redeclared below it) and checks that an --org passed at
// the root is visible, and correctly reported as explicitly set, all the
// way down inside the leaf's own Action.
func TestCLI_OrgFlagPropagatesThroughCommandTree(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var capturedOrg string
	var capturedIsSet bool

	leaf := &cli.Command{
		Name: "leaf",
		Action: func(_ context.Context, c *cli.Command) error {
			capturedOrg = c.String("org")
			capturedIsSet = c.IsSet("org")
			return nil
		},
	}
	group := &cli.Command{
		Name:     "group",
		Commands: []*cli.Command{leaf},
	}
	root := &cli.Command{
		Name:     "root",
		Flags:    GetGlobalFlags(),
		Commands: []*cli.Command{group},
	}

	err := root.Run(context.Background(), []string{"root", "--org", "test", "group", "leaf"})
	r.NoError(err)
	r.True(capturedIsSet)
	r.Equal("test", capturedOrg)
}

// TestCLI_OrgFlagShadowedByIntermediateRedeclaration documents the exact
// regression this fix removes: a same-named flag redeclared on an
// intermediate command node (the pre-fix shape, where every command group
// called GetGlobalFlags() again) shadows the root's parsed value for every
// descendant - the leaf resolves to the intermediate node's own, unset,
// default-valued flag object instead. Without this test, the propagation
// fix in commands.go/etc. could regress silently even though
// TestCLI_OrgFlagPropagatesThroughCommandTree above still passes for a tree
// that never reintroduces the shadow.
func TestCLI_OrgFlagShadowedByIntermediateRedeclaration(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var capturedOrg string
	var capturedIsSet bool

	leaf := &cli.Command{
		Name: "leaf",
		Action: func(_ context.Context, c *cli.Command) error {
			capturedOrg = c.String("org")
			capturedIsSet = c.IsSet("org")
			return nil
		},
	}
	group := &cli.Command{
		Name:     "group",
		Flags:    GetGlobalFlags(), // pre-fix shape: shadows root's "org" flag
		Commands: []*cli.Command{leaf},
	}
	root := &cli.Command{
		Name:     "root",
		Flags:    GetGlobalFlags(),
		Commands: []*cli.Command{group},
	}

	err := root.Run(context.Background(), []string{"root", "--org", "test", "group", "leaf"})
	r.NoError(err)
	r.False(capturedIsSet, "leaf resolves to group's own unset shadow flag, not root's set one")
	r.Equal(defaults.Organization, capturedOrg)
}
