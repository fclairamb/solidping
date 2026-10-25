package cli

import (
	"net/http"
	"testing"
)

//nolint:paralleltest,lll // coverRun swaps process-global stdio and HOME
func TestCLIResultsCover(t *testing.T) {
	row := `{"uid":"` + cuid + `","checkSlug":"web","status":"up","durationMs":12.5,"region":"eu",` +
		`"periodType":"raw","periodStart":"` + cts + `","fallback":{"reason":"rolled_up_to_day"}}`
	bare := `{"uid":"` + cuid + `","checkUid":"` + cuid2 + `","status":"down"}`
	list := `{"data":[` + row + `,` + bare + `],"pagination":{"cursor":"next"}}`
	last := `{"data":[` + row + `]}`

	for _, format := range []string{"text", "json"} {
		for _, args := range [][]string{
			{"results", "list", "--with", "durationMs,region", "--check", "web", "--status", "up"},
			{"results", "get", "web", cuid},
			{"results", "get", "web", cuid, "--region", "eu"},
		} {
			routes := []coverRoute{
				{method: http.MethodGet, path: "/results/" + cuid, status: 200, body: row},
				{method: http.MethodGet, path: "/results", status: 200, body: list},
			}
			if err := coverRun(t, routes, append([]string{"-o", format}, args...)...); err != nil {
				t.Errorf("%s %v: %v", format, args, err)
			}
		}
	}

	// A last page without a cursor ends the auto walk.
	_ = coverRun(t, []coverRoute{{path: "/results", status: 200, body: last}}, "results", "list", "--auto")

	// Failure paths.
	for _, args := range [][]string{
		{"results", "list"},
		{"results", "get", "web", cuid},
		{"results", "get", "web"},
		{"results", "get", "web", "not-a-uuid"},
	} {
		if err := coverRun(t, []coverRoute{{path: "/results", status: 500, body: `{"title":"boom"}`}}, args...); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
}
