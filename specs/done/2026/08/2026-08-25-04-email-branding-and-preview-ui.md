---
model: opus
effort: high
---

# Emails have almost no brand — add a dev preview UI, a branded template pass, and logos

## Problem

SolidPing's transactional emails (22 templates in
`server/internal/email/templates/`) are functional but visually anonymous:

- **The entire brand is a text `<span>`.** The header in
  `server/internal/email/templates/base.html:68-70` is literally
  `<span class="header-logo">SolidPing</span>` on a `#1a1a2e` band — no logo
  image anywhere in any template, and the palette (`base.html:7-60`) is a
  generic grey/blue that doesn't match the product's visual identity.
- **There is no way to look at an email while working on it.** A preview
  endpoint exists — `GET /api/mgmt/email-preview/:template?format=html|text`,
  registered at `server/internal/app/server.go:2057-2058`, gated on
  `SP_RUNMODE=test`, handler in
  `server/internal/handlers/emailpreview/handler.go` — but it has no index and
  no UI: you must already know the template filename, open each one by hand,
  and flip `?format=` manually. Iterating on `base.html` across 22 templates
  this way is blind.
- **Fixture coverage has gaps**:
  `server/internal/handlers/emailpreview/fixtures.go:31-51` covers 19 of the
  22 templates — `custom-domain-demoted.html`, `incident-acknowledged.html`
  and `incident-comment.html` have no fixture and cannot be previewed at all.
- **Org identity never reaches the emails.** Organizations already have a logo
  in the data model (`LogoURL` / `LogoFileUID` on
  `server/internal/db/models/organization.go:19-29`, upload/serve via
  `server/internal/handlers/orglogo/service.go`, publicly reachable under
  `/pub/assets/<uid>`), but no email view model threads it through — most
  don't even set `OrgName` (e.g. `buildIncidentViewModel` at
  `server/internal/notifications/email.go:525-575`).

## Proposal

Three stages, in order — the preview UI first, precisely so the later visual
work is fast to iterate on.

### 1. Email preview UI (dev/test only)

A dash0 page in the spirit of the design-reference page
(`web/dash0/src/routes/orgs/$org/design-reference.tsx`) — a development
catalog, not a user feature:

- Backend: add an **index endpoint** next to the existing preview handler
  (e.g. `GET /api/mgmt/email-preview` returning `{ "data": [...] }` with
  template names and which formats/fixtures exist), same `SP_RUNMODE=test`
  gate as the existing route (see `testapi_routes_gate_test.go:67` for the
  gate test pattern).
- Frontend: a page listing all templates with an iframe (or `srcDoc`) preview
  of the rendered HTML, an HTML/text toggle, and the subject line shown above
  the body. Keep it gated the same way the backend is — it 404s outside test
  mode, and the page should degrade gracefully.
- **Close the fixture gap**: add fixtures for `custom-domain-demoted`,
  `incident-acknowledged` and `incident-comment`, and add a test asserting
  every file in `templates/` (minus `base.html`) has a fixture, so future
  templates can't ship unpreviewable.
- Preview must go through the real `Format()` path
  (`server/internal/email/formatter.go:100-132`) so what you see includes
  premailer CSS inlining — the existing handler already does this; keep it.

### 2. Branded CSS pass

Rework `base.html`'s single `<style>` block to carry the SolidPing brand:

- Align the palette with the product (dash0/status0) rather than the current
  generic greys; keep the semantic status colors (`.status-down` red,
  `.status-recovered` green, etc.) recognizable.
- Stay within email-client reality: table layout, premailer-inlined styles,
  600px container, bulletproof-button pattern (`base.html:105-111`) — improve
  the look, don't modernize the markup into something Outlook breaks.
- Sweep the 4 templates carrying extra inline styles
  (`incident-comment.html`, `uptime-report.html`,
  `status-subscriber-update.html`) so they inherit the new look instead of
  fighting it.
- Verify every template + both formats through the stage-1 preview UI.

### 3. Logos — SolidPing and, when available, the sending org

- **SolidPing logo** in the header, as an `<img>` with an absolute URL built
  from the server base URL — the same base already used for `DashboardURL` /
  `DocsURL` (`server/internal/notifications/email.go:583-598`). Candidate
  asset: `/dash0/logo.png` (embedded from `res/logo.png` via
  `make sync-brand-assets`); prefer PNG over SVG for email-client support.
  Keep the text fallback (`alt` + the existing wordmark) since many clients
  block remote images by default.
