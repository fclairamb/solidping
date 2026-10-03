package checkcrawl

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	crawlconfig "github.com/fclairamb/solidping/server/internal/checkers/checkcrawl/config"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// site is a tiny httptest website: path → handler, with a hit counter.
type site struct {
	mu     sync.Mutex
	pages  map[string]http.HandlerFunc
	hits   map[string]int
	server *httptest.Server
}

func newSite(t *testing.T, tlsServer bool) *site {
	t.Helper()

	s := &site{pages: map[string]http.HandlerFunc{}, hits: map[string]int{}}
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		s.mu.Lock()
		s.hits[req.URL.Path]++
		page, ok := s.pages[req.URL.Path]
		s.mu.Unlock()

		if !ok {
			http.NotFound(w, req)

			return
		}

		page(w, req)
	})

	if tlsServer {
		s.server = httptest.NewTLSServer(handler)
	} else {
		s.server = httptest.NewServer(handler)
	}

	t.Cleanup(s.server.Close)

	return s
}

func (s *site) html(path, body string) {
	s.pages[path] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, "<html><body>"+body+"</body></html>")
	}
}

func (s *site) text(path, contentType, body string) {
	s.pages[path] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = fmt.Fprint(w, body)
	}
}

func (s *site) status(path string, code int) {
	s.pages[path] = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
}

func (s *site) redirect(path, to string) {
	s.pages[path] = func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, to, http.StatusFound)
	}
}

func (s *site) hitCount(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.hits[path]
}

func (s *site) url(path string) string { return s.server.URL + path }

// crawl runs a whole crawl through Step, maxUnits units per slice.
func crawl(t *testing.T, checker *CrawlChecker, cfgMap map[string]any, maxUnits int) checkerdef.StepOutput {
	t.Helper()

	if _, ok := cfgMap["delayMs"]; !ok {
		cfgMap["delayMs"] = 0
	}

	cfg := &CrawlConfig{}
	require.NoError(t, cfg.FromMap(cfgMap))
	require.NoError(t, cfg.Validate())

	var state []byte

	for range 10000 {
		out, err := checker.Step(t.Context(), cfg, checkerdef.StepInput{
			State: state, Budget: 10 * time.Second, MaxUnits: maxUnits,
		})
		require.NoError(t, err)

		if out.Done {
			require.NotNil(t, out.Result)

			return out
		}

		require.LessOrEqual(t, out.Units, maxUnits, "a slice never uses more than its units")
		state = out.State
	}

	t.Fatal("crawl never finished")

	return checkerdef.StepOutput{}
}

func reportOf(t *testing.T, out checkerdef.StepOutput) Report {
	t.Helper()

	var report Report
	require.NoError(t, json.Unmarshal(out.Report, &report))

	return report
}

func findingsOfType(report Report, findingType string) []Finding {
	var out []Finding

	for _, finding := range report.Findings {
		if finding.Type == findingType {
			out = append(out, finding)
		}
	}

	return out
}

func urlsOf(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, finding := range findings {
		out = append(out, finding.URL)
	}

	return out
}

func TestCrawlBrokenInternalLinks(t *testing.T) {
	t.Parallel()

	web := newSite(t, false)
	web.html("/", `<a href="/ok">ok</a><a href="/404">x</a><a href="/500">y</a><a href="/loop1">z</a>`)
	web.html("/ok", `fine`)
	web.status("/500", http.StatusInternalServerError)
	web.redirect("/loop1", "/loop2")
	web.redirect("/loop2", "/loop1")

	out := crawl(t, &CrawlChecker{}, map[string]any{"url": web.url("/"), "sitemap": "off"}, 20)
	report := reportOf(t, out)

	broken := findingsOfType(report, findingBrokenLink)
	require.ElementsMatch(t, []string{web.url("/404"), web.url("/500"), web.url("/loop1")}, urlsOf(broken))

	for _, finding := range broken {
		require.Equal(t, web.url("/"), finding.Source)
		require.Len(t, finding.Fingerprint, 40)
	}

	require.Equal(t, checkerdef.StatusDown, out.Result.Status)
	require.Contains(t, out.Result.Output[checkerdef.OutputKeyError], "broken_link")
	require.Equal(t, 5, out.Result.Output[OutputKeyPagesCrawled])
	require.False(t, report.Incomplete)
}

