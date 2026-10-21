# Localized emails

Read before adding or changing any text an email shows. Spec:
`specs/todos/2026-10-21-01-email-localization.md`. Template anatomy and rendering
traps: [email-templates.md](email-templates.md).

Every email is rendered for one reader, in their language (`en`, `fr`, `de`, `es`)
and their timezone. Phase A built the machinery and localized `base.html` and the
shared partials. Phase B localizes each template group; phase C reviews.

## The pieces

| Piece | Where | What |
|---|---|---|
| `i18n.Locale` | `server/internal/i18n` | Language + `*time.Location`. Zero value (`i18n.Default`) is English, UTC. `i18n.English` is the same value, used by producers shared with other channels. |
| Catalogs | `server/internal/i18n/locales/<lang>/<namespace>.json` | Embedded, nested JSON, flattened to `<namespace>.<dotted.path>`. Malformed JSON or a non-string leaf panics at init. |
| Template funcs | bound per render in `email.TemplateFormatter.Format` | `t`, `tn`, `tHTML`, `date`, `day`, `month`, `duration`, `number`, `pct`, `lang`. |
| Resolver | `server/internal/i18n/recipient` | Picks a recipient's Locale: account, hint, org owner, default. |
| Storage | `users.preferences` (`models.UserPreferences{Locale, Timezone}`) | Set by `PATCH /auth/me`, register, accept-invite. |
| Job fields | `jobtypes.EmailJobConfig.Locale/Timezone`, `email.Message.Locale/Timezone` | Carry the reader's locale to a deferred render and to the sender's own notice. |

`i18n` API (all methods on `Locale`):

```go
i18n.Supported() []string                  // en, fr, de, es
i18n.Normalize(tag string) string          // "fr-FR" -> "fr", unsupported -> ""
i18n.ParseTimezone(name string) (*time.Location, error) // rejects "", "Local", unknown
i18n.New(locale, timezone string) Locale   // each falls back on its own
loc.Lang() / loc.Location() / loc.Timezone()
loc.T(key, kv ...any) string               // {name} placeholders
loc.Tn(key, count, kv ...any) string       // key_one / key_other, {count} filled
loc.THTML(key, kv ...any) template.HTML    // catalog markup kept, values escaped
loc.Date(v) / loc.Day(v) / loc.Month(v)    // time.Time, *time.Time, RFC 3339 string
loc.Duration(v)                            // time.Duration, int/int64/float64 NANOSECONDS, "1h5m"
loc.Number(v, decimals) / loc.Pct(v, decimals) // Pct takes percent (99.95), not a ratio
i18n.MissingKeys() []string                // "<lang>:<key>" looked up and not found
```

## Catalog layout and ownership

One file per namespace per language. Each namespace has one owner so parallel work
never edits the same file.

| Namespace | Owner | Holds |
|---|---|---|
| `common` | phase A | months, date patterns, duration units, `base.html` header/footer, `incident-actions`, `fallback-link`, support-reply notice |
| `incident` | B: incident group | `incident-*.html`, notifications producers (degraded, burn, regions, ack, comment, reminder) |
| `alerts` | B: alerts group | `escalation`, `paging-nudge`, `test-email`, `region-*`, `usage-cap-reached`, `custom-domain-demoted` |
| `account` | B: account group | `registration`, `welcome`, `password-*`, `email-changed`, `invitation`, `membership_*` |
| `report` | B: report group | `uptime-report.html`, `uptimereport` labels |
| `subscriber` | B: subscriber group | `status-subscriber-*` |
| `notices` | B: raw-text group | AI auto-repair notice, operator notices |
| `pages` | B: landing pages group | unsubscribe page, "I'm on it" ack result page |

Create `locales/{en,fr,de,es}/<namespace>.json` together: the parity test fails on
a namespace present in English only. Do not add keys to `common` from a phase B
group; if two groups need the same string, say so in the PR and let one own it.

## Keys, placeholders, plurals

- Keys are `<namespace>.<template-or-topic>.<role>`, camelCase segments:
  `incident.created.subject`, `incident.created.preheader`, `incident.created.pill`,
  `incident.labels.startedAt`, `alerts.regionOffline.blindChecks_one`.
