---
model: opus
effort: medium
---

# Keep a version history of every check, with diff and restore

## Problem

A check's definition has no history. Once someone edits a check (dashboard, API,
`sp apply`, MCP `update_check`), the previous config is gone: no "who changed
what", no diff, no rollback. `models.EventTypeCheckUpdated` is declared
(`server/internal/db/models/event.go:17`) and consumed
(`handlers/incidents/service.go:1853`, `handlers/system/service.go:196`) but never
emitted.

Spec 2026-10-03-07 (AI-authored js checks) also needs script history and
pending repair proposals. A history of whole checks covers both, for every
check type.

## Proposal

### 1. Table `check_versions`

One scratch migration, `027_check_versions.up.sql` / `.down.sql`, in
`server/internal/db/postgres/migrations/` and `server/internal/db/sqlite/migrations/`
(rules: `wiki/conventions/migrations.md`). Don't edit `026_v0_38_0`: v0.38.0 is
in release PR #470. Additive only.

| Column | Meaning |
|---|---|
| `uid`, `created_at`, `updated_at` | standard |
| `organization_uid` | FK, `on delete cascade` |
| `check_uid` | FK, `on delete cascade` |
| `version` | int, unique with `check_uid`, starts at 1 |
| `snapshot` | JSON, the check definition (see 2) |
| `snapshot_hash` | sha256 of the canonical snapshot JSON |
| `status` | `applied` \| `proposed` \| `rejected` |
| `base_version` | version a proposal was built on, null otherwise |
| `origin` | `user` \| `api` \| `apply` \| `mcp` \| `system` \| `ai_generate` \| `ai_repair` |
| `actor_user_uid` | nullable, null for `system` |
| `reason` | one-line note, nullable |
| `decided_by_user_uid`, `decided_at` | approve/reject of a proposal, nullable |

Index on `(check_uid, version desc)`. A partial index on `status = 'proposed'`
for Postgres. SQLite gets a plain `(check_uid, status)` index.

### 2. Snapshot content: definition only, never secrets

The snapshot holds the definition columns of `models.Check`
(`server/internal/db/models/check.go:220-270`): `name`, `slug`, `description`,
`type`, `config`, `check_group_uid`, `regions`, `placement`, `region_count`,
`region_pool`, `fail_quorum`, `enabled`, `period`, plus the labels.

It never holds `config_private`, `config_private_keys` or `config_sealed`.
Secret config keys already live only there (`credentials.SplitConfig`), so the
plaintext `config` is safe to copy. Runtime state (`status`, counters, next run)
is not part of the snapshot.

Secrets are not versioned. A restore keeps the check's current secrets.

### 3. Recording

Record in the DB layer, inside the same transaction as the write, so all ~14
call sites of `UpdateCheck` are covered without touching each one:
`internal/db/postgres/postgres.go:2074`, `internal/db/sqlite/sqlite.go:1978`, and
the create/insert path of both engines.

- Compute the snapshot after the write, hash it, and insert a version only if
  the hash differs from the latest `applied` one. Writes that only touch secrets
  (`internal/credmigrate`) or runtime state (`internal/checkworker/worker.go`,
  `internal/handlers/degradedeval`) then add nothing.
- Actor and origin come from the context: a small helper next to
  `internal/db/dbctx/context.go` (`WithChangeSource(ctx, origin, userUID)`).
  The handler entry points set it (`handlers/checks/service.go:1528` create,
  `:2023` update, `apply.go:362`, `service.go:4185` import,
  `mcp/tools_checks.go:363`). Missing context means `system`.
- Emit `check.created` / `check.updated` events with `{version}` in the payload.

Retention: keep the last 100 `applied` versions per check. Prune on insert.

### 4. API

- `GET /api/v1/orgs/$org/checks/$uid/versions` → `{ "data": [...] }`, newest
  first, `limit` supported. Each item: version, status, origin, actor, reason,
  createdAt. No snapshot in the list.
- `GET .../versions/$version` → the snapshot, redacted like `GET` on the check
  (`ExportRedactedFields`, `handlers/checks/service.go:4102`).
- `GET .../versions/$version/diff?against=$other` (default: previous applied).
  Reuse the field diff from `handlers/checks/diff.go` where it fits.
- `POST .../versions/$version/restore` → applies the snapshot through
  `UpdateCheck` (so it records a new version, origin `user`, reason
  `restored v$version`). Secrets untouched.
- `POST .../versions/$version/approve` and `/reject` for `proposed` versions.
  Approving a proposal whose `base_version` is not the latest applied answers
  `409 CONFLICT`.
- OpenAPI: `server/internal/app/openapi/openapi.yaml`.

### 5. Dashboard

Check detail gets a "History" tab: list of versions, diff view against the
previous one, a Restore button. Proposed versions are shown on top with
Approve / Reject. Uses primitives from the design reference page.

### 6. Docs

`web/docs/`: check history page. CHANGELOG entry.

## Tests

- DB layer, both engines (`make test` SQLite, `make test-postgres`): create →
  version 1; update of `name` → version 2; update touching only secrets → no new
  version; update touching only runtime status → no new version.
- Snapshot never contains `config_private`, `config_private_keys`,
  `config_sealed` or any `SecretFields()` key (table test over check types with
  secrets: `js`, `postgres`, `ssh`).
- Actor/origin: update through the API records `user` and the user uid; a write
  without context records `system`.
- Restore: restores definition fields, keeps current secrets, records a new
  version.
- Proposal: approve applies it; approve with a stale `base_version` → 409; reject
  leaves the check unchanged.
- Retention: 101st applied version prunes the oldest.
- Delete check cascades its versions.
- API: list wraps `{ "data": [...] }`; version GET on another org's check → 404.
- Playwright: edit a check, open History, see the diff, restore.

## To verify

- How labels are stored and written (separate table?) and whether the label
  write path goes through `UpdateCheck` or needs its own hook.
- Whether `ApplyChecks` / `ImportChecks` write in one transaction per check or
  per document (a version per check either way).

## Resolved open questions

- Retention: keep the last 100 versions per check (as in the Retention section), no
  time-based limit.
