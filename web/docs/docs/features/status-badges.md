---
sidebar_position: 14
title: Status Badges
---

# Status Badges

A status badge is a small SVG image showing one check's live status —
embeddable in a GitHub README, a wiki page, or your own site, the same way
[the status page badge](status-pages.md#badge) reflects a whole page. A badge
belongs to a check, so the builder lives under the check: open a check, then
**Badges** (`/orgs/:org/checks/:check/badges`). Toggle the pieces you want, and
copy the URL, Markdown, or HTML snippet — or download the SVG/PNG directly.

## Components

A badge is built from one or more **components**, joined with commas in the
URL. Two of them render as a row below the others (a bar/graph); the rest
render as text segments on the first row, always in this order:

| Component | Shows |
|---|---|
| `status` | Current up/down status |
| `availability` | Uptime percentage over the selected period |
| `duration` | Time since the last status change |
| `response-time` | Mean response time over the selected period |
| `uptime-bar` | A horizontal strip showing availability per time bucket |
| `response-time-graph` | A response-time trend line |

`status` alone is the default. Combine several, e.g. `status,availability` for
a two-segment badge, or add `uptime-bar` for a second row underneath.

### Hover details

The `uptime-bar` and `response-time-graph` rows are interactive even when the
SVG is embedded as a plain `<img>`: hovering a bar segment highlights it and
shows a tooltip with the bucket's time range and availability percentage
(e.g. `Wed Jan 7: 99.8%` — including buckets too narrow to print the
percentage inside the bar), and hovering the graph shows a vertical highlight
with the bucket's average response time (e.g. `Wed Jan 7 → 304ms`). This works
without JavaScript, so it applies anywhere the badge is embedded, including
GitHub READMEs.

## URL and parameters

```
GET /api/v1/orgs/{org}/checks/{checkIdentifier}/badges/{components}
```

`checkIdentifier` is the check's UID or slug — a value that parses as a UUID
is looked up by UID, anything else by slug; `components` is the
comma-separated token list above. Query parameters:

| Parameter | Values | Default |
|---|---|---|
| `period` | `24h`, `7d`, `30d`, `90d` | `30d` |
| `style` | `flat`, `flat-square` | `flat` |
| `label` | Custom text | The check's name |
| `minWidth` | `0`–`800` (text-row badges only) | `0` (auto) |
| `width` | `60`–`800` (bar/graph badges only) | `300` |

Badges are served with `Cache-Control: public, max-age=60`.

## Embedding

The builder generates ready-to-paste snippets:

```md
![My API badge](https://status.acme.com/api/v1/orgs/acme/checks/my-api/badges/status)
```

```html
<img src="https://status.acme.com/api/v1/orgs/acme/checks/my-api/badges/status" alt="My API badge" />
```

## Visibility

A per-check badge URL is **public and unauthenticated** — anyone who has the
URL can view it, regardless of whether the check appears on any status page
or whether that status page is public. Treat the URL itself as the only
access control: once you publish it (in a README, a public wiki), the check's
name and status history become visible to anyone who finds it.

This is different from [the status page badge](status-pages.md#badge), which
reflects a whole status page and follows that page's own visibility setting.
