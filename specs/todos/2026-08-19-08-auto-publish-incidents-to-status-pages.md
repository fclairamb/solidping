---
model: opus
effort: xhigh
---

# Automatic incidents never reach the status page — add an incident-publication overlay with auto-publish

## Problem

When a check that is displayed on a status page goes down, the public page's
per-resource state flips (the `RollupPageStatus` vocabulary in
`server/internal/db/models/page_status.go` already handles that), but **no
incident entry ever appears**. The incident narrative — banner, "what is
happening", timeline of updates, history — exists only as hand-authored
`status_updates` rows. If nobody is awake to write one, the page shows a red
dot with no explanation, and subscribers are never notified. Every serious
status-page competitor auto-displays outages.

Three concrete gaps:

1. **No automatic publication.** An incident opening in
   `server/internal/handlers/incidents/service.go` (`createIncident`,
   `createOrReopenIncident`, `createGroupIncident`) has no path to the status
   page or its subscribers.
2. **No manual incident object either.** There is no
   `POST /status-pages/:uid/incidents` — operators can post loose
   `status_updates`, but there is no grouping object with a state
   (investigating → resolved), a public title, or an incident history.
3. **No public/internal boundary.** The operational `incidents` row carries
   ack/snooze metadata and auto-generated titles that are wrong for customers,
   and probe diagnostics that must never leak.

The May backlog spec
(`specs/backlog/2026/05/2026-05-08-04-outage-vs-incident-split.md`) solved
this with a four-phase rename (`incidents` → `outages`, API aliases, breaking
cutover, then a new customer table). **This spec supersedes that approach**
— see Decisions.

## Proposal

Add a **publication overlay**: one new table that says "this incident is
visible on this status page, under this customer-readable title, in this
state". Reuse `status_updates` for the narrative and the existing
`statussubscribers.Notifier` fan-out for delivery. No rename, no API break.

### Schema

One new table, in the next release's consolidated migration
(`NNN_vX_Y_Z.up.sql`, both `postgres/` and `sqlite/` dialects):

```sql
CREATE TABLE incident_publications (
    uid                 uuid PRIMARY KEY,
    organization_uid    uuid NOT NULL REFERENCES organizations(uid),
    incident_uid        uuid NULL REFERENCES incidents(uid),  -- NULL = manually authored
    status_page_uid     uuid NOT NULL REFERENCES status_pages(uid),
    public_title        text NOT NULL,      -- templated at creation, human-editable
    public_state        text NOT NULL,      -- investigating | identified | monitoring | resolved
    severity            text NULL,          -- minor | major | critical (public badge only)
    auto_created        boolean NOT NULL DEFAULT false,
    human_touched_at    timestamptz NULL,   -- set on first manual edit or manual update
    published_at        timestamptz NOT NULL,
    resolved_at         timestamptz NULL,
    created_at / updated_at / deleted_at    -- house pattern
);
-- unique (incident_uid, status_page_uid) where incident_uid is not null and not deleted
```

Updates are **`status_updates` rows** (they already carry
`status_page_uid`, `incident_uid`, `kind`, markdown body, author — see
`wiki/database-model/status-pages.md`). Append-only: never edit a posted
update, only post the next one. The publication row *is* the incident↔page
link; no join table.

Settings:

- `status_pages`: `auto_publish boolean NOT NULL DEFAULT false`,
  `auto_publish_delay_seconds integer NOT NULL DEFAULT 60`,
  `auto_resolve text NOT NULL DEFAULT 'if_untouched'`
  (`always | if_untouched | never`).
- `status_page_resources`: `auto_publish boolean NULL` (NULL = inherit page).
- **New pages created after this ships get `auto_publish = true`; the
  migration leaves existing pages at `false`** — nobody's internal blips go
  public on upgrade. (Implement the default in the create path, not the DDL,
  so the migration is honest about existing rows.)

### Auto-publish pipeline

**Trigger.** After an incident opens (the three create/reopen paths in
`incidents/service.go`), resolve policy: find every status page displaying
the failing check or its group — `status_page_resources` reference checks
*and* check groups (see the group-resource comment at
`server/internal/handlers/statuspages/service.go:581`), so both must resolve.
Skip when:

- the page/resource `auto_publish` resolves to false;
- `paging_suppressed` is true (rolled up under a parent — the parent or group
  incident publishes, members never do);
