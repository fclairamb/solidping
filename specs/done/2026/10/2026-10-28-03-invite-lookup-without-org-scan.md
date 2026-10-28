---
model: opus
effort: medium
---

# Each anonymous invite-token lookup scans every organization

## Problem

Found by the security scan of 2026-10-28 (finding X3).

**Severity: medium on SaaS, low self-hosted.** Anyone can trigger it, with no account. The cost grows with the number of organizations, and on SaaS any user can add organizations.

- `GET /api/v1/auth/invite/:token` is public (`server/internal/app/server.go:808`). `POST /auth/accept-invite` also runs without authentication when no credential is presented (`:812`).
- Both go through `findInvite` (`server/internal/handlers/auth/service.go:3893-3932`, called at `:3949` and `:3977`). For each request it:
  - loads every organization, all columns, with no limit (`ListOrganizations`, `server/internal/db/postgres/postgres.go:525-535`);
  - then issues two `GetStateEntry` queries per organization: the hashed key, then the legacy plaintext key.
- An unknown token always walks the whole list. One request costs about 2 × N + 1 queries, plus materializing N organization rows. The only bound is the global per-IP limiter (1800 per minute by default, `server/internal/config/config.go:1899`).
- With 1000 organizations, a single IP can drive about 3.6 million database queries per minute. Several IPs multiply that. Every tenant shares that database, so the whole instance degrades.
- The legacy branch (`service.go:3914-3928`) is marked `TODO(remove after 2026-10-10)`. Invites live at most a week, so every pre-hash invite has expired by now.

## Proposal

1. Resolve the invite with one lookup. Store a global index entry at invite creation (`CreateInvitation`, `service.go:3743`): `invite:<hash>` with a nil organization, its value holding the org UID. `findInvite` then reads that entry, then the org-scoped entry it points to.
   - An org-scoped entry with no index entry, i.e. an invite created before this change, can be found through the old loop for one more week. Drop that fallback in a follow-up, or accept that such invites must be re-sent. See Open questions.
2. Remove the legacy plaintext-key branch, whose removal date has passed.
3. Revoking an invitation deletes its index entry too.

## Tests

- `server/internal/handlers/auth` (both engines):
  - **Regression (fails before the fix):** with 50 organizations, `GetInviteInfo` on an unknown token runs a constant number of queries, independent of the organization count. Count them with a bun query hook. Before the fix it runs about 2 × 50.
  - **Positive controls:**
    - a valid invite created through `CreateInvitation` is found by `GetInviteInfo` and accepted by `AcceptInvite`;
    - a revoked invite is not found.
  - A token equal to a stored hash is still refused. This is the property the legacy-branch comment protected; keep the test after the branch is gone.

## Open questions

- Invites created before the upgrade (no index entry): keep the old loop as a fallback for 7 days, or ask admins to re-send? **Recommended: no fallback.** Invites are cheap to re-send, and the fallback is the DoS. Note it in the changelog.

## Resolved open questions

- No fallback for invites created before the upgrade; admins re-send them. Note it in the changelog. (autopilot: recommended answer)