- Placeholders are `{name}` (one pair of braces, letters/digits/underscore). Every
  translation of a key uses exactly the placeholders of its English value, in any
  order. Values are inserted in one pass: a check named `{date}` is printed as is.
- A whole sentence is one key. Never build a sentence from fragments ("X" + "is on"
  + "Y"): word order and agreement differ (fr/es past participles agree, de puts the
  verb last). Put the variable parts in placeholders.
- Plurals are two leaves, `x_one` and `x_other`, and are read with `tn "x" .Count`.
  French uses `_one` for 0 and 1; English, German and Spanish for 1 only. `{count}`
  is filled with the localized count unless you pass `"count"` yourself. Every
  `_one` needs its `_other` (tested).
- Placeholder values are plain text. Format dates, durations and numbers first:
  `t "incident.created.since" "since" (date .StartedAt)`.

## Template funcs

```gotemplate
{{define "subject"}}🔴 {{t "incident.created.subject" "check" .CheckName}}{{end}}
{{define "preheader"}}{{t "incident.created.preheader" "since" (date .StartedAt)}}{{end}}
{{define "statusbanner"}}{{template "statusbanner-render" dict "Variant" "down" "Label" (t "incident.pill.down")}}{{end}}
<p>{{tn "alerts.regionOffline.blindChecks" .BlindCount}}</p>
<p>{{tHTML "alerts.regionOffline.since" "since" (date .Since)}}</p>  {{/* "<strong>{since}</strong>" in the catalog */}}
```

- `t` returns a string: html/template escapes it in the HTML body, and the subject
  and text blocks (text/template) print it raw. That is the default to use.
- `tHTML` is for a catalog value that carries markup (`<strong>`, `<br>`). Values
  are HTML-escaped; a `template.HTML` value (from `link`) is kept. **Never** in a
  subject, preheader or text block: those are not HTML. Keep markup minimal; the
  token test compares tag names between languages.
- `date` renders `Jul 5, 12:00 CEST` / `5 juil., 12:00 CEST` / `5. Juli, 12:00 CEST`
  / `5 jul, 12:00 CEST`, in the reader's zone, 24-hour, with the year only when it
  is not the current one. A zone the tz database names by offset reads `UTC+03`.
  `day` drops the time (`Jul 12`), `month` is long month + year (`July 2026`,
  `juillet 2026`, `julio de 2026`). A string that is not RFC 3339 is printed as is,
  so a producer that still pre-formats keeps working while you migrate it.
- `duration` keeps `mailDuration`'s rounding (`45s`, `1m 30s`, `1h 5m`, `1d 2h`) with
  localized units (`1 h 5 min`, `1 Std. 5 Min.`).
- `number .X 1` and `pct .X 2` use CLDR separators via `golang.org/x/text`.
- fr, de and es put a **no-break space (U+00A0)** between a number and its unit
  or `%` (`99,95 %`, `5 min`), like CLDR. Write ` ` in Go test expectations
  and in catalog JSON, so it is visible.
- `lang` is the language code; `base.html` uses it for `<html lang>`.
- Partials need no locale in their dot: the funcs are closures over the render's
  locale, so `dict "Label" (t "...")` works anywhere.

## Localizing a template

For each template in your group:

1. Move every English literal to your namespace: `subject`, `preheader`, the
   `statusbanner` pill label, headings, body copy, detail-row labels, CTA labels,
   and the whole `text` block (including its footer lines).
2. Replace pre-formatted Go strings with raw values where you can: pass the
   `time.Time` / `time.Duration` / number in the view model and format it in the
   template with `date` / `duration` / `number`. The job path decodes the view model
   through JSON, and the funcs accept the shapes that produces.
3. Phrases still built in Go (ack attribution, reminder sentence, degraded reason,
   burn labels) take a Locale (next section).
4. Update the preview fixture of the template in your `fixtures_<group>.go` so it
   is produced by the same production helper, called with the fixture's locale.
5. Translate fr, de, es in the same change (see Tone). Run the tests below.

## Go producers

A function that builds user-facing text takes the locale:

