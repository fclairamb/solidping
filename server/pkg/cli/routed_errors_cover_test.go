package cli

import (
	"net/http"
	"testing"
)

// TestCommandsRoutedErrorsCover drives every leaf command against servers
// that fail, return nothing useful or return garbage, to exercise the error
// and empty-result branches of the commands.
//
//nolint:paralleltest // process-global stdio and HOME
func TestCommandsRoutedErrorsCover(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"server-error": {http.StatusInternalServerError, `{"title":"boom","code":"INTERNAL_ERROR","detail":"acme"}`},
		"not-found":    {http.StatusNotFound, `{"title":"missing","code":"NOT_FOUND"}`},
		"forbidden":    {http.StatusForbidden, `{"title":"nope","code":"FORBIDDEN"}`},
		"conflict":     {http.StatusConflict, `not json`},
		"empty-list":   {http.StatusOK, `{"data":[],"pagination":{"total":0}}`},
		"garbage":      {http.StatusOK, `<html>`},
		"empty-object": {http.StatusOK, `{}`},
	}
	for name, tc := range cases {
		for _, format := range []string{"text", "json"} {
			for _, all := range []bool{false, true} {
				//nolint:paralleltest // process-global stdio and HOME
				t.Run(name+"-"+format, func(t *testing.T) {
					runRoutedWith(t, format, all, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(tc.status)
						_, _ = w.Write([]byte(tc.body))
					}))
				})
			}
		}
	}
}
