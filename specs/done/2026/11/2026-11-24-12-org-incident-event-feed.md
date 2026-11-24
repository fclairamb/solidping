---
model: opus
effort: high
---

# No private org-wide Atom feed of incidents and events

GitHub Issue: https://github.com/fclairamb/solidping/issues/1014

## Source

Florent (2026-11-24) asked for "RSS on the status page, and a private feed for incidents and
events". The issue checked main: the per-page public feed exists and respects visibility. What is
missing is an org-level **private** feed of incidents and events, including incidents never
published to a status page, for feed readers, a NOC wall or automation. Proposed shape:
secret-in-URL like the on-call iCal feed, enable/disable/rotate endpoints, a dashboard toggle,
docs. Open: event scope, retention window, one secret or several per org.

The report is right: the status-page half already exists, only the org feed is missing.

## Problem

- The only Atom feed is per status page:
  `publicOrgAPI.GET("/status-pages/:org/:slug/feed.xml", statusSubscribersHandler.Feed)`
  (`server/internal/app/server.go:2348`; `Handler.Feed`/`buildFeed` and the `atomFeed` types at
  `server/internal/handlers/statussubscribers/handler.go:424-501`). It only holds the page's
  published status updates.
- The org event stream exists only behind auth: `GET /api/v1/orgs/:org/events`
  (`server/internal/app/server.go:1811`, `handlers/events`) and its websocket (`:1829`). Its types
  (`web/docs/docs/features/events.md`): `check.*`, `incident.created|acknowledged|escalated|resolved`,
  plus status page, membership and integration events. Feed readers cannot send a bearer token.
- The pattern to reuse is the on-call iCal feed: nullable `ical_secret` column
  (`server/internal/db/models/on_call_schedule.go:33`, NULL = disabled), looked up by
  `GetOnCallScheduleByICalSecret` (`server/internal/db/postgres/on_call.go:57`), served outside
  the auth middleware by `ServeICalFeed` (`server/internal/handlers/oncallschedules/ical.go:33`,
  410 Gone on an unknown secret), managed by `POST /:uid/ical-feed/{enable,disable,rotate}`
  (`server.go:1721-1723`).

## Why a spec

- DB migration (a secret column or table for org feeds, both postgres and sqlite).
- New endpoints (feed + management) and OpenAPI changes.
- Authorization scope: an unauthenticated URL exposing org-internal incidents. The secret
  handling (entropy, constant-time-safe lookup, rotation, who may enable it) decides whether
  this leaks data.
- Product decisions left open by the issue.

## Proposal

1. **Storage**: a table `org_event_feeds` (`uid`, `organization_uid`, `name`, `secret_hash`,
   `created_by_user_uid`, `created_at`, `last_used_at`, `deleted_at`), postgres + sqlite
   migrations. Store a SHA-256 hash of the secret, not the secret itself: unlike the iCal
   column, this one exposes data from every check in the org. 32 random bytes, base64url,
   prefixed (e.g. `spf_`) so secret scanners can spot it.
2. **Feed endpoint** outside the auth middleware, on the iCal model:
   `GET /api/v1/org-feeds/:secret/feed.xml`. The org comes from the secret, so the URL carries
   no org slug. Unknown or revoked secret: 410 Gone, the same body as an unknown one. Rate limit
   per secret. `Cache-Control: private, max-age=60`, `X-Robots-Tag: noindex`. Atom serialization
   factored out of `statussubscribers` into a shared helper (no copy). Entries: one per event,
   with `id` = event UID, `updated` = event time, title from the event label (i18n as the Events
   page), link to the dashboard incident or check. Bound: the last 100 events or 30 days,
   whichever comes first.
3. **Content**: the event types chosen in open question 1, from the same query as
   `GET /orgs/:org/events`, so the feed and the Events page never disagree.
4. **Management** (org admin only; check the permission model the iCal endpoints use):
   `GET/POST /api/v1/orgs/:org/event-feeds`, `POST …/:uid/rotate`, `DELETE …/:uid`. The secret
   is shown once, at creation and rotation. Each action is written to the audit log.
5. **Dashboard**: org settings, an "Event feeds" card next to the kiosk/iCal tokens: create
   (name + scope), copy URL once, rotate, revoke, last used. `data-testid` on each control.
6. **Docs**: `web/docs/docs/features/events.md` § "Private Atom feed": URL format, rotation,
   what it contains, and how it differs from the public status-page feed (which only shows
   published updates).
7. **Tests**: feed contains incidents never published to a page; unknown/revoked secret → 410;
   rotation invalidates the old URL; a non-admin cannot create one; one org's secret never shows
   another org's events; Atom validates (parse it back). Playwright: create, copy, revoke.

## Open questions

1. **Event scope**: incidents only, the whole event stream, or chosen per feed?
   Recommended: per feed, defaulting to incident events (`incident.*`) plus `check.created|updated|deleted`.
   Membership/integration events are closer to the audit log and riskier to expose through a
   URL. A fixed scope is simpler, but a NOC wall and an automation hook want different ones.
2. **Retention window**: Recommended: last 100 entries or 30 days. Readers poll, so a short
   window is enough, and it bounds the cost of an unauthenticated request.
3. **One secret per org, or several?** Recommended: several named feeds (as kiosk tokens), so
   one consumer can be revoked without breaking the others; cap at 10 per org.
4. **RSS 2.0 too?** Recommended: Atom only, like the status-page feed; every reader handles it.

## Resolved open questions

1. Event scope: chosen per feed, defaulting to `incident.*` plus `check.created|updated|deleted`; membership/integration events are not offered. (autopilot: recommended answer)
2. Retention window: the last 100 entries or 30 days, whichever is smaller. (autopilot: recommended answer)
3. Secrets: several named feeds per org (as kiosk tokens), capped at 10 per org, each revocable on its own. (autopilot: recommended answer)
4. Format: Atom only, like the status-page feed; no RSS 2.0. (autopilot: recommended answer)

## Closing the issue

This spec closes #1014. The implementing PR body **must** carry a `Closes #1014` line so the
squash-merge closes it. When the spec is archived to `specs/done/`, verify the issue is closed
and, if the merge did not close it, close it by hand:

    gh issue close 1014 --comment "Implemented by <PR or commit>; spec: <archived spec path>"