func TestCrawlBrokenExternalLinkIsAWarning(t *testing.T) {
	t.Parallel()

	ext := newSite(t, false)
	ext.html("/fine", `<a href="/never-followed">x</a>`)

	web := newSite(t, false)
	web.html("/", fmt.Sprintf(`<a href="%s">gone</a><a href="%s">fine</a>`, ext.url("/missing"), ext.url("/fine")))

	out := crawl(t, &CrawlChecker{}, map[string]any{"url": web.url("/"), "sitemap": "off"}, 20)
	report := reportOf(t, out)

	require.Equal(t, []string{ext.url("/missing")}, urlsOf(findingsOfType(report, findingBrokenExternalLink)))
	require.Equal(t, checkerdef.StatusWarning, out.Result.Status, "external breakage is a warning by default")
	require.Zero(t, ext.hitCount("/never-followed"), "links on another host are never followed")
	require.Equal(t, 1, ext.hitCount("/fine"), "an external URL is checked once per run")

	// failOn opts in to down.
	out = crawl(t, &CrawlChecker{}, map[string]any{
		"url": web.url("/"), "sitemap": "off", "failOn": []any{"broken_external_link"},
	}, 20)
	require.Equal(t, checkerdef.StatusDown, out.Result.Status)

	// checkExternalLinks off: nothing external is fetched.
	out = crawl(t, &CrawlChecker{}, map[string]any{
		"url": web.url("/"), "sitemap": "off", "checkExternalLinks": false,
	}, 20)
	require.Empty(t, reportOf(t, out).Findings)
	require.Equal(t, checkerdef.StatusUp, out.Result.Status)
}

func TestCrawlMixedContent(t *testing.T) {
	t.Parallel()

	body := `<script src="http://cdn.acme.com/a.js"></script>
<link rel="stylesheet" href="http://cdn.acme.com/s.css">
<iframe src="http://cdn.acme.com/frame"></iframe>
<img src="http://cdn.acme.com/i.png" srcset="http://cdn.acme.com/i2.png 2x">
<video src="http://cdn.acme.com/v.mp4"></video>
<a href="http://cdn.acme.com/page">a plain link is not mixed content</a>
<form action="http://cdn.acme.com/post"></form>`

	secure := newSite(t, true)
	secure.html("/", body)

	checker := &CrawlChecker{transport: secure.server.Client().Transport}
	out := crawl(t, checker, map[string]any{
		"url": secure.url("/"), "sitemap": "off", "checkExternalLinks": false, "respectRobots": false,
	}, 20)
	report := reportOf(t, out)

	require.ElementsMatch(t, []string{
		"http://cdn.acme.com/a.js", "http://cdn.acme.com/s.css",
		"http://cdn.acme.com/frame", "http://cdn.acme.com/post",
	}, urlsOf(findingsOfType(report, findingMixedContentActive)))
	require.ElementsMatch(t, []string{
		"http://cdn.acme.com/i.png", "http://cdn.acme.com/i2.png", "http://cdn.acme.com/v.mp4",
	}, urlsOf(findingsOfType(report, findingMixedContentPassive)))
	require.Equal(t, checkerdef.StatusDown, out.Result.Status)

	// The same page over plain http has no mixed content.
	plain := newSite(t, false)
	plain.html("/", body)

	out = crawl(t, &CrawlChecker{}, map[string]any{
		"url": plain.url("/"), "sitemap": "off", "checkExternalLinks": false,
	}, 20)
	require.Empty(t, reportOf(t, out).Findings)
}

