---
model: opus
effort: high
---

# Escalation policies are unassignable from the UI and have no org-wide fallback

## Problem

Escalation policies can be created and edited in dash0
(`web/dash0/src/routes/orgs/$org/escalation-policies.*.tsx`), but nothing in
the UI ever makes one fire:

- **No assignment surface.** `escalationPolicyUid` appears nowhere in
  `web/dash0/src` outside the policy CRUD routes — the check form has zero
  escalation references. The only way to attach a policy to a check is the raw
  REST API (`server/internal/handlers/checks/service.go:655` on create,
  `:1256` on PATCH, where `""` clears). The field is also absent from
  `server/internal/app/openapi/openapi.yaml`, so it is effectively
  undocumented.
- **No org-wide fallback.** `resolveEscalationPolicyUID`
  (`server/internal/handlers/incidents/service.go:1571`) resolves
  check → check-group → nothing. A check without an explicit policy (directly
  or via its group) never pages anyone, so every new check must be wired
  individually — which, per the point above, is not even possible from the UI.
- **Once a default exists, "no policy" becomes ambiguous.** Today
  `escalation_policy_uid IS NULL` means "silent". With an org default it would
  mean "inherit", so an explicitly-silent state must remain expressible.

## Proposal

Three pieces, sharing one design decision made up front: **no
`escalation_mode` column and no seeded magic "None" policy**. "Explicitly
silent" is represented by assigning a **zero-step policy** (null-object
pattern, by convention, with a picker shortcut). Zero-step policies are
already legal — validation only checks the steps provided
(`server/internal/handlers/escalationpolicies/service.go:79-90`, no
minimum-step rule) — and already page nobody: `ScheduleEscalationCycle`
returns immediately on empty steps
(`server/internal/jobs/jobtypes/job_escalation_step.go:816`).

### 1. Org-level default escalation policy

- New nullable column on `organizations`:
  `default_escalation_policy_uid uuid references escalation_policies(uid) on
  delete set null` (Postgres + SQLite migrations; keep parity per the
  sync-pg-to-sqlite convention). The table today is minimal
  (`server/internal/db/postgres/migrations/001_v0_1_0.up.sql:5`), so a column
  fits better than a settings row.
- Extend `resolveEscalationPolicyUID` to check → group → **org default** →
  none. Resolution stays at incident open only
  (`service.go:1390-1396`, `EventTypeIncidentCreated`); changing the default
  never retargets in-flight cycles.
- **Opt-in:** unset by default → zero behavior change at rollout. Orgs
  without a default keep today's semantics exactly.
- API: expose `defaultEscalationPolicyUid` on the org settings
  endpoint (GET + PATCH, org-admin only, `""` clears). Document it in
  `openapi.yaml`.
- UI: a field on the org settings page. When setting or changing the
  default, show the blast radius: *"N checks currently inherit and will start
  using this policy"* (checks with no direct policy whose group also has
  none).

### 2. Empty-policy-as-silent convention

- **Picker shortcut** (see §3): the policy picker offers a "No escalation
  (silent)" action that creates a zero-step policy (suggested name "No
  escalation", editable) and assigns it — or lists existing zero-step
  policies for reuse. No seeded row, no protected flag.
- **Legibility:** policy list shows a "0 steps — silent" badge on zero-step
  policies; the check form shows an informational (not error) note when the
  selected policy has no steps: *"This policy has no steps — this check will
  never page anyone."*
- **Deletion guard:** the policy delete confirmation must show usage count
  for **all** policies: *"Used by N checks and M groups — they will fall back
  to inherited escalation."* This matters doubly here: the FK is
  `on delete set null` (`001_v0_1_0.up.sql:297`, `:332`), so deleting a
  silent policy flips its checks back to inherit and can **un-silence them
  onto the org default**. Usage count requires a cheap
  count-by-policy-uid query over checks and check_groups.

### 3. Assignment picker on the check form

- New field in the check form (alongside the existing notification settings),
  a select with:
  - **"Inherit — currently: `<resolved name>`"** (default) — live-resolves
    group → org default so the choice is never blind; shows "nothing" when
    the chain resolves to no policy.
  - The org's policies (silent ones badged).
  - The "No escalation (silent)" shortcut from §2.
- PATCH semantics unchanged: uid to set, `""` to clear (= inherit). Add
  `escalationPolicyUid` to the check schemas in `openapi.yaml`.
- Check-group assignment: the model already supports it
  (`server/internal/db/models/check_group.go:18`) and the resolver already
  honors it; verify the group REST API exposes `escalationPolicyUid` and add
  it if missing. **No group UI work** — there is no check-group management UI
  today; out of scope.

### Non-goals

- No `escalation_mode` tri-state column, no seeded/protected "None" policy.
- No changes to the channel-broadcast pipeline (`queueNotifications`) — it
  never consults policies and stays independent.
- No retroactive effect: open incidents keep the cycle scheduled at open.

### Testing

