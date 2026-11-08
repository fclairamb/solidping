package app

import (
	"net/http"
)

// robotsIndexedHost is the only hostname search engines may index: the
// public cloud, where /docs lives. Every other host this binary answers on
// (self-hosted instances, nonprod, a customer's custom status domain, the
// raw IP) gets a robots.txt that blocks all crawling, so a test or private
// instance never shows up in search results next to solidping.io.
const robotsIndexedHost = "solidping.io"

// robotsAllow is served on robotsIndexedHost: the docs and the public
// pages are indexable, the API, the dashboard, its legacy prefix and the
// analytics proxy are not.
const robotsAllow = `User-agent: *
Disallow: /api/
Disallow: /d/
Disallow: /dash0/
Disallow: /ingest/
Allow: /

Sitemap: https://solidping.io/docs/sitemap.xml
`

const robotsDisallow = `User-agent: *
Disallow: /
`

// serveRobotsTxt answers /robots.txt on every host, custom domains
// included (serveCustomHost routes it here). Before it, the path fell
// through to the SPA catch-all and crawlers got an HTML "Not found" page.
func (s *Server) serveRobotsTxt(writer http.ResponseWriter, req *http.Request) error {
	body := robotsDisallow
	if hostOnly(req.Host) == robotsIndexedHost {
		body = robotsAllow
	}

	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = writer.Write([]byte(body))

	return nil
}
