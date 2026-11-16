---
model: sonnet
effort: medium
---

# Personal notification methods never fire when no escalation policy pages the user

GitHub Issue: https://github.com/fclairamb/solidping/issues/905

## Source

A SaaS user (v0.46.0) connected Telegram under **Account → Notifications**
(`/d/orgs/<org>/account/notifications`), verified the link with the bot
(`/status` answers fine), yet received nothing in Telegram when incidents
opened. They then found **Alerting → Integrations**, added an email
integration there (which works), and noticed Telegram is not offered among the
integrations. They find it confusing that notifications can be set up both on
the user account and under the organization's "Alerting".

The report is right that nothing reached them, and right that the two surfaces
are confusing. It is wrong that Telegram is broken: the link and the sender
work; nothing ever asked them to be paged.

## Problem

Two independent delivery paths exist, and the UI does not say which one a
setting belongs to, nor warn when one of them is dead:

- **Org integrations** (Alerting → Integrations: email, Slack, Discord,
  webhooks…) fire on every incident of the checks they are attached to.
- **Personal methods** (Account → Notifications: email, Telegram, WhatsApp,
  SMS, Slack/Discord DM…) only fire when an escalation step targets the user
  (`user`, `schedule`, `all_admins`):
  `server/internal/jobs/jobtypes/job_escalation_step.go:204-264` resolves the
  targets, `pageUser` at `:520` fans out over the routes. Telegram is a
  per-user direct channel by design, never an org integration
  (`specs/done/2026/08/2026-08-10-04-telegram-alert-channel.md`), which is why
  it is absent from Integrations.
- The escalation policy is resolved check → group → org default → none
  (`server/internal/jobs/jobtypes/escalation_policy_resolve.go:32-59`), and the
  org default is opt-in (unset for every new org). An org that never created a
  policy, or never assigned one, therefore pages nobody personally: every
  Telegram/WhatsApp/SMS route a member configures is silently dead.
- The only hint is the card subtitle
  (`web/dash0/src/locales/en/account.json:287`, *"All enabled methods fire when
  an escalation policy targets you."*), shown even when no policy targets the
  user. `ListRoutes`
  (`server/internal/handlers/usernotifications/service.go:242-270`) returns
  the routes and the Slack/Discord hints but nothing about whether the user is
  reachable at all, so the page cannot warn.
- A successful Telegram connect (the bot's confirmation, `/status`) reads as
  "you will get alerts here", which is not true in that org.

## Why a spec

- A changed API response (`GET …/account/notifications` gains a coverage
  field) and its OpenAPI entry.
- A product/UX decision the reporter didn't settle: warn only, or also seed a
  default policy so personal methods work out of the box (provisioning
  defaults).

## Proposal

1. **Coverage in the API.** `ListRoutesResponse` gains
   `paging: { coveredBy: "org_default" | "some_checks" | "none", policyCount }`
   (exact shape at the implementer's discretion), computed for the caller:
   `org_default` when the org default policy has a step targeting the user
   (directly, via `all_admins` when the user is an admin, or via a schedule
   they belong to); `some_checks` when at least one live policy assigned to a
   check or group targets them; `none` otherwise. Reuse the target resolution
   of the escalation job, don't duplicate it. Document it in `openapi.yaml`.
2. **Warning on Account → Notifications.** When `coveredBy` is `none`, an
   amber alert above the methods card
   (`data-testid="notifications-not-paged-alert"`): *"No escalation policy
   pages you in this organization, so these methods will not receive alerts."*
   with a link to the escalation policies page for admins, and *"Ask an admin
   to add you to an escalation policy"* for others. `some_checks`: a lighter
   note that only some checks page you. All four locales.
3. **Telegram connect confirmation.** The bot's link-confirmation message and
   the in-app "connected" toast say the same thing when `coveredBy` is `none`
   (the bot message may simply append it; keep the bot copy short).
4. **Integrations page.** One line under Alerting → Integrations explaining
   that personal channels (Telegram, WhatsApp, SMS, DMs) are set per user
   under Account → Notifications and reached through escalation policies,
   with a link.
5. **Tests.** Go: coverage computation (no policy, org default with
   `all_admins` for admin vs member, user target, schedule target, policy only
   on one check, soft-deleted policy ignored). Playwright: the alert shows for
   a fresh org and disappears once an org default policy targets the user.

## Open questions

1. **Seed a default escalation policy at org creation?** Recommended: **no,
   warn only** in this spec. Seeding one ("page all admins, all enabled
   methods") would make personal methods work out of the box, but it changes
   every new org's behaviour (admins suddenly paged on email in addition to
   org integrations, duplicate alerts) and deserves its own decision. The
   warning removes the silent failure without changing who gets paged.
2. **Should the warning offer a one-click "Create a default policy that pages
   me / all admins" for admins?** Recommended: yes for admins, as a secondary
   button that creates a single-step `all_admins` policy and sets it as the
   org default after a confirmation showing the blast radius (reuse the
   org-default confirmation of
   `specs/done/2026/07/2026-07-19-01-escalation-org-default-and-assignment-picker.md`).
   Trade-off: more surface in this spec; drop it if it grows past the budget.

## Closing the issue

This spec closes #905. The implementing PR body **must** carry a
`Closes #905` line so the squash-merge closes it. When the spec is archived to
`specs/done/`, verify the issue is closed and, if the merge did not close it,
close it by hand:

    gh issue close 905 --comment "Implemented by <PR or commit>; spec: <archived spec path>"
