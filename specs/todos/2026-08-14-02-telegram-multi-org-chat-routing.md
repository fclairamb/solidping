---
model: opus
effort: high
---

# Telegram: one chat linked to several orgs routes commands to an arbitrary org

## Problem

One Telegram chat can legitimately be linked in several organizations — each
`/start <token>` creates its own `user_contacts` row keyed (user, org, type,
chat id), and outbound paging correctly fans out per org. But the entire
interactive surface is pinned to **one** org, chosen arbitrarily:

1. **`contacts[0]` of an unordered query.** `requireLinked`
   (`server/internal/handlers/telegramcb/commands.go:50`) and the ack callback
   (`server/internal/handlers/telegramcb/callback.go:46`) both take the first
   row returned by `ListUserContactsByTypeValue`, which has **no `ORDER BY`**
   (`server/internal/db/postgres/user_contact.go:188`, same in
   `server/internal/db/sqlite/user_contact.go`). Which org answers `/status`
   is whatever the storage engine feels like, and can flip between commands.
2. **Inline Acknowledge buttons from the "other" org dead-end.** The callback
   resolves the org from `contacts[0]` then calls
   `GetIncident(orgUID, incidentUID)` (`callback.go:55-64`). A button attached
   to org B's alert misses when org A sorts first and answers *"That incident
   is no longer available."* — for an incident that was legitimately paged
   into this very chat. Fails safe (UID prevents cross-org acking) but looks
   broken.
3. **Typed refs can silently hit the wrong org.** Incident numbers are per-org
   sequential, so `#12` exists in every org. `/ack 12` resolves the number
   against `contacts[0]`'s org (`commands.go:203-222`); typed in response to
   org B's alert while org A is "first", it acknowledges org A's unrelated
   `#12`. The ack confirmation naming the check is the only guard.

## Proposal

Principle: **in a multi-org chat, every incident reference is org-qualified**
— rendered `#org:123` and typed `#org:123` — so each message and each command
is self-routing. Single-org chats (the overwhelming common case) keep the
short `#123` everywhere and notice nothing.

### 1. Deterministic contact ordering

Add `ORDER BY created_at, uid` to `ListUserContactsByTypeValue` in **both**
implementations (`server/internal/db/postgres/user_contact.go:188` and the
SQLite mirror). After the fixes below almost nothing depends on "first" any
more, but determinism keeps any residual single-pick stable (oldest link wins)
and makes tests reproducible. No migration needed.

### 2. Qualified incident refs — render and parse

- **Render.** Add `QualifiedIncidentRef(orgSlug string, number int64) string`
  → `"#" + orgSlug + ":" + number` next to `IncidentRef`
  (`server/internal/integrations/telegram/incidentview.go:13`). `AlertParams`
  (`server/internal/integrations/telegram/message.go:35`) already carries
  `OrgSlug`; add a `QualifyRef bool` field and render the qualified form at
  `message.go:100-102` when set. Same flag on the listing/detail view structs
  used by `/incidents` and `/incident` (`IncidentLine`,
  `IncidentDetailView`).
- **When to qualify.** A message qualifies its refs iff the destination chat
  is linked (verified) in **≥ 2 orgs** at render time. The command handlers
  already hold `linkedContacts`; the escalation job
  (`server/internal/jobs/jobtypes/job_escalation_step_telegram.go`, params
  built at `:151-160`) adds one `ListUserContactsByTypeValue` count on the
  chat id at send time.
- **Parse.** Extend `ParseIncidentRef`
  (`server/internal/integrations/telegram/webhook.go:214`) to return
  `(orgSlug string, number int64, ok bool)`: accepted forms `#123`, `123`,
  `#org:123`, `org:123` (slug per the org slug rules: 3-20 chars,
  alphanumeric + hyphens, matched case-insensitively; empty slug = bare ref).
  `/ack` and `/incident` pass the slug through to resolution.

### 3. Ref and command resolution in multi-org chats

