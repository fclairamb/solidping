---
model: opus
effort: high
---

# Operators cannot be paged when a support message arrives or a new user signs up — add instance-level operator notifications delivered through a chosen user's own notification routes

## Problem

Two events matter enormously for offering good support on a SolidPing
instance, and today neither of them can reach a human in real time:

1. **A new message on the support system.** Every inbound support surface
   (WhatsApp, Telegram, SMS, Slack, Discord) funnels through
   `(*support.Service).Capture` at
   [`server/internal/support/service.go:390`](../../server/internal/support/service.go).
   The only notification it produces is `(*Service).mirror`
   ([`server/internal/support/mirror.go:41`](../../server/internal/support/mirror.go)),
   which sends an **email** to the instance support mailbox (`email.reply_to`).
   If the operator lives in Telegram or Slack rather than in that mailbox, a
   customer's message sits unanswered until someone opens the unlinked
   `/support/` inbox.

2. **A new user registering.** Every signup method — password
   (`ConfirmRegistration`, `service.go:2420`), Google, GitHub, GitLab,
   Discord, Microsoft, Slack, OIDC, SAML, LDAP, and invite acceptance
   (`service.go:3474`) — ends in `createUserAndCapture`
   ([`server/internal/handlers/auth/signup_analytics.go:50`](../../server/internal/handlers/auth/signup_analytics.go)).
   Its only side effect is a PostHog analytics event, which is a no-op unless
   PostHog is configured and is not a paging surface either way. Nobody on the
   instance learns that a new person just arrived, so nobody can welcome them
   or notice a signup that got stuck (no org, no checks after an hour…).

What we want: a super admin picks **which user(s)** get told about **which of
these events**, and the notification goes out **on a particular integration**
that user already uses — Telegram, Slack DM, email, web push, SMS — so that
support can be offered from wherever the operator actually is.

### What already exists and must be reused

- **Delivery to "a specific user on their own channel" is a solved problem.**
  The platform watchdog (spec `2026-08-24-10`) delivers a free-text digest to
  configured recipients through each recipient's **own enabled notification
  routes** (`user_notification_routes` → `user_contacts`):
  `deliverWatchdogDigestToUser` / `watchdogRoutesFor` /
  `dispatchWatchdogRoute` in
  [`server/internal/jobs/jobtypes/job_platform_watchdog_delivery.go`](../../server/internal/jobs/jobtypes/job_platform_watchdog_delivery.go),
  with one renderer per contact type (email, Telegram, Slack DM, web push,
  SMS; WhatsApp skipped because Meta refuses free-form text outside a
  session). It dedups destinations across orgs (`watchdogDestinationKey`) and
  names every undeliverable recipient in the log. This is exactly the
  transport we need — it just has the word "watchdog" baked into every
  function name and a `watchdog.Digest`-shaped input.
- **Configuration lives in a system parameter**, not koanf: the watchdog
  reads the `platform_watchdog` JSON parameter (`Recipients []string` of user
  UIDs, `Enabled`), editable live through the super-admin
  `/system/parameters` CRUD (`server.go:1491-1497`).
- **Org-scoped `integrations` rows are not the right vehicle.** `Integration`
  has `organization_uid NOT NULL`
  ([`models/integration.go:106`](../../server/internal/db/models/integration.go))
  and the `notifications.Sender` payload is incident-shaped (`EventType`,
  `Incident`, `Check` — no free-text field,
  [`notifications/sender.go:15`](../../server/internal/notifications/sender.go)).
  Routing an instance-level event through an org's Slack channel would need a
  new payload kind across every sender. Out of scope here; see open
  questions.

## Proposal

Introduce **operator notifications**: an instance-level, super-admin-managed
subscription of users to platform events, delivered through the subscribed
user's own notification routes, using the watchdog's delivery fan-out
generalized into a reusable "operator notice" transport.

### A. Generalize the watchdog delivery into an operator-notice transport

Extract the delivery half of
`job_platform_watchdog_delivery.go` into a package (e.g.
`server/internal/opsnotify`) exposing roughly:

```go
type Notice struct {
    Event   string // "support.message", "user.registered", "watchdog.digest", "test"
    Subject string // one line, used as email subject / SMS lead / push title
    Body    string // multi-line plain text
    URL     string // deep link into the dashboard (may be empty)
}

// DeliverToUser fans one notice out over the user's enabled routes, in
// position order, deduplicated by destination across the user's orgs.
// Never returns an error; every skipped or failed route is logged and metered.
func DeliverToUser(ctx, jctx *jobdef.JobContext, log *slog.Logger, userUID string, n Notice) DeliveryReport
```

