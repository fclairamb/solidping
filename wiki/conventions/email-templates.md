# Email templates

Every transactional email SolidPing sends is one file in
`server/internal/email/templates/`, rendered by `internal/email.TemplateFormatter`
and previewed (test mode only) at `/api/mgmt/email-preview` — the dashboard lists
them under **Test → Emails**. The preview uses the *same* formatter as the send
path, so what it shows is what the mailer produces.

## Anatomy of a template

```gotemplate
{{define "subject"}}[DOWN] {{.CheckName}} is down{{end}}
{{define "preheader"}}The {{.CheckType}} check has been failing since {{.StartedAt}}.{{end}}
{{template "base.html" .}}
{{define "statusbanner"}}{{template "statusbanner-render" dict "Variant" "down" "Label" "Down"}}{{end}}
{{define "content"}}…{{end}}
{{define "text"}}…{{end}}
```

- **`subject`** — the mail header.
- **`preheader`** — *required*. The grey line the inbox shows next to the subject.
  Skipping it does not fail anything: `base.html` renders an empty hidden div and
  the client scrapes the first visible text instead, which is the "SolidPing"
  wordmark for an alert and a raw URL for a CTA mail. Pinned by
  `TestPreview_EveryTemplateShipsAPreheader`.
- **`statusbanner`** — optional; incident mail overrides it with a full-width
  colored bar (`down` / `recovered` / `escalated` / `reopened` / `comment` /
  `acknowledged`). Templates that don't get the thin accent rule instead.
- **`content`** — the body, dropped into the card.
- **`text`** — *required*. The plaintext alternative. It is also the only place a
  missing view-model key can be detected (see below).

## Rules

- **Fact grids use `td.label` / `td.value`, never `<th>`.** `base.html` styles the
  two-column grid on those classes; a bare `<th>` renders centered, unpadded and
  un-backgrounded, silently wrecking the layout while the template still renders.
  `base.html` also styles `<th>` as a safety net, and
  `TestPreview_DetailRowsUseStyledCells` asserts the convention on top of it.
- **Buttons go through the `button` partial** (`dict "URL" … "Label" … "Variant"
  "primary|success|secondary"`), which emits a bulletproof table-based CTA. On
  mobile, stacked action cells go full-width.
- **Opaque identifiers are footnotes, not facts.** The incident UUID lives in a
  `p.footnote` under the actions; the table carries what a woken-up human acts on.
- **Quoted human text uses `.quote`**, never `.fallback` — the latter is
  monospace with `word-break: break-all`, meant for URLs, and mangles prose.
- **Timestamps carry their zone.** `notifications.mailTimestamp` renders UTC with
  the suffix; a bare `2026-07-05 10:00:00` in an alert is unreadable on call.
- **No real company names** anywhere, fixtures included — see the root `CLAUDE.md`.

## Rendering, and the traps in it

- **The HTML body is `html/template`; the subject and text part are
  `text/template`.** The whole set used to parse as HTML, which escaped every
  interpolated value: a check named `Search & Discovery` reached the inbox as
  `Search &amp; Discovery` in the subject line. Literal template text is never
  escaped, which is why it stayed invisible. If you touch `Format()`, keep the
  split — rendering the HTML part as text would turn every user-controlled name
  into markup injection (`TestSubjectAndTextAreNotHTMLEscaped` pins both halves).
- **A missing key is only visible in the text part.** `html/template` renders a
  missing map key as `<no value>`; premailer then re-parses the document, reads
  that as an unknown start tag and drops it *along with the element it sat in*.
  The HTML part therefore loses the whole paragraph silently, and no string
  search can find it. `TestPreview_NoUnresolvedTemplateValues` checks the
  plaintext part for that reason.
- **CSS is written as classes and inlined by premailer at render time.** `@media`
  blocks and `:hover` survive in a `<style>` block; everything else becomes a
  `style=""` attribute. Write classes, not inline styles — except where the style
  must survive a client that strips `<style>` entirely (the preheader div).
- **The design is light-only, and says so.** `color-scheme: light only` plus the
  matching meta tags stop Apple Mail / Outlook.com / Gmail-Android from
  auto-inverting the palette — which recolors the status banner that carries the
  whole meaning of an incident alert. "No dark styles" is not "renders in light".
- **`base.html` reads branding keys through the nil-tolerant `field` helper.**
  `html/template` *errors* on a missing struct field (a missing map key is merely
  nil), and the view models are a mix of maps and structs, so a direct
  `.OrgLogoURL` in the shared wrapper breaks struct-backed templates at send time.

## Iterating

`make dev-test`, then open `/api/mgmt/email-preview` (or the dashboard's Test →
Emails page). `devloop` rebuilds on `.html` changes as well as `.go` ones —
the templates are `//go:embed`-ed, so an edit means nothing until the binary is
rebuilt.
