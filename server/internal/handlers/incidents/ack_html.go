package incidents

// HTML responses for the magic-link ack endpoint. Returned when the user
// follows the link from an email — the client is a browser, not a fetch
// caller, so JSON would be a regression in UX.

import (
	"encoding/json"
	"html"
	"net/http"
	"net/url"
)

// ackPageStyle is the inline stylesheet shared by every magic-link ack
// response. Kept as one constant so the HTML constants below stay readable
// (line-length lint balks at the long inline style otherwise). Card layout
// with light/dark variants via prefers-color-scheme — this page must render
// standalone (no external CSS/JS/font/image) since it's often opened from a
// mail client or while the SPA itself is down.
const ackPageStyle = `<style>` +
	`:root{--sp-bg:#f8fafc;--sp-card:#ffffff;--sp-text:#0f172a;` +
	`--sp-muted:#475569;--sp-border:#e2e8f0;--sp-link:#2563eb;` +
	`--sp-success:#16a34a;--sp-warn:#d97706;--sp-error:#dc2626}` +
	`@media (prefers-color-scheme:dark){:root{--sp-bg:#0f172a;` +
	`--sp-card:#1e293b;--sp-text:#f1f5f9;--sp-muted:#94a3b8;` +
	`--sp-border:#334155;--sp-link:#60a5fa;--sp-success:#4ade80;` +
	`--sp-warn:#fbbf24;--sp-error:#f87171}}` +
	`*{box-sizing:border-box}` +
	`body{font-family:system-ui,-apple-system,"Segoe UI",sans-serif;` +
	`margin:0;padding:1.5rem;min-height:100vh;display:flex;` +
	`align-items:center;justify-content:center;background:var(--sp-bg);` +
	`color:var(--sp-text)}` +
	`.card{width:100%;max-width:28rem;background:var(--sp-card);` +
	`border:1px solid var(--sp-border);border-radius:0.75rem;` +
	`padding:2rem 1.75rem;text-align:center;box-shadow:0 1px 3px ` +
	`rgba(15,23,42,0.08)}` +
	`.wordmark{font-weight:700;font-size:1rem;letter-spacing:0.02em;` +
	`color:var(--sp-muted);margin-bottom:1.25rem}` +
	`.icon{margin:0 auto 1rem;width:3.5rem;height:3.5rem}` +
	`.icon-success{color:var(--sp-success)}` +
	`.icon-warn{color:var(--sp-warn)}` +
	`.icon-error{color:var(--sp-error)}` +
	`h1{font-size:1.25rem;margin:0 0 0.5rem;line-height:1.3}` +
	`p{color:var(--sp-muted);line-height:1.6;margin:0.5rem 0}` +
	`a{color:var(--sp-link);text-decoration:none;font-weight:600}` +
	`a:hover{text-decoration:underline}` +
	`.ack-countdown{font-weight:700;display:inline-block;` +
	`min-width:1.2em;transform:scale(1);opacity:1;` +
	`transition:transform 0.25s ease,opacity 0.25s ease}` +
	`.ack-countdown-pop{animation:sp-pop 0.35s ease}` +
	`@keyframes sp-pop{0%{transform:scale(0.5);opacity:0}` +
	`60%{transform:scale(1.15);opacity:1}100%{transform:scale(1)}}` +
	`@media (prefers-reduced-motion:reduce){.ack-countdown-pop{` +
	`animation:none}}` +
	`.ack-now{margin-top:1rem;display:inline-block}` +
	`</style>`

// ackIcon returns a small self-contained inline SVG for the given outcome —
// green check for success, amber clock for expired, red cross for the
// remaining (invalid/missing-token/error) failure states.
func ackIcon(kind ackPageKind) string {
	switch kind {
	case ackHTMLSuccess:
		return `<svg class="icon icon-success" viewBox="0 0 24 24" fill="none" ` +
			`stroke="currentColor" stroke-width="2" stroke-linecap="round" ` +
			`stroke-linejoin="round" aria-hidden="true">` +
			`<circle cx="12" cy="12" r="10"></circle>` +
			`<path d="M8 12.5l2.5 2.5L16 9"></path></svg>`
	case ackHTMLExpired:
		return `<svg class="icon icon-warn" viewBox="0 0 24 24" fill="none" ` +
			`stroke="currentColor" stroke-width="2" stroke-linecap="round" ` +
			`stroke-linejoin="round" aria-hidden="true">` +
			`<circle cx="12" cy="12" r="10"></circle>` +
			`<path d="M12 7v5l3.5 2"></path></svg>`
	case ackHTMLInvalid, ackHTMLMissingToken, ackHTMLError:
		fallthrough
	default:
		return `<svg class="icon icon-error" viewBox="0 0 24 24" fill="none" ` +
			`stroke="currentColor" stroke-width="2" stroke-linecap="round" ` +
			`stroke-linejoin="round" aria-hidden="true">` +
			`<circle cx="12" cy="12" r="10"></circle>` +
			`<path d="M9 9l6 6M15 9l-6 6"></path></svg>`
	}
}

// buildIncidentURL returns the dashboard path for the given org/incident.
// Both segments are expected to already be validated by the caller (the
// incident UID is bound to the token by VerifyAckToken; the org slug is only
// used here after the DB ack itself has succeeded for that exact org+incident
// pair) — url.PathEscape is applied anyway as defense in depth, so a stray
// character in either value can't reshape the path.
func buildIncidentURL(orgSlug, incidentUID string) string {
	return "/dash0/orgs/" + url.PathEscape(orgSlug) + "/incidents/" + url.PathEscape(incidentUID)
}