func TestCrawlSitemap(t *testing.T) {
	t.Parallel()

	t.Run("missing in auto mode is not an error", func(t *testing.T) {
		t.Parallel()

		web := newSite(t, false)
		web.html("/", `hello`)

		out := crawl(t, &CrawlChecker{}, map[string]any{"url": web.url("/")}, 20)
		require.Empty(t, reportOf(t, out).Findings)
		require.Equal(t, 1, web.hitCount("/sitemap.xml"))
		require.Equal(t, checkerdef.StatusUp, out.Result.Status)
	})

	t.Run("invalid XML", func(t *testing.T) {
		t.Parallel()

		web := newSite(t, false)
		web.html("/", `hello`)
		web.text("/sitemap.xml", "application/xml", `<urlset><url><loc>broken`)

		out := crawl(t, &CrawlChecker{}, map[string]any{"url": web.url("/")}, 20)
		errs := findingsOfType(reportOf(t, out), findingSitemapError)
		require.Len(t, errs, 1)
		require.Contains(t, errs[0].Error, "invalid XML")
		require.Equal(t, checkerdef.StatusDown, out.Result.Status)
	})

	t.Run("listed URL returning 404, from robots.txt Sitemap line", func(t *testing.T) {
		t.Parallel()

		web := newSite(t, false)
		web.html("/", `hello`)
		web.html("/listed", `listed page`)
		web.text("/robots.txt", "text/plain", "User-agent: *\nDisallow:\nSitemap: "+web.url("/maps/site.xml")+"\n")
		web.text("/maps/site.xml", "application/xml", fmt.Sprintf(
			`<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`+
				`<url><loc>%s</loc></url><url><loc>%s</loc></url></urlset>`,
			web.url("/listed"), web.url("/gone")))

		out := crawl(t, &CrawlChecker{}, map[string]any{"url": web.url("/")}, 20)
		errs := findingsOfType(reportOf(t, out), findingSitemapError)
		require.Equal(t, []string{web.url("/gone")}, urlsOf(errs))
		require.Equal(t, web.url("/maps/site.xml"), errs[0].Source)
		require.Equal(t, 1, web.hitCount("/listed"), "sitemap URLs are crawled")
		require.Zero(t, web.hitCount("/sitemap.xml"), "the robots.txt Sitemap line wins over the guess")
	})

	t.Run("explicit sitemap unreachable", func(t *testing.T) {
		t.Parallel()

		web := newSite(t, false)
		web.html("/", `hello`)

		out := crawl(t, &CrawlChecker{}, map[string]any{"url": web.url("/"), "sitemap": web.url("/nope.xml")}, 20)
		require.Len(t, findingsOfType(reportOf(t, out), findingSitemapError), 1)
	})
}

func TestCrawlRobotsAndExclude(t *testing.T) {
	t.Parallel()

	web := newSite(t, false)
	web.html("/", `<a href="/private/a">p</a><a href="/skip/b">s</a><a href="/public">ok</a>`)
	web.html("/public", `ok`)
	web.text("/robots.txt", "text/plain", "User-agent: *\nDisallow: /private\n")

	out := crawl(t, &CrawlChecker{}, map[string]any{
		"url": web.url("/"), "sitemap": "off", "exclude": []any{"/skip"},
	}, 20)

	require.Empty(t, reportOf(t, out).Findings, "skipped paths are not reported")
	require.Zero(t, web.hitCount("/private/a"), "robots Disallow respected")
	require.Zero(t, web.hitCount("/skip/b"), "exclude respected")
	require.Equal(t, 1, web.hitCount("/public"))

	// The opt-out crawls the disallowed path (and finds it broken).
	out = crawl(t, &CrawlChecker{}, map[string]any{
		"url": web.url("/"), "sitemap": "off", "respectRobots": false, "exclude": []any{"/skip"},
	}, 20)
	require.Equal(t, []string{web.url("/private/a")}, urlsOf(findingsOfType(reportOf(t, out), findingBrokenLink)))
}

