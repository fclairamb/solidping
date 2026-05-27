# Per-user notification routing

## Context

When an escalation policy step targets a `user`, `schedule`, or `all_admins`,
`fanOutWithSeverity` in `server/internal/jobs/jobtypes/job_escalation_step.go:192-218`
always resolves to a plain email at `user.Email`. The comment at line 160-166
explicitly calls this out as a V1 limitation: "Slack/Discord per-user DMs
require a per-org bot which is its own spec."

The severity vocabulary in `server/internal/db/models/severity.go:111` already
names `email`, `sms`, `voice`, `push`, `critical_push` as channel types in
seed data — but for user-targeted escalation steps, all of them silently
collapse to email today. The gap was deferred by `specs/done/2026/05/2026-05-02-19-escalation-policies.md:285-287`.

Today there is:
- No `phone_number` on users (`server/internal/db/models/auth.go:56-71`)
- No `user_contacts` or `user_notification_routes` table
- `UserProvider` (`auth.go:115-130`) does store a Slack sub (the Slack user ID)
  when a user signs in with Slack — unused for notifications today
- No SMS provider integrated (Twilio / OVH / etc.) — the word `sms` exists only
  in seed vocabulary
- The sender abstraction at `server/internal/notifications/registry.go:8-31`
  accepts a clean drop-in for new senders

This spec wires the rails: a schema for per-user contact methods and ordered
notification routes, a UI for users to configure their own preferences, and a
rewritten `pageUser` that fans out over those routes. SMS is modelled and stored
but not delivered until a provider spec ships.

## Goal

A user in the dashboard can open **Account → Notifications**, see their email
address pre-populated, add a Slack DM route (one click if they signed in via
Slack), and optionally add a phone number for future SMS. When an escalation
policy step pages that user, all enabled routes fire. No user silently stops
receiving pages because they never visited the settings page — email is
auto-seeded on first access.

## Schema (migration 024)

### `server/internal/db/postgres/migrations/024_user_notification_routing.up.sql`

```sql
CREATE TABLE user_contacts (
    uid              uuid        PRIMARY KEY,
    user_uid         uuid        NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
    organization_uid uuid        NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
    type             text        NOT NULL,        -- 'email' | 'phone' | 'slack_user' | 'pushover_user' | 'ntfy_topic'
    value            text        NOT NULL,        -- email addr, E.164 phone, Slack user ID U…, etc.
    label            text        NOT NULL DEFAULT '',
    verified_at      timestamptz NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    deleted_at       timestamptz NULL,
    UNIQUE (user_uid, organization_uid, type, value)
);

CREATE INDEX idx_uc_user_org ON user_contacts (user_uid, organization_uid) WHERE deleted_at IS NULL;

CREATE TABLE user_notification_routes (
    uid          uuid    PRIMARY KEY,
    user_uid     uuid    NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
    org_uid      uuid    NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
    contact_uid  uuid    NOT NULL REFERENCES user_contacts(uid) ON DELETE CASCADE,
    enabled      boolean NOT NULL DEFAULT true,
    position     int     NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_unr_contact ON user_notification_routes (contact_uid);
CREATE INDEX idx_unr_user_org ON user_notification_routes (user_uid, org_uid);
```

### SQLite mirror (`024_user_notification_routing.up.sql`)

Same DDL with `uuid → text`, `timestamptz → text`,
`DEFAULT now() → DEFAULT (datetime('now'))`. Pattern from
`007_escalation_policies.up.sql`.

### Down migrations

```sql
DROP TABLE IF EXISTS user_notification_routes;
DROP TABLE IF EXISTS user_contacts;
```

### Contact type vocabulary

| `type`          | `value` format          | Sender today          |
|-----------------|-------------------------|-----------------------|
| `email`         | RFC 5322 address        | `notifications/email.go` |
| `phone`         | E.164, e.g. `+33612345` | **no-op** (see §SMS)  |
| `slack_user`    | Slack user ID `U…`      | `notifications/slack.go` via org's Slack `Channel` |
| `pushover_user` | Pushover user key       | future                |
| `ntfy_topic`    | topic string            | future                |

## Bun models

New file `server/internal/db/models/user_contact.go`. Follow
`escalation_policy.go` template.

