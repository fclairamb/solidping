---
model: opus
effort: high
---

# A status page section cannot follow a check group, and the section dialog asks what a section contains before what it is called

*Reported by Florent on 2026-09-20, from the section dialog of a public page on solidping.io.*

## Problem

Two things, on the same dialog.

### 1. There is no "By group" membership mode

A section's dynamic membership rule (spec `2026-08-29-11`) knows two shapes:

```json
{"all": true}
{"labels": {"env": "prod"}}
```

[`SectionSelector`](server/internal/db/models/status_page.go#L472-L480) has exactly those two
fields, `Validate()` insists on exactly one of them, and the dashboard's picker offers
**Manual / All checks / By label**
([`section-membership.tsx:98-113`](web/dash0/src/components/shared/section-membership.tsx#L98-L113)).

Check groups are the primary way checks are organised in this product (the checks list is
rendered group by group, groups carry escalation policies, SLOs and maintenance windows scope to
them). Yet the only way to put a group on a status page is spec `2026-08-01-03`'s **group
resource**: one aggregated component whose status is the group's rollup
([`NewStatusPageGroupResource`](server/internal/db/models/status_page.go#L673)). That is a
different product: a visitor sees "Payments — Operational", not the five checks inside it. An
operator who wants each member listed on its own, and wants a check added to the group next month
to appear on the page by itself, has to reach for labels — which means inventing a label that
mirrors the group and remembering to set it on every new check. The first user feedback that
prompted spec `2026-09-16-11` asked for exactly this: *"have a status page show all checks in a
group/label/all"*. We shipped label and all. Group is still missing.

The backend is already most of the way there. `SectionSelector.Filter()` renders the rule as a
[`ListChecksFilter`](server/internal/db/models/status_page.go#L524-L537), and that filter already
has a `CheckGroupUID` field wired through both stores
([`postgres.go:1984-1991`](server/internal/db/postgres/postgres.go#L1984-L1991),
[`sqlite.go:1888-1894`](server/internal/db/sqlite/sqlite.go#L1888-L1894)). The reconciler,
materialisation, the 200-row cap, "manual wins", claimed-elsewhere and the public-payload
redaction are all selector-shape-agnostic. What is missing is the third field and everything that
names the modes.

### 2. Name and Slug come after Membership

Spec `2026-09-16-11` §A moved the membership picker **above** the name and slug fields in both
`AddSectionDialog` and `EditSectionDialog`
([`status-pages.$statusPageUid.index.tsx:266-300`](web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx#L266-L300),
[`:380-408`](web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx#L380-L408)),
with a code comment arguing that "what a section contains is the decision, its name is only a
label for that decision". The discoverability problem that spec was solving is real, but the
placement is the wrong fix for it: a form that opens on a three-way segmented control and a legend,
and only then asks for a name, reads as a settings page rather than a "create a section" dialog,
and the screenshot the user sent shows the name field pushed below the fold once the legend is
there. The part of §A that actually delivered the discoverability — the always-visible one-line
hint for every mode, emphasised for the active one — does not depend on the picker being first.

With a fourth mode the segmented control gets wider and the legend gets a fourth line, so the
ordering question has to be settled in the same change.

## Proposal

### A. Backend — a third selector shape, `checkGroupUid`

Extend the selector to exactly one of three:

```json
{"all": true}
{"labels": {"env": "prod"}}
{"checkGroupUid": "b6c1…"}
```

- **Model** ([`status_page.go`](server/internal/db/models/status_page.go#L472)): add
  `CheckGroupUID string \`json:"checkGroupUid,omitempty"\``. The name matches
  `StatusPageResource.checkGroupUid` and the checks-list `checkGroupUid` query parameter; store the
  **UID**, never the slug (slugs are renameable, and every other status page reference to a group
  is by UID).
- **`Validate()`**: exactly one of `all` / `labels` / `checkGroupUid`; two or more set →
  `ErrSelectorAmbiguous` (reword the error to name all three); `{}` stays `ErrSelectorEmpty`; an
  empty `checkGroupUid` string is `ErrSelectorEmpty`, not a group lookup. Add
  `ErrSelectorGroupNotFound` for the service-level check below.
- **`Filter()`**: for the group shape set `filter.CheckGroupUID = &sel.CheckGroupUID`. The
  `"none"` sentinel in `ListChecksFilter` means *ungrouped checks* — it must never be reachable
  from a selector, which the existence check guarantees, but say so in a test.
- **`Equal()`** ([`:540`](server/internal/db/models/status_page.go#L540)) must compare
  `CheckGroupUID` too. Today it compares `All` and `Labels` only; without this, switching a section
  from group A to group B is a no-op update that never re-reconciles.
- **Service** ([`statuspages/service.go` create/update](server/internal/handlers/statuspages/service.go#L1020-L1031),
  [`parseSelector`](server/internal/handlers/statuspages/selector.go#L64)): after parsing, resolve
  the group with `GetCheckGroupByUidOrSlug(ctx, org.UID, uid)` scoped to the **page's org**. Not
  found, soft-deleted, or belonging to another org → `400 VALIDATION_ERROR` with a detail that
  says the group does not exist in this organization. Never leak whether the UID exists elsewhere.
- **Deleted group semantics.** `DeleteCheckGroup` is a soft delete that does **not** ungroup member
  checks ([`postgres.go:6131-6136`](server/internal/db/postgres/postgres.go#L6131-L6136),
  [`checkgroups/service.go:327`](server/internal/handlers/checkgroups/service.go#L327)), and the
  `check_group_uid = ?` filter does not join `check_groups.deleted_at`. So a naïve group selector
  would keep publishing the members of a group that no longer exists. Decide it the safe way: in
  `desiredChecks` / `reconcileSection`, a group selector whose group is missing or deleted
  **matches nothing** and its managed rows are dropped, exactly as a label rule that stopped
  matching would. Expose it on the authenticated section payload as `selectorGroupMissing: true`
  (next to `selectorMatchTotal`, admin-only like it) so the dashboard can render an amber warning
  rather than a silently empty section — a rule that suppresses nothing must never look neutral.
- **Reconcile trigger.** Check writes already call `ReconcileOrgSelectors`
  ([`checks/service.go:534-555`](server/internal/handlers/checks/service.go#L534-L555)), and a
  check changing group is a check write, so adoption/drop on regroup is covered. Group deletion is
  not: wire the same optional `StatusPageReconciler` into the checkgroups service and call it after
  a successful delete, so the section empties promptly rather than on the next page view. Keep it
  best-effort and non-failing, same contract as the checks side.
- **OpenAPI** ([`StatusPageSectionSelector`](server/internal/app/openapi/openapi.yaml#L12795)):
  add `checkGroupUid`, rewrite the "exactly one of `all` / `labels`" description for three shapes,
  spell out the deleted-group behaviour and add `selectorGroupMissing` to `StatusPageSection`.
  Regenerate `server/pkg/client` (`go generate`, see
  [`generate.go`](server/pkg/client/generate.go)).
- The MCP section tools ([`tools_statuspages.go:291`](server/internal/mcp/tools_statuspages.go#L291),
  [`:325`](server/internal/mcp/tools_statuspages.go#L325)) do not expose `selector` at all today.
  Leave that as is — out of scope, not a regression.

### B. Dashboard — "By group" mode, and Name/Slug first

- **Picker** ([`section-membership.tsx`](web/dash0/src/components/shared/section-membership.tsx)):
  `MembershipMode` gains `"group"`, `SectionMembershipValue` gains `checkGroupUid?: string`,
  and the segmented control gains a fourth option **By group** (`testId:
  "section-membership-group"`). In group mode render the existing
  [`CheckGroupPicker`](web/dash0/src/components/shared/check-group-picker.tsx) — the same one the
  add-component dialog uses at
  [`:503`](web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx#L503) — under a
  "Group" label with a hint that makes the difference from a group *component* explicit:
  *"Every check in this group is listed on its own, including checks added to the group later. To
  show the group as a single component instead, add it from the Components list."*
  `membershipFromSelector`, `selectorFromMembership` and `membershipIsComplete` round-trip the new
  shape (group mode with no group chosen is incomplete, like labels with none).
- **Public-page warning**: `showWarning` already fires for any non-manual mode; add a
  `publicWarningGroup` variant of the copy.
- **Missing group**: when the stored selector's group is not in `useCheckGroups(org)` (or the
  payload says `selectorGroupMissing`), show an amber alert in the editor and on the section card
  ("This group no longer exists; the section shows nothing until you pick another rule").
- **Client-side matcher** ([`check-publication.ts:47-57`](web/dash0/src/lib/check-publication.ts#L47-L57)):
  `selectorMatchesCheck` must handle the group shape (`check.checkGroupUid === selector.checkGroupUid`)
  — `sectionPublication` already receives `checkGroupUid`, so the post-create "published on…" hint
  from spec `2026-09-16-11` keeps telling the truth.
- **Locales**: new keys under `sections.membership` (`group`, `hint.group`, `groupField`,
  `groupHint`, `publicWarningGroup`, `groupMissing`) in `en`, `fr`, `de`, `es`. The parity test in
  [`section-membership.test.ts:101-140`](web/dash0/src/components/shared/section-membership.test.ts#L101-L140)
  enforces this; run `bun run test:unit`, not just the E2E.
- **Mobile**: `SegmentedControl` is `inline-flex` with no wrapping
  ([`segmented-control.tsx:48`](web/dash0/src/components/ui/segmented-control.tsx#L48)). Four
  options at 375 px must not overflow the dialog; if they do, let the control wrap or use the
  grid layout, and add the four-option case to the design-reference page.
- **Ordering** (both dialogs): render **Name**, then **Slug**, then **Membership**. Rewrite the
  two code comments at
  [`:266-272`](web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx#L266-L272)
  and [`:380-386`](web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx#L380-L386)
  so they no longer justify the reverse; the always-visible legend is what carries the
  discoverability now, and that stays. `manual` remains the default. The E2E suite targets
  `data-testid`s, so the reorder itself breaks nothing there.

### C. Docs

- [`status-pages.md`](web/docs/docs/features/status-pages.md#L27-L31) and
  [`:122-128`](web/docs/docs/features/status-pages.md#L122-L128): add the **By group** row to both
  tables, "Two rules are available" → three, and a sentence on the deleted-group behaviour.
- [`check-groups.md:143`](web/docs/docs/features/check-groups.md#L143): the "Status pages" row
  currently reads *"publish a whole group as one component | select checks into a section by
  label"* — groups now do both; say so.

### D. Tests

Backend, in [`selector_test.go`](server/internal/handlers/statuspages/selector_test.go) next to the
existing cases: `Validate` rejects group+all, group+labels and empty string; create/update rejects an
unknown UID, a deleted group and another org's group (positive control: the same UID in its own org
is accepted); round trip — a check moved into the group is adopted on reconcile and dropped when it
leaves; deleting the group drops the managed rows and sets `selectorGroupMissing`; manual wins and
claimed-elsewhere behave as for labels; switching group A → B re-materialises (the `Equal` case);
the public payload hides the selector as before.

Dashboard: extend the round-trip unit tests in `section-membership.test.ts`; add a group case to
[`status-page-section-selector.spec.ts`](web/dash0/e2e/status-page-section-selector.spec.ts) (pick
a group, a check created later in that group appears with the `auto` badge, the public warning
fires); and one assertion that Name precedes Membership in DOM order in the add dialog.

## Open questions

- Should a group selector also accept the group **slug** on input (resolved to the UID on save,
  like `GetCheckGroupByUidOrSlug` allows elsewhere)? Nice for the API and MCP users; the dashboard
  only ever sends the UID. Default: accept both on input, store and return the UID.

## Implementation Plan

1. Extend the status-page selector model and service validation to resolve a group UID or slug
   into the page organisation's canonical group UID; make reconciliation empty a selector whose
   group has been deleted and expose that state to authenticated editors.
2. Reconcile selectors after check-group deletion, update the OpenAPI/client surface, and cover
   validation, selector transitions, deleted-group cleanup, and public-payload redaction.
3. Add the dashboard's responsive By group membership mode, missing-group warning, publication
   matcher support, and Name/Slug-first dialog ordering with complete locale coverage and tests.
4. Document dynamic group sections, run formatting and the relevant backend/dashboard/E2E gates,
   then archive this completed spec.