- Resolver unit tests: precedence check > group > org default > none;
  org-default unset keeps current behavior (positive control: same fixtures
  with default set do page).
- Incident-open integration test: check with no policy in an org with a
  default schedules cycle 0 from the default; zero-step default schedules
  nothing.
- Policy deletion: usage-count endpoint/query correctness; deleting an
  assigned policy nulls references (existing FK behavior) — assert the
  resulting resolution falls back to the org default.
- Playwright: picker on the check form (inherit label shows resolved name,
  silent shortcut creates + assigns a zero-step policy), org settings default
  field with blast-radius count, policy list badge, delete confirmation
  usage count.

## Implementation Plan

### Backend
1. **Migration**: `alter table organizations add column
   default_escalation_policy_uid ... references escalation_policies(uid) on
   delete set null` (Postgres + SQLite, mirrored). Landed as its own scratch
   migration `008_v0_7_0` during development; squashed into the single
   `006_v0_5_0` consolidated release migration alongside 006/007 before
   release, per wiki/conventions/database.md "one migration per release".
2. **Organization model** (`models/organization.go`): add
   `DefaultEscalationPolicyUID *string` (bun `default_escalation_policy_uid`);
   add `DefaultEscalationPolicyUID *string` + `ClearDefaultEscalationPolicyUID
   bool` to `OrganizationUpdate`. Wire `UpdateOrganization` (pg + sqlite) to
   set/clear the column.
3. **Resolver** (`incidents/service.go` `resolveEscalationPolicyUID`): extend to
   check → group → **org default** → none by loading the org via
   `check.OrganizationUID`. Resolution stays at incident-open only.
4. **New count db methods** (interface + pg + sqlite + test mock stub):
   `CountEscalationPolicyStepsByPolicy(policyUIDs) map`,
   `CountChecksByEscalationPolicy(orgUID) map`,
   `CountCheckGroupsByEscalationPolicy(orgUID) map`,
   `CountChecksInheritingOrgDefault(orgUID) int` (checks with no direct policy
   whose group also has none).
5. **Org settings API** (`auth/service.go`): `OrgSettingsResponse` gains
   `defaultEscalationPolicyUid` + `inheritingCheckCount` (blast radius);
   `UpdateOrgSettingsRequest` gains `defaultEscalationPolicyUid` (`""` clears,
   org-admin only, already gated). Validate the uid belongs to the org.
6. **Check-group REST** (`checkgroups/service.go`): expose `escalationPolicyUid`
   on response + create/update requests (`""` clears on update); wire
   `UpdateCheckGroup`/`CreateCheckGroup` to persist it (pg + sqlite
   `UpdateCheckGroup` gets the missing write path).
7. **Escalation policy list** (`escalationpolicies` handler/service): list items
   gain `stepCount`, `usageCheckCount`, `usageGroupCount`.
8. **openapi.yaml**: add `escalationPolicyUid` to Check / CreateCheckRequest /
   UpdateCheckRequest / UpsertCheckRequest; `escalationPolicyUid` to CheckGroup +
   create/update; `stepCount`/`usageCheckCount`/`usageGroupCount` to
   EscalationPolicy; `defaultEscalationPolicyUid` + `inheritingCheckCount` to
   OrgSettingsResponse; `defaultEscalationPolicyUid` to UpdateOrgSettingsRequest.

### Frontend (dash0)
9. **hooks.ts** types: Check / Create / Update check requests gain
   `escalationPolicyUid`; CheckGroup + requests gain it; EscalationPolicy gains
   `stepCount`/`usageCheckCount`/`usageGroupCount`; OrgSettings gains
   `defaultEscalationPolicyUid`/`inheritingCheckCount`; UpdateOrgSettingsRequest
   gains `defaultEscalationPolicyUid`.
10. **Check form** (`shared/check-form.tsx` + a new
    `checks/form/sections/escalation.tsx`): picker in the Notifications card —
    "Inherit — currently: <resolved name>" (group → org default), the org's
    policies (silent badged), and "No escalation (silent)" shortcut
    (reuse-or-create a zero-step policy). Informational note when the selected
    policy has 0 steps. Thread `escalationPolicyUid` through `CheckFormData` and
    both routes' `onSubmit` (create: send when set; edit: always send, `""` =
    inherit).
11. **Org settings page**: default escalation policy `<Select>` + blast-radius
    count line ("N checks currently inherit…").
12. **Escalation policy list**: "0 steps — silent" badge; delete confirmation
    shows "Used by N checks and M groups — they will fall back to inherited
    escalation."

### Tests
13. Go resolver white-box unit tests (in-memory sqlite): precedence check >
    group > org default > none; org-default-unset control vs default-set pages.
14. Go incident-open integration: no-policy check + org default schedules cycle
    0; zero-step default schedules nothing.
15. Go usage-count query tests; policy delete nulls refs → resolution falls back
    to org default.
16. Go check-group `escalationPolicyUid` round-trip.
17. Playwright E2E for picker, org-default blast radius, list badge, delete
    usage count.
