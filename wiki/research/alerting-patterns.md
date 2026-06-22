# Monitoring & alerting design patterns — input for future specs

> Synthesis of monitoring and alerting design ideas observed in **Hyperping** ([competitors/hyperping/](../competitors/hyperping/)) and **BetterStack** ([competitors/betterstack/](../competitors/betterstack/)) during a focused 2026-05 research pass. This is **input for future specs**, not a spec itself — no implementation commitment, no priorities.
>
> Audience: contributors planning the next round of incident/notifier work and reviewers picking which ideas land in `specs/ideas/`.

## How to use this doc

For each pattern below:

- **What** — the design idea, in concrete terms.
- **Where seen** — the competitor doc that documents it.
- **What SolidPing does today** — current behavior with file pointer.
- **Gap & opportunity** — whether and why this is worth borrowing.

Patterns are grouped by subsystem. Within each group they're roughly ordered by leverage (cheapest, highest-impact ideas first).

---

## 1. Detection logic

### 1.1 Time-based confirmation period (`confirmationPeriod`)

- **What.** Wall-clock seconds between "first failed check" and "open an incident". User configures one number; alert delay is decoupled from `checkFrequency`.
- **Where seen.** [BetterStack monitoring §1](../competitors/betterstack/monitoring.md#detection-logic--confirmation_period). Default observed: 120 s. Allowed UI dropdown: Immediate / 30 s / 1 min / 2 min / 3 min / 5 min / 10 min.
- **What SolidPing does today.** Count-based: `checks.IncidentThreshold` consecutive failures (`server/internal/handlers/incidents/service.go:183`). Increasing `period` silently halves the alert delay.
- **Gap & opportunity.** Adopt `confirmationPeriod` (seconds) as a **second** knob alongside `IncidentThreshold`, or migrate to it. Lock in wall-clock semantics so increasing check frequency doesn't change alert latency.

### 1.2 Multi-region quorum

- **What.** A failure observed by one region triggers a *replay* against every other selected region. Open incident only if ≥N regions confirm.
- **Where seen.** Two distinct shapes:
  - BetterStack: hardcoded **3-of-N** (≥3 regions failing). Independent of `confirmation_period` — both must be satisfied. Not user-configurable.
  - Hyperping: also 3-of-N, but the trace of all confirmations is **embedded in the outgoing webhook payload** as `pings: [{ original: true|false, location, status }]`. The receiver can re-derive the decision.
- **What SolidPing does today.** Each region's check is independent; results land in `results` with `region` set; nothing cross-references them. No quorum.
- **Gap & opportunity.** Two-step:
  1. Add an explicit `failQuorum` setting on a check (default e.g. 3, or "majority"). When a region observes a failure, the scheduler enqueues immediate replays against all other regions; quorum decides whether the failure is "real" before incident creation.
  2. Embed the per-region trace in outgoing webhook payloads (matching Hyperping's `pings[]` shape with `original: true|false`).

### 1.3 The `validating` transient state

- **What.** A monitor in the confirmation window shows status `validating` (not `up`, not `down`) — externally visible to users.
- **Where seen.** [BetterStack monitoring §2](../competitors/betterstack/monitoring.md#the-validating-state). Status enum: `up` · `down` · `validating` · `paused` · `pending` · `maintenance`.
- **What SolidPing does today.** Status flips from `up` directly when the streak threshold is met. There's no "we're confirming" intermediate state.
- **Gap & opportunity.** Add a `validating` (or `confirming`) state to `models.CheckStatus`. Surface in the dashboard list view — answers the common confused-user question "why isn't my alert firing yet?".

### 1.4 Per-monitor `alertsWait` confirmation knob

- **What.** A discrete dropdown of "wait this long before alerting" values: `0/1/2/3/5/10/30/60`. Per-check, in addition to whatever multi-region confirmation runs underneath.
- **Where seen.** [Hyperping monitoring §`alerts_wait`](../competitors/hyperping/monitoring.md#alerts_wait--the-per-monitor-confirmation-knob).
- **What SolidPing does today.** No equivalent. `IncidentThreshold` is the only knob, and it's count-based (see §1.1).
- **Gap & opportunity.** If we adopt §1.1, this is just the `confirmationPeriod` field. The Hyperping observation is mainly that **a discrete dropdown is friendlier** than a free integer. Worth surfacing as such in the dashboard.

---

## 2. Recovery semantics

### 2.1 Time-based recovery period with flap-reset

- **What.** Monitor must stay `up` for `recoveryPeriod` seconds before the incident auto-resolves. **Any failure inside the window resets the counter.**
- **Where seen.** [BetterStack monitoring §Recovery](../competitors/betterstack/monitoring.md#recovery--recovery_period). Same field exists on incoming-webhook integrations with identical semantics.
- **What SolidPing does today.** Count-based via `effectiveRecoveryThreshold` (`server/internal/handlers/incidents/service.go:228`), which adapts based on relapse count. Different shape but same intent.
- **Gap & opportunity.** Add `recoveryPeriod` (seconds) as a **second**, time-based recovery option. Default 0 (instant resolve on first success). The flap-reset rule is the important part — single API call away if we expose this field. Users mentally model this in seconds, not "consecutive successes".

### 2.2 Distinct `recovery` notification

- **What.** When an incident auto-resolves via the recovery period, a separate `recovery` notification fires (separate from the original alert).
- **Where seen.** BetterStack docs.
- **What SolidPing does today.** We emit `incidentResolved` events that fan out to the same channels that fired at open time (see [features/notifications-and-escalation.md](../features/notifications-and-escalation.md)). Effectively we already do this — worth confirming the wording in the channel templates makes "recovered" vs "fired" clear.

---

## 3. Status-page subscriber notifications

- **What.** Public end-users subscribe to a status page (or specific components) to receive incident notifications via email / RSS / outgoing webhook / JSON API endpoint.
- **Where seen.**
  - [BetterStack platform §Status pages](../competitors/betterstack/platform.md#status-pages): per-page master switch (`subscribable`). Email + webhook need confirm-click; RSS needs none. **No native SMS or Slack** subscription. Component-level filtering applies to email/webhook; RSS gets all updates.
  - [Hyperping platform §Status pages](../competitors/hyperping/platform.md#status-pages): email + Slack channel + SMS (BYO Twilio). Component-level filtering reportedly drops unsubscribe rate from 8% to <2%.
- **What SolidPing does today.** Status pages exist with sections, resources, and availability metrics — but **no subscriber notifications**. Tier-2 roadmap item ([roadmap.md §1.2](../roadmap.md#12-status-page-subscriber-notifications)).
- **Gap & opportunity.** Concrete shape proposal:
  - Master switch `subscribable: true|false` on the status page resource.
  - Subscriber resource keyed by (statusPageUid, componentSet, channel, address) where `channel ∈ {email, webhook, rss, json}`.
  - Email + webhook need click-to-confirm via signed magic link; RSS unauthenticated.
  - Component-level filtering is a `componentSet` array on the subscriber row; RSS subscribers always get the whole page (per BetterStack's pragma).
  - Signed unsubscribe link in every email (RFC 8058).
  - Skip SMS in v1 (cost reasons; BetterStack also doesn't do native SMS for subscribers).
  - Skip Slack subscriber channels in v1 — we can add later as `channel = slack`.

---

## 4. Escalation policy step types

### 4.1 The four-type taxonomy

BetterStack ships four escalation step types. SolidPing today only has the equivalent of `escalation`. The other three are cheap wins.

#### `escalation` (page someone)

- **Where seen.** Both. BetterStack offers a richer `step_members[]` enum than us: `current_on_call`, `entire_team`, `user`, `webhook`, `slack_integration`, `microsoft_teams_integration`, `zapier_webhook`, `pagerduty_integration`, `policy` (chain), `incident_metadata`. Also bulk forms: `all_slack_integrations`, etc.
- **SolidPing today.** `user` / `on-call schedule` / `connection` / `all_admins`. We lack `entire_team` (without listing every user), `policy` chaining, `incident_metadata`, and the bulk integration forms.
- **Gap & opportunity.** Add `entire_team` (resolves at fire time), and `policy` (chain into another policy) at minimum. The bulk integration forms can wait.

#### `instructions` (runbook on the incident) — **highest leverage**

- **What.** A step that posts a markdown comment with `- [ ]` checklists into the incident timeline. Optional `reminder_enabled` + `reminder_interval_hours` to nag until checkboxes are ticked.
- **Where seen.** [BetterStack alerting §Step types](../competitors/betterstack/alerting.md#instructions--runbook-on-the-page).
- **SolidPing today.** None.
- **Gap & opportunity.** This is the cheapest "runbook on the incident" feature in this entire document. One step type, one markdown renderer, one cron job for reminders. Ship it.

#### `time_branching` (schedule-based routing)

- **What.** Step that conditionally jumps to a different policy based on day-of-week + time-of-day window.
- **Where seen.** BetterStack alerting §Step types.
- **SolidPing today.** No native time-based routing. To achieve "follow-the-sun" today, build separate on-call schedules + multiple policies + manually pick which policy to attach.
- **Gap & opportunity.** Worth implementing once `policy` chaining (see `escalation` above) is in. Together they unlock "after-hours" patterns that are otherwise impossible.

#### `metadata_branching` (data-driven routing)

- **What.** Step that branches on a typed metadata key on the incident. Values can be typed references (`User`, `Team`, `Policy`, `Schedule`, integration types), not just strings.
- **Where seen.** BetterStack — paired with their **catalog** of typed attribute references.
- **SolidPing today.** No incident metadata at all (events have a JSON `metadata` map, but it's untyped and not queryable for routing).
- **Gap & opportunity.** Significant scope. Defer until we have a concrete need. The architectural enabler is **typed metadata references on incidents** — retrofit-cost is high, so worth thinking about the schema even if we don't ship the feature soon.

### 4.2 Per-step `waitBefore` (relative, not cumulative)

- **What.** Each step's delay is measured **from the previous step's fire time**, not from incident-open. UI shows the cumulative time, but the field stays sequential.
- **Where seen.** BetterStack uses `wait_before`. SolidPing currently uses `delayMinutes` cumulative-from-start (`server/internal/jobs/jobtypes/job_escalation_step.go:398`).
- **What SolidPing does today.** Cumulative — every step's delay is from incident-open.
- **Gap & opportunity.** Both shapes work. Cumulative is easier to reason about at the policy level ("step 4 fires at minute 30"); relative is easier at the step level ("after the previous step, wait 5 more minutes"). Stick with cumulative — it's what we already have, and the UI can compute the relative form for editing.

### 4.3 Repeat at policy level, not step level

- **What.** `repeat_count` + `repeat_delay` apply to the whole policy. Repeating an individual step is not supported.
- **Where seen.** BetterStack `betteruptime_policy`. SolidPing matches: `repeatMax`, `repeatAfterMinutes` on the policy.
- **Gap & opportunity.** None — we already match. Don't add per-step repeat (would explode the UI).

### 4.4 Escalation progress on the incident

- **What.** The incident object exposes `escalationPolicy.alertedSteps` and `escalationPolicy.totalSteps`.
- **Where seen.** [Hyperping alerting §Ack model](../competitors/hyperping/alerting.md#ack-model).
- **What SolidPing does today.** `incidents.EscalatedAt` is a single boolean-like timestamp (set once on the first escalation event). No "we're at step N of M" field.
- **Gap & opportunity.** Cheap to add — track in the incident table. Useful for dashboards and downstream integrations that want to render "escalation in progress".

---

## 5. On-call schedules

### 5.1 Resolve `current_on_call` at fire time

- **What.** When an escalation step targets the on-call rotation, the lookup happens **when the step actually fires**, not when the incident opens or the policy is attached.
- **Where seen.** Both. BetterStack alerting §On-call calendars; Hyperping alerting §On-call.
- **What SolidPing does today.** Already correct — the runtime resolves the schedule at fire time (`features/notifications-and-escalation.md` documents this).
- **Gap & opportunity.** None. Worth keeping as a documented invariant.

### 5.2 Override events as `override: true` on a single events table

- **What.** Schedule overrides (PTO, swaps) are events with `override: true` in the same events table as regular shifts. Lookup precedence: override events win.
- **Where seen.** BetterStack on-call API.
- **What SolidPing does today.** Override is a separate concept (per the existing on-call schedules feature).
- **Gap & opportunity.** If we ever refactor schedule overrides, BetterStack's single-table approach is cleaner. Not urgent.

### 5.3 Concurrent shifts (N people on at once)

- **What.** A rotation can have `N` people on simultaneously for redundancy.
- **Where seen.** [Hyperping alerting §On-call](../competitors/hyperping/alerting.md#on-call-schedules).
- **What SolidPing does today.** Single on-call user per rotation slot.
- **Gap & opportunity.** Real-world need (especially for follow-the-sun handoffs). Modeling is small; runtime lookup returns a list instead of one user.

### 5.4 Per-user holiday mode

- **What.** Pause alerts to *one user* across all schedules, without removing them from the rotation.
- **Where seen.** BetterStack alerting §Holiday mode.
- **What SolidPing does today.** No equivalent.
- **Gap & opportunity.** Different shape from a schedule override (which moves the whole rotation). Useful when one person is unreachable but the schedule should still resolve to them on paper. Low priority but cheap.

---

## 6. Acknowledgement, snooze, manual resolve

### 6.1 Snooze with explicit duration as an API verb

- **What.** `POST /incidents/$id/snooze {"durationSeconds": N}`.
- **Where seen.** SolidPing already has it. BetterStack does **not** — they only have ack-forever or AI silencing. This is one place we're ahead.
- **Gap & opportunity.** Keep this as a documented advantage. Make sure the dashboard exposes it prominently.

### 6.2 The "screening alerts" pattern

- **What.** An option to route an incident to a *silent* policy first (humans triage, then click "Escalate to" to forward to real paging).
- **Where seen.** BetterStack docs — recommended workaround for the missing snooze. They've productized the workaround.
- **What SolidPing does today.** No first-class "silent routing" mode.
- **Gap & opportunity.** Add `incidentRouting: "page" | "silent"` as a check-level flag, plus a one-click "Escalate to: <policy>" button on the incident detail. Deterministic alternative to AI silencing. Cheap.

### 6.3 Don't ship AI silencing v1

- BetterStack markets AI silencing heavily but it's an opaque rate-feedback model with no API surface. Hard to do well. The deterministic version (§6.2) covers 90% of the use case and is debuggable. Defer.

---

## 7. Notification channels & integrations

### 7.1 Severity primitive (channel matrix)

- **What.** A `Severity` resource with `call · sms · email · push · critical_alert` booleans, referenced by escalation steps via `urgency_id`. Decouples "which channels at this step" from "where does the step go".
- **Where seen.** [BetterStack alerting §Severities](../competitors/betterstack/alerting.md#severities--the-channel-matrix).
- **What SolidPing does today.** Per-check / per-channel binding via `check_connections`. Each escalation step targets a connection (channel) directly, so the notion of "use email + SMS but not voice for this step" is implicit in which channels you bind.
- **Gap & opportunity.** **Don't replicate BetterStack's dual surface** (`monitor.email/sms/call/push` *plus* `policy.steps[].urgency_id`). If we adopt severities, retire the per-channel-type booleans on monitors. The current SolidPing model is simpler and probably fine — revisit only if we add SMS/voice channels and need a way to say "voice only at step 3".

### 7.2 HMAC signing on outgoing webhooks

- **What.** Per-webhook secret + signed timestamp header (`X-Signature: t=…,v1=hex(hmac(secret, t + body))`) so receivers can verify authenticity.
- **Where seen.** Neither competitor does this. BetterStack uses basic auth; Hyperping uses no auth on outgoing webhooks. **Both are weak here.**
- **What SolidPing does today.** Outgoing webhook channel exists; signing is not documented.
- **Gap & opportunity.** Cheap differentiator. Stripe-style HMAC + timestamp + tolerance window is a small change.

### 7.3 Hyperping-style multi-region trace in outgoing webhooks

- **What.** Webhook payload includes the multi-region confirmation trace as `pings: [{ original: true|false, location, status, statusMessage }]`.
- **Where seen.** [Hyperping API §Outgoing webhooks](../competitors/hyperping/api.md#outgoing-webhooks).
- **What SolidPing does today.** Outgoing webhooks fire per-event; we don't include cross-region context.
- **Gap & opportunity.** Pairs naturally with §1.2 (multi-region quorum). Once we collect the per-region results that produced the alert decision, pass them through.

### 7.4 Phone-call global throttle

- **What.** Hard-throttle phone calls to **1 per 5 minutes per user**, regardless of incident count. Defensive UX/billing guardrail.
- **Where seen.** [Hyperping alerting §Quotas](../competitors/hyperping/alerting.md#quotas--rate-limits).
- **What SolidPing does today.** No phone-call channel yet. Worth designing the throttle in from day one when we add Twilio.

---

## 8. Maintenance windows

### 8.1 Three-mode suppression (orthogonal flags)

- **What.** Maintenance windows should expose three orthogonal flags, each independent:
  - `pauseChecks` — stop the actual probing (no results recorded).
  - `suppressAlerts` — keep checking, suppress incident creation.
  - `postStatusPageNotice` — publish a maintenance notice to subscribers.
- **Where seen.**
  - Hyperping has `pauseChecks` + `postStatusPageNotice` as orthogonal flags but lacks the alert-suppression-only mode.
  - BetterStack uses *different semantics* for the same field name between monitors (pauses checks) and heartbeats (suppresses alerts). Confusing.
- **What SolidPing does today.** Maintenance windows suppress alerts (`ProcessCheckResult` returns early when in maintenance — see [features/notifications-and-escalation.md §Suppression](../features/notifications-and-escalation.md#suppression)). Status-page notice and check-pause are not separate modes.
- **Gap & opportunity.** Two-step:
  1. Make the existing alert-suppression behavior explicit and document it. Already correct; just needs the field name and docs.
  2. Add `pauseChecks` and `postStatusPageNotice` as additional orthogonal flags. **Crucially**: keep alert-suppression-only as the default — *both competitors are weak here*, and "keep recording, just don't page" is the most useful operator mode.

---

## 9. Incident model

### 9.1 The two-table outage/incident split

- **What.** Hyperping has **two distinct objects**:
  - **Outage** — operational alert object. Auto-generated from monitor failures. Acked, escalated, resolved by operators. Internal data.
  - **Incident** — customer-facing communication object. Localized, posted to status pages, has ordered `updates[]`.
- **Where seen.** [Hyperping alerting §Outages vs Incidents](../competitors/hyperping/alerting.md#outages-vs-incidents--the-two-table-model).
- **What SolidPing does today.** Single `incidents` table holds both responsibilities. The UI distinguishes "internal incident detail" from "what gets published to status page" implicitly via the events stream and the status-page resource layer.
- **Gap & opportunity.** Significant architectural decision. Pros of splitting:
  - Clean separation of access control (subscribers see incidents, operators see outages).
  - Different retention policies fit each (outages can be aggressively pruned; incident comms might be regulatory data).
  - Localized titles and `updates[]` belong on the customer-facing object only — keeps the operational object lean.
- Cons:
  - Schema migration is real work.
  - Adds a join everywhere a UI today shows "incident with operational + comms info".
- **Recommendation**: defer to a real spec. The win is structural — worth a serious read by anyone planning the next round of incident work.

### 9.2 Localized titles and updates as a first-class concept

- **What.** Incident titles and update bodies stored as a per-language map: `{ en, fr, de, ru, nl, pl, se }`. Promoted to a data-model concept, not just UI translation.
- **Where seen.** Both [Hyperping platform §Status pages](../competitors/hyperping/platform.md#localization) and Hyperping's incident/maintenance API.
- **What SolidPing does today.** UI is i18n'd (en + fr); incident/maintenance content is not stored per-language.
- **Gap & opportunity.** When status-page subscriber notifications ship, this becomes important — subscribers may speak different languages. Worth modeling now: `title: { default: "...", translations: { fr: "...", de: "..." } }` on incidents and maintenance windows.

### 9.3 Verb endpoints for incident operations

- **What.** Use POST to verb endpoints (`/ack`, `/resolve`, `/snooze`, `/escalate`) instead of PATCH to state fields.
- **Where seen.** Hyperping (`/v2/outages/{uuid}/acknowledge` etc.) and BetterStack (`/v3/incidents/{id}/acknowledge`).
- **What SolidPing does today.** Mixed — we have some verb endpoints; check the API spec.
- **Gap & opportunity.** Verbs make it easier to scope tokens ("ack-only" without the full PATCH surface). Audit the API surface and align.

---

## 10. Heartbeat enhancements

### 10.1 `/start` endpoint

- **What.** A second URL alongside the success ping: `/heartbeats/$token/start`. Marks job entry. The service can then alert if the job doesn't complete within `expectedDurationSeconds`.
- **Where seen.** [Hyperping platform §`/start` endpoint](../competitors/hyperping/platform.md#start-endpoint). BetterStack does **not** have this and explicitly cannot detect "task hung mid-run".
- **What SolidPing does today.** Only success/failure pings.
- **Gap & opportunity.** Cheap differentiator. Hyperping ships it, BetterStack doesn't, both audiences want it.

### 10.2 Exit code + body capture

- **What.** Heartbeat URL accepts an exit-code path segment (`/heartbeat/$token/$exitCode`) and the request body is captured as the incident cause.
- **Where seen.** [BetterStack platform §Heartbeats](../competitors/betterstack/platform.md#endpoints). Idiomatic curl: `curl -d "$output" .../heartbeat/$id/$?`.
- **What SolidPing does today.** Heartbeat endpoint accepts pings; doesn't capture exit codes or bodies.
- **Gap & opportunity.** Trivial to implement; dramatically nicer DX than "is it pinging or not". Pair with §10.1 for the full "BetterStack + Hyperping" heartbeat experience.

---

## 11. Browser / synthetic monitoring

### 11.1 Auto-collect Core Web Vitals on browser checks

- **What.** Synthetic browser runs automatically emit LCP, CLS, TBT, FCP, TTFB without script instrumentation. Free perf data on every run.
- **Where seen.** [Hyperping platform §Browser checks](../competitors/hyperping/platform.md#browser-checks-playwright).
- **What SolidPing does today.** Browser monitoring (Rod) records duration but not Web Vitals.
- **Gap & opportunity.** Page-speed monitoring is a Tier-3 roadmap item. Auto-collecting Web Vitals on every browser run is a way to ship it cheaply — no separate "page-speed" monitor type, just a metric flag on browser checks.

### 11.2 Multi-version runtime support

- **What.** Hyperping lets users pick the Playwright runtime version (current or legacy 2023.04). Useful when scripts written against an older API still need to run.
- **Where seen.** Hyperping platform §Browser checks.
- **Gap & opportunity.** Probably overkill for SolidPing v1. Note it; revisit if browser-script breakage becomes a recurring complaint.

---

## 12. API ergonomics

### 12.1 Per-resource versioning

- **What.** Both Hyperping and BetterStack version individual resources (`/v1/monitors`, `/v3/incidents`) rather than the whole API. Lets one subsystem evolve without forcing breakage on others.
- **Where seen.** Both, explicitly.
- **What SolidPing does today.** Global `/v1/` prefix.
- **Gap & opportunity.** When we hit a real backwards-incompatible change, prefer bumping just that resource (e.g. `/v2/incidents`) rather than coordinating an API-wide v2.

### 12.2 Token granularity (read+write vs read-only, scoped vs global)

- **What.** Hyperping ships project-scoped tokens with per-token Read+Write or Read-only. BetterStack ships global vs team-scoped tokens.
- **What SolidPing does today.** Personal access tokens with full scope.
- **Gap & opportunity.** Small but real DX win — a "read-only" token type for dashboards/exports.

### 12.3 Pagination consistency

- **What.** Pick one default and stick to it across endpoints.
- **Where seen.** BetterStack's incidents endpoint is a wart (10/page default while everything else is 50/page).
- **What SolidPing does today.** Cursor-based, default 50, well-documented (`base.ParsePageLimit`).
- **Gap & opportunity.** Stay disciplined. Don't add per-resource defaults.

---

## Cross-cutting recommendations

If implementing only a *handful* of the above, these are the highest-leverage to ship together:

1. **`recoveryPeriod` (seconds, flap-resets)** + **`confirmationPeriod` (seconds)** + **`validating` transient state**. Together they form the "time-based detection model" that's an industry standard. (§1.1, §1.3, §2.1)
2. **`instructions` escalation step type** — runbook-on-the-incident with markdown checkboxes and reminder cron. Cheapest "delight" feature on the list. (§4.1)
3. **Heartbeat `/start` endpoint + exit-code path + body capture**. Differentiator: BetterStack lacks `/start`, Hyperping lacks body capture. SolidPing can ship both. (§10)
4. **Status-page subscriber notifications** (Tier-2 roadmap). Concrete shape in §3.
5. **Multi-region quorum + per-region trace in webhook payloads**. Pair-shipped: §1.2 + §7.3.
6. **HMAC-signed outgoing webhooks**. Cheap differentiator both competitors lack. (§7.2)
7. **Three-mode maintenance windows** with alert-suppression-only as default. Both competitors have warts here. (§8.1)

## What this doc is NOT

- It's not a spec. None of the above is committed to.
- It's not exhaustive. Each pattern has a "see also" link to the deeper competitor doc.
- It's not the roadmap. See [roadmap.md](../roadmap.md) for actual priorities.
- It's not feature documentation. End-to-end pages for *shipped* features live in [features/](../features/).
