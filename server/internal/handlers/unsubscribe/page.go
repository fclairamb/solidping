package unsubscribe

import (
	"fmt"
	"net/http"
)

// pageStyle mirrors the incidents ack-page stylesheet (handlers/incidents/ack_html.go)
// so the two public, unauthenticated email-triggered pages look consistent.
const pageStyle = `<style>body{font-family:system-ui,-apple-system,` +
	`sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem;color:` +
	`#0f172a}h1{font-size:1.25rem;margin-bottom:0.5rem}p{color:#475569;` +
	`line-height:1.6}a{color:#2563eb}.btn{display:inline-block;padding:` +
	`10px 20px;border-radius:6px;text-decoration:none;font-weight:600;` +
	`margin:0.5rem 0.5rem 0.5rem 0}.btn-primary{background:#2563eb;` +
	`color:#fff!important}.btn-secondary{background:#64748b;color:#fff!important}` +
	`.meta{font-size:0.85rem;color:#94a3b8;margin-top:1.5rem}</style>`

// renderPage builds a complete HTML document. title/h1 are trusted static
// strings from this package; body is pre-escaped HTML built by the *Body
// functions below.
func renderPage(title, body string) string {
	return `<!doctype html>` +
		`<html lang="en"><head><meta charset="utf-8"><title>` + title + `</title>` +
		`<meta name="viewport" content="width=device-width,initial-scale=1">` +
		pageStyle +
		`</head><body>` + body + `</body></html>`
}

// writePage renders and writes a complete page with the given status.
// Errors writing to the client are ignored — the connection is likely
// already gone in that case, and there's nothing useful to do beyond log
// (mirrors handlers/incidents/ack_html.go's writeAckHTML).
func writePage(writer http.ResponseWriter, status int, title, body string) {
	page := renderPage(title, body)

	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_, _ = writer.Write([]byte(page))
}

// choiceBody renders the pre-unsubscribe scope-choice page: two buttons,
// "this check only" (shown only when the token carries a check) and "all
// alerts for this org". Both are plain links with the scope baked into the
// query string — GET is safe here because nothing is written until a scope
// link is actually followed, and following one link is itself the user's
// confirmation (no separate "are you sure" step, matching the spec's
// framing of unsubscribe as low-risk and reversible).
func choiceBody(token, email, checkUID, checkName string) string {
	body := `<h1>Unsubscribe from SolidPing alerts</h1>` +
		`<p>You are unsubscribing <strong>` + escapeHTML(email) + `</strong> from incident alert emails.</p>`

	if checkUID != "" {
		label := checkName
		if label == "" {
			label = checkUID
		}

		body += fmt.Sprintf(
			`<p><a class="btn btn-primary" href="/unsubscribe?token=%s&scope=check">`+
				`Unsubscribe from %s only</a></p>`,
			escapeHTML(token), escapeHTML(label),
		)
	}

	body += fmt.Sprintf(
		`<p><a class="btn btn-secondary" href="/unsubscribe?token=%s&scope=org">`+
			`Unsubscribe from all alert emails for this organization</a></p>`,
		escapeHTML(token),
	)

	body += `<p class="meta">You can undo this at any time from the confirmation page shown after you unsubscribe, ` +
		`as long as this link hasn't expired.</p>`

	return body
}

// successBody renders the post-unsubscribe outcome page with an undo link.
// token is the same unsubscribe token the request was verified against —
// the undo link re-presents it so Service.ResubscribeByUID can re-verify
// org+email ownership of the row before deleting it, rather than trusting
// the embedded UID alone.
func successBody(token string, res *Result) string {
	scopeLabel := "all alert emails for this organization"
	if res.CheckUID != "" {
		if res.CheckName != "" {
			scopeLabel = "alerts for " + escapeHTML(res.CheckName)
		} else {
			scopeLabel = "alerts for this check"
		}
	}

	body := `<h1>Unsubscribed</h1>` +
		`<p><strong>` + escapeHTML(res.Email) + `</strong> will no longer receive ` + scopeLabel + `.</p>` +
		`<p>Other recipients of these alerts are not affected.</p>`

	body += fmt.Sprintf(
		`<p class="meta">Changed your mind? <a href="/unsubscribe/undo?token=%s&uid=%s">Undo and re-subscribe</a></p>`,
		escapeHTML(token), escapeHTML(res.SuppressionUID),
	)

	return body
}

// resubscribedBody renders the undo confirmation page.
func resubscribedBody() string {
	return `<h1>Re-subscribed</h1>` +
		`<p>You will receive alert emails again for this scope.</p>`
}

// missingTokenBody renders the missing-token/uid error page.
func missingTokenBody() string {
	return `<h1>Missing link parameters</h1>` +
		`<p>This URL is missing required parameters. Please use the full link from the original email.</p>`
}

// invalidTokenBody renders the invalid/expired-token error page.
func invalidTokenBody() string {
	return `<h1>This link is invalid or has expired</h1>` +
		`<p>The unsubscribe link could not be verified, or it has expired. ` +
		`If you have a more recent alert email, try the link from that message instead.</p>`
}

// orgNotFoundBody renders the organization-not-found error page.
func orgNotFoundBody() string {
	return `<h1>Organization not found</h1>` +
		`<p>The organization this link refers to no longer exists.</p>`
}