// jsStringLiteral renders s as a double-quoted JavaScript string literal
// safe to interpolate into an inline <script> block. encoding/json HTML-
// escapes '<', '>' and '&' by default (Marshal, unlike a raw Encoder with
// SetEscapeHTML(false)), which is exactly what's needed here: it blocks a
// "</script>" breakout even though the values we pass it are already
// path-validated — escape defensively regardless of provenance.
func jsStringLiteral(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		// json.Marshal of a plain string cannot fail in practice; fall back
		// to an empty literal rather than ever emitting unescaped content.
		return `""`
	}

	return string(b)
}

// renderAckPage builds a complete HTML document for one of the magic-link
// outcomes. title/h1/icon/body/headExtra are either compile-time constants
// or values that have already been through html.EscapeString/jsStringLiteral
// by the caller — no further escaping happens here.
func renderAckPage(title, h1, icon, body, headExtra string) string {
	return `<!doctype html>` +
		`<html lang="en"><head><meta charset="utf-8"><title>` + title + `</title>` +
		`<meta name="viewport" content="width=device-width,initial-scale=1">` +
		headExtra +
		ackPageStyle +
		`</head><body><div class="card"><div class="wordmark">SolidPing</div>` +
		icon + `<h1>` + h1 + `</h1>` + body + `</div></body></html>`
}

// ackPageKind enumerates the magic-link outcomes — kept as constants
// instead of package-level variable bodies because the lint config bans
// non-trivial globals; the renderer below assembles the page each call,
// which is fine for the tiny strings involved.
type ackPageKind int

const (
	ackHTMLSuccess ackPageKind = iota
	ackHTMLExpired
	ackHTMLInvalid
	ackHTMLMissingToken
	ackHTMLError
)

// successRedirectFragment builds the head/body fragments unique to the
// success page: a <meta refresh> no-JS fallback, an animated "3…2…1…"
// countdown that then replaces the location, and an explicit "go now" link.
// redirectURL must already be a validated path (see buildIncidentURL) — it
// is escaped here for each of its two distinct contexts (an HTML attribute
// and a JS string literal) rather than trusted as pre-escaped.
func successRedirectFragment(redirectURL string) (headExtra, body string) {
	htmlURL := html.EscapeString(redirectURL)
	jsURL := jsStringLiteral(redirectURL)

	headExtra = `<meta http-equiv="refresh" content="3;url=` + htmlURL + `">`

	body = `<p>Thanks — the incident has been acknowledged.</p>` +
		`<p>Redirecting to the incident in ` +
		`<span id="ack-countdown" class="ack-countdown">3</span>…</p>` +
		`<p><a id="ack-now" class="ack-now" href="` + htmlURL + `">Go to the incident now</a></p>` +
		`<script>(function(){` +
		`var url=` + jsURL + `;` +
		`var el=document.getElementById("ack-countdown");` +
		`var n=3;` +
		`function render(){if(!el){return}el.textContent=String(n);` +
		`el.classList.remove("ack-countdown-pop");` +
		`void el.offsetWidth;` +
		`el.classList.add("ack-countdown-pop")}` +
		`render();` +
		`var timer=setInterval(function(){n-=1;` +
		`if(n<=0){clearInterval(timer);window.location.replace(url);return}` +
		`render()},1000);` +
		`})();</script>`

	return headExtra, body
}

// pageContent returns the (title, h1, icon, body, headExtra) for each
// outcome. Centralized so the writer doesn't need a switch and the
// renderAckPage helper stays content-agnostic. orgSlug/incidentUID are only
// used (and only meaningful) for ackHTMLSuccess — the other kinds ignore
// them, so callers can pass them through unconditionally.
func pageContent(kind ackPageKind, orgSlug, incidentUID string) (title, h1, icon, body, headExtra string) {
	icon = ackIcon(kind)

	switch kind {
	case ackHTMLSuccess:
		headExtra, body = successRedirectFragment(buildIncidentURL(orgSlug, incidentUID))

		return "Incident acknowledged", "Incident acknowledged", icon, body, headExtra
	case ackHTMLExpired:
		return "Link expired",
			"This link has expired",
			icon,
			`<p>Magic-link ack tokens are valid for 7 days. ` +
				`Open the dashboard to acknowledge this incident manually.</p>`,
			""
	case ackHTMLInvalid:
		return "Invalid link",
			"This link is invalid",
			icon,
			`<p>The acknowledgment link could not be verified. If you have a valid ` +
				`email notification, try the link from that message — or open the ` +
				`dashboard to acknowledge manually.</p>`,
			""
	case ackHTMLMissingToken:
		return "Missing token",
			"Missing token",
			icon,
			`<p>This URL is missing the <code>token</code> query parameter. ` +
				`Please use the full link from the original email.</p>`,
			""
	case ackHTMLError:
		fallthrough
	default:
		return "Something went wrong",
			"Something went wrong",
			icon,
			`<p>We couldn't acknowledge the incident. Please try again, or open ` +
				`the dashboard to acknowledge manually.</p>`,
			""
	}
}

// writeAckHTML renders the page for the given outcome and writes it with
// the given status. orgSlug/incidentUID are only consumed for ackHTMLSuccess
// (to build the post-ack redirect); pass them through regardless of kind,
// callers already have both in hand. Errors writing to the client are
// ignored — the connection is likely already gone in that case, and there's
// nothing useful to do beyond log.
func writeAckHTML(writer http.ResponseWriter, status int, kind ackPageKind, orgSlug, incidentUID string) {
	title, h1, icon, body, headExtra := pageContent(kind, orgSlug, incidentUID)
	page := renderAckPage(title, h1, icon, body, headExtra)

	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_, _ = writer.Write([]byte(page))
}
