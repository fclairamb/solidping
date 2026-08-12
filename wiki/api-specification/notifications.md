# Notifications

Per-user notification routing, notification delivery history, web push
registration, the email suppression list, and the public unsubscribe surface.

The integrations that notifications are delivered *through* (Slack, webhook,
email…) are documented in [integrations.md](integrations.md).

## Notification history

Read-only views over what was actually delivered. All require auth.

### GET /api/v1/orgs/:org/incidents/:uid/notifications
List the notifications fired for one incident (which route, which contact,
status, timestamps).

### GET /api/v1/orgs/:org/incidents/:uid/notifications/:notifUid
Get one incident notification.

### GET /api/v1/orgs/:org/notifications
List notifications across the org.

### GET /api/v1/orgs/:org/notifications/:notifUid
Get one notification.

### GET /api/v1/orgs/:org/users/:uid/notifications
List notifications delivered to a specific user.

### GET /api/v1/orgs/:org/me/notifications
List notifications delivered to the calling user.

## Notification routes & contacts

A *contact* is an address (email, phone, push subscription); a *route* binds a
contact to when it should fire. Both are managed under the `users/me` prefix —
a user only edits their own. All require auth.

### GET /api/v1/orgs/:org/users/me/notification-routes
List the calling user's notification routes (with their contacts).

### POST /api/v1/orgs/:org/users/me/notification-contacts
Create a contact for the calling user.

### PATCH /api/v1/orgs/:org/users/me/notification-routes/:routeUid
Update a route (enable/disable, delay, severity filter).

### DELETE /api/v1/orgs/:org/users/me/notification-contacts/:contactUid
Delete a contact (and the routes bound to it).

### POST /api/v1/orgs/:org/users/me/notification-routes/:routeUid/test
Send a test notification through the route so the user can confirm it works.
Every pageable contact type is covered: email, Slack DM, web push, Telegram,
SMS (via the org's default Twilio connection) and WhatsApp (via the approved
alert template). A contact whose setup round-trip is not complete (unverified
phone/WhatsApp, disconnected Telegram) is refused with `VALIDATION_ERROR`
rather than tested — the dashboard only shows the Test button once a route is
ready.

### POST /api/v1/orgs/:org/users/me/telegram/link
Mint a single-use Telegram connect link (`{url, expiresAt}`, TTL 15 minutes).
Nothing is created here: the `telegram` contact only comes into existence when
the user presses **Start** in Telegram and the resulting `/start <token>`
reaches the instance webhook. Pressing Start is both the reachability proof and
the opt-in, which is why this channel has no verification round-trip.
Returns `VALIDATION_ERROR` when the instance has no Telegram bot configured.

**`telegram` contacts may never be created via
`POST /notification-contacts`** — that request is rejected with
`VALIDATION_ERROR`. A telegram contact's value is a chat id and nothing would
catch a wrong one, so accepting it from a request body would let any user page
a stranger.

## Web push

### GET /api/v1/orgs/:org/webpush/vapid-public-key
Return the server's VAPID public key so the browser can create a push
subscription. Auth: required

## Email suppressions

Read + delete only — entries are *created* by bounce/complaint feedback and by
the public unsubscribe surface below, never through this authenticated API.

### GET /api/v1/orgs/:org/email-suppressions
List suppressed addresses for the org. Auth: required

### GET /api/v1/orgs/:org/email-suppressions/:uid
Get one suppression entry. Auth: required

### DELETE /api/v1/orgs/:org/email-suppressions/:uid
Remove a suppression (re-enable delivery to that address). Auth: required

## Unsubscribe (public, top-level)

These three routes are **not** under `/api/v1` — they are mounted at the root
so an unsubscribe link stays short and stable. There is no session and no org
in the path: a signed token authenticates the request and carries the org and
recipient. A recipient's unsubscribe link has no other org context available.

### POST /unsubscribe
RFC 8058 one-click unsubscribe — the target of the `List-Unsubscribe-Post`
header. Auth: public (signed token).

### GET /unsubscribe
Human-facing confirmation page for the same token. Auth: public (signed token).

### GET /unsubscribe/undo
Reverse a just-performed unsubscribe. Auth: public (signed token).
