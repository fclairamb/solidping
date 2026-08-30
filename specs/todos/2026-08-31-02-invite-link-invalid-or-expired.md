---
model: opus
effort: high
---

# Opening an invitation link reports "invalid or expired" (at least locally)

## Problem

At least in local development, opening a freshly created invitation link lands on
the dash0 error card "This invitation link is invalid or has expired"
(`auth:invite.invalidDescription`), instead of the join/accept page.

The chain involved:

1. **Create**: `Service.CreateInvitation`
   (`server/internal/handlers/auth/service.go:3239`) generates a random hex
   token, stores it as an org-scoped state entry under key `invite:<token>`
   (`inviteKeyPrefix`, `service.go:2250`) with a TTL resolved from the
   `getAllowedInviteExpirations()` whitelist (`service.go:3257`), and builds
   `inviteURL = <BaseURL>/<app>/invite/<token>` (`service.go:3298`). The URL is
   both emailed and returned to the dashboard (`inviteUrl` is surfaced in the
   invitations page as the primary channel).
2. **Open**: dash0 route `/invite/$token`
   (`web/dash0/src/routes/invite.$token.tsx:16`) calls `useInviteInfo(token)`
   (`web/dash0/src/api/hooks.ts:3839`), which does an unauthenticated
   `GET /api/v1/auth/invite/<token>` (public route,
   `server/internal/app/server.go:665`).
3. **Lookup**: `Service.GetInviteInfo` (`service.go:3381`) lists all
   organizations and probes `GetStateEntry(orgUID, "invite:<token>")` in each;
   the entry must be non-deleted and non-expired
   (`server/internal/db/postgres/postgres.go:3842` /
   `server/internal/db/sqlite/sqlite.go:3785`). No match → `ErrInvitationNotFound`.
4. **Render**: *any* error from `useInviteInfo` — not just a 404 — renders the
   "invalid or expired" card (`invite.$token.tsx:93`).

A surface read of each step looks correct (TTL comes from a whitelist so it
cannot be zero; `ListOrganizations` has no pagination/limit; the state-entry
upsert persists `expires_at`), so the defect is not obvious from static
inspection — this spec is investigation-shaped.

### Candidate causes to check (in rough order of suspicion)

- **Frontend collapses every failure into "invalid or expired"**
  (`invite.$token.tsx:93`): a 429 from rate limiting on the public auth
  surface, a 500, a CORS rejection, or a plain network error all render the
  same card. Even if the token is fine, any transient error looks like an
  expired invite. This is a real product bug regardless of the root cause of
  the report.
- **Token mismatch between the delivered link and the stored key**: email
  template wrapping/escaping the URL, the dashboard copy button, or the
  `/dash0/invite/$token` SPA param picking up a trailing character. Compare the
  token in the opened URL byte-for-byte against the `state_entries.key` row.
- **Expiry-time skew**: Postgres compares `expires_at > NOW()`
  (`postgres.go:3842`) against a Go-side `time.Now().Add(ttl)`
  (`postgres.go:3878`); SQLite compares against `datetime('now')` (UTC,
  `space`-separated format) with `expires_at` stored however the driver
  serializes `time.Time` (`sqlite.go:3785`) — string comparison across
  different formats/zones can misorder. Determine which dialect the local
  repro used and verify the comparison actually holds for a fresh entry.
- **State-entry lifecycle**: confirm no cleanup job, soft-delete, or upsert
  path clears the row between creation and opening.

### Open questions (report is thin — verify with a real repro)

- Which DB dialect was the failing local run on (Postgres via
  docker-compose, or SQLite)?
- Was the link opened from the invitation email or copied from the
  invitations page (`orgs/$org/organization/invitations`)?
- Was the invite fresh (minutes old) or near/past its chosen expiration?
- Does `GET /api/v1/auth/invite/<token>` itself return 404, or does it return
  200 while the page still shows the error card?

## Proposal

1. **Reproduce locally**: create an invitation via the API or the
   invitations page, capture the returned `inviteUrl` and the
   `state_entries` row, open the link, and record the exact HTTP response of
   `GET /api/v1/auth/invite/<token>`. Bisect the chain (raw curl vs browser,
   token equality, DB row expiry) until the failing step is identified.
2. **Fix the root cause** found in step 1, whichever layer it lives in.
3. **Stop conflating errors on the invite page**: only a 404/`NOT_FOUND` (and
   an explicit expired signal, if one is added) should render the
   "invalid or expired" card; other failures (429/5xx/network) should show a
   distinct retryable error state. Keep the copy in all four locales
   (`web/dash0/src/locales/*/auth.json`) in sync.
4. **Close the E2E gap**: `web/dash0/e2e/invitations.spec.ts` asserts the
   `inviteUrl` shape but never opens the link. Add an end-to-end path:
   create invite → open `/dash0/invite/<token>` logged-out → see the join
   card (org name, role, masked email) → accept as a new user → land in the
   org. Also cover the negative: a bogus token shows the invalid card.
5. If the investigation concludes there is **no product defect** (e.g. a
   stale local DB or an actually-expired invite), document the finding in
   `wiki/` (what looked broken and why it wasn't) and still land items 3 and 4
   — the error conflation and the missing E2E coverage stand on their own.

### Acceptance criteria

- A freshly created invitation link opens to the join/accept page locally on
  both dialects (or the failing dialect's bug is fixed with a regression
  test at the DB layer).
- The invalid/expired card appears only for a genuinely unknown or expired
  token; transient errors render a different, retryable state.
- E2E exercises the full create → open → accept flow.
