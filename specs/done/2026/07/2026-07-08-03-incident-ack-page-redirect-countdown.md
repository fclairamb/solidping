# Incident ack magic-link page: better UI + countdown redirect to the incident

## Problem

Clicking the acknowledgment magic link from an incident email lands on a bare
server-rendered page that just says:

> **Incident acknowledged**
> Thanks — the incident has been acknowledged. You can close this tab.

That's a dead end. The person who clicked is exactly the person who wants to
*look at* the incident — and we make them go find it themselves (or close the
tab and forget about it). The page is also visually spartan: unstyled
system-font text on a white page, nothing that looks like SolidPing.

The pages are assembled in
[server/internal/handlers/incidents/ack_html.go](server/internal/handlers/incidents/ack_html.go)
(static HTML strings, shared `ackPageStyle` constant) and written by
`AcknowledgeIncidentByLink` in
[handler.go:198](server/internal/handlers/incidents/handler.go) — which already
has both `orgSlug` and `incidentUID` in hand, so building the incident URL is
free.

## Proposal

### 1. Redirect countdown on success

On the success page (`ackHTMLSuccess`), after confirming the ack:

- Show "You will be redirected to the incident in **3… 2… 1…**" with a small
  inline `<script>` counting down, then `window.location.replace(...)` to the
  incident detail page:
  `/dash0/orgs/{orgSlug}/incidents/{incidentUID}` (route exists:
  [incidents.$incidentUid.tsx](web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx)).
- Animate the countdown (e.g. each digit fading/scaling in) rather than just
  swapping text — this is explicitly a "make it feel good" feature.
- Keep an explicit "Go to the incident now" link for people who don't want to
  wait, and as the no-JS fallback (optionally paired with
  `<meta http-equiv="refresh" content="3;url=…">` so no-JS still redirects).

Note `renderAckPage` currently guarantees "no user-controlled values are
interpolated". The redirect URL breaks that assumption: `orgSlug` and
`incidentUID` come from the URL path. Only build the redirect from
**validated** values — the incident UID verified against the token
(`VerifyAckToken` already binds the token to the incident) and the org slug as
confirmed by the successful DB ack — and HTML/JS-escape them when
interpolating. Alternatively re-read them from the acked incident row instead
of the request path.

### 2. Make the UI much better

Restyle all five ack pages (success, expired, invalid, missing token, error)
into something that looks like the product:

- Centered card layout, SolidPing logo/wordmark, proper typography, a large
  status icon (green check for success, amber clock for expired, red cross for
  invalid/error).
- Dark-mode support via `prefers-color-scheme` (mail clients and OS webviews
  are often dark).
- Still a self-contained server-rendered page: inline CSS, no external asset
  dependencies, works even if the SPA is down (the whole point of the
  magic-link page).
- Mobile-friendly — most of these clicks come from phone mail clients.

### Behavior details / open questions

- The dashboard route requires auth: an unauthenticated recipient will bounce
  through the login redirect (`?returnTo=`) and land on the incident after
  logging in — that's acceptable and arguably desirable. Recipients whose
  email doesn't match any user will hit the login wall; the "Go to the
  incident now" link makes the redirect non-blocking either way (they already
  got the "acknowledged" confirmation before the redirect fires).
- Error pages (expired/invalid) should *not* auto-redirect — but they can link
  to the incident/dashboard where the URL is known.
- Consider the same treatment for any sibling magic-link pages (e.g.
  unsubscribe) if they share this renderer, but that's out of scope unless
  trivial.

### Touched files

- `server/internal/handlers/incidents/ack_html.go` — page templates, countdown
  script, new styling; success page needs the org/incident to build the URL
  (signature change: pass them through `writeAckHTML`/`pageContent` for the
  success case).
- `server/internal/handlers/incidents/handler.go:198` — pass org slug +
  incident UID to the success renderer.
- Tests: `ack_html` / handler tests asserting the redirect URL is present,
  escaped, and only on the success page.

## Implementation Plan

1. **`ack_html.go` rewrite**
   - New `ackPageStyle`: centered card, SolidPing text wordmark, CSS
     variables for light/dark (`prefers-color-scheme: dark`), mobile-friendly
     (fluid width, no fixed px beyond max-width), status-icon color classes.
   - `ackIcon(kind ackPageKind) string`: inline SVG (no external assets) —
     green check for success, amber clock for expired, red cross for
     invalid/missing-token/error.
   - `buildIncidentURL(orgSlug, incidentUID string) string`: builds
     `/dash0/orgs/{orgSlug}/incidents/{incidentUID}`, `url.PathEscape`-ing each
     segment (defense in depth even though both inputs are validated by the
     time this is called).
   - `jsStringLiteral(s string) string`: `json.Marshal` a string to get a
     double-quoted JS literal — `encoding/json` HTML-escapes `<`, `>`, `&` by
     default, which is exactly what's needed to interpolate untrusted-ish
     text into an inline `<script>` block without a `</script>` breakout.
   - `pageContent(kind, orgSlug, incidentUID)` gains an `ackPage` struct
     return (title, h1, icon, body, headExtra) so the success case can carry
     a `<meta http-equiv="refresh">` fallback (`headExtra`) plus the
     countdown script + "go now" link (`body`), while the other four kinds
     leave the new fields empty.
   - `renderAckPage` grows a `headExtra` and `icon` param, still 100% static
     concatenation — no template engine needed since every interpolated value
     is either a compile-time constant or has already been through
     `html.EscapeString`/`jsStringLiteral`.
   - `writeAckHTML(writer, status, kind, orgSlug, incidentUID)` — new params
     threaded through; non-success call sites pass the org/incident straight
     through too (harmless, `pageContent` ignores them for those kinds).
2. **`handler.go`**: no call-site logic changes beyond passing `orgSlug`,
   `incidentUID` (already in scope) into every `writeAckHTML` call. The
   redirect URL is only ever built from the path's `incidentUID` (already
   verified by `VerifyAckToken` against the token) and `orgSlug` (only reached
   this line after `tryEmailAck`'s DB-scoped ack succeeded for that exact
   org+incident pair) — never from unvalidated request data.
3. **Tests** (`ack_html_test.go`, new): redirect URL present + correctly built
   from org/incident on success; HTML-escaping and JS-escaping both proven
   with a value containing `"`, `<`, `>`, `&`, `</script>`; redirect absent
   on expired/invalid/missing-token/error pages; every page carries its icon
   and a dark-mode media query.
4. `make fmt` after each step; `make build-backend lint-back test` at the end.