- the check is inside an active maintenance window
  (`isCheckInActiveMaintenance`, `incidents/service.go:155`).

**Debounce.** Enqueue a new `job_incident_publish` job type
(`server/internal/jobs/jobtypes/`) with
`scheduled_at = now + auto_publish_delay_seconds` (the `jobs` model already
supports future scheduling — `server/internal/db/models/job.go:40`). At fire
time, re-check: if the incident already resolved, do nothing — a 40-second
blip never reaches the public page. Delay 0 is allowed and publishes
immediately. The job must be idempotent (unique publication per
incident × page; concurrent fire + manual publish must not double-create).

**Templating — the leak boundary.** The templated title uses the resource's
public display name (`public_name`, falling back to the check name — the same
fallback the page already renders, so nothing new is exposed): "*Payments
API* is experiencing issues", localized via the page's `language` using the
same mechanism as subscriber emails. Initial `status_updates` row:
`kind = investigating`, templated one-liner. **Probe output, error strings,
and `results.output` never flow into any public field** — they routinely
contain internal hostnames, IPs, and stack fragments. This is the security
invariant of the feature; it gets an explicit negative test (see
Verification).

**Resolve sync.** On `resolveIncident` / group resolve, for each linked
auto-created publication, keyed off `human_touched_at` and the page's
`auto_resolve`:

- `if_untouched` (default) + untouched: post a templated `resolved` update,
  set `public_state = resolved`, `resolved_at = now`.
