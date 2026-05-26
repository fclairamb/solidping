# Web Push Notification Delivery (per-user routes, then org channel)

> Depends on `2026-05-25-16-web-push-foundation.md` (VAPID keypair, Go send helper,
> service worker, `WebPushEnableButton` component). Ship that spec first.

## Context

SolidPing has two independent notification models:

1. **Org-level channels** (`integration_connections`, `server/internal/db/models/
   integration.go:64-88`) — shared sinks attached to *checks* (Slack, webhook, email,
   ntfy, …). Delivery goes through the `notifications.Sender` interface
   (`server/internal/notifications/sender.go:16-37`) and the job runner
   `server/internal/jobs/jobtypes/job_notification.go`.

2. **Per-user notification routes** (`user_contacts` + `user_notification_routes`,
   `server/internal/db/models/user_contact.go`) — personal endpoints per user (email,
   Slack DM, …). Delivery is the `dispatchRoute` switch in
   `server/internal/jobs/jobtypes/job_escalation_step.go:385-451`, fired by escalation
   policies, on-call schedules, and the all-admins path.

A Web Push subscription is `{endpoint, p256dh, auth}` — per-user, per-device, per-browser,
and tied to one person's browser session. It is therefore a **personal contact method**
and belongs primarily in model (2). Model (1) is a useful secondary delivery path for
teams that want to share a "push alerts to all opted-in team browsers" channel attached
to a check, analogous to a shared Slack channel.

This spec delivers both, phased: Phase A (per-user) ships first; Phase B (org channel)
extends it.

### What already exists (build on these, don't reinvent)

- `UserContactTypeEmail`, `UserContactTypeSlackUser`, etc. in `user_contact.go:11-17`.
- `dispatchRoute` in `job_escalation_step.go:385-451`: a type switch over
  `route.Contact.Type`. Adding Web Push is a new `case` here.
- `pageSchedule` (`:476-518`) and `pageAllAdmins` (`:521-563`) bypass the route-walking
  loop and email users directly. They need extending so push fires for on-call and admin
  paging too.
- `CreateIncidentNotification` audit row used by the `email` and `slack_user` dispatch
  cases — reuse the same audit pattern.
- `handlers/usernotifications/senders.go` — test-send adapters per contact type; add a
  `webpush` case here.
- `UNIQUE(user_uid, organization_uid, type, value)` on `user_contacts` —
  deduplicates a browser re-subscribing with the same endpoint (same row, no dupe).
- `UNIQUE INDEX idx_unr_contact` on `user_notification_routes` — one route per contact;
  multiple devices = multiple `UserContact` rows, each with its own route.
- `notifications/registry.go:11-41` + `notifications/registry_test.go:16-26` — the
  sender registry for model (1).
- Channel create-type allowlist in `handlers/channels/service.go:324-341`.
- Frontend capability registry: `CAPABILITIES` map in `api/hooks.ts:2666-2690`;
  `ALL_TYPES` in `channels.new.tsx:38-49`; `PerTypePanel` switch in
  `channel-form.tsx:125-272`; icon+label maps in `channel-icon.tsx:18-65`.

## Goals

**Phase A (per-user — ship first):**
1. A user can subscribe their current browser at Account → Notifications; a new "Browser
   push" route row appears alongside email/Slack DM.
2. When an escalation policy targets a `user`, that user's Web Push contacts are paged.
3. On-call schedule and all-admins paging also fires Web Push (not just email).
4. A push subscription that returns `404`/`410` (expired) is automatically pruned.
5. The test-send button works for Web Push routes.

**Phase B (org channel — defer until Phase A is stable):**
6. A `webpush` org channel can be created and attached to checks; any browser that has
   clicked "subscribe" to that channel receives push alerts on incidents.
7. The channel-count quota (`Free 3 / Pro 5 / Team 10 / Ent ∞`) applies in `server/saas/`
   exactly as for any other channel — no OSS paywall code.

## Non-goals

