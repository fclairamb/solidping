package unsubscribe

import (
	"fmt"
	"net/http"

	"golang.org/x/text/language"

	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/i18n/recipient"
	"github.com/fclairamb/solidping/server/internal/incidentlinks"
	"github.com/fclairamb/solidping/server/internal/securityheaders"
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

// pageLocale is the language a page is written in (spec 2026-10-21-01,
// decision 11): the preferences of the account the token was sent to, else
// the browser's Accept-Language, else English. A token that does not verify
// names no address, so the browser decides.
func (h *Handler) pageLocale(req *http.Request, token string) i18n.Locale {
	email := ""

	if token != "" {
		if payload, err := incidentlinks.VerifyUnsubscribe(h.svc.secret, token); err == nil {
			email = payload.Email
		}
	}

	hint := recipient.Hint{Locale: acceptedLanguage(req.Header.Get("Accept-Language"))}

	return recipient.NewResolver(h.svc.dbService).ForAddress(req.Context(), "", email, hint)
}

// acceptedLanguage is the supported language the browser prefers most
// ("de-CH, fr;q=0.8" gives "de"), or "" when its Accept-Language names none.
// Same as incidents.acceptedLanguage.
func acceptedLanguage(header string) string {
	tags, _, err := language.ParseAcceptLanguage(header)
	if err != nil {
		return ""
	}

	for _, tag := range tags {
		// Exact only: "*" has no language, and Base would guess one.
		if base, confidence := tag.Base(); confidence == language.Exact {
			if lang := i18n.Normalize(base.String()); lang != "" {
				return lang
			}
		}
	}

	return ""
}

// markup is a catalog string for the page body: the catalog's own markup is
// kept and every value is HTML-escaped.
func markup(loc i18n.Locale, key string, kv ...any) string {
	return string(loc.THTML(key, kv...))
}

// renderPage builds a complete HTML document in loc's language. title is
// plain text; body is HTML built by the *Body functions below.
func renderPage(loc i18n.Locale, title, body string) string {
	return `<!doctype html>` +
		`<html lang="` + loc.Lang() + `"><head><meta charset="utf-8"><title>` + escapeHTML(title) + `</title>` +
		`<meta name="viewport" content="width=device-width,initial-scale=1">` +
		pageStyle +
		`</head><body>` + body + `</body></html>`
}

// writePage renders and writes a complete page with the given status.
// Errors writing to the client are ignored — the connection is likely
// already gone in that case, and there's nothing useful to do beyond log
// (mirrors handlers/incidents/ack_html.go's writeAckHTML).
func writePage(writer http.ResponseWriter, loc i18n.Locale, status int, title, body string) {
	page := renderPage(loc, title, body)

	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	// The language follows Accept-Language when the token names no account.
	writer.Header().Add("Vary", "Accept-Language")
	securityheaders.ApplyBaseline(writer.Header())
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
func choiceBody(loc i18n.Locale, token, email, checkUID, checkName string) string {
	body := `<h1>` + markup(loc, "pages.unsubscribe.choice.title") + `</h1>` +
		`<p>` + markup(loc, "pages.unsubscribe.choice.intro", "email", email) + `</p>`

	if checkUID != "" {
		label := checkName
		if label == "" {
			label = checkUID
		}

		body += fmt.Sprintf(
			`<p><a class="btn btn-primary" href="/unsubscribe?token=%s&scope=check">%s</a></p>`,
			escapeHTML(token), markup(loc, "pages.unsubscribe.choice.checkOnly", "check", label),
		)
	}

	body += fmt.Sprintf(
		`<p><a class="btn btn-secondary" href="/unsubscribe?token=%s&scope=org">%s</a></p>`,
		escapeHTML(token), markup(loc, "pages.unsubscribe.choice.wholeOrg"),
	)

	body += `<p class="meta">` + markup(loc, "pages.unsubscribe.choice.undoHint") + `</p>`

	return body
}

// successBody renders the post-unsubscribe outcome page with an undo link.
// token is the same unsubscribe token the request was verified against —
// the undo link re-presents it so Service.ResubscribeByUID can re-verify
// org+email ownership of the row before deleting it, rather than trusting
// the embedded UID alone.
func successBody(loc i18n.Locale, token string, res *Result) string {
	outcome := markup(loc, "pages.unsubscribe.success.org", "email", res.Email)

	if res.CheckUID != "" {
		if res.CheckName != "" {
			outcome = markup(loc, "pages.unsubscribe.success.check", "email", res.Email, "check", res.CheckName)
		} else {
			outcome = markup(loc, "pages.unsubscribe.success.thisCheck", "email", res.Email)
		}
	}

	body := `<h1>` + markup(loc, "pages.unsubscribe.success.title") + `</h1>` +
		`<p>` + outcome + `</p>` +
		`<p>` + markup(loc, "pages.unsubscribe.success.othersUnaffected") + `</p>`

	body += fmt.Sprintf(
		`<p class="meta">%s <a href="/unsubscribe/undo?token=%s&uid=%s">%s</a></p>`,
		markup(loc, "pages.unsubscribe.success.changedMind"),
		escapeHTML(token), escapeHTML(res.SuppressionUID),
		markup(loc, "pages.unsubscribe.success.undo"),
	)

	return body
}

// resubscribedBody renders the undo confirmation page.
func resubscribedBody(loc i18n.Locale) string {
	return `<h1>` + markup(loc, "pages.unsubscribe.resubscribed.title") + `</h1>` +
		`<p>` + markup(loc, "pages.unsubscribe.resubscribed.body") + `</p>`
}

// missingTokenBody renders the missing-token/uid error page.
func missingTokenBody(loc i18n.Locale) string {
	return `<h1>` + markup(loc, "pages.unsubscribe.missing.heading") + `</h1>` +
		`<p>` + markup(loc, "pages.unsubscribe.missing.body") + `</p>`
}

// invalidTokenBody renders the invalid/expired-token error page.
func invalidTokenBody(loc i18n.Locale) string {
	return `<h1>` + markup(loc, "pages.unsubscribe.invalid.heading") + `</h1>` +
		`<p>` + markup(loc, "pages.unsubscribe.invalid.body") + `</p>`
}

// orgNotFoundBody renders the organization-not-found error page.
func orgNotFoundBody(loc i18n.Locale) string {
	return `<h1>` + markup(loc, "pages.unsubscribe.orgNotFound.title") + `</h1>` +
		`<p>` + markup(loc, "pages.unsubscribe.orgNotFound.body") + `</p>`
}
