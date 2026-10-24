package cli

import (
	"net/http"
	"testing"
)

// TestCommandsAgainstFailingServer drives every leaf command against servers
// that fail in the ways a real one does (error status, wrong body, empty
// data). Each command must report the failure as an error, never panic.
//
//nolint:paralleltest // process-global stdio and HOME
func TestCommandsAgainstFailingServer(t *testing.T) {
	responders := map[string]http.HandlerFunc{
		"internal-error": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"title":"boom","code":"INTERNAL_ERROR","detail":"it broke"}`))
		},
		"not-found": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"title":"missing","code":"NOT_FOUND"}`))
		},
		"forbidden-html": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<html>nope</html>`))
		},
		"ok-garbage": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`<<not json>>`))
		},
		"ok-empty-data": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[],"pagination":{}}`))
		},
		"ok-wrong-shape": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":"text","uid":42,"name":[1]}`))
		},
	}

	for name, handler := range responders {
		for _, all := range []bool{false, true} {
			label := name
			if all {
				label += "-all"
			}
			//nolint:paralleltest // process-global stdio and HOME
			t.Run(label, func(t *testing.T) { runRoutedWith(t, "text", all, handler) })
		}
	}
}
