---
sidebar_position: 14
title: Status Badges
---

# Status Badges

A status badge is a small SVG image showing one check's live status —
embeddable in a GitHub README, a wiki page, or your own site, the same way
[the status page badge](status-pages.md#badge) reflects a whole page. Build
one under **Badges** in the dashboard (`/orgs/:org/badges`): pick a check,
toggle the pieces you want, and copy the URL, Markdown, or HTML snippet — or
download the SVG/PNG directly.

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