func TestCrawlMaxPagesStopsTheCrawl(t *testing.T) {
	t.Parallel()

	web := newSite(t, false)
	for i := range 30 {
		web.html(fmt.Sprintf("/p%d", i), fmt.Sprintf(`<a href="/p%d">next</a>`, i+1))
	}

	web.html("/", `<a href="/p0">start</a>`)

	out := crawl(t, &CrawlChecker{}, map[string]any{"url": web.url("/"), "sitemap": "off", "maxPages": 5}, 20)
	require.Equal(t, 5, out.Result.Output[OutputKeyPagesCrawled])
	require.Zero(t, web.hitCount("/p10"))
}

// Slicing never changes the findings: one URL per slice and one big slice
// crawl the same site to the same report.
func TestCrawlSlicingInvariance(t *testing.T) {
	t.Parallel()

	ext := newSite(t, false)
	ext.html("/ok", `ok`)

	web := newSite(t, false)
	web.html("/", fmt.Sprintf(`<a href="/a">a</a><a href="/b">b</a><a href="%s">e</a><a href="%s">e2</a>`,
		ext.url("/ok"), ext.url("/gone")))
	web.html("/a", `<a href="/c">c</a><a href="/404a">x</a><img src="/img.png">`)
	web.html("/b", `<a href="/c">c</a><a href="/404b">x</a><a href="/a">a</a>`)
	web.html("/c", `<a href="/d">d</a><script src="/missing.js"></script>`)
	web.html("/d", `end`)
	web.text("/img.png", "image/png", "png")
	web.text("/sitemap.xml", "application/xml", fmt.Sprintf(
		`<urlset><url><loc>%s</loc></url><url><loc>%s</loc></url></urlset>`, web.url("/d"), web.url("/s404")))

	cfg := func() map[string]any { return map[string]any{"url": web.url("/"), "concurrency": 3} }

	small := crawl(t, &CrawlChecker{}, cfg(), 1)
	big := crawl(t, &CrawlChecker{}, cfg(), 1000)

	require.Equal(t, reportOf(t, big), reportOf(t, small))
	require.NotEmpty(t, reportOf(t, big).Findings)
	require.Greater(t, small.Result.Metrics["slices"], big.Result.Metrics["slices"])
}

func TestCrawlFinalSliceReturnsWhatItHas(t *testing.T) {
	t.Parallel()

	web := newSite(t, false)
	web.html("/", `<a href="/a">a</a><a href="/b">b</a>`)
	web.html("/a", `a`)
	web.html("/b", `b`)

	cfg := &CrawlConfig{}
	require.NoError(t, cfg.FromMap(map[string]any{"url": web.url("/"), "sitemap": "off", "delayMs": 0}))

	checker := &CrawlChecker{}
	first, err := checker.Step(t.Context(), cfg, checkerdef.StepInput{Budget: 5 * time.Second, MaxUnits: 2})
	require.NoError(t, err)
	require.False(t, first.Done)
	require.Equal(t, 2, first.Units)
	require.Equal(t, 1, first.Progress["pagesDone"])

	final, err := checker.Step(t.Context(), cfg, checkerdef.StepInput{
		State: first.State, Budget: 5 * time.Second, MaxUnits: 20, Final: true,
	})
	require.NoError(t, err)
	require.True(t, final.Done)
	require.Equal(t, true, final.Result.Output[OutputKeyIncomplete])
	require.Equal(t, checkerdef.StatusWarning, final.Result.Status, "an incomplete run is a warning")
	require.Zero(t, web.hitCount("/b"), "a Final slice fetches nothing")
}

