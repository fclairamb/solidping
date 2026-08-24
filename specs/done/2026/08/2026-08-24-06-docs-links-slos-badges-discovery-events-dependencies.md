---
model: sonnet
effort: medium
---

# SLOs, badges, discovery, events and dependencies pages have no documentation link

## Problem

`PageHeader` supports a `docsHref` prop that renders the discreet header
`DocsLink` icon ([page-header.tsx:15](web/dash0/src/components/shared/page-header.tsx),
[docs-link.tsx](web/dash0/src/components/shared/docs-link.tsx)), and most major
pages use it (checks, incidents, maintenance windows, on-call, escalation
policies, integrations, status pages, status updates…). Five pages don't:

| Page | Route | `PageHeader` call | Docs page today |
|---|---|---|---|
| SLOs | `/orgs/$org/slos` | [slos.index.tsx:160](web/dash0/src/routes/orgs/$org/slos.index.tsx) | **exists**: `web/docs/docs/features/slos.md` |
| Status badges | `/orgs/$org/badges` | [badges.tsx:432](web/dash0/src/routes/orgs/$org/badges.tsx) | none |
| Discovery | `/orgs/$org/discovery` | [discovery.index.tsx:154](web/dash0/src/routes/orgs/$org/discovery.index.tsx) | none |
| Events | `/orgs/$org/events` | [events.tsx:90](web/dash0/src/routes/orgs/$org/events.tsx) | none (only an `## Events` section inside `features/incidents.md`, and the page is no longer only about checks/incidents) |
| Dependencies graph | `/orgs/$org/dependencies` | [dependencies.index.tsx:111](web/dash0/src/routes/orgs/$org/dependencies.index.tsx) | covered by `features/incidents.md` § "Group Incidents (Correlated Outages)" |

The `DocsLink` contract says to pass `docsHref` only when a genuinely relevant
docs page exists — so for badges, discovery and events the docs pages must be
written, not just linked.

## Proposal

### 1. New docs pages (`web/docs/docs/features/`)

Follow the structure/tone of existing feature pages (frontmatter with
`sidebar_position`, `#` title, task-oriented sections). Study the actual
feature code/routes before writing so the content is accurate, not invented:

- **`status-badges.md`** — embeddable per-check SVG status/uptime badges:
  what they show, the URL parameters surfaced by the builder UI in
  `badges.tsx` (`period`, `style`, `label`…), Markdown/HTML embed snippets,
  and how badge visibility relates to check/status-page visibility.
- **`discovery.md`** — discovery jobs: what gets scanned (CIDRs, hosts,
  Kubernetes namespaces per `discovery.index.tsx`), how a job turns findings
  into checks, scheduling/lifecycle, and the new-job flow (`discovery.new.tsx`).
- **`events.md`** — the org-wide events timeline: which event types exist
  (see the type filter in `events.tsx` and the backend event catalog), how it
  differs from the audit log, and how incident events relate. Cross-link the
  existing `## Events` section in `features/incidents.md` to this page rather
  than duplicating it.

`llms.txt` / `llms-full.txt` regenerate from docs content at build time — no
extra step needed.

### 2. Wire `docsHref` on the five pages

- `slos.index.tsx` → `/docs/features/slos`
- `badges.tsx` → `/docs/features/status-badges`
- `discovery.index.tsx` → `/docs/features/discovery`
- `events.tsx` → `/docs/features/events`
- `dependencies.index.tsx` → `/docs/features/incidents#group-incidents-correlated-outages`
  (anchor verified: `## Group Incidents (Correlated Outages)` in
  [incidents.md:50](web/docs/docs/features/incidents.md))

Match the existing usage pattern (e.g. `checks.index.tsx`,
`incidents.index.tsx`). Sub-pages of these sections (e.g. `slos.$uid.*`,
`discovery.$jobUid.*`) can carry the same link where they render their own
`PageHeader`, but the list/landing pages above are the required scope.

### 3. Tests

- Extend or mirror the existing E2E coverage for `data-testid="docs-link"`
  (see how currently-linked pages assert it) so each of the five pages
  asserts a `docs-link` with the exact expected `href`.
- Docs build (`web/docs`) must pass — Docusaurus fails on broken internal
  links, which also validates the incidents anchor.

## Open questions

- Exact slug for the badges page: `status-badges` (proposed, matches the
  feature's "status badges" name) vs `badges`. Implementer picks one and keeps
  the dash link consistent with it.
- Whether the dependencies graph deserves its own docs page later; for now the
  incidents grouping section is the agreed target per the request.
