---
model: sonnet
effort: medium
---

# Check groups have an escalation policy in the API but no way to set it from the UI

## Problem

The escalation-resolution chain is check → group → org default → none, and the
backend fully supports the group level: `check_groups.escalation_policy_uid`
exists on the model (`server/internal/db/models/check_group.go:18`), the REST
API exposes `escalationPolicyUid` on the check-group response and
create/PATCH requests with `""`-clears semantics
(`server/internal/handlers/checkgroups/service.go:95`, round-trip tested in
`server/internal/handlers/checkgroups/escalation_test.go`), and the incident
resolver honors it.

But no UI surface can set it. Group management in dash0 is entirely inline on
the checks list (`web/dash0/src/routes/orgs/$org/checks.index.tsx:649-695`):
a create dialog that only takes a name, a rename dialog, reorder, and delete.
The middle link of the escalation chain is therefore API-only — the check
form's inherit label ("Inherit — currently: …",
`web/dash0/src/components/checks/form/sections/escalation.tsx:75-80`) can
*display* a group policy that no one can actually assign from the dashboard.

This was an explicit non-goal of the spec that shipped the backend support
("No group UI work — there is no check-group management UI today";
`specs/done/2026/07/2026-07-19-01-escalation-org-default-and-assignment-picker.md:97`).
This spec is that follow-up.

## Proposal

### 1. Dedicated group edit route

Per the dash0 convention "editing always changes the route" (multi-field
forms never live in a dialog — `web/dash0/CLAUDE.md`), add
`web/dash0/src/routes/orgs/$org/check-groups.$uid.edit.tsx` with a form for:

- **Name** (existing rename dialog's job — the dialog can stay for the
  quick single-field rename, per the convention's inline-rename carve-out,
  or be retired in favor of the route; implementer's choice, but the route
  must exist).
- **Description** (already on the model/API, never exposed in UI).
- **Escalation policy** — the picker below.

Reach it from the group header's existing actions on the checks list (add a
`Pencil` edit action next to the current rename/reorder/delete actions,
following the row-actions convention). Submit PATCHes
`/api/v1/orgs/$org/check-groups/$uid` via a new `useUpdateCheckGroup`-based
form flow (the hook already exists, `web/dash0/src/api/hooks.ts:531`).

### 2. Escalation policy picker, generalized

Reuse the check form's `EscalationSelect`
(`web/dash0/src/components/checks/form/sections/escalation.tsx`) rather than
building a second picker. It needs a small generalization: for a group, the
inherit chain is shorter (org default → none, no group link), so the
component takes a variant/prop that

- renders the inherit option as **"Inherit — currently: `<org default name>`"**
  (or "nothing" when unset), skipping the group resolution step;
- keeps the org's policies (silent ones badged "0 steps — silent") and the
  "No escalation (silent)" zero-step shortcut, which work unchanged.

Form state semantics match the check form: `""` = inherit, UID = assign;
PATCH sends `""` to clear (already supported server-side).

### 3. Legibility on the checks list

On the grouped checks list, show a small indicator on group headers that
carry a policy (e.g. a muted badge or icon with the policy name in a
tooltip), so an operator can tell at a glance which groups override
escalation. Keep it subtle — most groups will have none.

### Open questions

