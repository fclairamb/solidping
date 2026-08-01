---
sidebar_position: 3
title: Status Pages
---

# Public Status Pages

SolidPing includes built-in public status pages that let you share service availability with your users, customers, or team members — no authentication required.

## Overview

Status pages provide a real-time view of your monitored services. Each organization can publish multiple pages, and each page displays:

- Current status of each service (up / down / degraded)
- Uptime percentage over a configurable history window
- Per-check response-time history
- Whole check groups published as a single aggregated component, hiding your internal probe topology
- Recent incident history
- Locale-aware date and time formatting

## Structure

A status page is organized into **sections** and **resources**:

- **Sections** group related services (for example, "API", "Database", "Frontend"). Sections are ordered and can be reordered.
- **Resources** are the components inside a section. A resource targets **either one check or one check group** — never both. Each resource can have a public display name and an explanation that override the internal name, so you control exactly what visitors see.

## Group components

A single host is often probed several times over — TCP, HTTP, TLS, RDP. Publishing all four checks tells your visitors far more about your internal monitoring topology than they need to know, and it quadruples the length of your page.

Point a resource at a **check group** instead and it renders as **one** component. Nothing about the group's members reaches the public page: no member names, no member types, not even how many there are.

### Status

The component's status is rolled up from the group's **enabled** member checks:

| Members | Component reads |
|---|---|
| All up | **Up** |
| All down | **Down** |
| Some — but not all — down | **Degraded** |
| None down, at least one warning | **Warning** |
| No members, or none reporting yet | **No data** |

A member in the transient "validating" state still reads up publicly, exactly as a standalone check does — the component only turns red once a failure is confirmed. Disabled members are ignored entirely.

### Availability

Per time bucket (a day, or an hour in the 24h view), availability is the **weighted average** across members:

```
availability = sum(successful checks across members) / sum(total checks across members) × 100
```

This is the same formula a single check already uses, extended across the group — not an average of per-member percentages. So a member probed every 10 seconds carries proportionally more weight in a bucket than one probed every 5 minutes, which is what you want: the number reflects how many probes actually succeeded.

A member with **no data at all** in a bucket contributes nothing to it — it is not counted as a zero. A bucket in which no member reported renders as "no data", exactly like a silent single check.

Group components do not publish a response-time chart. Interleaving several members' latencies into one plot would be meaningless, and it would expose per-member timing — precisely what the group component exists to hide. The availability bar is the group's public performance surface.

### Maintenance

A group component shows the "Scheduled Maintenance" badge when an active maintenance window targets **the group** *or* **any of its member checks**. You can therefore schedule maintenance at whichever granularity is natural and the public page stays correct.

### Setting one up

In the dashboard, open the status page, click **+** on a section, switch the dialog to the **Group** tab and pick the group. An existing component's target can be changed later — including switching it between a check and a group — with the pencil icon on its row.

Via the API, `POST .../sections/{section}/resources` accepts `checkUid` **or** `checkGroupUid` (a UID or a slug for either). Sending both — or neither — is a `VALIDATION_ERROR` naming both fields. `PATCH` on an existing resource accepts the same pair to move it to a different target.

From the CLI:

```bash
sp status-pages resources create public core --check-group web-frontend
```

## Configuration

Status pages are managed per organization from the dashboard (**Settings → Status Pages**) or via the API. Key options per page:

| Option | Description |
|--------|-------------|
| Name / Slug | Page identity and public URL path |
| Description | Optional intro text |
| Enabled | Toggle public visibility |
| Default | Mark one page as the org's default |
| Show Availability | Display overall and per-day uptime percentages |
| Show Response Time | Display per-check response-time history |
| History Days | Size of the lookback window (default 90 days) |
| Language | Locale used for date formatting on the public page |
| Custom CSS | Your own stylesheet, applied to the public page (see below) |

## Custom CSS

Every status page can carry its own stylesheet, so the page matches your brand
instead of SolidPing's. It is a **free** feature — no plan gating — and works
on the default `/status0/{org}/{slug}` URL and on a
[custom domain](./custom-domains.md) alike.

### Editing it

Open the page in the dashboard and choose **Appearance** (also reachable from
the edit screen). The editor is a plain CSS text box beside a **live preview**:
the preview is the real status page in an iframe, restyled as you type, so what
you see is exactly what visitors get. Nothing is published until you press
**Save**; emptying the box and saving removes the stylesheet again.

If the page has no CSS yet, **Insert starter template** drops in a commented
template listing every supported variable.

### CSS variables

The public page paints everything from CSS custom properties, so overriding a
handful of them re-themes the whole page without touching a single selector:

| Variable | What it controls |
|----------|------------------|
| `--brand` | Brand color: logo tint and outbound links |
| `--brand-foreground` | Text/icon color drawn on top of `--brand` |
| `--background` | Page background |
| `--foreground` | Default text color |
| `--card` | Section card background |
| `--card-foreground` | Text inside section cards |
| `--border` | Hairlines, separators and card outlines |
| `--muted` / `--muted-foreground` | Secondary surfaces and secondary text |
| `--status-ok` | "Operational" green: dots, badges, uptime bars |
| `--status-warning` | "Degraded" amber |
| `--status-error` | "Down" red |
| `--radius` | Corner radius used across the page |

Colors accept any CSS color syntax (`#rrggbb`, `rgb()`, `oklch()`, …).

Rules placed inside a `.dark { … }` block apply to visitors whose browser or OS
requests dark mode; rules in `:root { … }` apply to light mode. You are not
limited to variables — any CSS you write is applied to the live page.

### Example

```css
/* Light mode: warm brand, near-white page */
:root {
  --brand: #ff5500;
  --brand-foreground: #ffffff;
  --background: #fdfaf7;
  --card: #ffffff;
  --border: #ece4dc;
}

/* Dark mode: deep neutral background */
.dark {
  --background: #12100e;
  --foreground: #f2ede8;
  --card: #1c1917;
  --border: #2e2a26;
}
```

### Limits

- **Maximum size: 64 KB.** A larger stylesheet is rejected with a
  `VALIDATION_ERROR`.
- **`@import` is not allowed**, anywhere in the stylesheet and in any casing.
  It would let the page pull in further third-party stylesheets that were never
  reviewed; inline the rules you need instead.
- **External `url()` is allowed** — web fonts, background images and other
  assets fetched from your own CDN work normally.

The stylesheet is stored verbatim and rendered as a text node inside a
`<style>` element, so it cannot inject markup or scripts into the page.

## Subscribers

Visitors can subscribe to a status page to be notified of incidents by email:

- **Double opt-in** — a confirmation link is emailed before any updates are sent.
- Subscribers can unsubscribe at any time via a link in every message.
- The subscriber list is admin-only; addresses are redacted in API responses.

## Feeds

Each page also publishes an **Atom feed** (`/feed.xml`) of status updates, so users can follow along in a feed reader or pipe updates into other tools.

## Accessing Status Pages

Status pages are served directly by SolidPing at a dedicated URL path, making them easy to embed or link to from your own website. The default page is reachable at the organization root path, and named pages at their slug.

## Use Cases

- **Customer-facing status**: Show your users the health of your services
- **Internal dashboards**: Give teams visibility into infrastructure status
- **Incident communication**: Automatically reflect incidents on the status page
- **SLA reporting**: Track and display uptime metrics
