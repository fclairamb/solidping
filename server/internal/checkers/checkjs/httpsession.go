package checkjs

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
)

// Jar bounds. A cookie jar is filled by the TARGET, not by the script, so a
// hostile or broken endpoint could otherwise grow a check job's memory without
// limit — and the jar's contents are readable from the script, so they also end
// up in whatever the script puts in its output.
const (
	// maxJarCookies is how many distinct (domain, path, name) triples one
	// session keeps. Beyond it, NEW cookies are dropped; a cookie already in
	// the jar can still be updated, so a session stays functional rather than
	// freezing at an arbitrary point in a login flow.
	maxJarCookies = 100
	// maxCookieBytes bounds a single cookie (name + value).
	maxCookieBytes = 4 * 1024
)

// cookieMeta is the attribute pair net/http/cookiejar refuses to give back:
// Jar.Cookies() returns cookies with only Name and Value populated, so a
// script asserting on "which domain was this set for" has nothing to read.
// The wrapper records them as it observes SetCookies.
type cookieMeta struct {
	name   string
	domain string
	path   string
}

// boundedJar is an http.CookieJar wrapping the standard cookiejar with the two
// bounds above plus the domain/path bookkeeping.
//
// It is a wrapper rather than a reimplementation on purpose: the matching rules
// (public-suffix, domain/path scoping, secure, expiry) stay the standard
// library's. This type only decides what is allowed IN, and remembers what came
// in so a snapshot can describe it.
type boundedJar struct {
	inner *cookiejar.Jar

	mu   sync.Mutex
	meta map[string]cookieMeta // key: domain \x00 path \x00 name
}

func newBoundedJar() (*boundedJar, error) {
	inner, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}

	return &boundedJar{inner: inner, meta: make(map[string]cookieMeta)}, nil
}

func metaKey(domain, path, name string) string {
	return domain + "\x00" + path + "\x00" + name
}

// effectiveScope resolves the (domain, path) a cookie will be stored under,
// applying the RFC 6265 defaults the server omitted.
func effectiveScope(u *url.URL, cookie *http.Cookie) (string, string) {
	domain := strings.TrimPrefix(cookie.Domain, ".")
	if domain == "" && u != nil {
		domain = u.Hostname()
	}

	path := cookie.Path
	if path == "" {
		path = "/"
	}

	return domain, path
}

// SetCookies implements http.CookieJar. Oversized cookies are dropped, and once
// the jar holds maxJarCookies distinct triples only updates to existing ones
// get through.
func (j *boundedJar) SetCookies(target *url.URL, cookies []*http.Cookie) {
	accepted := make([]*http.Cookie, 0, len(cookies))

	j.mu.Lock()

	for _, cookie := range cookies {
		if len(cookie.Name)+len(cookie.Value) > maxCookieBytes {
			continue
		}

		domain, path := effectiveScope(target, cookie)
		key := metaKey(domain, path, cookie.Name)

		if _, known := j.meta[key]; !known && len(j.meta) >= maxJarCookies {
			continue
		}

		j.meta[key] = cookieMeta{name: cookie.Name, domain: domain, path: path}
		accepted = append(accepted, cookie)
	}

	j.mu.Unlock()

	if len(accepted) > 0 {
		j.inner.SetCookies(target, accepted)
	}
}

// Cookies implements http.CookieJar.
func (j *boundedJar) Cookies(u *url.URL) []*http.Cookie {
	return j.inner.Cookies(u)
}

// snapshot returns the cookies the jar would send to rawURL, as plain maps for
// the JS runtime: {name, value, domain, path}.
//
// Applicability and values come from the standard jar (so the list is exactly
// what the next request would carry); domain and path are decorated from the
// recorded metadata, picking the most specific scope that matches the URL.
func (j *boundedJar) snapshot(rawURL string) []map[string]any {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return []map[string]any{}
	}

	cookies := j.inner.Cookies(parsed)
	out := make([]map[string]any, 0, len(cookies))

	for _, cookie := range cookies {
		domain, path := j.scopeFor(parsed, cookie.Name)
		out = append(out, map[string]any{
			"name":   cookie.Name,
			"value":  cookie.Value,
			"domain": domain,
			"path":   path,
		})
	}

	return out
}

// scopeFor finds the recorded (domain, path) for a cookie name applicable to u,
// preferring the longest matching path — the same tie-break the cookie spec
// uses for send order. Falls back to the URL's own host and "/" if nothing was
// recorded (a cookie the jar somehow holds without this wrapper seeing it).
func (j *boundedJar) scopeFor(target *url.URL, name string) (string, string) {
	j.mu.Lock()
	defer j.mu.Unlock()

	bestDomain, bestPath := target.Hostname(), "/"
	found := false

	for key := range j.meta {
		meta := j.meta[key]
		if meta.name != name {
			continue
		}

		if !domainMatches(target.Hostname(), meta.domain) || !pathMatches(target.Path, meta.path) {
			continue
		}

		if !found || len(meta.path) > len(bestPath) {
			bestDomain, bestPath = meta.domain, meta.path
			found = true
		}
	}

	return bestDomain, bestPath
}

func domainMatches(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func pathMatches(requestPath, cookiePath string) bool {
	if requestPath == "" {
		requestPath = "/"
	}

	if requestPath == cookiePath || cookiePath == "/" {
		return true
	}

	return strings.HasPrefix(requestPath, strings.TrimSuffix(cookiePath, "/")+"/")
}