- The watchdog job becomes a caller of this transport (its `Digest` maps
  1:1 onto `Notice`). Its existing tests must keep passing unchanged in
  behavior — this is a refactor, not a redesign.
- Per-contact-type rendering (email job enqueue, Telegram HTML, Slack DM,
  web push, SMS truncated to ~300 chars) moves with it. The "cannot deliver
  over this contact type" WARN stays.
- Metrics: `solidping_operator_notice_total{event,contact_type,outcome}`
  (mirroring `solidping_support_mirror_total`) so a silent drop is visible.

### B. Configuration: the `operator_notifications` system parameter

A single JSON system parameter (constant next to
`watchdog.ParamPlatformWatchdog`), decoded into:

```json
{
  "enabled": true,
  "recipients": [
    { "userUid": "…", "events": ["support.message", "user.registered"] }
  ]
}
```

Rules:

- Unknown event names are rejected at write time (`VALIDATION_ERROR`
  listing the valid set) so a typo cannot silently subscribe to nothing.
- **Recipients must be super admins at delivery time.** Support-thread
  content is `RequireSuperAdmin`-gated
  ([`handlers/supportinbox/routes.go:24`](../../server/internal/handlers/supportinbox/routes.go));
  a notification that quotes a customer's message must not reach a user who
  cannot open the thread. A recipient who has since lost `super_admin` is
  skipped with a WARN naming them, not delivered to.
- `enabled: true` with zero recipients, or a recipient with no enabled
  routes, logs a WARN per run (same posture as the watchdog) — never a
  silent no-op.

### C. Event hooks (fire-and-forget, never on the request path's critical
section)

Both hooks enqueue a job (e.g. `operator_notice` job type carrying the
`Notice` + recipient list) rather than sending inline: the support webhooks
answer providers under a deadline, and a signup must not fail or slow down
because Telegram is down. Same reasoning as `analytics.Capture` being async.

1. **`support.message`** — from `(*support.Service).Capture` right where
   `s.mirror(ctx, thread, msg, created)` is called
   ([`service.go:469`](../../server/internal/support/service.go)). The notice
   carries: channel (`whatsapp`/`telegram`/…), whether this is a **new
   thread** or a follow-up, the sender's display label as the inbox shows
   it, a body preview capped at ~200 chars, and `URL` =
   `<base_url>/dash0/support/<threadUid>`.
   - Reuse the mirror's anti-burst posture: a per-thread fold window and an
     instance-wide per-hour ceiling (`mirrorsPerHour` /
     `mirrorFoldWindow` in `service.go`). A hundred messages in a minute
     must become "…and N more in this thread", not a hundred pushes.
     Whether to share the mirror's fold state or keep a parallel one is the
     implementer's call; the observable rule is "no more than one notice per
     thread per fold window, and the folded count is surfaced in the next
     one".
   - The email mirror keeps working exactly as before; this is additive.

2. **`user.registered`** — from `createUserAndCapture`
   ([`signup_analytics.go:50`](../../server/internal/handlers/auth/signup_analytics.go)),
   which every signup method already goes through. The notice carries the
   user's email, display name (if any), signup method (`password`, `google`,
   `invite`, …) and — since the caller knows it — the org they landed in,
   or "no organization" for the self-registration path that creates none
   (`confirm_registration_no_org_test.go`). `URL` points at the org's member
   list when there is one.
   - Unlike the analytics event, this notice **may** contain the email:
     the recipients are super admins who can already see it in the users
     admin. Document that explicitly in the code comment so nobody
     "fixes" it into a privacy leak later or strips it thinking it is one.
   - Invite acceptance counts as a registration (it is a new `users` row);
     the method string makes it distinguishable, so a recipient who only
     cares about organic signups can filter on sight. No separate event.

### D. Super-admin UI: a "Notifications" tab under Server

Add `/orgs/$org/server/notifications` to the `server.tsx` tab list
([`web/dash0/src/routes/orgs/$org/server.tsx:17-29`](../../web/dash0/src/routes/orgs/$org/server.tsx)):

