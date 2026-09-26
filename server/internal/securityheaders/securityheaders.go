// Package securityheaders owns the browser security headers SolidPing sends on
// the documents it serves: Content-Security-Policy, X-Frame-Options and
// Referrer-Policy (spec 2026-09-25-28).
//
// There is exactly one policy per surface, each built from a table in this file
// so it can be asserted in a unit test rather than rediscovered in a browser:
//
//   - Dashboard (dash0, /d/...): a full policy. Everything the shipped bundle
//     loads is first-party (fonts are bundled, analytics go through the /ingest
//     reverse proxy), so fetches are 'self' plus the few exceptions listed on
//     dashboardPolicy.
//   - StatusPage (status0, /s/... and verified custom domains): a full policy
//     whose point is the operator-authored custom stylesheet. style-src keeps
//     'unsafe-inline' so that stylesheet still applies, while img-src,
//     font-src and connect-src are first-party only, which removes the url()
//     exfiltration and beacon channels a stylesheet would otherwise have.
//     frame-ancestors can be widened per organization through
//     statuspage.allowed_embed_origins.
//   - Baseline (docs, the /openapi explorer, server-rendered one-click pages,
//     the not-found page): framing restrictions only. These pages either load
//     third-party code by design (the explorer) or render no user content, so a
//     fetch policy would only be a way to break them.
//
// JSON API routes get none of this: their responses are never rendered as a
// document.
package securityheaders

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"regexp"
	"strings"
)

// Header names.
const (
	HeaderCSP            = "Content-Security-Policy"
	HeaderFrameOptions   = "X-Frame-Options"
	HeaderReferrerPolicy = "Referrer-Policy"
)

// Header values.
const (
	// FrameOptionsSameOrigin is the legacy-agent twin of `frame-ancestors
	// 'self'`. SAMEORIGIN and not DENY: the dashboard frames the status page
	// in its custom-CSS preview, and `frame-ancestors 'self'` allows that, so
	// DENY would tell legacy agents something the CSP does not say.
	FrameOptionsSameOrigin = "SAMEORIGIN"

	// ReferrerNoReferrer is sent on status-page documents: a public page can
	// carry a kiosk token in its query string, and no outbound link or asset
	// fetch from it should ever repeat that URL to a third party.
	ReferrerNoReferrer = "no-referrer"
)

// CSP keywords.
const (
	srcSelf         = "'self'"
	srcNone         = "'none'"
	srcUnsafeInline = "'unsafe-inline'"
	srcData         = "data:"
	srcBlob         = "blob:"
	srcHTTPS        = "https:"
	srcHTTP         = "http:"
)

// Directive names used by the shipped policies.
const (
	dirDefault        = "default-src"
	dirScript         = "script-src"
	dirStyle          = "style-src"
	dirImg            = "img-src"
	dirFont           = "font-src"
	dirConnect        = "connect-src"
	dirWorker         = "worker-src"
	dirObject         = "object-src"
	dirBase           = "base-uri"
	dirFormAction     = "form-action"
	dirFrameAncestors = "frame-ancestors"
)

// directive is one CSP directive. Policies are ordered slices rather than maps
// so the rendered header is deterministic (and diffable in a test).
type directive struct {
	name    string
	sources []string
}

// policy is an ordered list of directives.
type policy []directive

// baselinePolicy is sent on documents that must not be framed by a third party
// but whose content does not warrant (or cannot survive) a fetch policy.
func baselinePolicy() policy {
	return policy{
		{dirFrameAncestors, []string{srcSelf}},
		{dirBase, []string{srcSelf}},
		{dirObject, []string{srcNone}},
	}
}

// dashboardPolicy is the dash0 policy. Every exception to 'self' is here on
// purpose:
//
//   - style-src 'unsafe-inline': Radix, CodeMirror and the toaster inject
//     <style> elements at runtime. The dashboard renders no operator-authored
//     CSS (the custom-CSS preview is a status-page iframe carrying its own
//     policy), so this is not an exfiltration channel.
//   - img-src data: blob: https: http: an organization logo may be an
//     external URL (organizations.logo_url), sign-in providers hand back
//     avatar URLs on their own CDNs, the QR codes and screenshot previews are
//     data:/blob: URLs.
//   - worker-src blob: the session-replay recorder may start a blob worker.
//   - the request's own origin and its ws(s) twin are added per request
//     (Params.SelfOrigins): the realtime socket, and the sandboxed srcdoc
//     widget preview, whose opaque origin makes 'self' unreliable across
//     browsers.
//   - third-party analytics origins (an operator-configured PostHog host) are
//     added to script-src and connect-src by Options.DashboardThirdParty.
func dashboardPolicy() policy {
	return policy{
		{dirDefault, []string{srcSelf}},
		{dirScript, []string{srcSelf}},
		{dirStyle, []string{srcSelf, srcUnsafeInline}},
		{dirImg, []string{srcSelf, srcData, srcBlob, srcHTTPS, srcHTTP}},
		{dirFont, []string{srcSelf, srcData}},
		{dirConnect, []string{srcSelf}},
		{dirWorker, []string{srcSelf, srcBlob}},
		{dirObject, []string{srcNone}},
		{dirBase, []string{srcSelf}},
		{dirFrameAncestors, []string{srcSelf}},
	}
}

