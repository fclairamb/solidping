# Localized emails

Read before adding or changing any text an email shows. Spec:
`specs/todos/2026-10-21-01-email-localization.md`. Template anatomy and rendering
traps: [email-templates.md](email-templates.md).

Every email is rendered for one reader, in their language (`en`, `fr`, `de`, `es`)
and their timezone. Phase A built the machinery and localized `base.html` and the
shared partials. Phase B localizes each template group in parallel; phase C reviews.

## The pieces

| Piece | Where | What |
|---|---|---|
| `i18n.Locale` | `server/internal/i18n` | Language + `*time.Location`. Zero value (`i18n.Default`) is English, UTC. `i18n.English` is the same value, used by producers shared with other channels. |
| Catalogs | `server/internal/i18n/locales/<lang>/<namespace>.json` | Embedded, nested JSON, flattened to `<namespace>.<dotted.path>`. Malformed JSON or a non-string leaf panics at init. |
| Template funcs | bound per render in `email.TemplateFormatter.Format` | `t`, `tn`, `tHTML`, `date`, `day`, `month`, `dayIn`, `monthIn`, `duration`, `number`, `signed`, `pct`, `listAnd`, `listOr`, `lang`. |
| Shared partials | `server/internal/email/templates/base.html` | HTML: `incident-actions`, `fallback-link`, footer. Text blocks: `incident-actions-text`, `footer-text`. |
| Resolver | `server/internal/i18n/recipient` | Picks a recipient's Locale: account, hint, org owner (addresses with no account only), default. |
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
loc.Tn(key, count, kv ...any) string       // key_one / key_other, {count} filled (no thousands separator)
loc.THTML(key, kv ...any) template.HTML    // catalog markup kept, values escaped
loc.Date(v) / loc.Day(v) / loc.Month(v)    // time.Time, *time.Time, RFC 3339 string; reader's zone
loc.In(*time.Location) / loc.InZone(name)  // copy showing dates in another zone (calendar dates, see below)
loc.Duration(v)                            // time.Duration, int/int64/float64 NANOSECONDS, "1h5m"
loc.Number(v, decimals) / loc.Pct(v, decimals) // Pct takes percent (99.95), not a ratio
loc.Signed(v, decimals)                    // "+1.50", "-0.25", "+1,5"
loc.ListAnd(items) / loc.ListOr(items)     // "a, b and c"; []string or []any
i18n.MissingKeys() []string                // "<lang>:<key>" looked up and not found
```

## Phase B groups and file ownership

Each group owns its namespace, its files and its test files, so parallel branches
never edit the same file. A file not listed here is not to be edited in phase B; if
you think you need one, stop and say so.

| # | Group | Namespace | Files it owns |
|---|---|---|---|
| 1 | Incident | `incident` | `email/templates/incident-*.html` (12); `notifications/email.go` and the notifications producers it calls (degraded, burn, regions, ack, comment, reminder); `emailpreview/fixtures_incident.go` |
| 2 | Alerts | `alerts` | `escalation.html`, `paging-nudge.html`, `test-email.html`, `region-offline.html`, `region-recovered.html`, `usage-cap-reached.html`, `custom-domain-demoted.html`; `jobs/jobtypes/job_escalation_step.go`, `handlers/members/provisioning.go` (paging nudge), `handlers/usernotifications/senders.go`, `handlers/system/service.go` (test email), `handlers/testapi/handler.go` (welcome / incident test sends), `regionsweep/orgs.go`, `jobs/jobtypes/phone_cap_notice.go`, `customdomain/alert.go`; `emailpreview/fixtures_alerts.go` |
| 3 | Account | `account` | `registration`, `welcome`, `password-*`, `email-changed`, `invitation`, `membership_*` templates; `handlers/auth/service.go`, `handlers/auth/membership_requests.go`, `handlers/auth/email_change.go`; `emailpreview/fixtures_account.go` |
| 4 | Report | `report` | `uptime-report.html`; `uptimereport/*`; `jobs/jobtypes/uptime_report_mail.go`; `emailpreview/fixtures_report.go` |
| 5 | Subscriber | `subscriber` | `status-subscriber-*.html`; `handlers/statussubscribers/handler.go` (confirm mail AND the landing pages: "Subscription confirmed", "Unsubscribed", "Link expired"), `handlers/statussubscribers/notifier.go`; `emailpreview/fixtures_subscriber.go` |
| 6 | Raw text | `notices` | `aichecks/repair.go` (AI auto-repair notice); the operator notices' email: `opsnotify/**`, `opsnotifywire/**`, `support/operator_notice.go`, `handlers/auth/signup_analytics.go` (the notice), `handlers/system/operator_notifications.go` (test notice), `regionsweep/operator.go`, `jobs/jobtypes/job_operator_notice.go`, `job_platform_watchdog.go`, `job_platform_watchdog_delivery.go`. The support-reply notice was done in phase A. |
| 7 | Pages | `pages` | `handlers/unsubscribe/page.go`, `handlers/incidents/ack_html.go` |
| 8 | dash0 | (dash0 catalogs) | `web/dash0` |

Shared files, and who touches what:

| File | Rule |
|---|---|
| `i18n/locales/*/common.json`, `i18n/*.go`, `email/formatter.go`, `email/templates/base.html` | Phase A only. No group edits them. |
| `i18n/testdata/identical/<namespace>.json` | One file per namespace, created by its group. |
| `emailpreview/fixtures.go` | Read only (`fixtureAckExpiresAt`, the template -> fixture map). |
| `emailpreview/locale_test.go` | Read only. Put your group's render tests in `emailpreview/locale_<namespace>_test.go`. |
| `email/email.go` (`Timestamp`) | Read only. Stop calling `Timestamp` and pass the `time.Time`; leave the function for phase C to delete. |

## Catalog layout

One file per namespace per language. Create `locales/{en,fr,de,es}/<namespace>.json`
together: the parity test fails on a namespace present in English only. The
`control` namespace is reserved for the tests' positive controls.

`common` holds months, date patterns, duration units, lists, the `base.html`
header/footer, `incident-actions`, `fallback-link`, the support-reply notice and the
text-block strings below. Never add keys to it in phase B. If you need a string
another group owns, copy it into your own namespace: a duplicate is cheap, a key
that only exists on someone else's unmerged branch is not. Phase C merges duplicates.

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
  French uses `_one` for 0 and 1; English, German and Spanish for 1 only. Every
  `_one` needs its `_other`, and every `_one` prints `{count}` ("1 check", never "One
  check": French would print "Une vérification" for 0). Both are tested.
- `{count}` is filled with the localized count without a thousands separator
  ("1234", "1,5" in French), so a producer shared with other channels keeps its old
  `%d` output. Pass `"count"` yourself to print something else.
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
  is not the current one (spec decision 10). A zone the tz database names by offset
  reads `UTC+03`. `day` drops the time (`Jul 12`), `month` is long month + year
  (`July 2026`, `juillet 2026`, `julio de 2026`). A string that is not RFC 3339 is
  printed as is, so a producer that still pre-formats keeps working while you
  migrate it.
- **Instants vs calendar dates.** `date`, `day` and `month` convert to the reader's
  zone: right for "when it happened". A calendar date anchored to a zone is not an
  instant: an uptime report's month is the schedule's month, its day strip counts UTC
  days. Use `dayIn "Europe/Paris" .PeriodStart` / `monthIn .ScheduleZone .PeriodStart`
  (zone first; unknown zone gives UTC), or `loc.In(scheduleLocation).Month(start)` in
  Go. Otherwise "Oct 1 00:00 Paris" prints "September 2026" to a reader in New York.
- `duration` keeps `mailDuration`'s rounding (`45s`, `1m 30s`, `1h 5m`, `1d 2h`) with
  localized units (`1 h 5 min`, `1 Std. 5 Min.`).
- `number .X 1`, `signed .Delta 3` (`+0.012`) and `pct .X 2` use CLDR separators via
  `golang.org/x/text`.
- `listOr .Providers` gives "Google, GitHub or Microsoft" ("Google u Okta" in
  Spanish); `listAnd` the same with "and". They take a `[]string`, or the `[]any`
  the job's JSON round trip makes of it.
- fr, de and es put a **no-break space (U+00A0)** between a number and its unit
  or `%` (`99,95 %`, `5 min`), like CLDR. Catalog JSON and the existing tests
  hold the literal character; `" "` in a Go string is the same and easier to
  see.
- `lang` is the language code; `base.html` uses it for `<html lang>`.
- Partials need no locale in their dot: the funcs are closures over the render's
  locale, so `dict "Label" (t "...")` works anywhere.
- **Never compare numbers with `eq`, `ne`, `lt`, `le`, `gt`, `ge` in a template sent
  through the email job.** The job decodes `TemplateData` as `any`, so every number
  is a `float64`, and `eq .BlindCount 1` fails with "incompatible types for
  comparison": the whole mail is lost. Use `tn` for plurals, or compute a bool in Go
  (`"HasNumber": n > 0`). `region-offline.html` has this bug today (group 2 fixes it).
  Templates rendered directly (`notifications/email.go`, escalation) are not affected,
  but prefer the same rule.

Helpers that are deliberately not in `common`: long-form durations ("48 hours",
"7 days": group 3 uses `tn` with its own `account.*` plural keys) and response times
("1483 ms", "1.48 s": group 4 uses `number` and its own `report.*` unit keys). Keep
such strings in your namespace; never edit `i18n/format.go` or `email/formatter.go`.

## Text blocks

The plaintext part repeats the HTML chrome, so the shared text lines are in `common`
too. Groups 1 and 2 (and 4 for the footer) use these and nothing else for them:

| Need | Use |
|---|---|
| "I'm on it" link + what it does + "The link works until …", then "View incident" | `{{template "incident-actions-text" .}}` (reads `AckURL`, `IncidentURL`, `AckExpiresAt`). It replaces "Acknowledge this incident: …" and the wrong "(link valid for 7 days)". A reminder-only addition ("stops these reminders") is your own key. |
| Dashboard, Docs, unsubscribe / stop alerts lines | `{{template "footer-text" .}}` (reads `DashboardURL`, `DocsURL`, `UnsubscribeURL`, `UnsubscribeCheckName`) |
| "This is an automated notification from SolidPing." | `{{t "common.footer.automated"}}` |
| A `Label: value` line | `{{t "common.textField" "label" (t "common.labels.target") "value" .CheckTarget}}` (French puts a space before the colon) |
| Labels shared by incident and escalation mails (HTML detail rows and text lines) | `common.labels.check`, `.target`, `.checkURL`, `.since`, `.incident`, `.docs` |

Labels only one group uses ("Started at", "Type", "Incident ID", "Failed checks",
"Acknowledged by") go in that group's namespace.

## Localizing a template

For each template in your group:

1. Move every English literal to your namespace: `subject`, `preheader`, the
   `statusbanner` pill label, headings, body copy, detail-row labels, CTA labels,
   and the whole `text` block (using the shared text partials above).
2. Replace pre-formatted Go strings with raw values where you can: pass the
   `time.Time` / `time.Duration` / number in the view model and format it in the
   template with `date` / `duration` / `number`. The job path decodes the view model
   through JSON, and the funcs accept the shapes that produces.
3. Phrases still built in Go (ack attribution, reminder sentence, degraded reason,
   burn labels, report labels) take a Locale (next section).
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
catalog value must therefore reproduce today's English byte for byte, numbers
included: `{count}` has no thousands separator, and `number` does (`1,234`), so a
count that used `%d` goes through `tn` or `"n" .Count`, never `number`.

## Raw-text mails (group 6)

Some mails have no template: the job sends `Text` as is and never calls `Format`, so
`EmailJobConfig.Locale` there only picks the language of the support-reply notice.

- The AI auto-repair notice (`aichecks/repair.go`) is email only. Resolve with
  `recipient.ForUser(owner)`, build subject and body with `loc.T("notices.aiRepair.…")`,
  and add `"locale"` / `"timezone"` to the job payload (`JobsMailer.Mail` builds a
  raw map; it carries the two keys). The diff and the reason stay as they are (not
  text we wrote).
- Operator notices (`opsnotify`: support message, new user, watchdog digest,
  watchdog region, test) are localized for the email medium only (spec decision
  11). The email copy is in the recipient super admin's locale
  (`recipient.ForUser`, resolved in `opsnotify.DeliverToUser`, which loads the
  user row); Slack, Telegram, Discord, push and SMS keep the exact English text.
  A `Notice` carries its English `Subject`/`Body` and, in `Localized`, an
  `opsnotify.Localizer` that writes it again for one reader. The two queued kinds
  (`opsnotify.SupportMessage`, `opsnotify.UserRegistered`) travel the job row as
  typed fields of `OperatorNoticeJobConfig`; a job queued before the upgrade has
  none and is mailed in English. The digest's anomaly lines (headline, detail,
  remediation) come from the detectors and stay English. Dates in the email use
  the reader's zone; the English text keeps its old formats. The `en` values
  reproduce that text byte for byte, em dashes included (the other media send
  it); fr, de and es have none.

## The `i18n-phase-b` marker

Phase A passes `i18n.Default` (or leaves `EmailJobConfig.Locale` empty) wherever the
recipient's locale is not at hand, with this exact comment on the line:

```go
// i18n-phase-b: resolve the recipient locale
```

`grep -rn "i18n-phase-b" server/` lists them; each is in a file of the ownership
table. Remove the marker when you resolve the site; phase C checks that none is left.

## Resolving a recipient's locale

| Call site | How |
|---|---|
| A `*models.User` is in scope | `recipient.ForUser(user)` |
| An address, maybe an account (integration "to" lists, uptime report recipients, test sends) | `res := recipient.NewResolver(db)` once per job run, then `res.ForAddress(ctx, orgUID, address, recipient.Hint{})` per recipient. An address with an account gets its own preferences, then the hint, then English/UTC: never the owner's. Only an address with no account falls back to the org owner's preferences (when `orgUID` is set). |
| An invitation to a new address | `res.ForAddress(ctx, "", invitee, recipient.HintFromUser(inviter))` |
| A status page subscriber (mail and landing pages) | `res.ForAddress(ctx, "", address, recipient.Hint{Locale: deref(page.Language)})` (no owner fallback: the page language is the hint). A landing page with no address at hand uses `i18n.New(deref(page.Language), "")`. |
| Registration (no account yet) | `i18n.New(req.Locale, req.Timezone)` from the request |
| A deferred email job | resolve at enqueue time and set `EmailJobConfig.Locale = loc.Lang()`, `Timezone = loc.Timezone()`. The job renders with `i18n.New(cfg.Locale, cfg.Timezone)`. The local mirrors of that struct in `auth`, `regionsweep` and `customdomain` already have the fields; `aichecks` builds a raw map (see Raw-text mails). |
| An email sent directly | pass the Locale to `Format` and set `email.Message.Locale` / `Timezone`, which the support-reply notice reads |

Until dash0 syncs preferences (group 8), every existing user has none and reads
English/UTC; that is expected.

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
  to English must be listed, with the reason, in
  `server/internal/i18n/testdata/identical/<namespace>.json`
  (`{"<key>": {"<lang>": "<why>"}}`), never to silence a missing translation. Each
  file only takes keys of its own namespace (tested).
- No em dashes in new copy.

## Test checklist

Already generic (phase A), rerun them:

- `go test ./internal/i18n/...`: key parity, placeholder/tag parity, untranslated
  values with the per-namespace allowlist and its stale-entry check, plural pairing,
  `{count}` in every `_one`, each with a positive control.
- `go test ./internal/handlers/emailpreview/...`:
  `TestPreview_EveryTemplateRendersInEveryLocale` renders every template in every
  language (HTML, text, index subject) and fails on `{{`, `<no value>`, a wrong
  `<html lang>`, a preheader under 20 characters, or a catalog key missing in **any**
  namespace. A typo, or a key another group owns, fails in your own branch.

Add for your group, in your own files (`emailpreview/locale_<namespace>_test.go`, or
your package's tests):

- Pin existing English assertions to `i18n.English`.
- One render test per template in a non-English locale asserting a translated
  subject and body sentence, with a non-UTC timezone where dates appear.
- `X() == XIn(i18n.English)` for every producer you split.
- A JSON round-trip render (like `uptimereport` tests' `roundTrip`) for templates
  sent through the email job, so float64 counts and string dates are exercised.

Phase C adds the "no English left in fr/de/es" check (a text diff of the en and
fr/de/es renders, minus fixture data such as check and org names).
