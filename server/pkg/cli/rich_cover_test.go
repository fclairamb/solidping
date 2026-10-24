package cli

import (
	"net/http"
	"strings"
	"testing"
)

// richItem extends coverItem with the nested objects list renderers branch on
// (last result, last status change, regions, config).
const richItem = `{"uid":"` + smokeUID + `","slug":"acme","name":"acme","type":"http","enabled":false,` +
	`"period":"00:01:00","internal":true,"createdAt":"2026-01-01T00:00:00Z","status":"down","state":"active",` +
	`"lastResult":{"status":"down","timestamp":"2026-01-02T00:00:00Z","duration":12,"region":"eu"},` +
	`"lastStatusChange":{"status":"down","time":"2026-01-01T00:00:00Z"},` +
	`"config":{"url":"https://acme.com"},"labels":{"env":"prod"},"regions":["eu","us"],` +
	`"checkUid":"` + smokeUID + `","email":"alice@acme.com","role":"admin","title":"acme"}`

// TestCommandsRichResponses drives every leaf against servers answering with
// fully populated objects, in every output format, so the renderers' optional
// branches run.
//
//nolint:paralleltest // process-global stdio and HOME
func TestCommandsRichResponses(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case coverUIDSuffix.MatchString(r.URL.Path) || r.Method != http.MethodGet:
			_, _ = w.Write([]byte(`{"data":` + richItem + `,` + strings.TrimPrefix(richItem, "{")))
		default:
			_, _ = w.Write([]byte(`{"data":[` + richItem + `,` + richItem + `],"pagination":{"cursor":"c","total":2}}`))
		}
	})

	for _, format := range []string{"text", "json", "yaml", "csv"} {
		for _, all := range []bool{false, true} {
			label := format
			if all {
				label += "-all"
			}
			//nolint:paralleltest // process-global stdio and HOME
			t.Run(label, func(t *testing.T) { runRoutedWith(t, format, all, handler) })
		}
	}
}