- A table of **super-admin users** (they are the only valid recipients),
  one row each, with a checkbox per event and a "routes" cell summarizing the
  user's enabled contact types (e.g. `telegram, email`) or an amber
  "no notification routes — nothing will be delivered" warning linking to
  that user's notification settings. A recipient with no routes is the most
  likely silent failure and must be visible on the page, not only in logs.
- A master **Enabled** switch.
- A **"Send me a test"** button that delivers a `test` notice to the current
  user through `DeliverToUser`, reusing the transport end to end (route
  `POST /system/operator-notifications/test`, super admin only). This is the
  one button an operator presses to confirm the setup works.
- Reads/writes the `operator_notifications` parameter through a dedicated
  `GET/PUT /system/operator-notifications` pair that validates the shape
  (rather than the raw parameter CRUD), so the UI cannot save an invalid
  document.
- Follow the design reference for every primitive (switch, table, checkbox,
  alert); mobile-usable.

### E. Tests

- `opsnotify`: destination dedup across orgs, super-admin gate, no-routes
  WARN, per-contact-type rendering incl. SMS truncation, WhatsApp skip —
  ported from the watchdog delivery tests, and the watchdog tests still
  green against the shared transport.
- Support: a captured message enqueues exactly one notice with the expected
  preview/URL; fold window collapses a burst; the email mirror is untouched
  (positive control: mirror still counted in `solidping_support_mirror_total`).
- Auth: one registration per method (at least password, one OAuth, invite)
  yields one `user.registered` notice with the right method; a notice
  failure never fails the signup (the negative case must be a real test with
  the enqueue stubbed to error).
- Config: validation rejects unknown events / non-super-admin recipients
  with `VALIDATION_ERROR`.
- dash0 unit test for the new locale keys in every locale; Playwright e2e
  for the Notifications tab (toggle a recipient, save, reload, still set;
  "no routes" warning shows for a user without routes; "Send me a test"
  reports success/failure).
- Docs: a short page under `web/docs/` (self-hosting / administration) and a
  `CHANGELOG.md` entry.

### Open questions (do not block; note the decision in the spec on completion)

