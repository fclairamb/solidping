# Post-registration org flow (backend)

**Pairs with:** spec #55 (frontend).

## Context

After confirming registration, a user with no organization membership
already lands in a `LoginActionNoOrg` state — but the API doesn't give
them anything to *do* from there beyond `POST /api/v1/orgs` (create a
brand-new org). There's no way to ask to join an existing one, and the
existing per-org auto-join regex (`registration.email_pattern`, read
inside `Service.autoJoinMatchingOrgs`) is unvalidated at write time, so
an admin can set `.*` and silently inhale every future registration.

This spec covers three workstreams:

1. **Auto-join regex hardening** — refuse dangerous patterns (`.*`,
   `.+@gmail.com`, …) at write time on `PATCH
   /api/v1/orgs/{org}/settings`, and skip them defensively at read time
   so leftover bad data can't crash signups.
2. **Membership requests** — a new entity letting a confirmed user
   request to join an org by slug, and letting org admins approve or
   reject. Notifications via the existing email machinery.
3. **`/auth/me` surface** — include the user's pending membership
   requests so the no-org screen (spec #55) can render them.

`POST /api/v1/orgs` already creates the caller as `admin` of a fresh
org; that path is unchanged. The frontend just needs to call it from
`/no-org` (spec #55).

## Honest opinion

1. **The auto-join regex feature *already exists* in the code, just
   without a safety net.** The dangerous footgun the user noticed is
   live today. Hardening it is the highest-priority piece of this
   spec — it fixes a real risk (`.*` setting on any org adopts every
   future signup) regardless of whether the request-to-join flow ever
   ships. Reviewers should validate that piece independently.
2. **Discovery model: by slug only, no public directory.** Listing all
   orgs is enumerable and leaks names users may not want disclosed
   (e.g. private trial orgs). The UX cost — "you have to know the
   slug" — is small; invite links remain the canonical onboarding
   path. A "discoverable org" toggle could be added later if there's
   demand.
3. **Domain verification (DNS TXT) is the gold standard for safe
   auto-join. It is *not* in this spec.** The denylist + structural
   checks here are a band-aid: a malicious admin can still claim
   `.+@victim-corp\.com` and harvest future signups from a domain
   they don't own. Flagging this as a known follow-up.
4. **Approval is transactional.** Approving a request must update the
   request row and create the membership row in the same DB
   transaction. Half-baked state ("approved but no membership") would
   be operationally painful and there's no good reason to allow it.
5. **Cooldown applies to requesters, not admins.** A 7-day cooldown
   stops a rejected user from spam-pinging admins, but admins should
   be able to re-approve a previously-rejected row instantly (mistake
   correction). The cooldown is a parameter
   (`membership_requests.cooldown_days`, default 7).
6. **Reuse the invitations notification machinery.** New email
   templates in the existing `notifications/` package — same dispatch
   path, same fake mailer in tests. Don't open a new channel for this.

## Scope

**In**

- `validateAutoJoinRegex(pattern string) error` helper, called from
  `Service.UpdateOrgSettings`. Defensive guard inside
  `autoJoinMatchingOrgs` skips invalid stored regexes instead of
  erroring the registration confirmation.
- New table `membership_requests` (Postgres + SQLite mirror migration).
- New entity, repository, and service methods for membership requests.
- Six new endpoints (see Design §4).
- New error codes:
  - `INVALID_AUTO_JOIN_REGEX`
  - `ALREADY_A_MEMBER`
  - `REQUEST_PENDING`
  - `REQUEST_NOT_FOUND`
  - `REQUEST_COOLDOWN_ACTIVE`
- Email notifications: new request → all org admins; decision →
  requester. Same package as invitations.
- Augment `MeResponse` with `pendingMembershipRequests`.
- Documentation in `docs/api-specification.md` and the API list in
  `CLAUDE.md`.

**Out**

- DNS-based domain verification (future spec).
- Public org directory / org search.
- Per-org override for the auto-join role (stays `user`).
- Frontend changes — see spec #55.

## Design

### 1. Auto-join regex validation

New helper in `server/internal/handlers/auth/service.go`:

```go
// validateAutoJoinRegex rejects patterns that would match too broadly.
func validateAutoJoinRegex(pattern string) error
```

Rules, in order:

- Empty string → allowed (disables auto-join).
- Must compile as Go RE2 (`regexp.Compile`).
- Must contain `@`.
- Best-effort static parse: extract the substring after the literal
  `@` and reject if it consists solely of `.*`, `.+`, `.`, `[^@]+`,
  `\S+`, or any pattern that lets the trailing domain be arbitrary
  (e.g. `.*@.*`).
- Free-mail domain denylist applied to the post-`@` portion. Stored
  in a private package var so we can extend it cheaply:
  ```
  gmail.com, googlemail.com, yahoo.com, outlook.com, hotmail.com,
  live.com, msn.com, icloud.com, me.com, protonmail.com, proton.me,
  aol.com, gmx.com, mail.com, yandex.com, tutanota.com, zoho.com,
  qq.com, 163.com, 126.com
  ```
- Synthetic permissiveness probe: must NOT match
  `attacker@evil.example`, `bob@gmail.com`, or `x@y.z`. If it
  matches any of these, validation fails (catches things the static
  parse misses).
- On failure: return a `ValidationError` carrying code
  `INVALID_AUTO_JOIN_REGEX` and a human-readable explanation of
  which rule failed.

`Service.UpdateOrgSettings` calls this before persisting. Inside
`Service.autoJoinMatchingOrgs`, every loaded pattern goes through
the same validator; if it fails, log a warning and skip that org —
do *not* fail the registration confirmation.

### 2. `membership_requests` table

```sql
CREATE TABLE membership_requests (
  uid              uuid PRIMARY KEY,
  organization_uid uuid NOT NULL REFERENCES organizations(uid),
  user_uid         uuid NOT NULL REFERENCES users(uid),
  message          text,
  status           text NOT NULL CHECK (status IN
                     ('pending','approved','rejected','cancelled')),
  decision_reason  text,
  decided_at       timestamptz,
  decided_by_uid   uuid REFERENCES users(uid),
  created_at       timestamptz NOT NULL,
  updated_at       timestamptz NOT NULL,
  UNIQUE (organization_uid, user_uid)
);
CREATE INDEX ON membership_requests (organization_uid, status);
CREATE INDEX ON membership_requests (user_uid, status);
```

Mirror the migration for SQLite (column types per existing pattern in
`server/internal/db/sqlite/migrations/`).

The `UNIQUE (organization_uid, user_uid)` constraint means each
(org,user) pair has at most one row; subsequent requests update the
existing row in place (state-machine transitions below).

### 3. State machine

```
        pending ──approve──▶ approved (terminal for this row,
           │                            membership row created in tx)
           ├──reject──▶ rejected
           ├──cancel──▶ cancelled (by requester only)
           │
rejected ──re-request──▶ pending (only if now ≥ decided_at + cooldown)
rejected ──admin-approve──▶ approved (no cooldown for admins)
cancelled ──re-request──▶ pending (no cooldown)
```

Approval is a single SQL transaction:
1. `UPDATE membership_requests SET status='approved', decided_at=now, decided_by_uid=...`
2. `INSERT INTO organization_members (...)` with role from request
   (`user` default; admin can pick from the existing role enum at
   approve time).

If either statement fails the whole tx rolls back.

Cooldown: `membership_requests.cooldown_days` parameter, default `7`.
Stored as a global parameter in `parameters` (org_uid NULL).

### 4. Endpoint surface

| Method | Path | Auth | Body | Response |
|---|---|---|---|---|
| POST   | `/api/v1/auth/membership-requests`                           | user            | `{orgSlug, message?}`            | `{uid, organization, status:"pending", createdAt}` |
| GET    | `/api/v1/auth/membership-requests`                           | user            | —                                | `{data:[…own requests with org summaries]}`        |
| DELETE | `/api/v1/auth/membership-requests/{uid}`                     | user (owner)    | —                                | 204                                                |
| GET    | `/api/v1/orgs/{org}/membership-requests`                     | admin of {org}  | `?status=pending` (optional)     | `{data:[{uid,user,message,status,createdAt}]}`     |
| POST   | `/api/v1/orgs/{org}/membership-requests/{uid}/approve`       | admin of {org}  | `{role?}` (default `user`)       | request row (status=`approved`)                    |
| POST   | `/api/v1/orgs/{org}/membership-requests/{uid}/reject`        | admin of {org}  | `{reason?}`                      | request row (status=`rejected`)                    |

Rules:
- POST self: org-slug lookup; 404 → `ORGANIZATION_NOT_FOUND`. Already
  a member → 409 `ALREADY_A_MEMBER`. Pending row exists → 409
  `REQUEST_PENDING`. Rejected within cooldown → 409
  `REQUEST_COOLDOWN_ACTIVE`.
- DELETE self: only the requester can cancel; admins can only
  reject. 403 `FORBIDDEN` if caller isn't the owner.
- Approve role accepts the existing role enum (`admin`, `user`,
  `viewer`); default `user`.

### 5. `MeResponse`

Add a field:

```go
PendingMembershipRequests []MembershipRequestSummary `json:"pendingMembershipRequests,omitempty"`
```

Each summary: `{uid, organization:{slug,name}, status, createdAt}`.
Populated only when the user has at least one row with status in
{`pending`,`rejected`} — the no-org screen needs both ("waiting for
approval" and "rejected, you can re-request after X days").

### 6. Notifications

Two new templates in `server/internal/notifications/`:
- `membership_request_new.{html,txt}` — to all org admins. Subject
  `New membership request from {name}`; links to the org's pending
  requests page.
- `membership_request_decision.{html,txt}` — to the requester.
  Subject `Your request to join {org} was {approved|rejected}`.

Dispatch via the existing notification path (same as invitations).
Failures are logged, not surfaced to the API caller.

## Files affected

| File / dir                                                                       | Change                                                                                                  |
|----------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------|
| `server/internal/handlers/auth/service.go`                                       | `validateAutoJoinRegex`; call from `UpdateOrgSettings`; defensive guard in `autoJoinMatchingOrgs`; new `MembershipRequest*` service methods. |
| `server/internal/handlers/auth/handler.go`                                       | Six new routes + handlers; admin gating helpers reused.                                                 |
| `server/internal/handlers/auth/types.go` (or wherever `MeResponse` lives)        | Add `PendingMembershipRequests` field.                                                                  |
| `server/internal/handlers/base/base.go`                                          | New error codes.                                                                                        |
| `server/internal/db/postgres/migrations/0NN_membership_requests.up.sql`          | New table + indexes.                                                                                    |
| `server/internal/db/postgres/migrations/0NN_membership_requests.down.sql`        | Drop table.                                                                                             |
| `server/internal/db/sqlite/migrations/0NN_membership_requests.up.sql` (+ down)   | Mirror.                                                                                                 |
| `server/internal/db/models/membership_request.go`                                | New entity.                                                                                             |
| `server/internal/db/repositories/membership_request_repo.go`                     | CRUD + status filters; "find by (org,user)"; "find rejected within cooldown".                           |
| `server/internal/notifications/membership_request.go` (+ templates)              | Dispatch helpers + HTML/text templates.                                                                 |
| `docs/api-specification.md`                                                      | Document the six endpoints.                                                                             |
| `CLAUDE.md`                                                                      | Add the routes to the key-routes list.                                                                  |

## Tests

- Table test for `validateAutoJoinRegex`: empty allowed; `.*`,
  `.+`, `.`, `[^@]+`, `.*@.*`, `.+@gmail.com`, `.+@.+`, missing `@`,
  unparseable rejected; `[a-z]+@acme\.com`, `.+@acme\.com`,
  `.+@(eu|us)\.acme\.com` allowed; probe-set check enforces the
  permissiveness rule.
- Integration: request → 201 pending; duplicate → 409
  `REQUEST_PENDING`; already-member → 409 `ALREADY_A_MEMBER`; cancel
  by owner → 204; cancel by non-owner → 403; rejected then re-request
  before cooldown → 409 `REQUEST_COOLDOWN_ACTIVE`; rejected then
  re-request *after* cooldown → 201; admin re-approves a rejected
  row → 200 (no cooldown for admins).
- Approve creates membership in same tx; with an injected
  `INSERT organization_members` failure, both rows roll back.
- Admin gating: non-admin GET/POST on org-scoped routes → 403.
- Notification dispatch: assert recipient list (all admins) and
  template payload via the fake mailer used in invitation tests.
- `MeResponse` includes pending and rejected requests; does not
  include approved ones.
- Defensive: with a stored pattern of `.*` left over in the DB,
  `autoJoinMatchingOrgs` skips that org and the registration
  confirmation succeeds (does not 500).

## Verification

1. `make dev`. Login as `admin@solidping.com` (org `default`).
2. `PATCH /api/v1/orgs/default/settings` with
   `{"registrationEmailPattern":".*"}` → 400 `INVALID_AUTO_JOIN_REGEX`.
3. Same with `{"registrationEmailPattern":".+@example\\.com"}` → 200.
4. Register and confirm a fresh `bob@unknown.com`. Login → response
   has `loginAction:"noOrg"`.
5. As Bob: `POST /api/v1/auth/membership-requests` with
   `{"orgSlug":"default","message":"please add me"}` → 201, status
   `pending`.
6. As admin:
   `GET /api/v1/orgs/default/membership-requests?status=pending` →
   see Bob's row.
7. Approve → Bob is a `user` member; Bob receives a decision email.
8. Bob logs in → `default` appears in `organizations`; no
   `loginAction:"noOrg"`.
9. Register `mallory@spam.example`, request, admin rejects with
   reason "spam". As Mallory, re-request immediately → 409
   `REQUEST_COOLDOWN_ACTIVE`.
10. As admin, click "approve" on Mallory's rejected row → 200, she's
    a member (admin override of cooldown).