// statusPagePolicy is the public status-page policy. style-src keeps
// 'unsafe-inline' for the custom stylesheet; img-src, font-src and
// connect-src stay first-party so that stylesheet cannot load external
// images or fonts (the url() beacon an attribute selector turns into a data
// leak) and nothing on the page can post to a third party. Uploaded logos and
// favicons are served same-origin (/pub/status-page-assets/...), so the
// supported branding path keeps working.
func statusPagePolicy() policy {
	return policy{
		{dirDefault, []string{srcSelf}},
		{dirScript, []string{srcSelf}},
		{dirStyle, []string{srcSelf, srcUnsafeInline}},
		{dirImg, []string{srcSelf, srcData}},
		{dirFont, []string{srcSelf, srcData}},
		{dirConnect, []string{srcSelf}},
		{dirObject, []string{srcNone}},
		{dirBase, []string{srcSelf}},
		{dirFormAction, []string{srcSelf}},
		{dirFrameAncestors, []string{srcSelf}},
	}
}

// has reports whether the policy sets the named directive.
func (p policy) has(name string) bool {
	for i := range p {
		if p[i].name == name {
			return true
		}
	}

	return false
}

// add appends sources to a directive the policy already sets, skipping
// duplicates. A directive the policy does not set is left alone: adding
// `img-src https://cdn` to a policy with no img-src would RESTRICT images to
// that one host rather than widen anything.
func (p policy) add(name string, sources ...string) {
	for i := range p {
		if p[i].name != name {
			continue
		}

		for _, src := range sources {
			if src == "" || containsString(p[i].sources, src) {
				continue
			}

			p[i].sources = append(p[i].sources, src)
		}

		return
	}
}

// frameAncestorsIsSelfOnly reports whether frame-ancestors is exactly 'self',
// the one case X-Frame-Options can express faithfully.
func (p policy) frameAncestorsIsSelfOnly() bool {
	for i := range p {
		if p[i].name == dirFrameAncestors {
			return len(p[i].sources) == 1 && p[i].sources[0] == srcSelf
		}
	}

	return false
}

// String renders the policy as a header value.
func (p policy) String() string {
	parts := make([]string, 0, len(p))
	for i := range p {
		parts = append(parts, p[i].name+" "+strings.Join(p[i].sources, " "))
	}

	return strings.Join(parts, "; ")
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}

	return false
}

// Headers is the set of security headers for one response.
type Headers struct {
	CSP            string
	FrameOptions   string
	ReferrerPolicy string
}

// Apply writes the headers. Empty values are not written, and an absent
// X-Frame-Options is actively removed so a stale value from an earlier layer
// cannot contradict a widened frame-ancestors.
func (h Headers) Apply(header http.Header) {
	if h.CSP != "" {
		header.Set(HeaderCSP, h.CSP)
	}

	if h.FrameOptions != "" {
		header.Set(HeaderFrameOptions, h.FrameOptions)
	} else {
		header.Del(HeaderFrameOptions)
	}

	if h.ReferrerPolicy != "" {
		header.Set(HeaderReferrerPolicy, h.ReferrerPolicy)
	}
}

// Params are the per-response inputs to a policy.
type Params struct {
	// ScriptHashes are 'sha256-…' sources for the inline <script> blocks of the
	// document being served (see InlineScriptHashes).
	ScriptHashes []string
	// SelfOrigins are the request's own explicit origins (see RequestOrigins).
	// Dashboard only.
	SelfOrigins []string
	// EmbedOrigins widen frame-ancestors. Status pages only; must come from
	// ParseEmbedOrigins.
	EmbedOrigins []string
}

// Options configure a Builder.
type Options struct {
	// ExtraSources is the raw headers.csp_extra_sources value (see
	// ParseExtraSources).
	ExtraSources string
	// DashboardThirdParty are origins the dashboard legitimately loads code
	// from and posts to (an operator-configured PostHog host). Added to the
	// dashboard's script-src and connect-src.
	DashboardThirdParty []string
}

// Builder renders the per-surface headers. It is immutable once built and safe
// for concurrent use.
type Builder struct {
	extras              []directive
	dashboardThirdParty []string
}

// New builds a Builder. Invalid extra-source entries are skipped and returned
// so the caller can log them; they never fail the boot, since the shipped
// policy without them is still a working one.
func New(opts Options) (*Builder, []error) {
	extras, errs := ParseExtraSources(opts.ExtraSources)

	thirdParty := make([]string, 0, len(opts.DashboardThirdParty))
	for _, origin := range opts.DashboardThirdParty {
		if origin != "" && isSafeSource(origin) && !containsString(thirdParty, origin) {
			thirdParty = append(thirdParty, origin)
		}
	}

	return &Builder{extras: extras, dashboardThirdParty: thirdParty}, errs
}