- Rich notification actions (accept/acknowledge incident from the push toast) — defer.
- Delivery receipts or read confirmations.
- Per-org VAPID keys (server-wide key from the foundation spec is sufficient).
- Web Push for `web/status0` status pages — separate feature.

---

## Phase A — per-user contact type

### Data model

No schema migration required for Phase A.

Add a new constant to `server/internal/db/models/user_contact.go:11-17`:

```go
const (
    UserContactTypeEmail     = "email"
    UserContactTypePhone     = "phone"
    UserContactTypeSlackUser = "slack_user"
    UserContactTypePushover  = "pushover_user"
    UserContactTypeNtfy      = "ntfy_topic"
    UserContactTypeWebPush   = "webpush"   // ← new
)
```

Storage conventions:
- `UserContact.Value` — full `PushSubscription` JSON from the browser
  (`JSON.stringify(pushSubscription)`). The existing `UNIQUE(user_uid, organization_uid,
  type, value)` naturally deduplicates re-subscribing the same browser.
- `UserContact.Label` — friendly device description derived from `navigator.userAgent`,
  e.g. `"Chrome on macOS"`. Displayed in the route row.
- `UserContact.VerifiedAt` — set to `now()` at creation (subscribing via browser IS the
  verification). Mirrors the behavior of the `email` path (`service.go:194-197`).

Dead subscriptions (push service returns `404`/`410`) are pruned by soft-deleting the
`UserContact` row inside `dispatchRoute`; the route cascades via the FK.

### Backend dispatch

`server/internal/jobs/jobtypes/job_escalation_step.go`:

**`dispatchRoute` (lines 385-451)** — add a new case:

```go
case models.UserContactTypeWebPush:
    err := webpush.Send(ctx, jctx.Services.WebPushOptions, route.Contact.Value, webpush.Message{
        Title: notificationTitle(payload),
        Body:  notificationBody(payload),
        URL:   incidentURL(payload),
    })
    if errors.Is(err, webpush.ErrSubscriptionGone) {
        // prune the dead subscription
        _ = jctx.DBService.DeleteUserContact(ctx, route.Contact.UID)
        return 0, nil
    }
    if err != nil {
        return 0, err
    }
    _ = jctx.DBService.CreateIncidentNotification(ctx, ...) // same audit pattern as email case
    return 1, nil
```

`jctx.Services.WebPushOptions` is a `webpush.Options` struct populated from config at
app startup (foundation spec wires this).

**`pageSchedule` (lines 476-518)** — currently sends a direct email to the on-call
user's `.Email`. Extend to also walk `ListUserContactsWithRoutes(ctx, onCallUser.UID,
orgUID)` and call `dispatchRoute` for each enabled route (not only email). Guard: keep
the direct-email fallback when the user has no routes at all (existing behavior).

**`pageAllAdmins` (lines 521-563)** — same extension: after the direct email, walk
routes for each admin user and call `dispatchRoute` per enabled route.

### API

`POST /api/v1/orgs/:org/users/me/notification-contacts`
(`handlers/usernotifications/service.go` `CreateContact`):
- Already accepts arbitrary `type` strings. Allow `webpush` through.
- Set `VerifiedAt = now()` (same as `email`, line 194-197).
- The body carries `{ "type": "webpush", "value": "<PushSubscription JSON>",
  "label": "Chrome on macOS" }`. The frontend populates all three.
- On `UNIQUE` constraint violation (same browser re-subscribing): return `409` with
  `code: CONFLICT` — the frontend silently treats this as already-subscribed.

`POST /api/v1/orgs/:org/users/me/notification-routes/:routeUid/test`
(`handlers/usernotifications/senders.go`):
- Add a `webpush` case that calls `webpush.Send(...)` with a test message
  (`"Test alert from SolidPing"`, body `"This is a test notification."`, URL pointing
  to the org's dashboard). Return success/failure in the existing response shape.

### Frontend (web/dash0)

**`web/dash0/src/routes/orgs/$org/account.notifications.tsx`:**

Icon/label maps (lines 41-65):
```ts
// contactTypeIcon — add:
webpush: BellRing,   // or MonitorSmartphone