- **Org-integration targets** ("post to this Slack channel / Telegram group
  configured as an org integration") are deliberately out of scope: they
  require a free-text notification payload across every `notifications.Sender`
  and a cross-org authorization story for an instance-level event. If that is
  what "a particular integration" meant rather than "the channel the user
  already set up for themselves", it should be a follow-up spec building on
  the `Notice` type from section A.
- Whether `support.message` should also fire for **outbound** replies
  (so a second operator sees a colleague already answered). Default: no —
  inbound only, which is what the request asks for.
- Whether to let each recipient pick a **minimum route** (e.g. "Telegram
  only, never SMS") instead of fanning out over all enabled routes like the
  watchdog does. Default: fan out over all enabled routes; users control this
  by enabling/disabling routes on their own notifications page.

## Implementation Plan

### Decisions taken on the open questions

- **Org-integration targets stay out of scope.** Delivery is only ever through
  each recipient user's OWN notification routes.
- **`support.message` fires for inbound captures only.** Outbound operator
  replies never produce a notice.
- **Recipients fan out over every enabled route.** No per-recipient minimum
  route picker; a user narrows the fan-out by disabling routes on their own
  notifications page.

### Package layout (and why `DeliverToUser` does not take a `*jobdef.JobContext`)

The spec sketches `DeliverToUser(ctx, jctx *jobdef.JobContext, …)`. That exact
signature is not reachable: `jobdef` imports `app/services`, whose `Registry`
holds a `*support.Service`, and `internal/support` is one of the two packages
that must be able to raise a notice. `support → opsnotify → jobdef → services →
support` is an import cycle. The same argument rules out `opsnotify` importing
`integrations/slack` (which imports `handlers/auth`, the other notice raiser).

So the transport is split in two:

- **`server/internal/opsnotify`** — a leaf package. It owns the `Notice` type,
  the event vocabulary, the `operator_notifications` config, recipient
  resolution, all per-contact-type *rendering*, the destination dedup, and
  `DeliverToUser(ctx, Deps, log, userUID, Notice) DeliveryReport`. Its
  `Deps` struct carries `db.Service` plus one closure per medium
  (`EnqueueEmail`, `SendTelegram`, `SendSlackDM`, `SendWebPush`, `SendSMS`); a
  nil closure means "this instance cannot carry that contact type" and is
  WARNed and skipped, exactly like the watchdog's old `default:` branch.
- **`server/internal/opsnotifywire`** — builds those closures from
  `db.Service` + `*services.Registry` + `*config.Config`. Imported by
  `jobs/jobtypes` and by `app/`; imports nothing that imports it.

### Steps

1. **Extract the transport (spec A).** New `opsnotify` package with `Notice`,
   `Deps`, `DeliveryReport`, `DeliverToUser`, `routesFor`, `destinationKey`
   and the five renderers moved verbatim from
   `job_platform_watchdog_delivery.go` (SMS truncated at 300 chars + opt-out
   footer, Telegram HTML-escaped, web push = subject + first content line,
   WhatsApp/pushover/ntfy skipped with a WARN). New
   `solidping_operator_notice_total{event,contact_type,outcome}` counter.
   `job_platform_watchdog_delivery.go` becomes a thin caller mapping
   `watchdog.Digest` onto `Notice{Event: "watchdog.digest"}`; the watchdog's
   own tests stay untouched and keep passing (they assert on the substrings
   "undeliverable" and "no recipients", which the shared transport preserves).

2. **Configuration (spec B).** `opsnotify.ParamOperatorNotifications =
   "operator_notifications"`, `Config{Enabled, Recipients []Recipient}` with
   `Recipient{UserUID, Events []string}`. `ValidateParameter` rejects a
   non-object, a blank `userUid`, a duplicate `userUid`, an empty `events`
   list and any unknown event, naming the valid set. Wired into
   `handlers/system.(*Service).SetParameter` next to the watchdog's guard.
   `ResolveRecipients` re-reads every recipient at DELIVERY time and drops
   anyone who is not (or is no longer) a super admin, with a WARN naming them —
   support content is `RequireSuperAdmin`-gated, so a notice quoting a
   customer must not reach someone who cannot open the thread.

3. **Job + dispatcher (spec C).** New `jobdef.JobTypeOperatorNotice`
   (`operator_notice`, not publicly creatable) and
   `job_operator_notice.go`, which loads the config, resolves the recipients
   and fans the notice out. `opsnotify.SetDispatcher` / `opsnotify.Notify`
   mirror `analytics.Capture`: a package-level, never-failing entry point that
   the raisers call, wired in `app/server.go` to enqueue the job. A dispatcher
   error is logged and metered, never propagated.

4. **`support.message` hook.** `support.Service` gains an operator-notice fold
   state parallel to the mail mirror's (a per-thread window plus an
   instance-wide hourly ceiling, both in memory, pruned as they age) so the
   email mirror's own counters and DB columns are untouched — the mirror is a
   positive control in the tests. The notice carries the channel, new-thread
   vs follow-up, the sender label, a 200-char body preview and the
   `/dash0/support/<uid>` deep link, plus "…and N more in this thread" once a
   burst folded.

5. **`user.registered` hook.** `createUserAndCapture` (both the
   `handlers/auth` and the `integrations/slack` copies) raises the notice after
   the row is created. It carries the email, name, method and landing org, or
   "no organization". A comment records that the email is deliberate: the
   recipients are super admins who can already read it in the users admin.

6. **API (spec D).** `GET/PUT /api/v1/system/operator-notifications` and
   `POST /api/v1/system/operator-notifications/test`, all super-admin only.
   GET returns the config plus, for each super admin, their enabled contact
   types, so the UI can flag "no routes". PUT validates before writing. The
   test endpoint delivers a `test` notice to the caller synchronously through
   `DeliverToUser` and reports the per-route outcome.

7. **dash0 tab.** `/orgs/$org/server/notifications`: a master Enabled switch, a
   table of super admins × events with checkboxes, a routes cell (or an amber
   "no notification routes" warning linking to that user's notification
   settings), Save, and "Send me a test". Locale keys in `en`/`fr`/`de`/`es`.

8. **Tests (spec E).** `opsnotify` unit tests (dedup across orgs, super-admin
   gate, no-routes WARN, SMS truncation, WhatsApp skip, unknown-event and
   non-super-admin validation); support tests (one notice per capture, fold
   collapses a burst, mirror still counted in
   `solidping_support_mirror_total`); auth tests (one notice per signup method,
   and a dispatcher stubbed to ERROR must not fail the signup); a dash0 locale
   parity unit test; a Playwright spec for the tab.

9. **Docs.** A self-hosting page under `web/docs/` plus a `CHANGELOG.md` entry.

No migration: the config is a `parameters` row and delivery reuses
`user_notification_routes` / `user_contacts`.