- **Org logo**: when the sending org has `LogoURL` set, show it (org logo
  primary, "sent by SolidPing" secondary, mirroring the existing footer
  wording at `base.html:79-92`). This requires threading `OrgLogoURL` (and
  `OrgName`, currently missing from most view models) into the email view
  models.
- **Watch the struct-view-model trap** documented in
  `server/internal/email/supportreply.go:150-158`: `html/template` errors on
  a missing *struct* field, so a new `.OrgLogoURL` referenced in `base.html`
  must be added to every struct-based view model (e.g. `uptimereport.Report`
  at `server/internal/uptimereport/report.go:82`) or accessed via a
  nil-tolerant pattern. A test rendering all templates against all fixtures
  (stage 1) is the guard here.

## Open questions

- Remote-image blocking: is a remote `<img>` acceptable, or should the logo be
  a CID inline attachment? (CID means touching `sender.go`'s MIME assembly —
  bigger change; suggest starting with remote + `alt` fallback and measuring.)
- Should status-subscriber emails use the *status page* branding logo
  (`status_page_settings.go:33-65`, which also has `hide_branding`) instead of
  the org logo? Probably yes for `status-subscriber-*` templates — decide
  during implementation.
- Dark-mode email clients: out of scope for the first pass unless the palette
  choice makes it cheap.

## Resolved open questions

Answered by the maintainer on 2026-08-25. The `## Open questions` section above
poses these without deciding; this section is prescriptive — implement exactly
what is written here and do not revisit the trade-offs.

**Q: Remote-image blocking — is a remote `<img>` acceptable, or should the logo
be a CID inline attachment?**

**Decision: remote `<img>` with an `alt` fallback. Do NOT implement CID.**
Build the logo `src` as an absolute URL from the server base URL (the same base
already used for `DashboardURL` / `DocsURL` at
`server/internal/notifications/email.go:583-598`), pointing at `/dash0/logo.png`
(PNG, not SVG). Keep the existing text wordmark as the `alt` text so a client
that blocks remote images still shows a legible header. **Do not touch
`sender.go`'s MIME assembly** — CID is explicitly out of scope for this spec.

**Q: Should status-subscriber emails use the status page branding logo instead
of the org logo?**

**Decision: yes.** The `status-subscriber-*` templates take their branding from
the status page settings (`status_page_settings.go:33-65`), **not** from the
org's `LogoURL`, and they **must honor `hide_branding`** — when it is set, render
no logo at all for those templates. Every other template uses the org logo as
described in stage 3. A subscriber opted into a status page, not into the
organization, so org branding must not leak into those emails.

**Q: Dark-mode email clients?**

**Decision: out of scope for this pass.** Do not add `prefers-color-scheme`
handling, and do not let dark-mode considerations constrain the palette choice
in stage 2. If the new palette happens to read acceptably in a dark client, that
is a bonus, not a requirement — do not spend effort on it or write tests for it.
A dedicated spec can follow once the palette has settled.

## Implementation Plan

Written 2026-08-25. Follows the three stages in the spec's order, and the
prescriptive `## Resolved open questions` above (remote `<img>` + `alt`, status
page branding + `hide_branding` for `status-subscriber-*`, no dark mode).

### Stage 1 — preview index endpoint, fixture gap, dash0 page

Backend (`server/internal/handlers/emailpreview/`):

1. `fixtures.go` — add the three missing fixtures (`custom-domain-demoted.html`,
   `incident-acknowledged.html`, `incident-comment.html`) and register them in
   `fixtureBuilders`.
2. `fixtures.go` — export `FixtureTemplateNames() []string` (sorted) so tests and
   the index handler share one source of truth.
