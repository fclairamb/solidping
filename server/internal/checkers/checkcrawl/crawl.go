package checkcrawl

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	crawlconfig "github.com/fclairamb/solidping/server/internal/checkers/checkcrawl/config"
	"github.com/fclairamb/solidping/server/internal/version"
)

// robotsAgentToken is the product token robots.txt groups are matched on.
const robotsAgentToken = "solidping-crawler"

// minSliceRemaining is the budget below which no new batch is started.
const minSliceRemaining = 300 * time.Millisecond

func userAgent() string {
	return "SolidPing-Crawler/" + strings.TrimPrefix(version.Version, "v") + " (+https://solidping.io/bot)"
}

// runner executes one slice against a loaded state.
type runner struct {
	cfg       *CrawlConfig
	state     *crawlState
	seen      map[uint64]struct{}
	findingFP map[string]struct{}
	client    *http.Client
	start     *url.URL
	hosts     map[string]bool // internal hosts
	includes  []string        // path patterns
	excludes  []string
	unitsLeft int
	used      int
}

func newRunner(cfg *CrawlConfig, state *crawlState, client *http.Client, maxUnits int) (*runner, error) {
	start, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, err
	}

	run := &runner{
		cfg:       cfg,
		state:     state,
		seen:      decodeSeen(state.Seen),
		findingFP: make(map[string]struct{}, len(state.Findings)),
		client:    client,
		start:     start,
		hosts:     map[string]bool{hostKey(start): true},
		unitsLeft: maxUnits,
	}

	for i := range state.Findings {
		run.findingFP[state.Findings[i].Fingerprint] = struct{}{}
	}

	run.includes, run.excludes = run.patterns()

	return run, nil
}

// patterns splits include into host grants and path patterns: an include
// that does not start with "/" names a host (optionally followed by a path)
// that counts as internal, which is how www and apex are joined.
func (r *runner) patterns() ([]string, []string) {
	var includes []string

	for _, pattern := range r.cfg.Include {
		if strings.HasPrefix(pattern, "/") {
			includes = append(includes, pattern)

			continue
		}

		host, path, _ := strings.Cut(pattern, "/")
		r.hosts[strings.ToLower(host)] = true

		if path != "" {
			includes = append(includes, "/"+path)
		}
	}

	return includes, r.cfg.Exclude
}

// hostKey is the scheme-less identity of a URL's host: the lowercase host
// name, plus the port when it is not a default one (so http://x and
// https://x are the same site, x:8080 is another).
func hostKey(target *url.URL) string {
	host := strings.ToLower(target.Hostname())

	switch port := target.Port(); port {
	case "", "80", "443":
		return host
	default:
		return host + ":" + port
	}
}

// initialState builds the state of a run's first slice.
func initialState(cfg *CrawlConfig, now time.Time) *crawlState {
	state := &crawlState{
		Phase:       phaseCrawl,
		Queue:       []queueItem{{U: cfg.URL}},
		StartedAtMs: now.UnixMilli(),
		Admitted:    1,
		Sitemap:     sitemapState{Status: "off"},
	}

	seen := map[uint64]struct{}{urlHash(cfg.URL): {}}
	state.Seen = encodeSeen(seen)

	mode := cfg.SitemapMode()

	switch {
	case cfg.Robots() || mode == crawlconfig.SitemapAuto:
		state.Phase = phaseRobots
	case mode != crawlconfig.SitemapOff:
		state.Phase = phaseSitemap
		state.Sitemap.Pending = []sitemapItem{{U: mode}}
	}

	if mode != crawlconfig.SitemapOff {
		state.Sitemap.Status = "pending"
	}

	return state
}

// runSlice advances the crawl until the units or the slice context run out,
// or the run is complete. It reports whether the run is complete.
func (r *runner) runSlice(ctx context.Context) bool {
	for r.unitsLeft > 0 && !sliceExhausted(ctx) {
		switch r.state.Phase {
		case phaseRobots:
			if !r.stepRobots(ctx) {
				return false
			}
		case phaseSitemap:
			if !r.stepSitemap(ctx) {
				return false
			}
		default:
			if len(r.state.Queue) == 0 {
				return true
			}

			if !r.stepBatch(ctx) {
				return false
			}
		}
	}

	return r.state.Phase == phaseCrawl && len(r.state.Queue) == 0
}

func sliceExhausted(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}

	deadline, ok := ctx.Deadline()

	return ok && time.Until(deadline) < minSliceRemaining
}

func (r *runner) spend() {
	r.unitsLeft--
	r.used++
	r.state.Units++
}

