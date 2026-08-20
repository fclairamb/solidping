---
model: opus
effort: high
---

# Deleting a notification contact leaves a ghost, undeletable route — and deleting the default email contact bricks the page

## Problem

Two related defects in the per-user notification methods flow, both reproduced
with throwaway service tests (in-memory SQLite; the Postgres query has the
identical shape, so both dialects are affected).

### 1. Every contact deletion leaves a ghost route

Deleting a notification contact soft-deletes the contact row but leaves its
`user_notification_routes` row behind, and the list endpoint keeps returning
that route with an **all-empty contact** (`uid=""`, `type=""`, `value=""`).

On the dashboard (`web/dash0/src/routes/orgs/$org/account.notifications.tsx`)
that renders as a ghost row: the `BellRing` fallback icon
(`web/dash0/src/components/integrations/integration-icon.tsx:116` — unknown/empty
type), no title, no value. The row cannot be removed: `handleDelete`
(`account.notifications.tsx:379`) calls the delete endpoint with
`route.contact.uid`, which is `""`, so the request can never match a contact
("Failed to remove notification contact" forever). Observed in production-like
use (dev deployment screenshot: one bell-icon row with no label between two
working methods).

Root cause — the list query's soft-delete guard is a tautology:

- `models.UserContact.DeletedAt` carries bun's `soft_delete` tag
  (`server/internal/db/models/user_contact.go:75`), and
  `UserNotificationRoute.Contact` is a `belongs-to` relation
  (`models/user_contact.go:108`).
- For a soft-delete relation model, bun v1.2.18 generates
  `LEFT JOIN user_contacts AS contact ON (contact.uid = ...) AND
  contact.deleted_at IS NULL` — the soft-delete predicate goes into the **JOIN
  ON** clause (`bun@v1.2.18/relation_join.go`, `appendHasOneJoin`).
- A soft-deleted contact therefore fails the join and all `contact.*` columns
  come back NULL. The query's own filter
  `Where("contact.deleted_at IS NULL")`
  (`server/internal/db/postgres/user_contact.go:24`, mirrored at
  `server/internal/db/sqlite/user_contact.go:24`) then evaluates **TRUE**
  (NULL IS NULL) — the guard that was meant to hide deleted contacts is
  exactly what lets the orphaned route through, with `route.Contact == nil`.
- `toRouteResponse` (`server/internal/handlers/usernotifications/service.go:428`)
  serializes the nil contact as a zero-value `ContactResponse{}` → ghost row.

