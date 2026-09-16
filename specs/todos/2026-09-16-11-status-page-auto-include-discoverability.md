---
model: opus
effort: high
---

# A new check doesn't reach the status page, and the feature that fixes it is invisible

*Reported by **Jens**, an early user, on 2026-09-16, in first-run feedback.*

## What Jens said

> When I add a new check, I also need to go into the Status page and add the check there, maybe
> have a status page show all checks in a group/label/all all?

## The uncomfortable part: we built exactly that, and he could not find it

**Dynamic sections already exist** (spec `2026-08-29-11`). A status page section can carry a
`selector` that is either `{"all": true}` or `{"labels": {k: v, …}}`, and a reconciler
materializes matching checks into the section:

- Model: [`models/status_page.go:457-472`](server/internal/db/models/status_page.go#L457-L472)
  (`SectionSelector`; ≤10 labels, ANDed, exact match, `SectionSelectorMaxLabels` at `:430`).
- Column: `status_page_sections.selector jsonb`,
  [`postgres/migrations/017_v0_21_0.up.sql:50-77`](server/internal/db/postgres/migrations/017_v0_21_0.up.sql#L50-L77).
- Reconciler: [`handlers/statuspages/selector.go`](server/internal/handlers/statuspages/selector.go),
  whose header comment pins three commitments (`:11-25`): **materialize, don't virtualize**;
  **manual wins**; **best effort, never transactional**. Cap of 200 managed resources per section
  at `:57`; backstop reconcile on page view at `:168`.
- UI: [`components/shared/section-membership.tsx`](web/dash0/src/components/shared/section-membership.tsx)
  — `MembershipMode = "manual" | "all" | "labels"` (`:26`), mounted in both the add-section and
  edit-section dialogs
  ([`status-pages.$statusPageUid.index.tsx:288, :389`](web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx#L288)).
- Docs: [`web/docs/docs/features/status-pages.md:86-188`](web/docs/docs/features/status-pages.md#L86-L188),
  including the recommended `public=true` label opt-in at `:118-131`.

Jens guessed the design of a shipped feature, in the vocabulary we used for it ("all checks in a
group/label/all"), without ever encountering it. That is not a feature request. It is a
discoverability defect, and it is more valuable than a feature request because the expensive part
is already paid for.

## Why he never saw it

1. **Manual is the default and nothing argues otherwise.** `AddSectionDialog`
   ([`:181`](web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx#L181)) opens on
   `MembershipMode = "manual"`
   ([`:195-200`](web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx#L195-L200)).
   A user creating their first section takes the default, gets a manual section, and never learns
   the other two modes exist — the cost of the default only becomes visible weeks later, one check
   at a time.
2. **The check-creation flow never mentions status pages.** `grep -n 'statusPage|status-page|StatusPage'`
   across [`check-form.tsx`](web/dash0/src/components/shared/check-form.tsx) and
   [`checks.new.tsx`](web/dash0/src/routes/orgs/$org/checks.new.tsx) returns **zero hits**. The
   form offers group, labels and escalation, and says nothing about publication. So the moment
   Jens is describing — "when I add a new check" — is precisely the moment the product is silent.
3. **There is no "add this check to an existing page" affordance.** The check detail page's
   "Publish on a status page" button
   ([`checks.$checkUid.index.tsx:1271-1291`](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx#L1271-L1291))
   links to `/orgs/$org/status-pages/new?checkUid=…` — the **new page** flow only
   ([`status-pages.new.tsx:15-23`](web/dash0/src/routes/orgs/$org/status-pages.new.tsx#L15-L23)).
   For a user who already has a page, the one button that sounds like the answer leads to
   creating a second page.
4. **Labels are undocumented.** There is no `web/docs/docs/features/labels.md`; labels appear only
   incidentally across `cli.md`, `config-as-code.md`, `status-pages.md`. The recommended
   `public=true` opt-in pattern therefore rests on a concept with no home page. (Spec `13` covers
   this.)
5. **The "Prefill for me" wand on page creation is a one-shot.**
   [`status-pages.new.tsx:39-73`](web/dash0/src/routes/orgs/$org/status-pages.new.tsx#L39-L73)
   pages through the whole org and attaches every check — at creation time, as manual resources.
   It looks like "show all checks" but is a materialization, not a standing rule. A user who used
   the wand has the strongest possible reason to believe auto-inclusion exists *and* the worst
   possible experience of it, because new checks silently stop appearing.

## Proposal

This is three small changes, in descending order of value. Do them all; none is large.

### A. Make the choice visible at section creation, and stop defaulting silently

In `AddSectionDialog`, present the three membership modes as an explicit up-front choice rather
than a control the user must notice. Concretely:

- Render the mode selector **above** the name/slug fields, not below, and give each mode a
  one-line description ("Manual — you pick each check", "All checks — new checks appear
  automatically", "By label — checks matching a label appear automatically").
- Keep **manual** as the default. Do not change the default behaviour: `selector.go`'s
  "manual wins" contract and the public-disclosure warning
  ([`section-membership.tsx:70-78`](web/dash0/src/components/shared/section-membership.tsx#L70-L78))
  exist because auto-publishing checks to a **public** page is a disclosure decision, not a
  convenience. Making the option obvious is right; making it the default is not.

### B. Fix the "Publish on a status page" button to reach existing pages

Change [`checks.$checkUid.index.tsx:1271-1291`](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx#L1271-L1291)
so it opens a small dialog instead of navigating straight to page creation:

- List the org's existing status pages, each with its sections, and add the check to the chosen
  section as a manual resource (`useCreateResource`, already used at
  [`:654`](web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx#L654)).
- Keep "Create a new status page" as an option in that dialog, preserving today's behaviour and
  the `?checkUid=` prefill.
- If the check already appears on a page — directly, via its group, or via a selector — say so
  instead of offering a duplicate. The group case matters: a resource can target a check **or** a
  check group ([`008_v0_7_0.up.sql:197-228`](server/internal/db/postgres/migrations/008_v0_7_0.up.sql#L197-L228),
  XOR constraint), and a check inside a published group is already visible without a row of its
  own.

### C. Say something at check-creation time

On the check form, after a successful create, surface a single non-blocking line in the success
toast or the post-create state: whether the new check landed on a status page, and if not, a link
to publish it. Something like *"Not on any status page — publish it"* / *"Appears on Public
Status via the 'API' section"*.

Deliberately **not** proposed: a status-page picker inside the check form. The form is already
long, publication is not part of creating a probe, and the CLAUDE.md rule that editing happens on
dedicated routes points the same way. One line of feedback, one link.

### D. Docs

- [`status-pages.md`](web/docs/docs/features/status-pages.md) already documents dynamic sections
  well at `:86-188`. Add a short **"A new check isn't showing up"** troubleshooting entry near the
  top that names the three membership modes — that is the phrasing someone will search for, and
  today the answer is 90 lines into the page under a heading they have no reason to open.
- Note the interaction the docs already state at
  [`:151`](web/docs/docs/features/status-pages.md#L151): *"Check groups are untouched. A rule only
  ever adds individual checks."* So Jens's "show all checks in a group" is served by publishing
  the **group itself** as one component (a group-targeted resource), which is a different
  mechanism from a label selector. Both answer his question; they behave differently on the public
  page (a group renders as a single rolled-up component, hiding member detail —
  [`status-pages.md:28-72`](web/docs/docs/features/status-pages.md#L28-L72)). The docs should put
  those two side by side, because "group" and "label" sound interchangeable and are not.

## Tests

- E2E: create a section with `all`, then create a new check, then assert it appears on the public
  page without further action. This is the exact loop Jens described and it is the one thing that
  must be provably true.
- E2E: the "Publish on a status page" dialog adds a check to an **existing** page's section, and
  reports correctly when the check is already published via its group.
- E2E: a check published only via a group selector is reported as already-published, not offered
  a duplicate row.
- `bun run test:unit` for any new locale keys — all four of `en/fr/de/es`, enforced by
  [`locale-parity.test.ts`](web/dash0/src/locales/locale-parity.test.ts), and not run by the
  backend QA loop.
- Run the full dash0 E2E suite; status pages share React Query keys with the checks list and
  per-file runs have missed cross-suite breakage here before.

## Reply to Jens

Tell him plainly that this exists, that it is called a dynamic section, and how to switch his
section to "all checks" or to a label — and that the fact he had to ask means it is not
discoverable enough, which is being fixed. Point him at the `public=true` label pattern; it is the
better answer for a page he intends to show customers, because it keeps publication an opt-in
decision per check.