- Whether to also fold group **creation** into a `/check-groups/new` route
  or keep the current name-only quick-create dialog. Suggested: keep the
  quick-create dialog (it's a single field) and let the edit route handle
  everything else.

### Non-goals

- No backend changes expected — the API surface is complete and tested.
- No changes to the check form's own picker or the resolver.
- No group list page — groups remain managed from the checks list.

### Testing

- Playwright E2E (`web/dash0/e2e/`): edit route assigns a policy to a group
  → check form's inherit label immediately reflects it ("Inherit —
  currently: <that policy>"); clearing back to inherit restores the org
  default in the label; description round-trips; group header indicator
  appears/disappears with the assignment.
- Component-level: picker variant renders the shorter inherit chain
  correctly for admins and non-admins (org settings query is admin-only —
  the existing "unknown default" fallback must keep working).

## Implementation Plan

1. **`web/dash0/src/api/hooks.ts`** — add `useCheckGroup(org, uid)`, a
   singular-fetch query hook mirroring `useEscalationPolicy` (`GET
   /api/v1/orgs/:org/check-groups/:uid` — the endpoint already exists,
   `server.go:730`, just unused by the frontend so far). Query key
   `["checkGroups", org, uid]`; already covered by the existing
   `invalidateQueries({ queryKey: ["checkGroups", org] })` calls in
   `useUpdateCheckGroup`/`useDeleteCheckGroup` (React Query's default
   `exact: false` matches the prefix), so no other hook needs touching.

2. **`web/dash0/src/components/checks/form/sections/escalation.tsx`** —
   generalize `EscalationSelect` with a `variant?: "check" | "group"` prop
   (default `"check"`, so every existing call site is unaffected). When
   `variant === "group"`, `inheritedUid` resolves directly to
   `orgSettings?.defaultEscalationPolicyUid` (skip the group-resolution
   step); `checkGroupUid`/`checkGroups` stay optional and are simply unused
   in that variant. Everything else (policy list, silent badge, "No
   escalation" shortcut, non-admin fallback to "nothing") is untouched.

3. **New route `web/dash0/src/routes/orgs/$org/check-groups.$uid.edit.tsx`**
   — dedicated edit page (Name, Description, `EscalationSelect
   variant="group"`), modeled on `checks.$checkUid.edit.tsx`'s
   skeleton/error/`refetchOnMount: "always"` pattern. Submits via
   `useUpdateCheckGroup(org, uid).mutateAsync({ name, description,
   escalationPolicyUid })` (the empty string clears, already supported
   server-side) and navigates back to `/orgs/$org/checks`. The existing
   name-only rename dialog and quick-create dialog on the checks list stay
   untouched (implementer's choice per the spec's open question) — this
   route is the one true place for description + escalation policy.

4. **`web/dash0/src/routes/orgs/$org/checks.index.tsx`**:
   - `CheckGroupSection` header: add a ghost `Pencil` icon-button `Link` to
     the new edit route, placed next to the existing `MoreVertical`
     dropdown (which keeps rename/move/delete — those don't fit the
     two-icon convention, only edit/delete do, and delete already has its
     own confirmation flow via the dropdown).
   - Fetch `useEscalationPolicies(org)` at the page level and pass a
     `uid -> policy` map down so `CheckGroupSection` can render a subtle
     indicator (muted `ArrowUpRight` icon — the same glyph the sidebar uses
     for Escalation Policies — inside a `Tooltip`, mirroring the existing
     SSH-tunnel indicator pattern at line ~178-192 of this file) next to the
     check-count badge when `group.escalationPolicyUid` is set. Tooltip
     text: policy name (+ " — silent" when zero-step).

5. **i18n** — add new keys to `checks.json` (en/fr/de/es): `menu.editGroup`
   (Pencil action label/aria-label), a `groupForm.*` block (title,
   description labels, save button, toasts) for the new route, and a
   tooltip-prefix key for the escalation indicator.

6. **`design-reference.tsx`** — no new primitive: Pencil-icon-`Link` next to
   a `DropdownMenu` and the muted-icon-in-`Tooltip` badge are both already
   documented/exemplified (row-actions section, SSH-tunnel indicator). No
   edit needed unless review surfaces a gap.

7. **Tests**:
   - Component (vitest, jsdom): new `escalation.test.tsx` mocking
     `@/api/hooks` to exercise the `variant="group"` inherit-label
     resolution for an admin (org default resolved) and a non-admin
     (`useOrgSettings` errors/undefined → "nothing" fallback), plus a
     regression check that `variant="check"` behavior is unchanged.
   - E2E (Playwright): extend `web/dash0/e2e/check-groups.spec.ts` (or a new
     `check-group-escalation.spec.ts`, mirroring
     `escalation-assignment.spec.ts`'s API helpers) covering: navigate to
     the group edit route, assign a policy, verify the check form's
     inherit label updates to "Inherit — currently: `<policy>`"; clear back
     to inherit and verify the label reverts to the org default; edit the
     description and verify it round-trips via API; verify the group
     header indicator appears after assignment and disappears after
     clearing.