New shared resolver in `telegramcb`, replacing the single-org
`requireLinked` pick for every command that takes a ref:

- **Qualified ref** (`#org:123`): find the linked contact whose org's current
  slug matches. No match → reply "this chat isn't connected to `org` — use
  the connect link from that org's dashboard" (reveals nothing about whether
  the org exists; the chat's own links are already known to its user). Match
  → resolve `GetIncidentByNumber` in that org only.
- **Bare ref** (`#123`) in a single-org chat: today's behavior, unchanged.
- **Bare ref in a multi-org chat**: run `GetIncidentByNumber` across all
  linked orgs. Exactly one hit → act on it. Multiple hits → **never guess**:
  reply a disambiguation list of the qualified candidates
  (`#org-a:123 — <check> (down 23m)` / `#org-b:123 — …`) and do nothing.
  Zero hits → the existing not-found answer.
- **`/status` with no arg, multi-org chat**: fan out — one status line per
  linked org (read-only, so fanning out is safe). Single-org: unchanged.
- **`/incidents`, multi-org chat**: per-org header + rows, refs qualified,
  the existing `maxListedIncidents` cap applied to the **total**.
- **`/ack` with no arg, multi-org chat**: aggregate open incidents across all
  linked orgs; zero → "nothing to ack"; exactly one (whichever org) → ack it;
  several → the existing needs-a-ref listing with qualified refs.
- **`/status <org>` / `/incidents <org>`**: accept an optional org-slug
  argument to scope to one linked org, same not-connected answer as above for
  a slug that isn't linked.

`/help`, `/start`, `/stop` are unchanged (`/stop` deliberately unlinks all
orgs, `handler.go:396`).

### 4. Ack callback: route by incident UID across all linked orgs

In `handleCallbackQuery` / `ackFromCallback` (`callback.go:36-64`), stop
assuming `contacts[0]`: iterate the chat's linked contacts and try
`GetIncident(contact.OrganizationUID, incidentUID)` until one hits — incident
UIDs are globally unique so at most one org matches. Ack with **that**
contact's `(OrganizationUID, UserUID)` so the audit trail names the right user
row. No hit across any linked org → the existing "no longer available" answer
(unchanged, still reveals nothing to a chat the incident wasn't paged to).

### 5. Tests

Table-driven, `testify/require`, `t.Parallel()`, extending
`commands_test.go` / `callback_test.go` / `handler_test.go` fakes:

- Ordering: two contacts inserted out of order → `ListUserContactsByTypeValue`
  returns oldest first (both PG via testcontainers and SQLite).
- Parse: `#123`, `123`, `#org:123`, `ORG:123` (case-insensitive), rejects
  `#:123`, `#org:`, `#org:abc`.
- Render: multi-org chat alert and `/incidents` rows show `#org:123`;
  single-org chat shows `#123` (positive control both ways).
- Bare ref, multi-org, number exists in both orgs → disambiguation reply,
  **no ack performed** (prove the negative).
- Bare ref, multi-org, number exists in exactly one org → acted on.
- Qualified ref to a linked org → resolved there even when the other org has
  the same number and sorts first.
- Qualified ref to a non-linked slug → not-connected answer, no incident
  lookup observable.
- Callback: button for org B's incident while org A sorts first → ack
  succeeds in org B, audit carries org B's user UID.
- Callback: incident UID in no linked org → "no longer available".
- `/status` and `/ack`-no-arg fan-out behaviors per §3.

### Out of scope (explicit follow-ups)

- WhatsApp: no interactive command surface today, nothing to route.
- Resolving qualified refs through `organization_previous_slugs` aliases
  (renamed orgs) — nice-to-have; current-slug matching only in v1.
- A `/orgs` command listing the chat's linked orgs — trivial add once the
  resolver exists, but not needed for correctness.
- Coordination note: `2026-08-14-01-telegram-incident-resolution-notice.md`
  also touches `job_escalation_step_telegram.go`; whichever lands second
  rebases the `AlertParams` change (§2) trivially.
