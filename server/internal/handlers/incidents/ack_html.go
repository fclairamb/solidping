package incidents

// HTML responses for the magic-link ack endpoint. Returned when the user
// follows the link from an email — the client is a browser, not a fetch
// caller, so JSON would be a regression in UX.

import (
	"net/http"
)

// ackPageStyle is the inline stylesheet shared by every magic-link ack
// response. Kept as one constant so the HTML constants below stay readable
// (line-length lint balks at the long inline style otherwise).
const ackPageStyle = `<style>body{font-family:system-ui,-apple-system,` +
	`sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem;color:` +
	`#0f172a}h1{font-size:1.25rem;margin-bottom:0.5rem}p{color:#475569;` +
	`line-height:1.6}a{color:#2563eb}</style>`

// renderAckPage builds a complete HTML document for one of the magic-link
// outcomes. Everything is static — no user-controlled values are
// interpolated.
func renderAckPage(title, h1, body string) string {
	return `<!doctype html>` +
		`<html lang="en"><head><meta charset="utf-8"><title>` + title + `</title>` +
		`<meta name="viewport" content="width=device-width,initial-scale=1">` +
		ackPageStyle +
		`</head><body><h1>` + h1 + `</h1>` + body + `</body></html>`
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

// pageContent returns the (title, h1, body) for each outcome. Centralized so
// the writer doesn't need a switch and the renderAckPage helper stays
// content-agnostic.
func pageContent(kind ackPageKind) (string, string, string) {
	switch kind {
	case ackHTMLSuccess:
		return "Incident acknowledged",
			"Incident acknowledged",
			`<p>Thanks — the incident has been acknowledged. You can close this tab.</p>`
	case ackHTMLExpired:
		return "Link expired",
			"This link has expired",
			`<p>Magic-link ack tokens are valid for 7 days. ` +
				`Open the dashboard to acknowledge this incident manually.</p>`
	case ackHTMLInvalid:
		return "Invalid link",
			"This link is invalid",
			`<p>The acknowledgment link could not be verified. If you have a valid ` +
				`email notification, try the link from that message — or open the ` +
				`dashboard to acknowledge manually.</p>`
	case ackHTMLMissingToken:
		return "Missing token",
			"Missing token",
			`<p>This URL is missing the <code>token</code> query parameter. ` +
				`Please use the full link from the original email.</p>`
	case ackHTMLError:
		fallthrough
	default:
		return "Something went wrong",
			"Something went wrong",
			`<p>We couldn't acknowledge the incident. Please try again, or open ` +
				`the dashboard to acknowledge manually.</p>`
	}
}

// writeAckHTML renders the page for the given outcome and writes it with
// the given status. Errors writing to the client are ignored — the connection
// is likely already gone in that case, and there's nothing useful to do
// beyond log.
func writeAckHTML(writer http.ResponseWriter, status int, kind ackPageKind) {
	title, h1, body := pageContent(kind)
	page := renderAckPage(title, h1, body)

	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_, _ = writer.Write([]byte(page))
}
