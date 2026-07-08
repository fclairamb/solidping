# E2E specs hardcode `API_BASE = "http://localhost:4000"`, ignoring `E2E_BASE_URL`

## Problem

`web/dash0/e2e/discovery.spec.ts` seeds/cleans up test data via direct
`page.request` calls to a module-level `API_BASE` constant, and correctly
derives it from the environment:

```ts
// discovery.spec.ts:3-7
// Honor E2E_BASE_URL (side-car test server) like playwright.config.ts does;
// fall back to the CI default.
const API_BASE = process.env.E2E_BASE_URL
  ? new URL(process.env.E2E_BASE_URL).origin
  : "http://localhost:4000";
```

Thirteen other spec files instead hardcode the fallback value with no
`E2E_BASE_URL` check:

```
web/dash0/e2e/listing-pages-style.spec.ts:3
web/dash0/e2e/notification-detail.spec.ts:3
web/dash0/e2e/escalation-policies.spec.ts:3
web/dash0/e2e/check-groups.spec.ts:3
web/dash0/e2e/invitations.spec.ts:3
web/dash0/e2e/badges.spec.ts:4
web/dash0/e2e/membership-requests.spec.ts:3
web/dash0/e2e/oncall-edit.spec.ts:3
web/dash0/e2e/slack-socket-mode.spec.ts:3
web/dash0/e2e/channels-webhook.spec.ts:3
web/dash0/e2e/integrations.spec.ts:3
web/dash0/e2e/incident-notifications.spec.ts:3
web/dash0/e2e/entitlements-usage.spec.ts:3
```

(Confirmed exhaustive via `grep -rn 'API_BASE = "http://localhost:4000"' web/dash0/e2e/`.)

This repo's own `CLAUDE.md` documents a supported local workflow — a
side-car E2E server on an alternate port with `E2E_BASE_URL` set, used to
avoid disturbing a live `:4000` devloop (see `project_local_e2e_workflow`
convention) — and `playwright.config.ts`'s `use.baseURL` already honors
`E2E_BASE_URL` for page navigation. When a spec's `API_BASE` doesn't follow
suit, page assertions correctly hit the side-car server while the test's own
setup/cleanup API calls (create/delete on-call schedules, escalation
policies, etc.) silently hit whatever is listening on `:4000` instead.

**Reproduced**: running `oncall-edit.spec.ts` and `listing-pages-style.spec.ts`
with `E2E_BASE_URL=http://localhost:4001/dash0/` while something else was
listening on `:4000` made setup/cleanup calls (create/delete on-call
schedules, escalation policies) hit the wrong server (`:4000`), while page
assertions read from the correct one (`:4001`) — causing deterministic
failures (asserted row counts of 0 instead of 2). This reproduces every time
under that setup; it is not a flake.

This was discovered as a side effect of spec `2026-07-06-02` (dashboard E2E
flake fix) but is out of that spec's scope — it's a pre-existing test-authoring
bug unrelated to the login/fixture changes made there.

## Proposal

In each of the 13 affected files, replace:

```ts
const API_BASE = "http://localhost:4000";
```

with `discovery.spec.ts`'s pattern:

```ts
const API_BASE = process.env.E2E_BASE_URL
  ? new URL(process.env.E2E_BASE_URL).origin
  : "http://localhost:4000";
```

No other logic in these files needs to change — `API_BASE` is already used
consistently as a template-string prefix for `page.request` calls.

Consider factoring the derivation into a shared helper (e.g.
`web/dash0/e2e/fixtures.ts`, which already exists and was touched by the
sibling flake-fix spec) so future spec files can't reintroduce the hardcoded
form — but a mechanical per-file fix is sufficient to close the bug even if
the helper extraction is skipped.

## Out of scope

- Any change to `discovery.spec.ts` itself (already correct).
- The login/fixture contention work from spec `2026-07-06-02` — unrelated.
- Adding new E2E coverage — this is a fix to existing test plumbing only.

## Acceptance criteria

- All `web/dash0/e2e/*.spec.ts` files derive `API_BASE` from `E2E_BASE_URL`
  when set, falling back to `http://localhost:4000` otherwise (verified by
  `grep -rn 'API_BASE = "http://localhost:4000"' web/dash0/e2e/` returning no
  matches, or only matches inside the fallback expression).
- With a side-car E2E server on a non-4000 port and `E2E_BASE_URL` pointed at
  it (something else listening on `:4000`), the previously-reproduced
  failures in `oncall-edit.spec.ts` and `listing-pages-style.spec.ts` pass.
- `make test-dash` remains green under the default (`:4000`) setup.

## Implementation plan

- [ ] Update `API_BASE` in the 13 files listed above to mirror
      `discovery.spec.ts`'s `E2E_BASE_URL`-aware derivation (optionally via a
      shared helper in `web/dash0/e2e/fixtures.ts`).
- [ ] Verify: run `oncall-edit.spec.ts` and `listing-pages-style.spec.ts` with
      `E2E_BASE_URL` pointed at a side-car server on a different port while
      another process listens on `:4000`; confirm they pass instead of
      hitting the wrong host.
- [ ] Run `make test-dash` under the default setup to confirm no regression.