func TestCrawlNewFindingsDiff(t *testing.T) {
	t.Parallel()

	web := newSite(t, false)
	web.html("/", `<a href="/old">x</a><a href="/new">y</a>`)

	checker := &CrawlChecker{}
	out := crawl(t, checker, map[string]any{"url": web.url("/"), "sitemap": "off"}, 20)

	newFindings, ok := out.Result.Output[OutputKeyNewFindings].(map[string]any)
	require.True(t, ok)
	require.Equal(t, 2, newFindings["count"], "with no previous report every finding is new")

	previous, err := json.Marshal(Report{Findings: []Finding{
		newFinding(findingBrokenLink, web.url("/old"), web.url("/"), http.StatusNotFound, ""),
	}})
	require.NoError(t, err)

	checker.DiffAgainst(previous, &out)

	newFindings, ok = out.Result.Output[OutputKeyNewFindings].(map[string]any)
	require.True(t, ok)
	require.Equal(t, 1, newFindings["count"])

	items, ok := newFindings["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	first, ok := items[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, web.url("/new"), first["url"])

	errText, ok := out.Result.Output[checkerdef.OutputKeyError].(string)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(errText, "1 new finding"))
}

func TestCrawlExecuteRunsAWholeCrawl(t *testing.T) {
	t.Parallel()

	web := newSite(t, false)
	web.html("/", `<a href="/a">a</a>`)
	web.html("/a", `a`)

	cfg := &CrawlConfig{}
	require.NoError(t, cfg.FromMap(map[string]any{"url": web.url("/"), "sitemap": "off", "delayMs": 0}))

	result, err := (&CrawlChecker{}).Execute(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, checkerdef.StatusUp, result.Status)
	require.Equal(t, 2, result.Output[OutputKeyPagesCrawled])
}

// A clean crawl's report carries an empty findings list, never null: the
// attachment rail refuses a report without a findings array.
func TestCrawlCleanReportHasAnEmptyFindingsList(t *testing.T) {
	t.Parallel()

	web := newSite(t, false)
	web.html("/", `clean`)

	out := crawl(t, &CrawlChecker{}, map[string]any{"url": web.url("/"), "sitemap": "off"}, 20)
	require.Contains(t, string(out.Report), `"findings":[]`)
}

func TestCrawlConfigValidation(t *testing.T) {
	t.Parallel()

	cases := map[string]map[string]any{
		"no url":         {},
		"ftp url":        {"url": "ftp://acme.com"},
		"maxPages 0":     {"url": "https://acme.com", "maxPages": 0},
		"maxPages 2001":  {"url": "https://acme.com", "maxPages": 2001},
		"concurrency 5":  {"url": "https://acme.com", "concurrency": 5},
		"delay too long": {"url": "https://acme.com", "delayMs": 5001},
		"timeout 31s":    {"url": "https://acme.com", "timeout": "31s"},
		"run 3h":         {"url": "https://acme.com", "maxRunDuration": "3h"},
		"bad failOn":     {"url": "https://acme.com", "failOn": []any{"nope"}},
		"bad sitemap":    {"url": "https://acme.com", "sitemap": "sometimes"},
		"21 excludes":    {"url": "https://acme.com", "exclude": make([]any, 21)},
	}

	for name, cfgMap := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if list, ok := cfgMap["exclude"].([]any); ok {
				for i := range list {
					list[i] = fmt.Sprintf("/p%d", i)
				}
			}

			err := crawlconfig.ValidateSpec(&checkerdef.CheckSpec{Config: cfgMap})
			require.Error(t, err)

			var cfgErr *checkerdef.ConfigError
			require.ErrorAs(t, err, &cfgErr)
		})
	}

	spec := &checkerdef.CheckSpec{Config: map[string]any{"url": "https://www.acme.com/", "maxPages": 2000}}
	require.NoError(t, crawlconfig.ValidateSpec(spec))
	require.Equal(t, "Crawl: www.acme.com", spec.Name)

	cfg := &CrawlConfig{}
	require.NoError(t, cfg.FromMap(map[string]any{"url": "https://acme.com", "maxPages": 500}))
	require.Equal(t, 500, cfg.UnitsPerRunHint())
	require.Equal(t, crawlconfig.DefaultMaxRunDuration, cfg.MaxRunDuration())

	roundTrip := &CrawlConfig{}
	require.NoError(t, roundTrip.FromMap(cfg.GetConfig()))
	require.Equal(t, cfg, roundTrip)
}
