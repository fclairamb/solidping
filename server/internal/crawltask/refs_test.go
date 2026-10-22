package crawltask

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/paramkeys"
	"github.com/fclairamb/solidping/server/internal/secretref"
)

// A `${param:}` in the crawl url is resolved server-side at claim time: the
// resolved site is crawled, while the stored config and the result keep the
// reference.
func TestParamRefResolvedAtClaim(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, newSQLite(t), nil)
	web := site(t, 3)
	region := "eu-param"

	host, err := url.Parse(web.URL)
	r.NoError(err)
	r.NoError(h.db.SetOrgParameter(t.Context(), h.org.UID, paramkeys.StorageKey("crawl_host"), host.Host, false))

	cfg := siteConfig(web.URL)
	cfg["url"] = "http://${param:crawl_host}/"
	check := h.crawl(h.org.UID, region, cfg)

	since := time.Now()
	h.runToEnd(cloudWorker(t, h.db, region), check)

	results := h.results(check, since)
	r.Len(results, 1)
	r.Equal(int(models.ResultStatusDown), *results[0].Status, "the resolved site was crawled (its broken link)")
	r.GreaterOrEqual(results[0].Output["pagesCrawled"], float64(4))
	r.Equal("http://${param:crawl_host}/", results[0].Output["url"], "the result shows the reference")
	r.Equal("http://${param:crawl_host}/", h.job(check).Config["url"], "the stored config keeps the reference")
}

// An unresolvable `${param:}` ends the run on an error result at claim time;
// nothing is fetched.
func TestMissingParamRefIsAnErrorResult(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, newSQLite(t), nil)
	web, hits := countingSite(t)
	region := "eu-noparam"

	cfg := siteConfig(web.URL)
	cfg["url"] = web.URL + "/${param:missing}"
	check := h.crawl(h.org.UID, region, cfg)

	since := time.Now()
	r.Nil(h.slice(cloudWorker(t, h.db, region)), "the claim ends the run without a slice")

	results := h.results(check, since)
	r.Len(results, 1)
	r.Equal(int(models.ResultStatusError), *results[0].Status)
	r.Contains(results[0].Output["error"], "param:missing")
	r.Zero(hits.Load(), "the literal reference is never fetched")

	tasks := h.tasksOf(check)
	r.Len(tasks, 1)
	r.False(tasks[0].IsActive())
	r.Nil(h.job(check).LeaseExpiresAt, "the job is released")
}

// An agent claim carries no resolved `${param:}`: like an org agent over the
// WebSocket, the agent finds the reference unresolved and ends the run on an
// error result naming it, and the site is never fetched. The cloud tests
// above are the positive control.
func TestAgentClaimCarriesNoParamValue(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, newSQLite(t), nil)
	web, hits := countingSite(t)
	region := "@dc-param"

	host, err := url.Parse(web.URL)
	r.NoError(err)
	r.NoError(h.db.SetOrgParameter(t.Context(), h.org.UID, paramkeys.StorageKey("crawl_host"), host.Host, false))

	cfg := siteConfig(web.URL)
	cfg["url"] = "http://${param:crawl_host}/"
	check := h.crawl(h.org.UID, region, cfg)

	since := time.Now()
	claim := h.slice(agentWorker(h.org.UID, region))
	r.NotNil(claim, "the agent claims the run")
	r.NotContains(string(claim.Input), host.Host, "the resolved value is not in the claim")

	var input Input
	r.NoError(json.Unmarshal(claim.Input, &input))
	r.Empty(input.ParamOverlay)
	r.Equal("http://${param:crawl_host}/", input.Config["url"], "the agent gets the reference")

	results := h.results(check, since)
	r.Len(results, 1)
	r.Equal(int(models.ResultStatusError), *results[0].Status)
	r.Contains(results[0].Output["error"], secretref.ErrUnresolved.Error())
	r.Contains(results[0].Output["error"], "param:crawl_host")
	r.NotContains(results[0].Output["error"], host.Host)
	r.Zero(hits.Load(), "neither the value nor the literal reference is fetched")

	tasks := h.tasksOf(check)
	r.Len(tasks, 1)
	r.False(tasks[0].IsActive(), "the run is over")
}

// `${env:}` resolves in the executor, against the worker's own environment
// and allowlist; one it may not read is an error result, nothing fetched.
func TestEnvRefResolvedByTheExecutor(t *testing.T) {
	r := require.New(t)
	h := newHarness(t, newSQLite(t), nil)
	web := site(t, 3)
	region := "eu-env"
	t.Setenv("SP_TEST_CRAWL_SITE", web.URL)

	cfg := siteConfig(web.URL)
	cfg["url"] = "${env:SP_TEST_CRAWL_SITE}/"
	check := h.crawl(h.org.UID, region, cfg)

	// Not on the allowlist: an error result, no request.
	since := time.Now()
	h.runToEnd(cloudWorker(t, h.db, region), check)

	results := h.results(check, since)
	r.Len(results, 1)
	r.Equal(int(models.ResultStatusError), *results[0].Status)
	r.Contains(results[0].Output["error"], "SP_TEST_CRAWL_SITE")

	// Allowed: the worker's environment names the site.
	h.exec.allowEnv = secretref.AllowEnvNames("SP_TEST_CRAWL_SITE")
	h.due(check)

	since = time.Now()
	h.runToEnd(cloudWorker(t, h.db, region), check)

	results = h.results(check, since)
	r.Len(results, 1)
	r.Equal(int(models.ResultStatusDown), *results[0].Status, "the site was crawled")
	r.Equal("${env:SP_TEST_CRAWL_SITE}/", results[0].Output["url"])
}

// countingSite is a site counting the requests it gets.
func countingSite(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()

	hits := new(atomic.Int64)
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(web.Close)

	return web, hits
}