```go
const (
    UserContactTypeEmail      = "email"
    UserContactTypePhone      = "phone"
    UserContactTypeSlackUser  = "slack_user"
    UserContactTypePushover   = "pushover_user"
    UserContactTypeNtfy       = "ntfy_topic"
)

type UserContact struct {
    bun.BaseModel `bun:"table:user_contacts"`

    UID             string     `bun:"uid,pk,type:varchar(36)"`
    UserUID         string     `bun:"user_uid,notnull,type:varchar(36)"`
    OrganizationUID string     `bun:"organization_uid,notnull,type:varchar(36)"`
    Type            string     `bun:"type,notnull"`
    Value           string     `bun:"value,notnull"`
    Label           string     `bun:"label,notnull"`
    VerifiedAt      *time.Time `bun:"verified_at"`
    CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"`
    UpdatedAt       time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
    DeletedAt       *time.Time `bun:"deleted_at,soft_delete"`
}

type UserNotificationRoute struct {
    bun.BaseModel `bun:"table:user_notification_routes"`

    UID        string    `bun:"uid,pk,type:varchar(36)"`
    UserUID    string    `bun:"user_uid,notnull,type:varchar(36)"`
    OrgUID     string    `bun:"org_uid,notnull,type:varchar(36)"`
    ContactUID string    `bun:"contact_uid,notnull,type:varchar(36)"`
    Enabled    bool      `bun:"enabled,notnull,default:true"`
    Position   int       `bun:"position,notnull,default:0"`
    CreatedAt  time.Time `bun:"created_at,notnull,default:current_timestamp"`
    UpdatedAt  time.Time `bun:"updated_at,notnull,default:current_timestamp"`

    Contact *UserContact `bun:"rel:belongs-to,join:contact_uid=uid"`
}
```

## Repo layer

Add to `server/internal/db/service.go`:

```go
// --- UserContacts / UserNotificationRoutes ---
ListUserContactsWithRoutes(ctx context.Context, userUID, orgUID string) ([]*models.UserNotificationRoute, error)
EnsureDefaultEmailRoute(ctx context.Context, userUID, orgUID, email string) error
UpsertUserContact(ctx context.Context, c *models.UserContact) error
DeleteUserContact(ctx context.Context, uid string) error
SetRouteEnabled(ctx context.Context, routeUID string, enabled bool) error
ReorderRoutes(ctx context.Context, userUID, orgUID string, routeUIDs []string) error
GetSlackChannelForOrg(ctx context.Context, orgUID string) (*models.Channel, error) // type=slack
```

Implementations: `server/internal/db/postgres/user_contact.go` +
`server/internal/db/sqlite/user_contact.go` (identical body, different
package names).

### `EnsureDefaultEmailRoute`

Idempotent. Upserts one `UserContact(type=email, value=email, verified_at=now)`
and one `UserNotificationRoute(enabled=true, position=0)` if neither exists.
Uses `INSERT … ON CONFLICT DO NOTHING` so concurrent calls are safe.

## Auto-seeding on first access

In the handler for `GET /api/v1/orgs/:org/users/me/notification-routes` (see §API),
call `db.EnsureDefaultEmailRoute(ctx, userUID, orgUID, user.Email)` before
listing. This guarantees every user has at least email before the page renders.

A user who disables all routes is explicitly opting out — `EnsureDefaultEmailRoute`
is only called by the list handler (once per session at most due to staleTime),
never on every incident.

## Slack DM auto-suggest

In the same `GET …/notification-routes` response, include an optional
`slackSuggestion` field:

```json
{
  "routes": [...],
  "slackSuggestion": {
    "slackUserID": "U0123ABCDE",
    "workspaceName": "Acme Inc",
    "channelUID": "abc-def-..."
  }
}
```

Server-side logic:
1. Load `UserProvider(type=slack)` for `userUID`; if absent → `slackSuggestion = null`.
2. Load `Channel(type=slack)` for `orgUID`; if absent → `slackSuggestion = null`.
3. Check no `UserContact(type=slack_user, value=provider.ProviderID, orgUID)` already
   exists → if so → `slackSuggestion = null` (already added).
4. Return suggestion with `slackUserID = provider.ProviderID` + workspace name from
   `SlackSettings.TeamName`.

The frontend renders a dismissible banner: "You signed in with Slack (Acme Inc).
**Add Slack DM notifications** [one-click button]". Clicking POSTs
`/notification-routes` with the suggestion payload.

## API routes

All under `/api/v1/orgs/:org/users/me/` — auth: current user, org-scoped.

| Method | Path | Action |
|--------|------|--------|
| `GET`  | `notification-routes` | List routes (with contacts); auto-seed |
| `POST` | `notification-contacts` | Create a new contact + route |
| `PATCH` | `notification-routes/:routeUid` | Toggle enabled; reorder |
| `DELETE` | `notification-contacts/:contactUid` | Soft-delete contact + cascade route |
| `POST` | `notification-routes/:routeUid/test` | Send test notification |

Wrap list in `{ "data": [...], "slackSuggestion": … }`.

### Test notification endpoint

`POST .../notification-routes/:routeUid/test` immediately dispatches a test
message through the route's contact type:
- `email` → send a test email to `contact.value` via `EmailSender`
- `slack_user` → `chat.postMessage` "Test notification from SolidPing" to the
  user ID via the org's Slack `Channel` bot token
- anything else → return 422 "provider not configured"

Returns 204 on success. This is the "Send test notification" button the UI surfaces.

## Rewritten `pageUser` (escalation fan-out)

`server/internal/jobs/jobtypes/job_escalation_step.go:317`

Replace the current "send one email" logic with:

```go
func (r *EscalationStepJobRun) pageUser(
    ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
    incident *models.Incident, userUID *string,
) int {
    if userUID == nil {
        log.WarnContext(ctx, "pageUser: nil userUID")
        return 0
    }
    routes, err := jctx.DBService.ListUserContactsWithRoutes(ctx, *userUID, incident.OrganizationUID)
    if err != nil || len(routes) == 0 {
        // Fallback to direct email if table doesn't exist yet or is empty.
        user, userErr := jctx.DBService.GetUser(ctx, *userUID)
        if userErr == nil {
            return r.sendEscalationEmail(ctx, jctx, log, incident, user.Email,
                user.UID, models.IncidentNotificationSourceEscalationUser)
        }
        return 0
    }

    sent := 0
    for _, route := range routes {
        if !route.Enabled {
            continue
        }
        sent += r.dispatchRoute(ctx, jctx, log, incident, route)
    }
    return sent
}
```

`dispatchRoute` switches on `route.Contact.Type`:
- `email` → `sendEscalationEmail` (unchanged code path)
- `slack_user` → load org Slack `Channel`, post DM via `notifications/slack.go`
  sender with a synthesized payload where `settings.channel_id = contact.Value`
  and `settings.destination_type = "dm"`
- `phone` → `slog.WarnContext(…, "SMS provider not configured; skipping route")`;
  return 0 — visible in logs but not an error
- others → warn and skip

The fallback branch (no routes in DB) preserves V1 behaviour during the migration
window: users who haven't visited the notification settings page still get emailed.

## SMS: stored but not delivered

The data model accepts `type=phone` + E.164 `value`. The UI allows adding a phone
number. But `dispatchRoute` emits a structured log warning and returns 0 for any
`phone` route until a future SMS-provider spec ships the actual sender.

The warning log entry includes `contact_uid`, `user_uid`, and `org_uid` so
operators can audit which users have phone-only routes that are silently
no-oping. The notifications settings UI shows a badge:

> SMS not available — requires an SMS provider to be configured by your admin.

## UI

New route `web/dash0/src/routes/orgs/$org/account.notifications.tsx`  
Pattern: mirror `account.profile.tsx` (form page layout).

Sections:
1. **Notification methods** — a table of `UserContact` rows:
   - Columns: Type (icon + label) · Value · Status (Verified / Unverified / **SMS
     not available** for phone contacts) · Actions (toggle route enabled, delete)
   - "Add method" dropdown: Email (pre-filled, email-only if not already present),
     Phone (E.164 input), Slack DM (available only if suggestion exists; one-click),
     (future: Pushover, Ntfy)
2. **Slack DM suggestion banner** (dismissible) — rendered from `slackSuggestion`
   when present
3. **Order / priority** — drag-to-reorder routes via `ReorderRoutes`; displayed order
   is the delivery order when a user is paged (all fire in parallel, but the order is
   what the user configured)
4. **"Send test notification"** button per row → calls `POST
   .../notification-routes/:routeUid/test`; shows inline success/failure feedback

Add a **Notifications** link in the `account.*` sidebar section, adjacent to
*Profile* and *Security*.

## Severity filter (V2 upgrade path)

V1: every enabled route fires on every escalation event targeting the user.

V2 (documented here, not implemented): add `severities text[]` to
`user_notification_routes`. Null = fires on all. Non-null = fires only when the
escalation step's severity is in the array. The `dispatchRoute` switch would check
this after the `!route.Enabled` guard. No schema changes needed in V1 other than
reserving the column name.

## Out of scope

- SMS / voice provider wiring — own spec; phone contacts stored-but-not-delivered
- Phone number verification flow — own spec (pairs with SMS provider)
- Per-severity per-route filtering (documented V2 path above)
- Admin editing another user's contacts
- Webhook / Discord / Telegram as a personal contact type (use org Channels + escalation
  `connection` target for those)
- Cross-org contact copy

## Integration tests

New file `server/test/integration/user_notification_routes_test.go`.

1. **Auto-seed**: call `EnsureDefaultEmailRoute` twice for the same user+org;
   assert exactly one contact row + one route row.
2. **Escalation fan-out — email only**: create a user with the default route,
   run `pageUser`, assert one email sent.
3. **Escalation fan-out — Slack DM**: add a `slack_user` contact with a known
   user ID, stub `notifications/slack.go` sender, run `pageUser`, assert DM
   dispatched and no email sent (email route disabled).
4. **Escalation fan-out — phone skipped**: add a `phone` contact, run `pageUser`,
   assert warn log emitted and sent count = 0.
5. **Fallback to direct email when no routes**: seed a user with no DB routes,
   run `pageUser`, assert fallback email fires.
6. **Test notification — email**: `POST .../test` for an email route; assert email
   subject contains "Test notification".

## Verification

- [ ] `make build` — new models, interface methods, and handler compile.
- [ ] `make lint` — no new linting errors.
- [ ] `make test` — six new integration test cases pass; existing escalation tests
      unaffected (fallback path preserves V1 behaviour).
- [ ] `make migrate` — both postgres and SQLite migrations apply cleanly.
- [ ] Manual `make dev-test`:
      1. Log in as `test@test.com`
      2. Navigate to Account → Notifications — email auto-seeded, toggle visible
      3. Add Slack DM via suggestion — test notification arrives in DM
      4. Add phone number — badge "SMS not available" displayed
      5. Trigger an incident via an escalation policy targeting the user — Slack DM
         arrives instead of email (email route disabled)
- [ ] Playwright E2E `web/dash0/e2e/account-notifications.spec.ts`:
      mock the route endpoints; assert auto-seeded email row rendered; add Slack
      suggestion, click "Add Slack DM", assert new row appears with "Send test" button.

## Implementation plan

1. Write migrations `024_user_notification_routing.{up,down}.sql` for both dialects.
2. Run `make migrate`.
3. Write `server/internal/db/models/user_contact.go` — structs + constants.
4. Extend `server/internal/db/service.go` with the 7 new interface methods.
5. Write `server/internal/db/postgres/user_contact.go` + SQLite sibling.
6. Add handler + service for `GET /notification-routes` (with auto-seed + suggestion)
   in a new `server/internal/handlers/usernotifications/` package.
7. Add `POST /notification-contacts`, `PATCH /notification-routes/:routeUid`,
   `DELETE /notification-contacts/:contactUid`, `POST .../test` handlers.
8. Register all routes in `server/internal/app/server.go`.
9. Rewrite `pageUser` in `job_escalation_step.go:317` with fallback.
10. Add `dispatchRoute` with email + slack_user + phone-warn + fallback branches.
11. Write `server/test/integration/user_notification_routes_test.go` (6 cases).
12. Build `web/dash0/src/routes/orgs/$org/account.notifications.tsx` + sidebar link.
13. Add `useNotificationRoutes`, `useCreateNotificationContact`, `useDeleteNotificationContact`,
    `usePatchNotificationRoute`, `useTestNotificationRoute` hooks in `api/hooks.ts`.
14. Write `web/dash0/e2e/account-notifications.spec.ts`.
15. `make lint && make test && make test-dash`.
