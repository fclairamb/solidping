---
model: opus
effort: high
---

# "Status pages" and "Status updates" are adjacent nav items that mean unrelated things

*Reported by **Jens**, an early user, on 2026-09-16 — the first thing he named
when asked what got in his way.*

## What Jens said

> There were many concepts and some of them sound similar, like "status page" and "status updates"
> but are two different things which confused me.

## Three entities, two adjacent nav rows, one shared prefix

| Entity | Table / model | What it is |
|---|---|---|
| **Status page** | `status_pages` | the public site itself |
| **Status update** | `status_updates` ([`models/status_update.go:41-64`](server/internal/db/models/status_update.go#L41-L64), table [`001_v0_1_0.up.sql:544-562`](server/internal/db/sqlite/migrations/001_v0_1_0.up.sql#L544-L562)) | "an operator-written narrative post anchored to a status page" — **one post on a timeline** |
| **Incident publication** | `incident_publications` ([`models/incident_publication.go:117-172`](server/internal/db/models/incident_publication.go#L117-L172)) | the **stateful public incident record** on a page |

The nav shows the first two as `statusPages` / `statusUpdates`
([`en/nav.json:11-12`](web/dash0/src/locales/en/nav.json#L11-L12)), one directly above the other,
and gives no hint that the third exists — even though the third is the one most people mean when
they say "post an update about an outage".

### The actual distinction

A **status update** is a single post. It always names a `status_page_uid` (NOT NULL) and may
optionally scope to a section, check, incident, or publication — all nullable. Its `kind` is
`investigating | identified | monitoring | resolved | maintenance | info`, so it can exist with
**no incident at all** (a maintenance notice, an FYI). `author_uid` is nullable: NULL means the
auto-publish pipeline wrote it ([`status_update.go:57-60`](server/internal/db/models/status_update.go#L57-L60)).

An **incident publication** is the publication overlay. Its own doc comment
([`incident_publication.go:117-125`](server/internal/db/models/incident_publication.go#L117-L125))
is the clearest statement in the codebase:

> "IncidentPublication is the publication overlay: 'this incident is visible on this status page,
> under this customer-readable title, in this state'. It is deliberately NOT the incident row."

It has its own public lifecycle (`investigating → identified → monitoring → resolved`,
deliberately a separate vocabulary from `models.IncidentState`,
[`:9-14`](server/internal/db/models/incident_publication.go#L9-L14)), a display-only severity
badge that "never routes, pages, or gates anything" ([`:59-60`](server/internal/db/models/incident_publication.go#L59-L60)),
and `HumanTouchedAt`, which is the basis of the `if_untouched` auto-resolve policy
([`:151-158`](server/internal/db/models/incident_publication.go#L151-L158)).

**Status updates are the children of a publication**: `StatusUpdate.IncidentPublicationUID`
([`status_update.go:48-51`](server/internal/db/models/status_update.go#L48-L51)) threads a post
under one, and `PublicationState.UpdateKind()`
([`incident_publication.go:42-55`](server/internal/db/models/incident_publication.go#L42-L55))
exists so "a reader never sees the timeline and the incident header disagree".

So `/orgs/$org/status-updates` is a **flat, cross-page list of every narrative post in the org**,
while `/orgs/$org/status-pages/$uid/incidents/$uid` — whose component is literally named
`PublicationEditorPage` ([`status-pages.$statusPageUid.incidents.$uid.tsx:39-42`](web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.incidents.$uid.tsx#L39-L42))
— edits one publication's header and appends posts to its thread. Both write to the same public
timeline. Only one of them has a lifecycle.

That is a genuinely subtle model. The nav labels make it worse by implying the two items are
variations on one thing.

## Proposal

The model is right; the model does not need changing. Fix the **labels, the placement, and the
absent documentation** — in that order. Rename nothing in the database, the API, or the route
paths.

### A. Rename the nav item

`statusUpdates` → **"Updates & notices"**, or **"Public updates"**.

The requirement is that the label must not begin with "Status", because the sole cause of the
confusion is the shared prefix sitting in adjacent rows. Any candidate that keeps it (e.g.
"Status posts") fails. Pick one, apply it to **all four locales** — `en/fr/de/es`, enforced by
[`locale-parity.test.ts`](web/dash0/src/locales/locale-parity.test.ts) — and check the other three
for the same collision, since fr/de/es may already read better or worse than English here.

Also update the page's own `<h1>` and description so the nav label and the page agree.

### B. Say what the page is, on the page

The status-updates list page needs one line of subtitle stating the relationship, e.g.

> Narrative posts published to your status pages — maintenance notices, general announcements, and
> updates threaded under a published incident.

And where an update belongs to a publication, link to that publication from the row. Today the
list flattens away the single most useful piece of context: whether a post stands alone or is part
of an ongoing public incident.

### C. Place it as a child of Status pages, not a sibling

Spec `07` groups the sidebar and puts **Status pages**, **Status updates** and **Maintenance** in a
"Public status" group. That already helps. Go one step further here: since every status update
names a status page (NOT NULL), the flat cross-page list is a *convenience view*, not a peer
concept. Consider making it a tab inside the status-pages section rather than its own nav row.

If that is done, it is a change to [`AppSidebar.tsx`](web/dash0/src/components/layout/AppSidebar.tsx),
which **spec `07` owns** — coordinate, and let `07` land last. If `07` has already landed, this
part is a follow-up edit to the group it created. **Do not do this part and part A in a way that
conflicts with `07`.**

### D. Document the three-way distinction

There is **no docs page for status updates** — `ls web/docs/docs/features/` has
`status-pages.md`, `status-page-tv-mode.md`, `status-badges.md`, and nothing else in the family. A
top-level nav item with no documentation is part of why the concept had to be guessed at.

Add a section to [`status-pages.md`](web/docs/docs/features/status-pages.md) — not a separate page,
because the whole point is that these belong together — covering: what a publication is and how it
differs from the operational incident; what an update is; that updates thread under a publication;
that an update can also stand alone as maintenance or info; and that the public state vocabulary is
deliberately separate from the internal incident state.

The two doc comments quoted above are already good prose. Lift them.

## Tests

- `bun run test:unit` — locale parity across all four locales for the renamed keys.
- E2E: any test asserting on the sidebar text "Status Updates" must be updated. Grep
  `web/dash0/e2e/` for it before starting.
- E2E: the status-updates list shows the linked publication for a threaded update.
- Docs build: `bun run build` in `web/docs`.
- Run the full dash0 E2E suite.

## Reply to Jens

Give him the distinction in three lines — page = the site, publication = one public incident with
its own lifecycle, update = one post on the timeline (which can also be standalone maintenance or
an announcement) — and tell him the nav label is being changed because the shared "Status" prefix
is the whole problem.
