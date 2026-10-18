package checkvendorstatus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	idActions  = "br0l2tvcx85d"
	idGit      = "8l4ygp009s5s"
	idIssues   = "kr09ddfgbfsf"
	idWebhooks = "4230lsnqdsld"

	incidentLink = "https://stspg.io/abc123"
)

// summaryFixture loads the captured GitHub summary and lets a test derive
// variants from it.
func summaryFixture(t *testing.T, mutate func(doc map[string]any)) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("testdata", "github.json"))
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))

	if mutate != nil {
		mutate(doc)
	}

	out, err := json.Marshal(doc)
	require.NoError(t, err)

	return string(out)
}

func setIndicator(indicator string) func(map[string]any) {
	return func(doc map[string]any) {
		doc["status"] = map[string]any{"indicator": indicator, "description": "desc " + indicator}
	}
}

// withComponents sets the status of components by id and optionally adds incidents.
func withComponents(statuses map[string]string, incidents ...map[string]any) func(map[string]any) {
	return func(doc map[string]any) {
		components, _ := doc["components"].([]any)
		for _, item := range components {
			component, _ := item.(map[string]any)
			if status, ok := statuses[component["id"].(string)]; ok {
				component["status"] = status
			}
		}

		list := make([]any, 0, len(incidents))
		for _, incident := range incidents {
			list = append(list, incident)
		}

		doc["incidents"] = list
	}
}

func incident(name, impact, status string, ids ...string) map[string]any {
	affected := make([]any, 0, len(ids))
	for _, id := range ids {
		affected = append(affected, map[string]any{"id": id})
	}

	return map[string]any{
		"name": name, "impact": impact, "status": status, "shortlink": incidentLink,
		"updated_at": "2026-10-17T10:00:00Z", "components": affected,
	}
}

func serve(t *testing.T, status int, contentType, body string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != summaryPath {
			http.NotFound(w, r)

			return
		}

		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	return server
}

func run(t *testing.T, cfg *VendorStatusConfig) *checkerdef.Result {
	t.Helper()

	result, err := (&VendorStatusChecker{}).Execute(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, result)

	return result
}

func TestType(t *testing.T) {
	t.Parallel()
	require.Equal(t, checkerdef.CheckTypeVendorStatus, (&VendorStatusChecker{}).Type())
}

func TestWholePageIndicator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		indicator string
		want      checkerdef.Status
	}{
		{"none", checkerdef.StatusUp},
		{"maintenance", checkerdef.StatusUp},
		{"minor", checkerdef.StatusWarning},
		{"major", checkerdef.StatusDown},
		{"critical", checkerdef.StatusDown},
		{"surprise", checkerdef.StatusError},
	}

	for _, tt := range tests {
		t.Run(tt.indicator, func(t *testing.T) {
			t.Parallel()

			body := summaryFixture(t, setIndicator(tt.indicator))
			result := run(t, &VendorStatusConfig{URL: serve(t, 200, "application/json", body).URL})

			require.Equal(t, tt.want, result.Status)

			if tt.want == checkerdef.StatusUp {
				require.Empty(t, outputError(result))
			} else {
				require.NotEmpty(t, outputError(result))
			}
		})
	}
}

func TestSelectedComponents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		statuses map[string]string
		selected []string
		want     checkerdef.Status
	}{
		{"all operational", nil, []string{idActions, idGit, idIssues}, checkerdef.StatusUp},
		{"maintenance is up", map[string]string{idActions: "under_maintenance"}, []string{idActions}, checkerdef.StatusUp},
		{
			"degraded alone is a warning",
			map[string]string{idGit: "degraded_performance"},
			[]string{idActions, idGit, idIssues},
			checkerdef.StatusWarning,
		},
		{
			"partial outage is down",
			map[string]string{idGit: "partial_outage"},
			[]string{idActions, idGit},
			checkerdef.StatusDown,
		},
		{
			"worst of three wins",
			map[string]string{idActions: "degraded_performance", idIssues: "major_outage"},
			[]string{idActions, idGit, idIssues},
			checkerdef.StatusDown,
		},
		{
			"unselected major outage stays up",
			map[string]string{idWebhooks: "major_outage"},
			[]string{idActions, idGit},
			checkerdef.StatusUp,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := summaryFixture(t, withComponents(tt.statuses))
			result := run(t, &VendorStatusConfig{
				URL: serve(t, 200, "application/json", body).URL, Components: tt.selected,
			})

			require.Equal(t, tt.want, result.Status, outputError(result))
		})
	}
}

func TestMissingComponentIsAnErrorNamingTheID(t *testing.T) {
	t.Parallel()

	body := summaryFixture(t, nil)
	result := run(t, &VendorStatusConfig{
		URL: serve(t, 200, "application/json", body).URL, Components: []string{idActions, "gone-id"},
	})

	require.Equal(t, checkerdef.StatusError, result.Status)
	require.Contains(t, outputError(result), `"gone-id"`)
}