// applyExtras widens p with the operator's extra sources.
func (b *Builder) applyExtras(p policy) {
	if b == nil {
		return
	}

	for i := range b.extras {
		p.add(b.extras[i].name, b.extras[i].sources...)
	}
}

// finish renders p into Headers, deriving X-Frame-Options from the final
// frame-ancestors.
func finish(p policy, referrer string) Headers {
	headers := Headers{CSP: p.String(), ReferrerPolicy: referrer}
	if p.frameAncestorsIsSelfOnly() {
		headers.FrameOptions = FrameOptionsSameOrigin
	}

	return headers
}

// Baseline returns the headers for a document that only needs framing
// protection.
func (b *Builder) Baseline() Headers {
	p := baselinePolicy()
	b.applyExtras(p)

	return finish(p, "")
}

// ApplyBaseline writes the framing-only headers with no operator extras. It
// is for the server-rendered one-click pages (incident acknowledgement,
// unsubscribe, subscription confirmation) whose handlers have no Builder:
// a page with a single confirm button is the textbook clickjacking target.
func ApplyBaseline(header http.Header) {
	var builder *Builder

	builder.Baseline().Apply(header)
}

// Dashboard returns the headers for a dash0 response.
func (b *Builder) Dashboard(params Params) Headers {
	p := dashboardPolicy()
	p.add(dirScript, params.ScriptHashes...)
	p.add(dirScript, params.SelfOrigins...)
	p.add(dirConnect, params.SelfOrigins...)

	if b != nil {
		p.add(dirScript, b.dashboardThirdParty...)
		p.add(dirConnect, b.dashboardThirdParty...)
	}

	b.applyExtras(p)

	return finish(p, "")
}

// StatusPage returns the headers for a status-page response (status0 on /s/…
// or on a verified custom domain).
func (b *Builder) StatusPage(params Params) Headers {
	p := statusPagePolicy()
	p.add(dirScript, params.ScriptHashes...)

	for _, origin := range params.EmbedOrigins {
		// Re-validated here so a value that did not come through
		// ParseEmbedOrigins can never inject a directive.
		if normalized, err := ValidateEmbedOrigin(origin); err == nil {
			p.add(dirFrameAncestors, normalized)
		}
	}

	b.applyExtras(p)

	return finish(p, ReferrerNoReferrer)
}

// inlineScriptRE matches a <script> element and captures its attributes and
// body. Good enough for the build-generated shells it is pointed at; it is not
// an HTML parser and is never run on user input.
var inlineScriptRE = regexp.MustCompile(`(?is)<script(\s[^>]*)?>(.*?)</script\s*>`)

// srcAttrRE detects a src attribute among a <script>'s attributes.
var srcAttrRE = regexp.MustCompile(`(?i)(^|\s)src\s*=`)

// InlineScriptHashes returns a 'sha256-…' CSP source for every inline
// <script> body in an HTML document. Both SPA shells carry one (the no-flash
// dark-mode snippet); hashing them from the served bytes, instead of pinning a
// literal, means editing that snippet cannot silently break the page.
func InlineScriptHashes(html []byte) []string {
	matches := inlineScriptRE.FindAllSubmatch(html, -1)
	hashes := make([]string, 0, len(matches))

	for _, match := range matches {
		if srcAttrRE.Match(match[1]) {
			continue
		}

		if len(match[2]) == 0 {
			continue
		}

		sum := sha256.Sum256(match[2])
		hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"

		if !containsString(hashes, hash) {
			hashes = append(hashes, hash)
		}
	}

	return hashes
}

// hostRE is the shape of a Host header this package is willing to reflect
// into a policy: DNS name or IPv4, optional port. Anything else (a bracketed
// IPv6 literal, or the sub-delimiters Go's server lets through a Host header,
// `;` included) is simply not reflected — 'self' still applies.
var hostRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?(?::[0-9]{1,5})?$`)

// RequestOrigins returns the request's own origin and its WebSocket twin
// (e.g. "https://solidping.acme.com" and "wss://solidping.acme.com"), or nil
// when the Host is not safe to reflect. The scheme is the first
// X-Forwarded-Proto token when it is exactly http or https, else the TLS state.
func RequestOrigins(req *http.Request) []string {
	host := req.Host
	if !hostRE.MatchString(host) {
		return nil
	}

	scheme := "http"
	if req.TLS != nil {
		scheme = "https"
	}

	if proto := req.Header.Get("X-Forwarded-Proto"); proto != "" {
		if idx := strings.IndexByte(proto, ','); idx != -1 {
			proto = proto[:idx]
		}

		switch strings.ToLower(strings.TrimSpace(proto)) {
		case "https":
			scheme = "https"
		case "http":
			scheme = "http"
		}
	}

	wsScheme := "ws"
	if scheme == "https" {
		wsScheme = "wss"
	}

	host = strings.ToLower(host)

	return []string{scheme + "://" + host, wsScheme + "://" + host}
}