// stepRobots fetches robots.txt (one unit). False means the fetch was cut by
// the slice deadline and must be retried next slice.
func (r *runner) stepRobots(ctx context.Context) bool {
	robotsURL := r.start.Scheme + "://" + r.start.Host + "/robots.txt"

	reqCtx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout())
	body, status, errText := r.readDocument(reqCtx, robotsURL)

	cancel()

	if errText != "" && ctx.Err() != nil {
		return false
	}

	r.spend()

	var parsed parsedRobots

	r.state.Robots.Fetched = errText == "" && status < statusBroken
	if r.state.Robots.Fetched {
		parsed = parseRobots(body)
	}

	if r.cfg.Robots() {
		r.state.Robots.Rules = parsed.rules
	}

	r.state.Phase = phaseSitemap

	switch mode := r.cfg.SitemapMode(); mode {
	case crawlconfig.SitemapOff:
		r.state.Phase = phaseCrawl
	case crawlconfig.SitemapAuto:
		for _, sitemapURL := range parsed.sitemaps {
			r.state.Sitemap.Pending = append(r.state.Sitemap.Pending, sitemapItem{U: sitemapURL})
		}

		if len(r.state.Sitemap.Pending) == 0 {
			guess := r.start.Scheme + "://" + r.start.Host + "/sitemap.xml"
			r.state.Sitemap.Pending = []sitemapItem{{U: guess, Optional: true}}
		}
	default:
		r.state.Sitemap.Pending = []sitemapItem{{U: mode}}
	}

	return true
}

// stepSitemap fetches the next pending sitemap (one unit).
func (r *runner) stepSitemap(ctx context.Context) bool {
	if len(r.state.Sitemap.Pending) == 0 || r.state.Sitemap.Fetched >= maxSitemapFetches {
		r.state.Sitemap.Pending = nil
		r.state.Phase = phaseCrawl

		if r.state.Sitemap.Status == "pending" {
			r.state.Sitemap.Status = "done"
		}

		return true
	}

	item := r.state.Sitemap.Pending[0]

	reqCtx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout())
	body, status, errText := r.readDocument(reqCtx, item.U)

	cancel()

	if errText != "" && ctx.Err() != nil {
		return false
	}

	r.spend()
	r.state.Sitemap.Pending = r.state.Sitemap.Pending[1:]
	r.state.Sitemap.Fetched++

	switch {
	case item.Optional && status == http.StatusNotFound:
		// auto mode's /sitemap.xml guess: no sitemap is not an error.
	case errText != "" || status >= statusBroken:
		finding := newFinding(findingSitemapError, item.U, "", status, errText)
		r.addFinding(&finding)
	default:
		r.absorbSitemap(item.U, body)
	}

	return true
}

func (r *runner) absorbSitemap(sitemapURL string, body []byte) {
	parsed, err := parseSitemap(body)
	if err != nil {
		finding := newFinding(findingSitemapError, sitemapURL, "", 0, "invalid XML: "+err.Error())
		r.addFinding(&finding)

		return
	}

	for _, child := range parsed.children {
		r.state.Sitemap.Pending = append(r.state.Sitemap.Pending, sitemapItem{U: child})
	}

	for _, page := range parsed.pages {
		target, parseErr := url.Parse(page)
		if parseErr != nil || !r.isInternal(target) || !r.allowed(target) {
			continue
		}

		target.Fragment = ""
		r.state.Sitemap.URLs++
		r.admit(queueItem{U: target.String(), D: 1, Src: sitemapURL, K: kindSitemap}, true)
	}
}

// stepBatch fetches up to `concurrency` queued URLs in parallel, then merges
// the outcomes in queue order so the findings never depend on timing.
func (r *runner) stepBatch(ctx context.Context) bool {
	size := min(r.cfg.EffectiveConcurrency(), r.unitsLeft, len(r.state.Queue))
	batch := r.state.Queue[:size]
	outcomes := make([]itemOutcome, size)

	var group sync.WaitGroup

	for i := range batch {
		group.Go(func() {
			outcomes[i] = r.processItem(ctx, batch[i])
		})
	}

	group.Wait()

	for i := range outcomes {
		if outcomes[i].cut {
			// Everything from the first cut item on goes back on the queue,
			// in order, so a slice boundary never reorders discovery.
			r.state.Queue = append(append([]queueItem{}, batch[i:]...), r.state.Queue[size:]...)

			return false
		}

		r.spend()
		r.merge(batch[i], &outcomes[i])
	}

	r.state.Queue = r.state.Queue[size:]

	if delay := r.cfg.Delay(); delay > 0 && len(r.state.Queue) > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()

		select {
		case <-ctx.Done():
		case <-timer.C:
		}
	}

	return true
}
