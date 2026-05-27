# Status updates: public status page timeline

## Context

**Depends on** [2026-05-18-02-status-updates-backend-and-admin.md](2026-05-18-02-status-updates-backend-and-admin.md) —
Spec 02 delivers the `status_updates` table, admin API, and dash0 write UI. This spec closes the
public read side.

The public status page (`web/status0/`) currently has no history or narrative. Visitors see a
green/red dot and daily uptime bars, but have no way to learn what happened yesterday, whether a
maintenance window is upcoming, or what an operator is doing about an ongoing degradation.

This spec adds:
1. A `recentUpdates` array embedded in the existing public status-page API response.
2. A "load more" endpoint for pagination.
3. `react-markdown` in `web/status0/` for safe markdown rendering.
4. A **Status updates timeline** section at the bottom of the public status page view.

## Goal

- Visitors on `/<org>` or `/<org>/<slug>` see a chronological list of recent updates (within
  `historyDays`) in a new "Recent updates" section below the resource cards.
- Incident-attached updates are grouped under an incident thread (showing the kind progression).
- Standalone updates (maintenance, info) appear as individual cards.
- Markdown is rendered safely (no HTML injection).
- The "read more" link on each card opens in a new tab with `rel="noopener noreferrer"`.
- Accessibility: kind badge has `aria-label`, time uses `<time datetime="...">`.

## Non-goals

