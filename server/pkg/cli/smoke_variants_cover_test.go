package cli

import (
	"net/http"
	"strings"
	"testing"
)

// TestCommandsSmokeVariants drives every leaf with the argument vectors and
// server answers the main smoke run does not use: nothing but the command
// path (usage errors), an expired token (401 on every call) and a validation
// answer (400/409). Each must come back as an error, never a panic or a hang.
//
//nolint:paralleltest // swaps os.Stdout/Stdin and HOME, which are process-global
func TestCommandsSmokeVariants(t *testing.T) {
	bare := func(leaf smokeLeaf) []string { return append([]string{}, leaf.path...) }
	full := func(leaf smokeLeaf) []string { return smokeArgs(leaf, true) }

	//nolint:paralleltest // see above
	t.Run("no arguments", func(t *testing.T) {
		runSmokeWith(t, http.StatusOK, `{"data":[]}`, "text", bare)
	})
	//nolint:paralleltest // see above
	t.Run("expired token", func(t *testing.T) {
		runSmokeWith(t, http.StatusUnauthorized, `{"title":"expired","code":"UNAUTHORIZED"}`, "text", full)
	})
	//nolint:paralleltest // see above
	t.Run("expired token minimal args", func(t *testing.T) {
		runSmokeWith(t, http.StatusUnauthorized, `{"title":"expired","code":"UNAUTHORIZED"}`, "json",
			func(leaf smokeLeaf) []string { return smokeArgs(leaf, false) })
	})
	//nolint:paralleltest // see above
	t.Run("validation error", func(t *testing.T) {
		runSmokeWith(t, http.StatusBadRequest,
			`{"title":"bad","code":"VALIDATION_ERROR","detail":"x","fields":[{"name":"n","message":"m"}]}`, "text", full)
	})
	//nolint:paralleltest // see above
	t.Run("conflict", func(t *testing.T) {
		runSmokeWith(t, http.StatusConflict, `{"title":"dup","code":"CONFLICT"}`, "json", full)
	})
	//nolint:paralleltest // see above
	t.Run("forbidden", func(t *testing.T) {
		runSmokeWith(t, http.StatusForbidden, `{"title":"no","code":"FORBIDDEN"}`, "text", full)
	})

	//nolint:paralleltest // see above
	t.Run("not logged in", func(t *testing.T) {
		runSmokeCfg(t, http.StatusOK, `{"data":[]}`, "text", full, func(url string) string {
			return `{"url":"` + url + `","org":"acme"}`
		})
	})
	//nolint:paralleltest // see above
	t.Run("no settings file", func(t *testing.T) {
		runSmokeCfg(t, http.StatusOK, `{"data":[]}`, "text", full, nil)
	})
	//nolint:paralleltest // see above
	t.Run("broken settings file", func(t *testing.T) {
		runSmokeCfg(t, http.StatusOK, `{"data":[]}`, "text", full, func(string) string { return `{not json` })
	})
	//nolint:paralleltest // see above
	t.Run("no org", func(t *testing.T) {
		runSmokeCfg(t, http.StatusOK, `{"data":[]}`, "text", full, func(url string) string {
			return `{"url":"` + url + `","auth":{"pat":"sp_pat_test"}}`
		})
	})

	//nolint:paralleltest // see above
	t.Run("malformed uids", func(t *testing.T) {
		malformed := func(leaf smokeLeaf) []string {
			args := smokeArgs(leaf, true)
			for i, arg := range args {
				if arg == smokeUID {
					args[i] = "not-a-uid"
				}
			}

			return args
		}
		runSmokeWith(t, http.StatusOK, `{"data":[]}`, "text", malformed)
	})
	//nolint:paralleltest // see above
	t.Run("missing input files", func(t *testing.T) {
		missing := func(leaf smokeLeaf) []string {
			args := smokeArgs(leaf, true)
			for i, arg := range args {
				if arg == smokeUID && i > 0 && strings.HasSuffix(args[i-1], "file") {
					args[i] = "/nonexistent/input.file"
				}
			}

			return args
		}
		runSmokeWith(t, http.StatusOK, `{"data":[]}`, "text", missing)
	})
}