func TestUnreadablePagesAreErrorsNeverDown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		message     string
	}{
		{"404", 404, "text/plain", "not found", "HTTP 404"},
		{"500", 500, "text/plain", "boom", "HTTP 500"},
		{"HTML", 200, "text/html", "<html><body>status</body></html>", "not a Statuspage v2 summary"},
		{"another JSON shape", 200, "application/json", `{"page":{"status":"UP"}}`, "not a Statuspage v2 summary"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := run(t, &VendorStatusConfig{URL: serve(t, tt.status, tt.contentType, tt.body).URL})

			require.Equal(t, checkerdef.StatusError, result.Status)
			require.Contains(t, outputError(result), tt.message)
		})
	}
}

func TestUnreachableVendorIsNeverDown(t *testing.T) {
	t.Parallel()

	server := serve(t, 200, "application/json", "{}")
	url := server.URL

	server.Close()

	result := run(t, &VendorStatusConfig{URL: url})

	require.NotEqual(t, checkerdef.StatusDown, result.Status)
	require.Equal(t, checkerdef.StatusError, result.Status)
}

func TestErrorMessageCarriesTheIncident(t *testing.T) {
	t.Parallel()

	body := summaryFixture(t, func(doc map[string]any) {
		setIndicator("major")(doc)
		withComponents(map[string]string{idActions: "major_outage"},
			incident("Incident with Actions", "major", "investigating", idActions),
			incident("Resolved earlier", "minor", "resolved", idActions),
		)(doc)
	})

	result := run(t, &VendorStatusConfig{URL: serve(t, 200, "application/json", body).URL})

	require.Equal(t, checkerdef.StatusDown, result.Status)
	require.Equal(t, `GitHub: "Incident with Actions" (major) `+incidentLink, outputError(result))

	incidents, ok := result.Output["incidents"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, incidents, 1, "the resolved incident is left out")
	require.Equal(t, "Incident with Actions", incidents[0]["name"])
	require.Equal(t, incidentLink, incidents[0]["shortlink"])
	require.Equal(t, "2026-10-17T10:00:00Z", incidents[0]["updatedAt"])
}

func TestComponentModeOnlyReportsIncidentsOfSelectedComponents(t *testing.T) {
	t.Parallel()

	body := summaryFixture(t, withComponents(
		map[string]string{idActions: "partial_outage", idWebhooks: "major_outage"},
		incident("Webhooks are down", "major", "investigating", idWebhooks),
		incident("Actions are slow", "minor", "identified", idActions),
	))

	result := run(t, &VendorStatusConfig{
		URL: serve(t, 200, "application/json", body).URL, Components: []string{idActions},
	})

	require.Equal(t, checkerdef.StatusDown, result.Status)
	require.Equal(t, `GitHub: "Actions are slow" (minor) `+incidentLink, outputError(result))
}

func TestOutputListsEveryComponentOfThePage(t *testing.T) {
	t.Parallel()

	body := summaryFixture(t, nil)
	result := run(t, &VendorStatusConfig{
		URL: serve(t, 200, "application/json", body).URL, Components: []string{idActions},
	})

	components, ok := result.Output["components"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, components, 12, "not only the selected ones")
	require.Equal(t,
		map[string]any{"id": "8l4ygp009s5s", "name": "Git Operations", "status": "operational"}, components[0])
}

func TestFallbackMessageWithoutIncident(t *testing.T) {
	t.Parallel()

	body := summaryFixture(t, withComponents(map[string]string{idGit: "degraded_performance"}))
	result := run(t, &VendorStatusConfig{
		URL: serve(t, 200, "application/json", body).URL, Components: []string{idGit},
	})

	require.Equal(t, checkerdef.StatusWarning, result.Status)
	require.Equal(t, "GitHub: Git Operations is degraded performance", outputError(result))
}

func TestIncidentIOStyleSummaryWithoutIncidentsKey(t *testing.T) {
	t.Parallel()

	body := `{"page":{"name":"Acme"},"status":{"indicator":"none","description":"ok"},` +
		`"components":[{"id":"01A","name":"API","status":"operational"}]}`
	result := run(t, &VendorStatusConfig{URL: serve(t, 200, "application/json", body).URL, Components: []string{"01A"}})

	require.Equal(t, checkerdef.StatusUp, result.Status)
}

func TestListComponentsSkipsGroupHeaders(t *testing.T) {
	t.Parallel()

	body := `{"page":{"name":"Acme"},"status":{"indicator":"none"},"components":[` +
		`{"id":"g1","name":"Core","status":"operational","group":true},` +
		`{"id":"c1","name":"API","status":"operational","group_id":"g1"},` +
		`{"id":"c2","name":"Web","status":"operational"}]}`

	components, err := ListComponents(context.Background(), serve(t, 200, "application/json", body).URL)
	require.NoError(t, err)
	require.Equal(t, []Component{{ID: "c1", Name: "API", Group: "Core"}, {ID: "c2", Name: "Web"}}, components)

	_, err = ListComponents(context.Background(), serve(t, 500, "text/plain", "x").URL)
	require.ErrorIs(t, err, ErrFetch)
}