- Markdown live preview in the dash0 editor (that's v2 if operators ask for it).
- Automatic i18n / translation of update content — content renders as-authored; the language
  switcher keeps existing behavior for UI strings.
- Server-side rendering of markdown.
- Real-time push / SSE for new updates while the page is open.
- Section-scoped or check-scoped inline rendering (the timeline section shows all updates for the
  page; v2 could also embed section-scoped updates inside section cards).

## Design

### Public API extension

#### Embed in existing response

Extend `StatusPageResponse` in `server/internal/handlers/statuspages/service.go:70` with:

```go
type StatusPageResponse struct {
    // ... existing fields unchanged ...
    RecentUpdates []StatusUpdatePublicResponse `json:"recentUpdates,omitempty"`
}

type StatusUpdatePublicResponse struct {
    UID         string    `json:"uid"`
    SectionUID  *string   `json:"sectionUid,omitempty"`
    CheckUID    *string   `json:"checkUid,omitempty"`
    IncidentUID *string   `json:"incidentUid,omitempty"`
    Title       string    `json:"title"`
    BodyMarkdown string   `json:"bodyMarkdown"`
    LinkURL     *string   `json:"linkUrl,omitempty"`
    Kind        string    `json:"kind"`
    PublishedAt time.Time `json:"publishedAt"`
}
```

Populate `RecentUpdates` only in `ViewDefaultStatusPage` and `ViewStatusPage` (the two
public handlers at `server/internal/app/server.go:782-784`). The admin CRUD handler
(`GetStatusPage`) leaves it nil. Use `historyDays` as the cutoff — query `status_updates WHERE
status_page_uid = $1 AND published_at >= now() - historyDays * interval '1 day' AND deleted_at IS
NULL ORDER BY published_at DESC LIMIT 100`. Inject `StatusUpdatesService` into the status pages
service (or query directly via a shared Bun DB handle — whichever matches the existing pattern in
`server/internal/app/services/`).

#### Pagination endpoint (optional, for "load more")

```
GET /api/v1/status-pages/:org/:slug/updates
    ?from=<RFC3339>&to=<RFC3339>&limit=&offset=
    → { "data": [StatusUpdatePublicResponse] }
```

No authentication. Register in the public route group alongside the existing
`ViewStatusPage` handler. This endpoint is optional for v1 if `historyDays * 100 updates` is
enough in the embedded payload. Implement if the status page service already has enough updates to
warrant pagination.

### Markdown rendering (`web/status0/`)

**Library**: `react-markdown` with `remark-gfm`.

```bash
pnpm add react-markdown remark-gfm
```

**Safe renderer config** — no raw HTML, no javascript: links:

```tsx
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

const ALLOWED_SCHEMES = ['http', 'https', 'mailto'];

function SafeMarkdown({ content }: { content: string }) {
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      skipHtml           // drop <script>, <iframe>, raw HTML tags
      components={{
        a: ({ href, children, ...props }) => {
          const scheme = href?.split(':')[0] ?? '';
          if (!ALLOWED_SCHEMES.includes(scheme)) return <>{children}</>;
          return (
            <a href={href} rel="noopener noreferrer" target="_blank" {...props}>
              {children}
            </a>
          );
        },
        h1: 'h3', h2: 'h3', h3: 'h3', // cap heading depth
      }}
    >
      {content}
    </ReactMarkdown>
  );
}
```

All `a` elements rendered by markdown get `rel="noopener noreferrer"` and open in a new tab.
`javascript:` URLs are stripped (scheme not in `ALLOWED_SCHEMES`). Raw HTML is dropped via
`skipHtml`. This replaces any need for `rehype-sanitize` and avoids adding a second dependency.

### Components (`web/status0/src/components/shared/`)

#### `status-update-card.tsx`

One card per update. Props: `StatusUpdatePublicResponse`.

Layout:
- Top row: **kind badge** (colored pill, see kind → color mapping below) + localized timestamp
  (`<time dateTime={update.publishedAt}>` + relative format, e.g. "3 hours ago").
- Title as `<h3>`.
- Body as `<SafeMarkdown content={update.bodyMarkdown} />`.
- If `linkUrl` is set: "Read more →" `<a>` with `rel="noopener noreferrer"` below the body.

Kind → badge color (CSS variable-based, no inline hex):

| Kind | Badge style |
|---|---|
| investigating | `var(--status-degraded)` / orange |
| identified | `var(--status-degraded)` / orange |
| monitoring | `var(--status-checking)` / blue |
| resolved | `var(--status-up)` / green |
| maintenance | neutral / gray |
| info | neutral / gray |

Use existing CSS variable names from the design reference at
`web/status0/src/routes/<org>/design-reference.tsx` (or the equivalent status0 design tokens).

#### `status-updates-timeline.tsx`

Props: `updates: StatusUpdatePublicResponse[]`.

Grouping logic:
1. Collect all updates with a non-null `incidentUid` and group them by `incidentUid`, sorted
   by `publishedAt ASC` within each thread (oldest first = natural reading order for a thread).
2. Standalone updates (`incidentUid == null`) are individual entries.
3. Merge the two lists sorted by the **latest `publishedAt`** within each item (incident thread =
   latest update's timestamp), descending. This puts the most recently active incident at the top.

Thread rendering: a `<section>` containing a header row ("Incident thread" or the incident UID
abbreviation if no title is available) and then each `<StatusUpdateCard>` in chronological order.

No "load more" UI in v1 unless the pagination endpoint is also implemented.

#### Integration into `status-page-view.tsx`

`web/status0/src/components/shared/status-page-view.tsx` — after the existing sections loop
(currently ends around the resource cards area), add:

```tsx
{data.recentUpdates && data.recentUpdates.length > 0 && (
  <section aria-label="Recent updates">
    <h2>{t('status.recentUpdates')}</h2>
    <StatusUpdatesTimeline updates={data.recentUpdates} />
  </section>
)}
```

Add the `recentUpdates` field to the TypeScript type for the status page API response in
`web/status0/src/api/hooks.ts` (or wherever the response type is defined).

Add an i18n key `status.recentUpdates` (English: "Recent updates") to the existing translation
files used by status0.

## Files to change

### New files

- `web/status0/src/components/shared/status-update-card.tsx`
- `web/status0/src/components/shared/status-updates-timeline.tsx`

### Modified files

- `server/internal/handlers/statuspages/service.go` — add `StatusUpdatePublicResponse` type;
  add `RecentUpdates []StatusUpdatePublicResponse` to `StatusPageResponse`; populate it in the
  view path.
- `server/internal/handlers/statuspages/handler.go` — inject / call the new query in
  `ViewDefaultStatusPage` and `ViewStatusPage`.
- `server/internal/app/server.go` — optional pagination route; inject StatusUpdates dep if needed.
- `web/status0/package.json` — `react-markdown`, `remark-gfm` deps.
- `web/status0/src/api/hooks.ts` — extend status page response type with `recentUpdates`.
- `web/status0/src/components/shared/status-page-view.tsx` — render `StatusUpdatesTimeline`.
- `web/status0/src/locales/*.json` — add `status.recentUpdates` translation key.

## Verification

**Build:**

```bash
cd web/status0 && pnpm install && pnpm build   # no type errors
make build                                       # backend with RecentUpdates populated
```

**XSS smoke tests** (add to `status-update-card.test.tsx` or Playwright):

```
<script>alert(1)</script>          → rendered as plain text, no alert
<img src=x onerror=alert(1)>       → tag stripped entirely (skipHtml)
[click here](javascript:alert(1))  → link rendered as plain text (scheme blocked)
```

**Playwright (status0):**

1. Via API, create a `maintenance` update scoped to the default status page with `publishedAt = now`.
2. Navigate to `http://localhost:4000/status0/default`.
3. Assert: "Recent updates" section visible; the card shows kind badge "Maintenance" and the title.
4. Assert: `<time>` element has correct `datetime` attribute.
5. Assert: if `linkUrl` is set, the "Read more →" link has `rel="noopener noreferrer"`.

**Playwright (incident thread):**

1. Trigger a check failure to create an auto-incident.
2. Via dash0, attach two updates to that incident (kinds: `investigating`, then `resolved`).
3. Navigate to `http://localhost:4000/status0/default`.
4. Assert: both updates appear grouped in a thread, investigating first, resolved second.

**Manual:**

```bash
make dev
# Confirm no "Recent updates" section when no updates exist (section is hidden).
# Create a maintenance update via the API or dash0.
# Reload the status page — section should appear with the update.
```

## Risk log

| Risk | Mitigation |
|---|---|
| `react-markdown` XSS via raw HTML | `skipHtml` prop drops all raw HTML tags at parse time; no `rehype-raw`. |
| `javascript:` URL bypass | Scheme allowlist check in custom `a` renderer; `javascript:` never renders as a clickable link. |
| Embedded payload too large (many updates) | Cap at 100 in the embedded query; add the load-more endpoint if needed. |
| `historyDays = 0` or missing → empty updates | Guard with `historyDays > 0` before querying; no updates shown when page has zero history configured. |
| Grouping by `incidentUid` when incident table is renamed (backlog spec) | `incidentUid` is a FK to the existing `incidents` table; grouping is on the UID string, not the name — transparent to any future rename. |
