package checkcrawl

import (
	"context"
	"io"
	"net/http"
	"net/url"
)

// itemOutcome is what fetching one queue item produced.
type itemOutcome struct {
	findings []Finding
	refs     []pageRef
	page     bool // an internal URL was fetched
	cut      bool
}

func (r *runner) processItem(ctx context.Context, item queueItem) itemOutcome {
	if item.K == kindExternal {
		return r.processExternal(ctx, item)
	}

	res := r.fetch(ctx, http.MethodGet, item.U, true)
	if res.cut {
		return itemOutcome{cut: true}
	}

	out := itemOutcome{page: true}

	if res.broken() {
		findingType := findingBrokenLink
		if item.K == kindSitemap {
			findingType = findingSitemapError
		}

		out.findings = append(out.findings, newFinding(findingType, item.U, item.Src, res.status, res.errText))

		return out
	}

	if res.body != nil && res.finalURL != nil && r.isInternal(res.finalURL) {
		out.refs = extractRefs(res.body, res.finalURL)
		out.findings = append(out.findings, r.mixedContent(res.finalURL, out.refs)...)
	}

	return out
}

func (r *runner) processExternal(ctx context.Context, item queueItem) itemOutcome {
	res := r.fetch(ctx, http.MethodHead, item.U, false)
	if res.cut {
		return itemOutcome{cut: true}
	}

	switch res.status {
	case http.StatusForbidden, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		res = r.fetch(ctx, http.MethodGet, item.U, false)
		if res.cut {
			return itemOutcome{cut: true}
		}
	}

	if !res.broken() {
		return itemOutcome{}
	}

	return itemOutcome{findings: []Finding{
		newFinding(findingBrokenExternalLink, item.U, item.Src, res.status, res.errText),
	}}
}

// mixedContent reports the http:// references of an https page.
func (r *runner) mixedContent(page *url.URL, refs []pageRef) []Finding {
	if !r.cfg.MixedContent() || page.Scheme != schemeHTTPS {
		return nil
	}

	var out []Finding

	for _, ref := range refs {
		if ref.url.Scheme != schemeHTTP {
			continue
		}

		switch ref.mixed {
		case mixedActive:
			out = append(out, newFinding(findingMixedContentActive, ref.url.String(), page.String(), 0, ""))
		case mixedPassive:
			out = append(out, newFinding(findingMixedContentPassive, ref.url.String(), page.String(), 0, ""))
		case mixedNone:
		}
	}

	return out
}

// merge folds one outcome into the state, in queue order.
func (r *runner) merge(item queueItem, outcome *itemOutcome) {
	if outcome.page {
		r.state.Pages++
	}

	for i := range outcome.findings {
		r.addFinding(&outcome.findings[i])
	}

	for _, ref := range outcome.refs {
		if !ref.check {
			continue
		}

		target := ref.url.String()

		if r.isInternal(ref.url) {
			if r.allowed(ref.url) {
				r.admit(queueItem{U: target, D: item.D + 1, Src: item.U}, true)
			}

			continue
		}

		if r.cfg.ExternalLinks() {
			r.admit(queueItem{U: target, D: item.D + 1, Src: item.U, K: kindExternal}, false)
		}
	}
}

// admit queues a URL once per run, within the page / external caps.
func (r *runner) admit(item queueItem, internal bool) {
	hash := urlHash(item.U)
	if _, dup := r.seen[hash]; dup {
		return
	}

	if internal {
		if r.state.Admitted >= r.cfg.EffectiveMaxPages() {
			return
		}

		r.state.Admitted++
	} else {
		if r.state.Externals >= maxExternalLinks {
			return
		}

		r.state.Externals++
	}

	r.seen[hash] = struct{}{}
	r.state.Queue = append(r.state.Queue, item)
}

func (r *runner) addFinding(finding *Finding) {
	if _, dup := r.findingFP[finding.Fingerprint]; dup || len(r.state.Findings) >= maxFindings {
		return
	}

	r.findingFP[finding.Fingerprint] = struct{}{}
	r.state.Findings = append(r.state.Findings, *finding)
}

// isInternal reports whether target is on a crawled host (scheme-less).
func (r *runner) isInternal(target *url.URL) bool {
	return r.hosts[hostKey(target)]
}

// allowed applies robots.txt, include and exclude to an internal URL.
// Skipped URLs are neither fetched nor reported.
func (r *runner) allowed(target *url.URL) bool {
	path := target.EscapedPath()
	if path == "" {
		path = "/"
	}

	if target.RawQuery != "" {
		path += "?" + target.RawQuery
	}

	if r.cfg.Robots() && !robotsAllowed(r.state.Robots.Rules, path) {
		return false
	}

	for _, pattern := range r.excludes {
		if globMatch(pattern, path, false) {
			return false
		}
	}

	if len(r.includes) == 0 {
		return true
	}

	for _, pattern := range r.includes {
		if globMatch(pattern, path, false) {
			return true
		}
	}

	return false
}

// readDocument GETs a small document (sitemap) and returns its body.
func (r *runner) readDocument(ctx context.Context, target string) ([]byte, int, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, err.Error()
	}

	req.Header.Set("User-Agent", userAgent())

	client := *r.client
	client.CheckRedirect = nil // a sitemap may redirect; follow it

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, networkError(err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, resp.StatusCode, networkError(err)
	}

	return body, resp.StatusCode, ""
}

// failOnSet returns the effective failOn as a set.
func failOnSet(cfg *CrawlConfig) map[string]bool {
	set := map[string]bool{}
	for _, findingType := range cfg.EffectiveFailOn() {
		set[findingType] = true
	}

	return set
}
