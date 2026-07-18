---
model: sonnet
effort: medium
---

# The SSH-tunnel declare-and-use flow in the dashboard has UX gaps

## Problem

The request was "update the UI to allow to declare and use an SSH tunnel
within a check". **The core of this already shipped on this branch** (spec
`2026-07-16-04-ssh-tunnel-via-check-dependency`, done):

- Declaring a bastion = creating an SSH check; the form exposes host/port,
  username, password, private key and the host-key fingerprint required for
  bastion use (`web/dash0/src/components/checks/form/types/network.tsx:116`).
- Using it = the "Run through SSH tunnel" selector rendered in the shared
  check form for every type whose server-declared capability metadata says
  `supportsTunnel` (http + tcp today) — it writes the well-known
  `tunnelCheckUid` config key
  (`web/dash0/src/components/checks/form/tunnel-select.tsx`,
  `web/dash0/src/components/shared/check-form.tsx:281`).
- The check detail page shows the tunnel edge in both directions
  (`web/dash0/src/components/checks/tunnel-detail.tsx`), and E2E coverage
  exists (commit `cd403bec`).

What remains are UX gaps that make the declare→use loop clunky:

1. **The empty state dumps you out of the form.** When the org has no SSH
   checks yet, `TunnelSelect` shows "create one for your bastion first" and
   links to the plain checks *list*
   (`tunnel-select.tsx:58`) — the user loses their half-filled check form and
   then has to figure out which type to pick. The new-check route already
   accepts a `checkType` search param
   (`web/dash0/src/routes/orgs/$org/checks.new.tsx`), so the link can go
   straight to `/orgs/$org/checks/new?checkType=ssh`.
2. **Unverified-bastion copy leaks an internal key name.** A disabled
   candidate in the selector reads "— set expected_fingerprint first"
   (`tunnel-select.tsx:86`) — raw config key instead of the field's human
   label ("Host key fingerprint").
3. **The checks list gives no hint a check is tunneled.** Neither the list
   row nor any badge shows "via <bastion>"; the edge is only discoverable by
   opening the check. A tunneled check whose bastion is down looks like an
   unexplained outage from the list.

## Proposal

Close the gaps; do not redesign the shipped model (tunnel = reference to a
separate SSH check via `tunnelCheckUid` — that design is deliberate, see
`server/internal/checkers/checkerdef/tunnel.go:56`).

1. Point the `TunnelSelect` empty-state link at
   `/orgs/$org/checks/new` with `checkType: "ssh"` in `search`, and word it
   as "Create an SSH check for your bastion". (If preserving the in-progress
   form is cheap — e.g. the link opens in the same tab and loses state —
   note it in the help text; do not build a draft-persistence mechanism for
   this.)
2. Reword the disabled-option suffix to use the human field label, e.g.
   "— needs a host key fingerprint".
3. In the checks list, show a small muted indicator on tunneled checks
   (e.g. a `Cable`/`Waypoints` lucide icon with a tooltip "via <bastion
   name>", reusing `tunnelCheckUidOf` from
   `web/dash0/src/components/checks/tunnel.ts`). Keep it display-only — no
   new API calls; the list already has all checks in memory to resolve the
   bastion's name. Follow the design reference
   (`web/dash0/src/routes/orgs/$org/design-reference.tsx`) for the badge
   /tooltip primitives.
4. Extend the existing tunnel E2E (`web/dash0/e2e/`) to cover the empty-state
   deep link (lands on the new-check form with type ssh preselected) and the
   list indicator.

## Open questions

- If the original intent was an *inline* tunnel declaration (SSH host/key
  embedded directly in the http/tcp check's own config, no separate SSH
  check), that is a departure from the shipped one-bastion-many-checks
  model and needs its own spec — explicitly out of scope here.

## Implementation Plan

All changes are in `web/dash0`, no server/Go changes.

1. **Empty-state link** (`tunnel-select.tsx`): point the `Link` at
   `/orgs/$org/checks/new` with `search.checkType = "ssh"` (mirroring the
   exhaustive search-object shape TanStack Router's typed `Link` requires,
   as already done for the "New check" button in `checks.index.tsx`), word
   it "Create an SSH check for your bastion", add
   `data-testid="tunnel-empty-create-link"`, and add a short note that
   following the link leaves the current form (no draft persistence).
2. **Disabled-option copy** (`tunnel-select.tsx`): replace
   `" — set expected_fingerprint first"` with
   `" — needs a host key fingerprint"` (matches the form's field label in
   `network.tsx`: "Host key fingerprint (optional)").
3. **List tunnel indicator** (`checks.index.tsx`): build a `uid -> Check`
   map from the already-loaded infinite-query pages (all types, alongside
   the existing group/ungrouped bucketing), thread it down through
   `ChecksTable` / `CheckGroupSection` / `UngroupedChecksSection` into
   `CheckRow`. In `CheckRow`, resolve `tunnelCheckUidOf(check)` and render a
   small muted `Waypoints` icon (consistent with `tunnel-detail.tsx`) next
   to the check name, wrapped in the existing `Tooltip` primitive showing
   "via `<bastion name>`" (falls back to a generic "via SSH tunnel" if the
   bastion isn't in the loaded page). Display-only, no new queries.
4. **E2E** (`e2e/check-ssh-tunnel.spec.ts`): extend the existing tunnel
   describe block with two tests:
   - Empty-state deep link: stub the `type=ssh` checks query to force zero
     candidates (robust regardless of bastions other tests/DB state may
     have created), assert the empty-state link's wording/testid, click it,
     and assert the new-check form lands with `checkType=ssh` preselected
     (`check-type-select` shows "SSH").
   - List indicator: create a verified bastion + a tunneled check via the
     API (reusing `createBastion`), navigate to the checks list, search for
     the check, and assert the tunnel indicator is visible and its tooltip
     reads "via `<bastion name>`" on hover.
5. `make fmt`, then scoped QA: `make build-dash0`, `cd web/dash0 && bun run
   lint`, and the extended Playwright file (best-effort locally per the
   local E2E workflow notes).
