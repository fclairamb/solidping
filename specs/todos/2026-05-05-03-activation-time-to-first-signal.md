# Activation — get every new user to first signal in under 60 seconds

## Context

Removing form fields on the invite page (specs `2026-05-05-02` and the
upcoming `2026-05-05-05`) shaves seconds off registration. Useful, but small.
The bigger funnel leak is between **"new user is now logged in"** and **"new
user has seen SolidPing do something useful for them."**

The product's "aha" moment is roughly: *I configured a check, it ran, and I
got a notification when it went down.* That's a four-step path — create
check, wait for run, configure notifier, witness alert — and we have **zero
instrumentation** on it. We can't currently answer "what % of new orgs
configure a check?" or "what % see a result within 5 minutes?" That is the
real gap.

This spec covers two things that have to land together to be meaningful:

1. **Make the path to first signal so easy a distracted user can't fail it.**
2. **Instrument the funnel so we can tell whether step 1 worked, and where
   the next leak is.**

Either half on its own is half a feature: a smoother onboarding we can't
measure, or measurements of an onboarding nobody completes. Land both in one
PR.

## Goal

A brand-new user lands on `/orgs/$org` for the first time. Within 60 seconds,
without leaving that page, they have:
- a check configured against a URL they care about,
- a first result shown back to them on the dashboard,
- a notification channel set up (or at minimum, a clear next-step nudge).

## Scope

In scope:

1. **Empty-state hero on the org dashboard** (`web/dash0/src/components/dashboard/`,
   wired into `routes/orgs/$org/index.tsx`):
   - When the org has zero checks, replace the regular dashboard with a
     focused "create your first check" card.
   - Three quick-start chips: **HTTP URL**, **Ping host**, **SSL certificate**.
     Each opens a one-input form (URL / host / domain) — everything else uses
     sensible defaults (1-minute period, default region, enabled = true).
   - Submitting creates the check and stays on the dashboard.

2. **First-run scheduling**: after the first check is created via this empty
   state, force a one-shot immediate run instead of waiting for the next
   scheduler tick. The dashboard polls at 30s already; first-result latency
   should be well under that.
   - Either expose a "run now" hook in the existing scheduler or piggy-back
     on the worker claim path. Implementation detail; backend lead picks.

3. **First-result celebration**: when the first result lands, the dashboard
   shows a transient banner: *"✓ We just checked <url>. Now hook up
   notifications so you'll know if it ever goes down."* with a single CTA
   button to the integrations page.

4. **Empty-state hero on integrations** (`routes/orgs/$org/integrations*`,
   verify exact path during implementation): when zero connections exist,
   show three big cards — Slack, Discord, Email — with a single-screen
   connect flow each. Email is the lowest-friction default; it should be
   pre-configured to use the user's own login email, requiring only a
   "Confirm" click.

5. **Activation event taxonomy**: emit events into the existing events table
   (or a dedicated `activation_events` if events would pollute the
   user-facing audit log — decide during impl) for:
   - `org.activation.signup_completed`
   - `org.activation.first_check_created`
   - `org.activation.first_result_received`
   - `org.activation.first_notification_configured`
   - `org.activation.first_incident_paged`

   Each event carries `organization_uid`, `user_uid`, `at`, and a
   `source` label (`empty_state`, `regular_form`, `import`, `api`) so we
   can tell which path produced the action.

6. **Activation funnel view (super-admin only)**: a simple table at
   `/admin/activation` showing per-org timestamps for each milestone. No
   charts in v1 — a sortable table is enough to spot whether the median
   org reaches `first_result_received` and how long it takes.

Out of scope:

- **Demo data mode** (a "show me what this looks like with fake data"
  toggle for evaluators). Worth doing later, separate spec.
- **Onboarding email drip**. Outside this codebase's responsibility today.
- **Public-facing landing-page changes**. Pre-signup funnel; different
  surface, different team's call.
- **A/B testing infrastructure**. Measure the new path against itself
  over time; we are not big enough yet for split tests to be meaningful.
- **Magic-link / passwordless auth**. Out per security pushback in
  conversation — kept for the record so future readers don't relitigate.

## Implementation notes

- The empty-state should be a true empty-state, not a modal that overlays
  the regular dashboard. Modals are dismissible and dismissed onboarding
  is dead onboarding.
- Defaults for the quick-start check matter. `period: 60s`,
  `enabled: true`, `region: "auto"` (or first available), and a
  reasonable timeout. Do not surface these in the quick-start UI — the
  full check editor is one click away if the user wants control.
- The first-result polling must not double the dashboard's request load.
  Reuse the existing 30s poll; if no result is in by tick 1, show "running
  now…" with a spinner anchored to the new check card.
- Email-as-default-notification needs to handle the case where the org
  hasn't configured outbound email yet. If it can't send, the
  empty-state CTA points to the email-config setup instead.

## Edge cases

- **User aborts the first check creation halfway.** The empty state must
  re-render on next visit — don't show the regular zero-state list view as
  a fallback; it's strictly worse.
- **User imports checks via API/CLI before opening the UI.** Skip the
  empty state — they're already past it. Detect via `checks.length > 0`.
- **First check fails on first run** (DNS error, 4xx, etc.). The
  celebration banner should still fire — the *system* worked, even if the
  *target* is broken. That's actually the most useful onboarding moment:
  "look, we already caught a problem."
- **User creates an org but never logs in again before timeout**.
  Activation event `signup_completed` fires; nothing else does. Funnel
  table shows them as stalled at step 1. That's correct.

## Test plan

- [ ] Manual: fresh org, new user, never seen the app. Land on dashboard,
      type one URL, hit go, see result within 30s. Time it end-to-end.
      Goal: under 60s wall-clock for HTTP type.
- [ ] Manual: same but ping and SSL. Same target.
- [ ] Manual: integrations empty state — confirm the three cards render
      and the email connect path requires only one confirmation click.
- [ ] Manual: super-admin funnel table renders for every org and shows
      reasonable timestamps.
- [ ] Backend: events are emitted exactly once per org per milestone,
      idempotent if a worker retries.
- [ ] e2e: happy-path activation flow in `web/dash0/e2e/`.

## Files touched (estimate)

- `web/dash0/src/components/dashboard/empty-state-onboarding.tsx` (new)
- `web/dash0/src/routes/orgs/$org/index.tsx`
- `web/dash0/src/routes/orgs/$org/integrations*` (verify path)
- `web/dash0/src/routes/admin/activation.tsx` (new, super-admin)
- `web/dash0/src/locales/{en,fr,de,es}/onboarding.json` (new namespace)
- `server/internal/handlers/checks/service.go` — first-run hook
- `server/internal/handlers/orgs/` — emit activation events
- `server/internal/jobs/` or scheduler — immediate-run path

## Why this is worth more than the form-field cuts

Removing form fields lifts signup completion by some small percentage points.
Compressing the path from "logged in for the first time" to "got a real
notification about a real outage on a real URL" lifts *retention*, which is
the metric that turns trial users into paying ones. The form-field specs are
cheap and we should ship them. This is the spec that actually moves the
business outcome the user named.
