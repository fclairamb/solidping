# BetterStack Uptime — Competitor Analysis

BetterStack Uptime (formerly Better Uptime) is a SaaS uptime + status-page + on-call platform that markets itself as a one-stop replacement for "PagerDuty + Pingdom + Statuspage.io". For SolidPing it's the most relevant competitive reference — closest in scope and the source of several patterns we already adopt (escalation policies, multi-step on-call, group-incident correlation).

This directory replaces the previous monolithic `betterstack.md` (1004 lines) and re-organizes the material per the size rule in `../../CLAUDE.md`. New depth on **detection logic, recovery semantics, escalation, on-call, and ack/snooze** has been folded in from a fresh research pass (2026-05).

## Files in this directory

- [monitoring.md](monitoring.md) — Detection logic (`confirmation_period`), recovery semantics (`recovery_period`), region selection and the 3-of-N quorum, monitor-status enum, the `validating` state.
- [alerting.md](alerting.md) — Escalation-policy schema (incl. `time_branching`, `metadata_branching`, `instructions`), on-call calendars, ack/resolve/snooze model, severities (channel matrix), incident grouping, dual surfaces (`policy_id` vs per-monitor booleans).
- [platform.md](platform.md) — Heartbeat semantics (`/fail`, exit codes, no `/start`), Playwright monitors, status pages and subscribers, maintenance windows (per-monitor vs team-wide).
- [api.md](api.md) — REST surface, auth, pagination quirks (default 50 vs incidents' 10), versioning (`v1`/`v2`/`v3`), rate limits, monitor-types reference, complete endpoint table.
- [integrations.md](integrations.md) — Outgoing webhooks (custom payloads, no HMAC), Terraform provider, Telemetry/Logtail, AI features (smart merging, silencing, post-mortems).
- [comparison.md](comparison.md) — vs SolidPing (advantages each way), feature gaps to close, design-pattern lessons.
- [sources.md](sources.md) — All source URLs used.

## At a glance

| Aspect | BetterStack |
|---|---|
| Founded | 2021 (rebrand of "Better Uptime") |
| Pricing | Modular ($25 Uptime + $34 On-call + $25 Telemetry) — combined plans from ~$59/mo |
| Free tier | 10 monitors, 3-min interval, 10 heartbeats |
| Min interval | 30 s (paid) |
| Probe regions | **4 logical buckets**: `us`, `eu`, `as`, `au` (each backed by multiple PoPs internally) |
| Confirmation | Time-based `confirmation_period` (seconds) **+** fixed 3-of-N region quorum (hardcoded) |
| Recovery | Time-based `recovery_period`; flap inside the window resets the timer |
| Escalation | Multi-step policies with 4 step types: `escalation`, `time_branching`, `metadata_branching`, `instructions` |
| On-call | Calendars with rotations (hour/day/week, no custom intervals); event-based overrides |
| Ack | Forever, until resolve. **No time-bounded snooze** in standard flow (see `incident_silencing` AI feature) |
| API | `https://uptime.betterstack.com/api/` — `v2` for most resources, `v3` for incidents, `v1` for heartbeat ingest |
| Terraform | First-party (`BetterStackHQ/better-uptime`) — 31 resources, 8 data sources |
| Notable | Severity primitive · 3-of-4 region quorum default · `recovery_period` flap-reset semantics · per-monitor + per-policy dual surface (legacy) · custom outgoing webhook templates · no HMAC signing on webhooks |

## What's worth borrowing

Distilled in [wiki/research/alerting-patterns.md](../../research/alerting-patterns.md). Headline items:

1. **`recoveryPeriod` (seconds)** with explicit "any failure inside the window resets the timer" semantic.
2. **`confirmationPeriod` as wall-clock seconds**, not as a fail-count — decouples alert delay from `checkFrequency`.
3. **A transient `validating` check status** so users see "we're confirming" rather than guessing why no incident opened.
4. **Step types** for escalation policies: `escalation`, `time_branching`, `metadata_branching`, `instructions`. The `instructions` step (markdown comment with checkboxes + reminder interval) is a cheap "runbook on the page" feature.
5. **`current_on_call` resolved at fire time**, not at policy-attach time.
6. **Override events** as `override: true` on a single events table — no separate "override" object class.
7. **HMAC signing on outgoing webhooks** — BetterStack lacks this and it's a low-cost differentiator.
8. **Severity primitive** decouples "what channels do I use" from "what step am I in".
9. **`incident_metadata` / typed metadata references** — unlocks data-driven escalation routing.

## Where BetterStack is weak

These are gaps SolidPing already covers or could leverage:

- Region quorum is **fixed at 3-of-N** — not user-configurable.
- **No `/start` endpoint** for heartbeats — can't detect mid-run hangs.
- **No HMAC signing** on outgoing webhooks (only basic auth or static custom headers).
- **`request_timeout` field has three different unit conventions** — seconds for HTTP, milliseconds for ports, also seconds for Playwright but with discrete allowed values. Footgun.
- **Dual surface** for monitor-level booleans (`email`/`sms`/`call`/`push`) vs policy-level `urgency_id` is internally confusing.
- **Snooze: not exposed** in the public API — only "screening alerts" workaround or AI silencing.
- **Pagination quirk**: incidents endpoint defaults to 10/page (vs 50 elsewhere) without a documented reason.
- **Rate limits are not published**.
- **No documented anomaly-free unit-tested SLO modeling** despite the marketing of SLAs.
