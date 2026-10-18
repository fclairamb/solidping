package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func runScript(t *testing.T, script string, config map[string]any) *checkerdef.Result {
	t.Helper()

	path := filepath.Join(t.TempDir(), "check.js")
	require.NoError(t, os.WriteFile(path, []byte(script), 0o600))

	tr, err := newJSTransport(path)
	require.NoError(t, err)

	res, err := tr.execute(t.Context(), "x-test", testConfig(config))
	require.NoError(t, err)

	return LimitResult(res)
}

func TestJSPluginConfigIsReadOnly(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	res := runScript(t, `
"use strict";
var errors = [];
try { config.host = "evil"; } catch (e) { errors.push("top"); }
try { config.nested.key = "evil"; } catch (e) { errors.push("nested"); }
return { status: "up", output: { host: config.host, nested: config.nested.key, errors: errors.join(",") } };
`, map[string]any{"host": "gopher.acme.com", "nested": map[string]any{"key": "value"}})

	r.Equal(checkerdef.StatusUp, res.Status, res.Output)
	r.Equal("gopher.acme.com", res.Output["host"])
	r.Equal("value", res.Output["nested"])
	r.Equal("top,nested", res.Output["errors"])
}

func TestJSPluginStatuses(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	warning := runScript(t, `return { status: "warning", output: { message: "meh" } };`, nil)
	r.Equal(checkerdef.StatusWarning, warning.Status, warning.Output)

	unknown := runScript(t, `return { status: "great" };`, nil)
	r.Equal(checkerdef.StatusError, unknown.Status)
	r.Contains(fmt.Sprint(unknown.Output["error"]), `unknown status "great"`)
}

func TestJSPluginEntryChecks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	broken := filepath.Join(dir, "broken.js")
	require.NoError(t, os.WriteFile(broken, []byte("return {"), 0o600))

	_, err := newJSTransport(broken)
	require.ErrorIs(t, err, ErrInvalidManifest)

	big := filepath.Join(dir, "big.js")
	require.NoError(t, os.WriteFile(big, make([]byte, maxScriptBytes+1), 0o600))

	_, err = newJSTransport(big)
	require.ErrorIs(t, err, ErrInvalidManifest)
}
