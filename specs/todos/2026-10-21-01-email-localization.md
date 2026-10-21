# Localized emails (en, fr, de, es) that follow the recipient

Every outbound email is rendered in the recipient's language, with dates in the recipient's
timezone. Today every email is English and every date is UTC.

## Decisions

1. **Storage: `users.preferences`**, a typed JSON column (precedent: `users.signup_attribution`).
   `models.UserPreferences{Locale string json:"locale,omitempty"; Timezone string json:"timezone,omitempty"}`.
   Migration `030_v0_41_0.tx.{up,down}.sql` for postgres (jsonb + `comment on column`) and sqlite (text).
   No DB CHECK and no `default:` tag; validation lives in Go. Not the `parameters` table (org config and
   secrets) and not `state_entries` / `/me/ui-state` (UI state, allowlisted, "not a junk drawer").
2. **Supported locales: `en`, `fr`, `de`, `es`** (same set as dash0). One normalizer
   (`i18n.Normalize`): lowercase, primary subtag of a BCP 47 tag (`fr-FR` -> `fr`), unsupported -> `""`.
   `incidentpublications.templatesFor` reuses it.
3. **Timezone**: IANA name validated with `time.LoadLocation`. Dates in emails are shown in the
   recipient's timezone with its abbreviation (`12:00 CEST`); `UTC` when unknown.
4. **API**: `UserInfo.preferences {locale, timezone}` (reaches `/auth/me` and every login response
   through `newUserInfo`). `PATCH /auth/me` accepts `preferences` (each field optional; a
   preferences-only PATCH must work, fix the early return on `name == nil`). 400 `VALIDATION_ERROR`
   on an unsupported locale or an unknown timezone. `RegisterRequest` and `AcceptInviteRequest`
   accept `locale` and `timezone`, stored like `signup_attribution` and applied when the user row is
   created. OpenAPI and `wiki/api-specification/auth.md` updated.
5. **dash0 sync** (one effect in `AuthProvider`): server locale set -> `i18n.changeLanguage(server)`;
   server locale empty -> PATCH it from `i18n.resolvedLanguage`. Timezone always follows the browser
   (`Intl.DateTimeFormat().resolvedOptions().timeZone`): PATCH when it differs. The language switcher
   PATCHes when signed in. Never PATCH for impersonation, demo or must-change-password sessions
   (they get 403). Register and accept-invite forms send `locale` + `timezone`.
6. **Locale per recipient** (`emaillocale` resolution, cached per job run):
   1. the recipient's user row (`GetUserByEmail`, case-insensitive) -> its preferences;
   2. a context hint: inviter (invitation to a new address), `status_pages.language`
      (subscriber mail), the request's `locale` (registration);
   3. for org-scoped mail to a non-user address (email integration lists, uptime report lists):
      the org owner's preferences;
   4. `en` / `UTC`.
   Locale and timezone fall back independently.
7. **Rendering**: `Formatter.Format(templateName string, loc i18n.Locale, data any)`. The interface
   change is deliberate: the compile error at every call site is what guarantees no email is
   forgotten. `EmailJobConfig` and `email.Message` gain `Locale`/`Timezone` so deferred jobs and the
   sender's own notice (support reply) are localized. Funcs are bound per render (Format already
   re-parses per call, so no shared mutable state): `t "key" "name" value...`,
   `tn "key" count "name" value...` (plural), `tHTML` (catalog may hold markup; args are escaped;
   never in subject or text blocks), `date`, `day`, `month`, `duration`, `number`, `pct`, `lang`.
   Funcs accept the shapes the job JSON round trip produces (RFC 3339 strings, float64).
8. **Go producers** (degraded, burn, regions, ack, comment, reminder, uptime report, durations,
   timestamps) take an `i18n.Locale`. Functions shared with Slack/Discord/Telegram/SMS keep their
   current English output for those channels (`X()` == `XIn(i18n.English)`); only email passes the
   recipient's locale. Other channels are out of scope.
9. **Catalogs**: embedded JSON, `server/internal/i18n/locales/<lang>/<namespace>.json`, nested
   objects flattened to dotted keys `<namespace>.<path>`. Named placeholders `{name}`. Plurals as
   `_one` / `_other` leaves (fr: 0 and 1 are `one`). Month names, duration units and number formats
   live in `common`. A missing key renders the `en` value and logs once; tests fail on it.
10. **Dates**: 24-hour everywhere. en `Jul 5, 12:00 CEST`, fr `5 juil., 12:00 CEST`,
    de `5. Juli, 12:00 CEST`, es `5 jul, 12:00 CEST`. Zone abbreviations come from the tz database
    as is (a numeric one like `+03` is shown as `UTC+03`). Year shown only when it differs from the
    current year. The ack link wording states its real
    expiry ("works until Jul 12") from `incidentlinks.AckTokenTTL` and the incident start, instead
    of a literal "7 days".
11. **Also localized**: the support-reply notice (idempotency check no longer matches an English
    substring), the AI auto-repair notice (raw-text email), and the pages reached from email links:
    the unsubscribe page, the "I'm on it" ack result page (locale from the token's email address ->
    user, else `en`; `lang` attribute set) and the status-subscriber landing pages (the page's
    language). Instance operator notices (`opsnotify`: support message, new user, watchdog, test)
    stay English on every channel, email included: one `Notice` is fanned out to every medium
    (the others stay English, decision 13), it is queued as finished text before its recipient is
    resolved, and it goes to the instance's super admins, not to customers.
12. **Preview**: `?locale=en|fr|de|es` on `/api/mgmt/email-preview` (index and template), validated
    before rendering, `en` default keeps today's bytes. Fixtures are produced by the production
    helpers with the requested locale (fixes the drift: AckVia, CommentSource, custom-domain
    Diagnostic, ChangedAt). Dashboard Test -> Emails gets a language control.
13. **Out of scope**: Slack/Discord/Telegram/SMS/WhatsApp/push text, status page UI (already
    translated), an org-level default language setting.

## Tests

- Catalog parity: same keys in every locale as `en`; same placeholder set per key; no fr/de/es value
  identical to `en` unless listed in an allowlist (with a stale-entry check), mirroring
  `web/dash0/src/locales/*.test.ts`. Each check has a positive control.
- Every shipped template x every locale renders (html + text + subject), with no `{{`, no
  `<no value>`, no missing-key marker, and a preheader of 20+ characters.
- Existing tests that assert English literals pin `en`.
- Plural rules, date/duration/number formatting per locale, timezone conversion.
- Locale resolution chain (user, hint, owner, default) and the dash0 sync rules.

## Implementation phases

- **A. Foundation**: i18n package, catalogs loader + `common` namespace, migration + model + API +
  validation, `Format` signature + funcs, job/message locale fields, resolver, base.html and shared
  partials localized, preview `?locale`, catalog tests. Fixtures split into per-group files.
- **B. Per group** (disjoint files, ownership table in `wiki/conventions/email-i18n.md`): incident
  templates + notifications producers; escalation / paging / test / region / usage-cap /
  custom-domain; account + membership + invitation; uptime report; status subscriber (with its
  landing pages); AI auto-repair notice (the support notice was done in phase A); unsubscribe and
  ack pages; dash0.
- **C. Review**: translation quality per language, missed strings, other channels unchanged,
  renders in every locale, real test emails.