// contactTypeLabel — add:
webpush: 'Browser push',
```

`AddContactForm` (lines 228-314): currently a two-button toggle (`email` | `phone`) with
a text `<Input>`. Add a third affordance for Web Push:

- A "Add browser" section (below the email/phone inputs) using the `WebPushEnableButton`
  component from the foundation spec.
- On subscription success, the component calls back with the subscription JSON; the form
  auto-submits `useCreateNotificationContact({ type: 'webpush', value: subscriptionJSON,
  label: deriveDeviceLabel() })` where `deriveDeviceLabel()` parses `navigator.userAgent`
  into a short human-readable string.
- No manual text entry — the subscription is captured from the browser, not typed.

`RouteRow` (lines 67-171): the toggle (`enabled` switch), test button, and delete button
already work generically for any contact type — no changes needed.

**`web/dash0/src/locales/{en,de,fr,es}/`** — add `"webpush"` label in whichever file
holds per-contact-type strings (check `account-notifications.spec.ts` for the exact key
paths used by test selectors).

## Phase B — org-shared channel

### Data model

No schema migration required. The existing `integration_connections` table stores
type-specific config as JSONB in `Settings`. For a `webpush` channel, `Settings`
holds a `subscriptions` array:

```json
{
  "subscriptions": [
    { "endpoint": "https://fcm.googleapis.com/…", "keys": { "p256dh": "…", "auth": "…" }, "label": "Chrome on macOS" }
  ]
}
```

The `settings_private` / `settings_private_keys` encryption envelope is not needed here
— the VAPID private key lives server-side in config/app_settings (foundation spec); the
subscription endpoint+keys are useless without the VAPID private key.

### Backend

**`server/internal/db/models/integration.go:11-26`** — add:

```go
ConnectionTypeWebPush ConnectionType = "webpush"
```

`CapabilitiesFor` default branch already returns `{CanNotify: true, CanSource: false}` —
no capability change needed.

**New file `server/internal/notifications/webpush.go`**:

```go
type WebPushSender struct{}

func (s *WebPushSender) Send(ctx context.Context, jctx *jobdef.JobContext, payload *Payload) error {
    subs, _ := payload.Connection.Settings["subscriptions"].([]interface{})
    var gone []string
    for _, raw := range subs {
        sub, _ := json.Marshal(raw)
        err := webpush.Send(ctx, jctx.Services.WebPushOptions, string(sub), webpush.Message{
            Title: notificationTitle(payload),
            Body:  notificationBody(payload),
            URL:   incidentURL(payload),
        })
        if errors.Is(err, webpush.ErrSubscriptionGone) {
            gone = append(gone, endpointOf(raw))
        }
        // non-gone errors: log and continue (best-effort fan-out)
    }
    if len(gone) > 0 && payload.UpdateChannel != nil {
        pruneSubscriptions(payload.Connection.Settings, gone)
        _ = payload.UpdateChannel(ctx, payload.Connection)
    }
    return nil
}
```

Template: `server/internal/notifications/ntfy.go` (simplest existing sender — HTTP POST
with no secrets or signing). The `UpdateChannel` persistence callback is already wired in
`job_notification.go` (the same mechanism used for webhook secret rotation).

**`server/internal/notifications/registry.go:11-41`** — add:

```go
case models.ConnectionTypeWebPush:
    return &WebPushSender{}, true