The route row is never cleaned up because `DeleteUserContact` only soft-deletes
the contact (`db/postgres/user_contact.go:97`); the
`on delete cascade` FK on `user_notification_routes.contact_uid`
(`db/postgres/migrations/001_v0_1_0.up.sql:976`) only fires on hard deletes.
The comment on `Service.DeleteContact`
(`handlers/usernotifications/service.go:395` — "the route cascades via the DB
constraint") is wrong about soft deletes.

Repro (service level): create a second email contact, `DeleteContact` it,
`ListRoutes` again → still 2 routes, one with `contactUID="" type="" value=""`.

Side effects beyond the UI:
- The escalation dispatcher iterates these ghost routes and skips them with a
  warning on every dispatch (`jobs/jobtypes/job_escalation_step.go:512`) — log
  noise that scales with dead routes.
- The webpush prune path (`job_escalation_step.go:624`, on 404/410 Gone) and
  the Telegram webhook (`handlers/telegramcb/handler.go:453`) also soft-delete
  contacts, so ghosts appear without any user action too — the screenshot's
  ghost sits right next to a Browser-push row, consistent with a pruned dead
  webpush subscription.

### 2. Deleting the seeded default email contact bricks the whole page

`ListRoutes` seeds a default email route on every call via
`EnsureDefaultEmailRoute` (`db/postgres/user_contact.go:35`, sqlite mirror).
If the user deletes that seeded email contact:

1. The seeding INSERT hits `ON CONFLICT (user_uid, organization_uid, type,
   value) DO NOTHING` — the unique constraint
   (`001_v0_1_0.up.sql:967`) is **not** partial, so the soft-deleted row still
   conflicts and nothing is inserted.
2. The follow-up SELECT for the canonical UID is auto-filtered to
   `deleted_at IS NULL` by the `soft_delete` tag → `sql.ErrNoRows`.
3. `ListRoutes` returns `seed default email route: load default email contact:
   sql: no rows in result set` → the notifications page shows "Failed to load
   notification settings" **permanently** for that user+org.

Repro (service level): `ListRoutes` (seeds 1 route) → `DeleteContact` on it →
`ListRoutes` errors with the message above.

## Proposal

Fix the source of the dangle, the query that exposes it, the seeder that
crashes on it, and clean up existing rows. Apply every DB change to **both**
`db/postgres/` and `db/sqlite/` (the two files are line-for-line mirrors).

1. **Delete the route when its contact is deleted.** In `DeleteUserContact`
   (both dialects), also hard-`DELETE` the `user_notification_routes` row for
   that contact (the routes table has no `deleted_at`; hard delete is the
   design). Revive flows stay correct: `UpsertUserContact` resurrects a
   soft-deleted contact and every caller already re-creates the route
   (`CreateContact` inserts one when missing, and the Telegram webhook /
   members provisioning go through `EnsureUserNotificationRoute`). Fix the
   misleading "cascades via the DB constraint" comment on
   `Service.DeleteContact` while there.

2. **Make the list query actually exclude dangling routes.** In
   `ListUserContactsWithRoutes` (both dialects), replace the tautological
   `Where("contact.deleted_at IS NULL")` with `Where("contact.uid IS NOT
   NULL")` — bun's ON-clause predicate already excludes soft-deleted contacts,
   so requiring a joined row turns the LEFT JOIN into an effective inner join
   and covers any historical dangling rows and future regressions. Keep a
   comment explaining the bun soft-delete-in-ON behavior so the tautology is
   not reintroduced.

3. **Make the seeder tolerate a deleted default email — without resurrecting
   it.** The user's deletion is a choice; re-seeding on next page load would
   make the email method undeletable. In `EnsureDefaultEmailRoute`, when the
   insert conflicts, load the row including soft-deleted (bun
   `WhereAllWithDeleted()` + explicit checks): if the matching contact is
   soft-deleted, return without seeding; only ensure the route for a live
   contact.

4. **Clean up existing dangling routes** in the next release's consolidated
   migration (both dialects, following the `NNN_vX_Y_Z` naming rule):
   `delete from user_notification_routes r where not exists (select 1 from
   user_contacts c where c.uid = r.contact_uid and c.deleted_at is null);`
   (adapt syntax per dialect).

5. **Defense in depth at the API boundary:** skip routes with a nil contact in
   `toRouteResponses` so the endpoint can never emit an all-empty contact
   again, whatever a future path dangles.

### Tests

- Service: delete a contact → `ListRoutes` no longer returns its route (this
  is the exact repro that fails today — a regression test that proves the
  negative).
- Service: delete the seeded default email → `ListRoutes` still succeeds,
  returns no email route, and a subsequent call does **not** resurrect it;
  re-adding the same email via `CreateContact` revives contact + route.
- Service: after delete + re-add of the same contact value, exactly one route
  exists (revive path unbroken — positive control).
- Migration: seed a dangling route, run the cleanup, assert it is gone and
  live routes survive.
- E2E (`web/dash0/e2e/`): add a method, delete it, assert the row count drops
  and no empty-labelled row renders (there is currently no E2E coverage of
  contact deletion at all).

## Implementation Plan

Root cause re-verified against the vendored dependency before writing any code:
`bun@v1.2.18/relation_join.go:appendHasOneJoin` puts the relation's soft-delete
predicate in the **JOIN ON** clause (`isSoftDelete` → `" AND " + alias +
".deleted_at IS NULL"`), exactly as the Problem section describes. The
tautology is real, and `WhereAllWithDeleted()` exists on `*SelectQuery`
(`query_select.go:196`) for step 3.

### Step 1 — DB layer, both dialects (`db/postgres/user_contact.go` + `db/sqlite/user_contact.go`)

1a. **`DeleteUserContact`** — run both writes in one `RunInTx` (the pattern
already used in `db/*/escalation.go`, `on_call.go`): hard-`DELETE` the
`user_notification_routes` row for the contact, then soft-delete the contact.
Deleting the route first means a mid-transaction failure can never leave a
live contact with no route (the state `CreateContact` would silently repair);
the transaction makes the pair atomic either way. `user_notification_routes`
has no `deleted_at` — hard delete is the design.

1b. **`ListUserContactsWithRoutes`** — replace `Where("contact.deleted_at IS
NULL")` with `Where("contact.uid IS NOT NULL")`, carrying a comment that names
`appendHasOneJoin` so the tautology cannot be reintroduced by someone
"fixing" what looks like a missing soft-delete guard. This turns the LEFT JOIN
into an effective inner join and covers historical dangling rows.

1c. **`EnsureDefaultEmailRoute`** — after the conflict-ignoring INSERT, load
the canonical row with `WhereAllWithDeleted()` so a soft-deleted row is
visible instead of raising `sql.ErrNoRows`. If `existing.DeletedAt != nil`,
return nil **without** seeding: the deletion was a deliberate user choice, and
re-seeding on the next page load would make the email method undeletable. Only
a live contact gets its route ensured.

### Step 2 — API boundary (`handlers/usernotifications/service.go`)

2a. `toRouteResponses` skips routes with a nil contact (defense in depth: the
endpoint can never emit an all-empty `ContactResponse` again, whatever future
path dangles a route). `toRouteResponse` keeps its nil guard for its direct
callers.

2b. Fix the wrong doc comment on `Service.DeleteContact` — the FK's `on delete
cascade` only fires on hard deletes, so nothing cascaded; the route is now
deleted explicitly by the DB layer.

### Step 3 — Migration (both dialects, appended to the unreleased `014_v0_17_0`)

Append a `-- SECTION: dangling-notification-routes` block to
`db/postgres/migrations/014_v0_17_0.up.sql` and its SQLite mirror, deleting
routes whose contact is missing or soft-deleted. It is idempotent and
re-runnable, so the section-slicing migration tests can replay it. The
`.down.sql` counterpart is a documented no-op (rows deleted as garbage are not
resurrectable, and would not be wanted back) added at the *top* of the down
file's section list, since the down file unwinds sections in reverse order.

### Step 4 — Tests

- `handlers/usernotifications/service_test.go` (in-memory SQLite):
  1. delete a second contact → `ListRoutes` no longer returns its route (the
     exact repro that fails today — proves the negative);
  2. delete the seeded default email → `ListRoutes` still succeeds, returns no
     email route, and a **second** call does not resurrect it;
  3. re-adding the same email via `CreateContact` revives contact + route;
  4. delete + re-add of the same contact value leaves exactly **one** route
     (positive control — the revive path is unbroken).
- `db/sqlite/…_migration_test.go` + `db/postgres/…_migration_postgres_test.go`:
  seed a live route, a route whose contact is soft-deleted, and a route whose
  contact row is gone entirely; run the sliced section; assert the two dangling
  routes are gone and the live one survives; re-run for idempotence.
- `web/dash0/e2e/account-notifications-delete.spec.ts`: add a phone method,
  delete it, assert the row count drops back and that **no** row renders with an
  empty contact uid (`[data-testid="delete-contact-"]` has count 0 — a ghost row
  is exactly a row whose `route.contact.uid` is `""`).