```go
func ReminderSentenceIn(loc i18n.Locale, p *Payload) string { ... loc.T("incident.reminder.sentence", ...) }
func ReminderSentence(p *Payload) string { return ReminderSentenceIn(i18n.English, p) }
```

Slack, Discord, Telegram, SMS and the other channels keep calling the old name and
keep their exact English output (`X() == XIn(i18n.English)`; add a test that says
so). Only the email path calls `XIn` with the recipient's locale. The English
catalog value must therefore reproduce today's English byte for byte.

## The `i18n-phase-b` marker

Phase A passes `i18n.Default` (or leaves `EmailJobConfig.Locale` empty) wherever the
recipient's locale is not at hand, with this exact comment on the line:

```go
// i18n-phase-b: resolve the recipient locale
```

`grep -rn "i18n-phase-b" server/` lists them. Remove the marker when you resolve
the site; phase C checks that none is left.

## Resolving a recipient's locale

| Call site | How |
|---|---|
| A `*models.User` is in scope | `recipient.ForUser(user)` |
| An address, maybe an account (integration "to" lists, uptime report recipients, test sends) | `res := recipient.NewResolver(db)` once per job run, then `res.ForAddress(ctx, orgUID, address, recipient.Hint{})` per recipient. With an `orgUID` the org owner's preferences fill what the address lacks. |
| An invitation to a new address | `res.ForAddress(ctx, "", invitee, recipient.HintFromUser(inviter))` |
| A status page subscriber | `res.ForAddress(ctx, "", address, recipient.Hint{Locale: deref(page.Language)})` (no account, no owner fallback: the page language is the hint) |
| Registration (no account yet) | `i18n.New(req.Locale, req.Timezone)` from the request |
| A deferred email job | resolve at enqueue time and set `EmailJobConfig.Locale = loc.Lang()`, `Timezone = loc.Timezone()`. The job renders with `i18n.New(cfg.Locale, cfg.Timezone)`. The local mirrors of that struct (`auth`, `regionsweep`, `customdomain`) already have the fields. |
| An email sent directly | pass the Locale to `Format` and set `email.Message.Locale` / `Timezone`, which the support-reply notice reads |

A shared view model built once for many recipients (region, cap notice,
custom-domain) must be built per locale once Go-built phrases are localized. Cache
per language if the fan-out is large.

`email-changed` goes to the OLD address after the row was updated: resolve from
`change.user` at enqueue time, never by address at render time.

## Tone and register

The product voice is plain and short. Match dash0's register for each language:

- **French**: *vous*. Natural product French, not a calque: "Je m'en occupe" for
  "I'm on it", "Voir l'incident". A regular space before `?`, `:`, `;`, `!` (as in
  dash0), French guillemets `« … »` for quoted UI labels, "e-mail".
- **German**: *Sie*. "Vorfall" for incident, "Benachrichtigungen" for alerts,
  "E-Mail". Quotes `„…“`.
- **Spanish**: *tú*, the register dash0 uses ("Revisa tu correo"). "incidente",
  "escalado", "correo". Quotes `«…»`.
- Keep brand and protocol names (SolidPing, HTTP, SMS) untranslated. A value equal
  to English must be listed in `identicalAllowlist` (`server/internal/i18n/catalog_test.go`)
  with the reason and the language, never to silence a missing translation.
- No em dashes in new copy.

## Test checklist

Already generic (phase A), rerun them:

- `go test ./internal/i18n/...`: key parity, placeholder/tag parity, untranslated
  values with allowlist and stale-entry check, plural pairing, each with a positive
  control.
- `go test ./internal/handlers/emailpreview/...`:
  `TestPreview_EveryTemplateRendersInEveryLocale` renders every template in every
  language (HTML, text, index subject) and fails on `{{`, `<no value>`, a wrong
  `<html lang>` or a missing `common.*` key. Phase C extends it to every namespace
  and to "no English left in fr/de/es".

Add for your group:

- Pin existing English assertions to `i18n.English`.
- One render test per template in a non-English locale asserting a translated
  subject and body sentence, with a non-UTC timezone where dates appear.
- `X() == XIn(i18n.English)` for every producer you split.
- A JSON round-trip render (like `uptimereport` tests' `roundTrip`) for templates
  sent through the email job, so float64 counts and string dates are exercised.
