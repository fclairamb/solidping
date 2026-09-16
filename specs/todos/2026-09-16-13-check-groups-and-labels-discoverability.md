---
model: opus
effort: high
---

# Check groups have no home, and labels have no documentation

*Reported by **Jens**, an early user, on 2026-09-16, in first-run feedback.*

## What Jens said

> I have not figured out how to group checks, there is also something about labels which I have
> not understood

Two failures in one sentence: he could not find the groups feature, and he *did* encounter labels
without being able to tell what they were for or how they differ from groups.

## The two systems, and why confusing them is reasonable

| | **Check groups** | **Labels** |
|---|---|---|
| Cardinality | 0..1 per check — an FK column, `checks.check_group_uid` ([`sqlite/001_v0_1_0.up.sql:251`](server/internal/db/sqlite/migrations/001_v0_1_0.up.sql#L251), "NULL means ungrouped") | N per check — a join table ([`001_v0_1_0.up.sql:285-304`](server/internal/db/sqlite/migrations/001_v0_1_0.up.sql#L285-L304)) |
| Shape | named entity: name, slug, description, sort order ([`models/check_group.go:10-49`](server/internal/db/models/check_group.go#L10-L49)) | free `key=value` pairs, org-scoped ([`models/check.go:498-505`](server/internal/db/models/check.go#L498-L505)) |
| Drives the checks list | **yes** — `GROUP_BY_MODES = ["groups", "host"]` ([`checks.index.tsx:125-126`](web/dash0/src/routes/orgs/$org/checks.index.tsx#L125-L126)) | **no** — filter only |
| Behaviour attached | escalation-policy inheritance, status rollup, incident correlation, publishable as one status-page component, SLO scope, maintenance-window targeting | filtering, and status-page **dynamic section selectors** |
| Docs | [`features/check-groups.md`](web/docs/docs/features/check-groups.md) | **none** |

They are genuinely different tools — one is an exclusive hierarchy with behaviour, the other is a
non-exclusive tagging system — but nothing in the product says so, and both are offered in the
same column of the same check form
([`check-form.tsx:1617-1626`](web/dash0/src/components/shared/check-form.tsx#L1617-L1626) for
labels, [`:1632-1637`](web/dash0/src/components/shared/check-form.tsx#L1632-L1637) for the group
select).

## Why he could not find groups

**Check groups have no page.** There is no `check-groups.index.tsx` and no `check-groups.new.tsx`;
the only route file is [`check-groups.$uid.edit.tsx`](web/dash0/src/routes/orgs/$org/check-groups.$uid.edit.tsx),
whose `goBack()` returns to `/orgs/$org/checks` ([`:65`](web/dash0/src/routes/orgs/$org/check-groups.$uid.edit.tsx#L65)).

Everything happens inside the checks list:

- Creation is a **dialog** — trigger at [`checks.index.tsx:1527`](web/dash0/src/routes/orgs/$org/checks.index.tsx#L1527),
  markup at [`:1824-1895`](web/dash0/src/routes/orgs/$org/checks.index.tsx#L1824-L1895);
  `useCreateCheckGroup` ([`hooks.ts:1166`](web/dash0/src/api/hooks.ts#L1166)) is called from
  [`checks.index.tsx:1316, :1347`](web/dash0/src/routes/orgs/$org/checks.index.tsx#L1316) and
  **nowhere else in the frontend**.
- Assignment happens in four places: the check form's group select — which is **only rendered
  when groups already exist** (`const showGroup = (checkGroups?.length ?? 0) > 0;`,
  [`check-form.tsx:1080`](web/dash0/src/components/shared/check-form.tsx#L1080)) — the
  `?group=<slug>` prefill ([`checks.new.tsx:98-99, :124-136`](web/dash0/src/routes/orgs/$org/checks.new.tsx#L98-L99)),
  a "Change group" row action ([`checks.index.tsx:589-593`](web/dash0/src/routes/orgs/$org/checks.index.tsx#L589-L593)),
  and the API.

That `showGroup` guard is the trap. A new user has zero groups, so the check form **hides the
group field entirely**. They create checks; the concept never appears. To discover groups they
must notice a button on the checks list, in a toolbar, next to a grouping mode they have no reason
to switch. Jens did not, and the product never told him.

## Proposal

Ordered by value. A and B are the substance; C and D are cheap and worth doing in the same pass.

### A. Document labels

Write **`web/docs/docs/features/labels.md`**. It is the only feature in this area with no page at
all, and it now drives **public status-page membership** via section selectors
([`models/status_page.go:457-472`](server/internal/db/models/status_page.go#L457-L472)) — a
concept that gates what customers see should not be undocumented.

It must cover: the `key=value` shape and limits (key ≤50, value ≤200,
[`models/check.go:498-505`](server/internal/db/models/check.go#L498-L505)); that filtering ANDs
across pairs ([`ListChecksFilter.Labels`, `check.go:548`](server/internal/db/models/check.go#L548));
the autocomplete endpoint `GET /orgs/:org/labels`; the recommended `public=true` opt-in pattern
already referenced from [`status-pages.md:118-131`](web/docs/docs/features/status-pages.md#L118-L131);
and setting labels via config-as-code and the CLI.

### B. A "Groups vs labels" section, and cross-links

Add a short, blunt comparison — a version of the table above — and put it in **both**
[`check-groups.md`](web/docs/docs/features/check-groups.md) and the new `labels.md`, each linking
to the other. The one-sentence version to lead with:

> A check belongs to **one group** and carries **any number of labels**. Use a group for "what
> this check is part of" — it drives escalation, incident correlation and status-page rollups. Use
> labels for "how I want to slice the list" — they filter, and they can select checks onto a
> status page.

[`check-groups.md`](web/docs/docs/features/check-groups.md) currently ends by saying "Assign a
check to a group by setting its `checkGroupUid`" — an API instruction as the last word of a
user-facing page. Replace it with the four real UI paths listed above.

### C. Stop hiding the group field on the check form

Remove or invert the `showGroup` guard at
[`check-form.tsx:1080`](web/dash0/src/components/shared/check-form.tsx#L1080). When an org has no
groups, render the field with an empty state that explains what a group is and offers to create
one inline, rather than rendering nothing. Hiding a feature until it is already in use guarantees
nobody finds it.

Check the design reference first — `http://localhost:4000/d/orgs/default/design-reference`
([source](web/dash0/src/routes/orgs/$org/design-reference.tsx)) — for the empty-state pattern, and
note it already ships a [`CheckGroupPicker`](web/dash0/src/components/shared/check-group-picker.tsx)
(reference entry at `:5600-5620`) used by the status page editor. If that is the better control
here than the raw `Select`, use it.

### D. Give groups a visible surface

Minimum: keep creation where it is, but make the trigger legible — a labelled **"New group"**
button rather than one competing for attention with the grouping-mode toggle, and an empty state
on the checks list when an org has checks but no groups, explaining the feature in one sentence.

Deliberately **not** proposed: a top-level "Groups" nav item. Spec `07` is removing three nav
entries to cut the sidebar from 14 items to 12 on Jens's own complaint that it is too long; adding
one back for a feature that is intrinsically a view of the checks list would undo that. Groups
belong on the checks page. They just need to be visible there.

## Tests

- E2E: with **zero** groups in the org, open the check form and assert the group field renders
  with its empty state. This is the regression that `showGroup` currently causes and nothing
  tests it, because the test org has groups.
- E2E: create a group from the checks list empty state and confirm a check can be assigned to it
  in the same flow.
- `bun run test:unit` — new locale keys in all four of `en/fr/de/es`, enforced by
  [`locale-parity.test.ts`](web/dash0/src/locales/locale-parity.test.ts). Not covered by the
  backend QA loop; run it explicitly.
- Docs build: `bun run build` in `web/docs`. A new page needs a `sidebar_position` that does not
  collide — `check-groups.md` is 10, `discovery.md` 15, `events.md` 16, `status-badges.md` 14.
- Run the full dash0 E2E suite; `check-form.tsx` is shared by every check-creation test.

## Reply to Jens

Answer the question he actually asked, in two sentences: a group is an exclusive bucket that also
drives escalation and status-page rollups, created from the button on the Checks page; labels are
free `key=value` tags for filtering and for selecting checks onto a status page. Then admit the
real problem — the group field is hidden until you already have a group, so there was no way for
him to find it.