```

**`server/internal/notifications/registry_test.go:16-26`** — add
`models.ConnectionTypeWebPush` to the `notifyTypes` slice (the test that verifies every
`CanNotify` type resolves a sender).

**`server/internal/handlers/channels/service.go:324-341`** — add
`models.ConnectionTypeWebPush` to the create-type allowlist switch.

`conn_secrets.go` — no entry needed (no secret fields on webpush channels).

### Frontend (web/dash0)

**`web/dash0/src/api/hooks.ts`:**
- Line 2644-2654: add `'webpush'` to the `ConnectionType` union.
- Line 2666-2690: add `webpush: { canNotify: true, canSource: false }` to `CAPABILITIES`.

**`web/dash0/src/routes/orgs/$org/channels.new.tsx:38-49`** — add `'webpush'` to
`ALL_TYPES`.

**`web/dash0/src/components/channels/channel-form.tsx:125-272`** — add a
`case 'webpush'` in `PerTypePanel`:

```tsx
case 'webpush': return (
    <WebPushChannelPanel
        settings={settings}
        onChange={onSettingsChange}
        org={org}
        isEdit={isEdit}
    />
);
```

`WebPushChannelPanel` lists the subscribed devices (from `settings.subscriptions`, showing
`label` + a `Trash2` ghost icon button to remove), plus the `WebPushEnableButton`
component (from the foundation spec) to add the current browser. On click:
1. `WebPushEnableButton` captures the subscription.
2. The panel appends `{ endpoint, keys, label }` to `settings.subscriptions` and calls
   `onChange(newSettings)`.
3. The parent `ChannelForm` includes the updated settings in the next save.

Pruning (remove button): filter the subscription out of `settings.subscriptions` and call
`onChange`. The PATCH persists the pruned array.

**`web/dash0/src/components/channels/channel-icon.tsx`:**
- `ICONS` (line 18-29): add `webpush: BellRing`.
- `channelLabel` (line 40-65): add `case 'webpush': return 'Browser push'`.

**`web/dash0/src/locales/{en,de,fr,es}/channels.json`** — add:
```json
"hint": {
  "webpush": "Receive alerts as browser notifications on your subscribed devices"
}
```

**Quota** (`specs/questions/2026-01-03-saas-pricing.md`): a `webpush` org channel counts
toward the channel limit (Free 3 / Pro 5 / Team 10 / Ent ∞). Enforcement is in
`server/saas/` only — no paywall code in the OSS layer.

## Files (high level)

| Area | Files |
|---|---|
| **Phase A** | |
| Model const | `server/internal/db/models/user_contact.go` |
| Dispatch | `server/internal/jobs/jobtypes/job_escalation_step.go` |
| Test sender | `server/internal/handlers/usernotifications/senders.go` |
| Frontend | `web/dash0/src/routes/orgs/$org/account.notifications.tsx` |
| i18n | `web/dash0/src/locales/*/` (contact type label) |
| **Phase B** | |
| Model const | `server/internal/db/models/integration.go` |
| Sender | `server/internal/notifications/webpush.go` (new) |
| Registry | `server/internal/notifications/registry.go`, `registry_test.go` |
| Service | `server/internal/handlers/channels/service.go` |
| Frontend | `web/dash0/src/api/hooks.ts`, `channels.new.tsx`, `channel-form.tsx`, `channel-icon.tsx`, `locales/*/channels.json` |

## Tests

### Backend (table-driven, `testify/require`, `t.Parallel()`)

**Phase A:**

`job_escalation_step_test.go`:
- `pageUser` with a user who has one webpush contact: a mock push service receives the
  request; an audit row is written.
- `pageUser` where the push service returns `410`: the contact is soft-deleted; no error
  propagates.
- `pageSchedule` / `pageAllAdmins` with a user who has a webpush route: push is sent.

`handlers/usernotifications/service_test.go`:
- `CreateContact` with `type: "webpush"`: succeeds; `VerifiedAt` is set immediately.
- Re-subscribing the same endpoint (duplicate `value`) returns `CONFLICT`.

`handlers/usernotifications/senders_test.go`:
- `TestRoute` for a webpush route: mock push service receives a test message; success
  response returned.

**Phase B:**

`server/internal/notifications/registry_test.go` (`:16-26`): confirm
`ConnectionTypeWebPush` is in `notifyTypes` and resolves a non-nil sender.

`server/internal/notifications/webpush_test.go`:
- `WebPushSender.Send` with two subscriptions: mock push service returns `201` for both;
  `UpdateChannel` callback not called.
- One subscription returns `410`: that endpoint is removed from the settings; callback is
  called with the pruned channel; the other subscription's push still fires.

### E2E Playwright

**Phase A — `web/dash0/e2e/account-notifications.spec.ts`:**
- `page.context().grantPermissions(['notifications'])` (same pattern as
  `e2e/channels-webhook.spec.ts` for clipboard).
- Navigate to Account → Notifications.
- Assert "Add browser" affordance is visible.
- Click `WebPushEnableButton`; mock `POST .../notification-contacts` to capture the
  submitted body; assert `type === 'webpush'` and `value` parses as a valid JSON
  subscription object.
- Assert a new route row with label "Browser push" appears in the list.
- Click the test button for the webpush route (mock `POST .../test`); assert success toast.

**Phase B — `web/dash0/e2e/channels.spec.ts`:**
- Type picker shows `pick-webpush` in `group-notify`.
- Click webpush, assert the panel renders a subscribe button and an empty device list.
- Simulate subscription (mock `PushManager.subscribe` via `page.route` or
  `page.evaluate`); assert channel PATCH body contains `settings.subscriptions[0]`.

## Verification

1. `make build`, `make lint`, `make test` — green (both DB backends for DB tests).
2. `make test-dash` — Playwright E2E green.
3. **Phase A manual smoke**: create an escalation policy that targets a user; open Account
   → Notifications and subscribe the browser; trigger a test incident; confirm an OS
   browser notification arrives with the correct title and clicking it opens the incident
   page.
4. **Phase B manual smoke**: create a webpush channel via the UI; subscribe the browser
   to it; attach the channel to a check; trigger a test alert; confirm the notification
   arrives.
5. **Pruning**: use DevTools → Application → Storage → Push to manually invalidate the
   subscription; trigger an alert; verify the dead subscription disappears from the
   UI without a 500 error.

## Priority

P1.3 (depends on foundation spec `2026-05-25-16-web-push-foundation.md`)

## Implementation plan

**Phase A:**

1. `UserContactTypeWebPush` const in `user_contact.go`; extend `dispatchRoute` with
   webpush case (send + gone-prune + audit); extend `pageSchedule` and `pageAllAdmins` to
   walk routes. Tests. Commit: `feat(notifications): dispatch web push for escalation policy user targets`.

2. Extend `CreateContact` to accept and verify `webpush`; extend `TestRoute` with webpush
   case in `senders.go`. Tests. Commit: `feat(api): accept webpush contacts in user notification routes`.

3. Frontend Account → Notifications: add icon/label, `WebPushEnableButton` affordance in
   `AddContactForm`, `deriveDeviceLabel()` helper, i18n keys.
   Commit: `feat(dash0): add browser push subscribe affordance to account notifications`.

4. Phase A E2E (`account-notifications.spec.ts` webpush flows).
   Commit: `test(dash0): e2e for web push per-user notification routes`.

5. `make build`, `make lint`, `make test`, `make test-dash` — fix issues.

**Phase B:**

6. `ConnectionTypeWebPush` const; `notifications/webpush.go` sender + registry + allowlist;
   `registry_test.go` + `webpush_test.go`. Commit: `feat(notifications): add webpush org channel sender`.

7. Frontend channels: `hooks.ts`, `channels.new.tsx`, `channel-form.tsx`,
   `channel-icon.tsx`, `locales/*/channels.json`.
   Commit: `feat(dash0): add webpush channel type to channel management UI`.

8. Phase B E2E (`channels.spec.ts` webpush flows).
   Commit: `test(dash0): e2e for webpush org channel`.

9. `make build`, `make lint`, `make test`, `make test-dash` — fix issues.

10. Archive: move spec to `specs/done/2026/05/2026-05-25-17-web-push-notification-delivery.md`.
