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
- Recent incident history
- Locale-aware date and time formatting

## Structure

A status page is organized into **sections** and **resources**:

- **Sections** group related services (for example, "API", "Database", "Frontend"). Sections are ordered and can be reordered.
- **Resources** are individual checks assigned to a section. Each resource can have a public display name and an explanation that override the internal check name, so you control exactly what visitors see.

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