- `if_untouched` + human touched: post a templated `monitoring` note ("the
  affected component has recovered"); the human owns the final resolve.
- `always`: resolve regardless. `never`: post nothing, resolve nothing.

**Relapse.** When `reopenIncident` (`incidents/service.go:860`) fires within
a dedupe window (reuse or mirror the incident reopen window) after the
publication resolved, **reopen the same publication** (clear `resolved_at`,
set `public_state = investigating`, post a templated update) instead of
minting a new entry.

**Group incidents** publish once per page. The publication's affected
resources are the member checks intersected with the page's resources; a new
member joining appends a rate-limited "also affecting X" update.

**Fan-out.** Auto-created updates go through the existing
`statusupdates.Service` → `SubscriberNotifier`
(`statusupdates/service.go:52`) path, so subscriber email (double-opt-in,
per-incident scope) and the Atom feed work with no new delivery code. Add a
storm cap — max N subscriber sends per publication per hour (default 4,
org parameter) — a flapping group incident must not spam. Trigger the
realtime status hint (`publishStatusHint`, `incidents/service.go:280`) on
publication changes so open status pages refresh.

### API (house conventions: camelCase, `data` envelope, `$uid`, PATCH)

- `GET  /api/v1/orgs/:org/status-pages/:uid/incidents?state=` — list.
- `POST /api/v1/orgs/:org/status-pages/:uid/incidents` — manual create
  (`title`, `severity`, optional `incidentUid`, initial update body).
- `GET / PATCH /api/v1/orgs/:org/status-pages/:uid/incidents/:uid` — read /
  edit title, severity, state (any edit sets `human_touched_at`).
- `POST /api/v1/orgs/:org/status-pages/:uid/incidents/:uid/updates` — append
  an update (`kind`, `bodyMarkdown`).
- `POST /api/v1/orgs/:org/incidents/:uid/publications`
  (`{statusPageUid}`) and `DELETE .../publications/:uid` — publish/unpublish
  from the incident side.
- Public payload: the status-page JSON gains `activeIncidents[]`
  (uid, title, state, severity, startedAt, affected resource names,
  updates[]) and a history endpoint/page for resolved ones.

Outgoing-webhook events fork: new `statuspage.incident.published`,
`statuspage.incident.updated`, `statuspage.incident.resolved` fire on
publication lifecycle — distinct from the internal `incident.created` /
`incident.resolved` events, which are unchanged.

Follow-on surfaces (same PR series, after the core works): MCP tools
(list/create publication, post update), config-as-code fields for the new
page settings, OpenAPI spec (`server/internal/app/openapi/openapi.yaml`).

### UI

- **status0** (start from the design reference; components in
  `web/status0/src/components/shared/`): top banner when any publication is
  active, colored by severity; active-incidents section reusing
  `status-updates-timeline.tsx`; per-resource badge tied to the publication's
  affected resources; incident history page. All fully mobile-usable.
- **dash0**: status-page settings gain the three auto-publish fields;
  incident detail gains a "Published on …" block with publish/unpublish; the
  publication editor lives on a dedicated route
  (`/orgs/$org/status-pages/$uid/incidents/$uid`) — never a modal, per repo
  convention.

### Suggested commit slices

1. Schema + publication service + manual CRUD + status0 rendering + public
   JSON (valuable alone: manual incidents don't exist today).
2. Auto-publish policy + debounce job + templates + resolve/relapse sync +
   subscriber wiring + storm cap.
3. Group-membership updates, webhook events, MCP tools, config-as-code,
   OpenAPI, docs (`web/docs/`).

## Decisions

- **Overlay, not the four-phase rename.** The May spec's Phases 1–3 cost
  weeks of mechanical churn across the 146-path API, MCP, CLI, Terraform
  provider and webhooks — plus a breaking cutover — before any user value.
  Since it was written, `status_updates` and the subscriber notifier shipped,
  covering the narrative and delivery halves. The overlay resolves all four
  papercuts the May spec identified (retention, access boundary, editing
  semantics, webhook fork) without them. The `incidents`→`outages` rename
  remains available later as pure hygiene; nothing here forecloses it.
- **Publication is per page, 1:1 with an incident.** The N:M
  outage↔incident aggregation from the May spec is dropped for v1; group
  incidents already aggregate related member checks.
- **Auto-publish defaults on for new pages, off for existing ones.**
- **Updates are append-only** (`status_updates` semantics, matching
  Hyperping/BetterStack).
- **Retention guard:** the incident reaper/retention path must never delete
  an incident row referenced by a publication.

## Out of scope

- The `incidents` → `outages` rename (future hygiene, separate spec if ever).
- N:M cross-incident aggregation beyond group incidents.
- Postmortems, localized narrative beyond the templated strings, webhook/
  Slack *subscription* channels for status pages, AI-suggested copy.

## Verification

Negatives need proving, with positive controls:

- Incident opens on an auto-publish page → publication + templated
  "investigating" update appear after the delay; the incident's probe output
  (seeded with a sentinel internal hostname) appears **nowhere** in the
  public JSON, Atom feed, or subscriber email. Positive control: the sentinel
  *is* visible on the internal incident API.
- Blip: incident resolves inside the delay → no publication ever appears.
  Positive control: same scenario with delay 0 → publication appears.
- `paging_suppressed` member incident → no member publication; the
  parent/group incident yields exactly one.
- Check in an active maintenance window → no publication.
- Human edits the title, then the incident resolves (`if_untouched`) →
  publication **not** auto-resolved; templated monitoring note posted.
  Untouched twin → auto-resolved with a resolved update.
- Relapse within the dedupe window → same publication row reopened, no
  second row (unique index holds under concurrent job + manual publish).
- Subscriber fan-out fires through the existing notifier; the storm cap
  stops the Nth+1 send in an hour.
- Private-page publications are invisible without page access.
- Existing pages upgrade with `auto_publish = false`; new pages default true.
- Migrations apply on both dialects; `make lint`, backend tests, and a
  status0 Playwright E2E (banner + timeline render) green.

## Implementation Plan

1. **Schema + models + DB layer.**
   `models/incident_publication.go` (row, state/severity/auto-resolve
   vocabularies, update struct, filter). `status_pages` gains
   `auto_publish` / `auto_publish_delay_seconds` / `auto_resolve`;
   `status_page_resources` gains a nullable `auto_publish` (NULL = inherit).
   New-page default `auto_publish = true` lives in `NewStatusPage`, never in
   the DDL, so existing rows upgrade to `false`. Migration
   `017_incident_publications` in BOTH `postgres/` and `sqlite/`, with the
   partial unique index on `(incident_uid, status_page_uid)`. `db.Service`
   gains create/get/find/list/update/soft-delete plus the public list and the
   retention guard `CountIncidentPublicationsForIncident`.

2. **Publication service + manual CRUD + public JSON.**
   New `internal/handlers/incidentpublications` package (service + handler).
   Routes: `GET/POST /status-pages/:uid/incidents`,
   `GET/PATCH /status-pages/:uid/incidents/:uid`,
   `POST .../incidents/:uid/updates`,
   `POST/DELETE /orgs/:org/incidents/:uid/publications`. Any manual edit or
   manual update stamps `human_touched_at`. The public status-page JSON gains
   `activeIncidents[]` and `/status-pages/:org/:slug/incidents` returns the
   resolved history. Public payloads carry title/state/severity/timestamps/
   affected public names/updates only — never probe output.

3. **Auto-publish policy + debounce job + templates + resolve/relapse sync +
   fan-out + storm cap.** `policy.go` resolves the pages displaying a check
   (directly or through its group), applies the page/resource `auto_publish`
   override, and skips `paging_suppressed` incidents and checks in an active
   maintenance window. `incidents.Service` gets a small
   `PublicationHook` interface (open / resolve / reopen) wired in
   `server.go` — no import cycle. Non-zero delay enqueues the new
   `incident_publish` job at `now + delay`; the job re-checks eligibility at
   fire time so a blip never publishes. Templates live in `templates.go`,
   keyed by the page language (en/fr/de/es, en fallback), built ONLY from the
   resource public name — probe output, error strings and `results.output`
   are structurally unreachable. Resolve honors `auto_resolve` ×
   `human_touched_at`; relapse reopens the same row. Subscriber fan-out goes
   through the existing `statusupdates.SubscriberNotifier`, capped at N sends
   per publication per hour (`status_page.publication_notify_cap` org
   parameter, default 4) using two counter columns on the publication row.
   Realtime `publishStatusHint` fires on every publication change.

4. **status0.** Severity-colored banner when a publication is active, an
   active-incidents section reusing `status-updates-timeline.tsx`, and an
   incident-history view. Mobile-first, i18n strings in all four locales.

5. **dash0.** Status-page settings gain the three auto-publish fields; the
   incident detail page gains a "Published on …" block with publish /
   unpublish; the publication editor is a dedicated route
   (`/orgs/$org/status-pages/$statusPageUid/incidents/$uid`), never a modal.

6. **Slice 3 surfaces.** Group-membership "also affecting X" updates
   (rate-limited), the forked webhook events
   `statuspage.incident.published|updated|resolved`, MCP tools
   (list/create publication, post update), config-as-code / MCP fields for the
   new page settings, OpenAPI paths and schemas, and `web/docs/` coverage
   under the status-pages feature page.

7. **Tests.** Every negative in `## Verification` with its positive control:
   the sentinel-hostname leak test (public JSON + feed + subscriber mail vs
   the internal incident API), blip-vs-delay-0, `paging_suppressed`,
   maintenance window, touched-vs-untouched resolve, relapse idempotency
   under concurrency, storm cap, private-page invisibility, and the
   migration default (existing pages `false`, new pages `true`) on both
   dialects. Plus a status0 Playwright spec for banner + timeline.

### Not applicable: config-as-code fields for the new page settings

The plan's step 6 listed "config-as-code fields for the new page settings",
following the Proposal's follow-on list. **That item has no surface to land
on and was not implemented.**

SolidPing's config-as-code is **checks-only**. The whole of it is
`server/internal/handlers/checks/apply.go` plus three routes —
`GET /api/v1/orgs/:org/checks/export`, `POST /checks/import`,
`POST /checks/apply` — and the manifest/import/apply logic in
`server/internal/handlers/checks/service.go` (see the manifest-name comment at
`apply.go:19`). There is **no** status-page export, import or apply path
anywhere in the repository: `grep -rn "config-as-code" server/ --include='*.go'`
returns only that checks file, `internal/app/server.go`,
`internal/envcheck/envcheck.go` and one org-rename test.

So there is no manifest schema to add `autoPublish` /
`autoPublishDelaySeconds` / `autoResolve` to. Building a status-page
config-as-code surface — manifest shape, export, diffing, apply semantics,
section/resource identity across applies — is a feature in its own right and
is far larger than this spec. It should be its own spec if it is wanted.

The settings ARE reachable declaratively today through the two surfaces that do
exist for status pages: the REST API (`POST`/`PATCH
/api/v1/orgs/:org/status-pages`, documented in `openapi.yaml`) and the MCP tools
`create_status_page` / `update_status_page`, both of which carry all three
fields.
