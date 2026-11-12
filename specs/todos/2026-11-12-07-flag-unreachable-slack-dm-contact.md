---
model: sonnet
effort: medium
---

# Flag a Slack DM contact Slack rejects as unreachable, instead of retrying it on every escalation

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/71

## Source

Daily review of the dev pod logs (2026-11-12). The escalation step logged
`failed to send escalation Slack DM` at WARN twice, `slack API error: channel_not_found`, for
the same contact, once per escalation repeat of the same incident. The stored Slack user id of
that contact is stale or belongs to another workspace. The reporter suggests logging the Slack
error code more clearly, flagging or disabling the contact after `channel_not_found`, and
surfacing it in the UI.

The same issue lists three other findings, handled outside this spec:

- tlsedge WARN per refused unknown host: fixed directly (commit `29c5951f`, now DEBUG).
- An SNMP check flapping between up and warning (`Check up` / `Check warning` INFO lines): no
  code change. Up and warning are INFO by design (`server/internal/handlers/incidents/status_log.go:15`,
  the transitions that notify), warning opens no incident, and flap damping already exists for
  incidents (`FlappingWindowSeconds` / `FlapBackoffFactor` on the check). The target itself
  times out; it is an operator matter.
- Browser sidecar `socat ... Connection reset by peer` on Chrome restart: harmless, no action.

## Problem

`sendEscalationSlackDM` (`server/internal/jobs/jobtypes/job_escalation_step.go:617`) posts to
`route.Contact.Value` through `postSlackDM` (`:1255`). Any error is logged at WARN with the
wrapped message (`:667`) and the route counts 0 deliveries. Nothing is recorded on the contact:

- The next repeat, and every later incident, posts to the same dead id again and logs the same
  WARN. The user is silently unreachable on Slack.
- The user never learns it: the contact keeps showing as a working route in their notification
  settings.
- The Slack error code (`channel_not_found`, `user_not_found`, `invalid_auth`…) is only inside the error string
  (`server/internal/integrations/slack/client.go:616`, `fmt.Errorf("%w: %s", ErrSlackAPI, apiResp.Error)`),
  so the caller cannot tell a dead recipient from a transient Slack outage or a revoked bot token.

`UserContact` (`server/internal/db/models/user_contact.go:71`) has no delivery-state column.
Discord's stale DM channel already self-heals (`DMChannelID`, re-opened on 404); Slack has no
equivalent because a Slack user id is the address itself.

## Why a spec

- A DB migration (a delivery-failure state on `user_contacts`).
- An API change: the contact resource must expose that state, and the dashboard must show it.
- A product decision: disable the route, or only flag it and keep trying.

## Proposal

1. **Typed Slack error.** In `server/internal/integrations/slack/client.go`, return a typed
   error carrying the Slack `error` code (e.g. `*APIError{Code string}` wrapping `ErrSlackAPI`, so
   existing `errors.Is` callers keep working). Classify codes that mean "this recipient cannot be
   reached": `channel_not_found`, `user_not_found`, `user_disabled`, `cannot_dm_bot` (check the
   `chat.postMessage` docs for the exact list). Token/workspace codes (`invalid_auth`,
   `token_revoked`, `account_inactive`, which Slack returns for the bot's own token) are a
   connection problem, not a contact problem: keep them as today.
2. **Log.** The WARN at `job_escalation_step.go:667` gains a `slackError` attribute with the code.
3. **Record it on the contact.** New nullable columns on `user_contacts` (next migration number,
   SQLite + Postgres): `delivery_failed_at` and `delivery_failure_code`. On a recipient-class
   error, set them; on the next successful send to that contact, clear them. Updating the
   contact column must not fail the escalation step (log and continue).
4. **Stop the repeat noise.** While `delivery_failed_at` is set, the escalation step still tries
   the contact (an id can come back, e.g. a reactivated account) but logs the repeat failure at
   DEBUG instead of WARN, so one dead contact yields one WARN, not one per repeat.
5. **Surface it.** Expose `deliveryFailedAt` / `deliveryFailureCode` on the user contact in the
   API (OpenAPI + handler). In dash0's notification-settings contact list, show a warning badge
   ("Slack could not reach this contact: user not found. Reconnect or remove it.") with a
   `data-testid="contact-delivery-failed"`. Re-linking or editing the contact clears the flag.
6. **Tests.** Go: an httptest Slack stand-in (`slackDMBaseURL` seam) answering
   `{"ok":false,"error":"channel_not_found"}` sets the flag and logs WARN once, a second send
   logs DEBUG, a success clears it, and `invalid_auth` does not flag the contact. Playwright: the
   badge shows for a flagged contact.

## Open questions

1. **Flag only, or also disable the route?** Recommended: flag only and keep trying. Disabling
   would silently drop a page if Slack answered `user_not_found` transiently or the user
   reconnects; the badge plus the quieter log fixes the noise without risking a missed page.
   Trade-off: a dead contact keeps costing one Slack API call per escalation.
2. **Notify the user by another channel (email) when their Slack contact is flagged?**
   Recommended: not in this spec; the badge is enough for a first pass.

## Closing the issue

This spec closes fclairamb/solidping-business#71 (its other findings are fixed by `29c5951f`
or need no change, see Source). The implementing PR body **must** carry a
`Closes fclairamb/solidping-business#71` line. The issue is already closed by `/fix-issues`;
when the spec is archived to `specs/done/`, verify it is still closed and, if not, close it:

    gh issue close 71 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