3. `server/internal/email/supportreply.go` — export `ShippedTemplateNames()`
   (today's unexported helper) so the preview package can enumerate the embedded
   templates directory rather than keeping a hand-maintained list.
4. `handler.go` — add `Index`: `GET /api/mgmt/email-preview` returning
   `{"data": [{template, subject, hasText, previewUrl}]}` (REST convention:
   wrapped, camelCase). Subjects come from the real `Format()` path, same as the
   body preview.
5. `server/internal/app/server.go` — register the index inside the existing
   `RunMode == "test"` block, next to the per-template route.
6. `server/internal/app/testapi_routes_gate_test.go` — add the index to
   `testModeGatedRoutes` so the existing positive+negative gate test covers it.
7. Tests (`handler_test.go`):
   - drive the render-everything test off `email.ShippedTemplateNames()` instead
     of a hardcoded list (this is the guard for the struct-view-model trap and
     for stages 2/3);
   - `TestEveryShippedTemplateHasFixture` — every file in `templates/` except
     `base.html` has a fixture, so a future template cannot ship unpreviewable;
   - `TestPreviewIndex_*` — index shape, `{"data": …}`, one row per template.

Frontend (`web/dash0/`):

8. New page `src/routes/orgs/$org/test.emails.tsx` under the existing
   `/orgs/$org/test` layout — that layout is already gated on
   `runMode === "test"` and already degrades gracefully, which is exactly the
   gate the spec asks for. Template list on the left, subject + HTML/text toggle
   + `<iframe>` preview on the right. Components come from the design reference
   (`Card`, `Button`, `TabNav`, `PageHeader` patterns).
9. `test.tsx` — add the tab; `src/api/hooks.ts` — add the index hook;
   `src/locales/{en,fr,de,es}/nav.json` — add the strings in all four locales.

### Stage 2 — branded CSS pass on `base.html`

10. Rework the single `<style>` block onto the product palette taken from
    `web/dash0/src/index.css` (`--primary` `#0072D5`, `--brand` crimson
    `#DA1D69`, ink `#0F1A24`, wash `#F0F4F8`, border `#DDE2E7`, muted text
    `#55606B`) while keeping the semantic status colors recognizable
    (`status-down` red, `status-recovered` green, `status-escalated` /
    `status-reopened` amber, `status-comment` slate, `status-acknowledged` blue).
11. No markup modernization: table layout, 600px container, premailer inlining
    and the bulletproof-button pattern all stay.
12. Sweep the templates carrying extra inline styles so they inherit rather than
    fight the new look: `incident-comment.html` (blockquote), `uptime-report.html`
    (two `<h2 style=…>`), `status-subscriber-update.html` (label + body div) —
    replaced by new `base.html` classes (`.quote`, `.section-title`,
    `.eyebrow`, `.prose`).

### Stage 3 — logos

13. `server/internal/email/formatter.go` — `NewFormatter(opts ...Option)` plus
    `WithBaseURL(...)`, wired from `server/internal/app/server.go` with
    `cfg.Server.BaseURL` (the same base `DashboardURL` / `DocsURL` already use).
    Three new template funcs:
    - `productLogoURL` → `<base>/dash0/logo.png` (PNG, absolute), `""` with no base;
    - `absURL v` → passes through `http(s)://…`, prefixes `/…` with the base;
    - `field data "Name"` → **nil-tolerant** map-or-struct lookup, which is how
      the struct-view-model trap (`supportreply.go:150-158`) is dodged: every new
      key `base.html` reads goes through `field`, so a struct view model that
      lacks the field renders empty instead of erroring.
14. `base.html` header — `<img>` with an absolute `src` and a text `alt`
    fallback, reading (all via `field`): `OrgLogoURL` (primary logo — the org's,
    or the status page's for subscriber mail), `BrandName` (alt text / wordmark),
    `HideBranding`. Falls back to the SolidPing logo, then to the existing text
    wordmark. When `HideBranding` is set: no logo image and no SolidPing
    attribution at all. **No CID, no change to `sender.go`'s MIME assembly.**
15. Thread `OrgName` + `OrgLogoURL` where the org is already loaded:
    - `notifications.Payload` gains `OrgName`/`OrgLogoURL`, filled by
      `job_notification.go` (which already loads the org for `OrgSlug`), consumed
      by `buildIncidentViewModel` → covers the six incident templates;
    - `customdomain/alert.go`, `handlers/auth/service.go` (invitation),
      `handlers/auth/membership_requests.go`, `handlers/members/provisioning.go`;
    - `uptimereport.Data` gains an `OrgLogoURL` field (struct view model).
    Account/security mail (registration, password reset/changed, welcome,
    test-email) has no org context and deliberately stays SolidPing-branded.
16. `status-subscriber-*`: branding comes from the **status page**, never the org.
    `statusupdates.SubscriberUpdateEvent` and `statussubscribers.UpdateEvent` gain
    `PageLogoURL` / `PageHideBranding` (both producers already hold the
    `*models.StatusPage`); `SubscribeResult` gains the same for the confirm mail.
    `HideBranding` true ⇒ no logo rendered.

### QA

`make build-backend lint-back test`; `make build-dash0`, `bun run lint`,
`bun run test:unit` in `web/dash0`. Playwright E2E authored for the new page;
run locally only if a `SP_RUNMODE=test` server is available.
