package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The multi-step run endpoints (spec 2026-10-03-03) are on the published API
// surface: a route the server serves but the spec omits is one no generated
// client can call.
func TestOpenAPI_CheckRunRoutesDocumented(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	spec := loadOpenAPIPaths(t)

	run, ok := spec.Paths["/api/v1/orgs/{org}/checks/{checkUid}/run"]
	r.True(ok, "the run path must be documented")
	r.Equal("getCheckRun", run["get"].OperationID)
	r.Contains(run["get"].Responses, "200")
	r.Equal("cancelCheckRun", run["delete"].OperationID)
	r.Contains(run["delete"].Responses, "204")

	reports, ok := spec.Paths["/api/v1/orgs/{org}/checks/{checkUid}/crawl-reports"]
	r.True(ok, "the crawl reports path must be documented")
	r.Equal("listCheckCrawlReports", reports["get"].OperationID)
	r.Contains(reports["get"].Responses, "200")

	for _, name := range []string{"CheckRun", "CrawlReport", "CrawlReportListResponse"} {
		_, declared := spec.Components.Schemas[name]
		r.True(declared, "%s must be declared in components", name)
	}
}
