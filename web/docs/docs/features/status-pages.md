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

A section's contents are hand-curated by default. It can instead carry a
membership rule — "all checks", or "every check labelled `public=true`" — so
services created later appear on their own; see [Dynamic
sections](#dynamic-sections).

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

## Dynamic sections

By default a section is **hand-curated**: you add each component yourself, and
that is exactly what happens for every section that already exists. The problem
with a hand-curated page is silent: a new service ships, its check is created,
the check goes down — and the page (or the office wallboard reading it) stays
**green**, because nobody remembered to attach it. A board that lies green is
worse than no board.

A section can instead carry a **membership rule**, and SolidPing keeps its
components in sync for you. Two rules are available:

| Rule | Meaning |
|---|---|
| **All checks** | Every check in the organization, now and in the future |
| **By label** | Every check carrying **all** of the given `key=value` labels |

Label matching is AND, and values are exact — a check labelled `env=staging`
does not match `env=prod`. There is no wildcard.

Internal checks are never matched by either rule.

### Recommended: label opt-in

Prefer **By label** with a label you control, such as `public=true`:

```bash
sp checks update payments-api --label public=true
```

This inverts the risk. With **All checks**, every check you create is published
unless you remember to stop it. With a label, a check is private until someone
deliberately adds the label — the publish decision lives on the check, next to
the person who knows whether the service is safe to name.

### On a public page, read this twice

A membership rule on a **public** page means every current **and future**
matching check appears on the public internet, including checks created later.
A scratch check named `pg-primary-eu-west-1.internal` becomes visible the moment
you create it. The dashboard shows a warning whenever you enable a rule on a
public page, with stronger wording for **All checks** — it is there because this
is genuinely easy to get wrong.

Private and password-protected pages carry no such risk, and no warning.

### How it behaves

- **Manual placement wins.** A check you added by hand anywhere on the page is
  never duplicated by a rule. Adding one by hand for a check the rule already
  placed in that section takes the component over, keeping your public name.
  Remove the manual component and the rule adopts the check again on its next
  pass.
- **Automatic components are owned by the rule.** They carry an `auto` badge and
  cannot be deleted, reordered or repositioned individually — the dashboard
  hides those controls and the API answers `409 CONFLICT`, because such an edit
  would be undone on the next pass and a change that reports success then
  silently reverts is worse than a refusal. Change the rule instead. Their
  public name and explanation *are* still editable. They are otherwise ordinary
  components: same status, same uptime history, same behaviour on the public
  page.
- **Order is stable.** Manual components keep their positions and come first;
  automatic ones follow in alphabetical order. Two page loads never shuffle.
- **Removal is immediate.** Remove the label, or delete the check, and the
  component goes away.
- **Sections mix freely.** One page can hold a hand-curated "Core" section and a
  dynamic "Everything else" section.
- **Check groups are untouched.** A rule only ever adds individual checks; a
  group component stays exactly as you configured it.
- **Large rules are capped** at 200 automatic components per section. Above that
  the section shows a stable alphabetical prefix and the dashboard tells you how
  many are hidden — narrow the rule with labels to see the rest.

Two things keep the page honest: a rule is re-applied immediately after any
check is created, updated or deleted, and re-applied again when the page is
viewed, so a component can never quietly go missing.

### Via the API

`selector` is a field on a section:

```bash
# Every check labelled public=true
curl -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Services","slug":"services","selector":{"labels":{"public":"true"}}}' \
  'https://solidping.io/api/v1/orgs/acme/status-pages/public/sections'

# Every check in the organization
curl -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"selector":{"all":true}}' \
  'https://solidping.io/api/v1/orgs/acme/status-pages/public/sections/services'

# Back to hand-curated (removes the components the rule owned)
curl -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"selector":null}' \
  'https://solidping.io/api/v1/orgs/acme/status-pages/public/sections/services'
```

Omitting `selector` on a `PATCH` leaves the rule alone; sending `null` clears it.
Unknown keys are rejected, and so are `{}`, an empty `labels` object, and setting
both `all` and `labels` — a rule that quietly matches nothing forever is the
failure this feature exists to remove, so a typo is a `VALIDATION_ERROR` rather
than an empty section.

Automatic components are marked `managedBySelector: true` in the API response.

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
| Visibility | `Public`, `Private` (hidden entirely) or `Password protected` (see below) |
| Logo / Favicon | Your own brand assets, replacing the SolidPing mark (see below) |
| Hide "Powered by SolidPing" | White-label the footer, when your plan includes it (see below) |
| Custom CSS | Your own stylesheet, applied to the public page (see below) |
| Auto-publish incidents | Turn monitoring incidents into public incidents automatically (see below) |
| Publication delay | Debounce before an incident goes public (default 60 s) |
| When the incident resolves | `always` / `if_untouched` / `never` |

## Branding

A status page can wear your brand rather than ours.

### Logo and favicon

Upload them from **Status page → Edit → Branding**:

- **Logo** — PNG, JPEG, WebP, GIF or SVG, up to 1 MB. When set, it *replaces*
  the SolidPing mark in the page header rather than sitting next to it.
- **Favicon** — ICO, PNG, WebP, GIF or SVG, up to 256 KB. It applies on your
  custom domain too, not just on the SolidPing-hosted URL.

Both are served from a stable public URL you can paste anywhere. That URL stops
working the moment the asset is **replaced**, **removed**, or the page is
**deleted**.

:::caution Disabling a page does not hide its logo

Turning a page off — or setting it to **private** or **password** — takes the
*page* offline, but the logo and favicon URLs keep resolving. They are stable,
unguessable links to an image, not to anything about your monitoring, and on a
password-protected page the logo is what visitors see above the prompt anyway.

To take an uploaded image offline, **clear it** (or replace it). Disabling the
page is not enough.

:::

The `sp-logo` CSS hook still applies to an uploaded logo, so existing custom CSS
keeps working.

### White label

Turning on **Hide "Powered by SolidPing"** removes the footer link from the
public page. It takes effect when your plan includes white labelling — on a
self-hosted instance that is always, and on SolidPing Cloud it comes with a paid
plan. The setting is stored either way, so upgrading applies it immediately with
nothing to re-tick. The version line stays: it is information about the
instance, not an advertisement, and support answers depend on it.

## Private and password-protected pages

Visibility has three settings:

| Setting | What visitors see |
|---|---|
| **Public** | Anyone with the link sees the page. |
| **Private** | The page is hidden entirely — its public URLs answer exactly as if the page did not exist. |
| **Password protected** | The page asks for a password, then shows normally. |

**Password protected** is the one to use for a status page you share with
customers but not with the internet. Set a password (at least 6 characters) and
send it to the people who should see the page.

A visitor who opens the page gets a single password field. Once they enter it,
their browser holds an unlock for 12 hours — on that hostname only, so an unlock
obtained on your custom domain never travels anywhere else. Everything the page
exposes is gated behind it: the page itself, the incident history, the Atom feed,
the summary endpoint, the badge, and the subscribe form.

Two things worth knowing:

- **Changing the password signs everyone out immediately.** There is no separate
  revocation step; the previous password's unlocks simply stop working.
- **People already subscribed keep receiving notifications.** Locking a page down
  gates who can *look* at it, never who has already opted into updates.

Wrong-password attempts are rate-limited, so the password does not have to be
long enough to survive a brute-force script on its own.

## TV mode

Any status page can be put on an office television by adding `/tv` to its URL:
a single non-scrolling view with big type, one dominant state signal, a live
uptime number and ticking incident durations. Nothing to configure — it reuses
this page's curation, branding and thresholds.

A non-public page needs a **kiosk token** so an unattended screen can render it
(the 12-hour password unlock cannot survive a night; a private page 404s). See
[TV Mode](status-page-tv-mode.md).

Combine it with a [dynamic section](#dynamic-sections) for a wallboard nobody
has to maintain: a **private** page, a section set to **All checks**, and a
kiosk token on the television. Every check the team creates is on the screen
from the moment it exists, and because the page is private there is nothing to
disclose — which is what makes **All checks** the right rule here and the wrong
one on a public page.

## Caching

A **public** page is served with `Cache-Control: public, max-age=60` (the Atom
feed gets 300 s), so browsers, CDNs and corporate proxies can absorb the traffic
spike that arrives exactly when your infrastructure is already having a bad day.
A status change still surfaces within a minute.

A **private** or **password-protected** page is served with `Cache-Control:
private, no-store` instead, on every one of its public URLs — the page, the
summary, the badge, the incident history, the feed — so no shared cache
anywhere retains its body.
That stays true after a visitor unlocks it: the unlock belongs to that visitor,
not to the proxy in front of them. The "not found" answers are `no-store` too,
so a cache cannot be probed for which pages exist.

## Incidents

A status page shows two different things, and it is worth keeping them apart:

- the **component grid** — a dot per check or group, flipping green/amber/red
  on its own;
- **incidents** — the narrative. A title, a state, and an append-only list of
  updates explaining what is going on.

Before this feature, only the grid was automatic: a check going down turned a
dot red and said nothing else. If nobody was awake to write an update, visitors
saw a red dot with no explanation. Incidents fix that.

### Automatic publication

When `Auto-publish incidents` is on, an incident that opens on a check
displayed by the page becomes a public incident, with a templated title built
from the component's **public name** and a first "investigating" update.

Four things stop a publication:

| Situation | Behaviour |
|---|---|
| The incident resolves inside the publication delay | Nothing is ever published — a short blip stays private |
| The incident is rolled up under a parent | The parent (or group) incident publishes; members never do |
| The check is inside a maintenance window | Planned work is not an incident |
| The page — or that one component — has auto-publish off | Nothing is published |

The per-component override is three-state: leaving it unset means "inherit the
page", which is not the same as switching it off. So you can turn a page on
without silently opting in a component you deliberately excluded — and vice
versa.

:::info Existing pages are not opted in
Pages that existed before this feature shipped keep `auto-publish` **off**.
Nobody's internal blips become public because they upgraded. Pages created
since default to **on**.
:::

### What customers see — and what they never see

Public incident copy is built from exactly one incident-derived value: the
**public display name** of the affected component (falling back to the check
name, which is the same fallback the component list already uses).

Probe output, error strings, response bodies and internal hostnames are
**never** written into a public field. This is a structural guarantee, not a
convention — the publication table has no column any of it could land in, and
the templates interpolate only that one name.

### Resolving

When the underlying incident recovers, what happens depends on the page's
`When the incident resolves` setting and on whether a human has touched the
public incident:

| Setting | Nobody edited it | Somebody edited it |
|---|---|---|
| `if_untouched` (default) | Resolved automatically, with a "resolved" update | A "component has recovered" note is posted; **you** close it |
| `always` | Resolved automatically | Resolved automatically |
| `never` | Nothing posted, nothing resolved | Nothing posted, nothing resolved |

The default exists because the moment you edit a public incident, its narrative
is yours. Auto-resolving under you would overwrite a deliberate editorial
decision with a machine's opinion.

If the check relapses shortly after recovering, the **same** incident is
reopened rather than a second one being created — a flapping service reads as
one incident with a history, not five.

### Writing them by hand

You do not need an underlying monitoring incident at all. From
**Status Pages → (page) → Incidents** you can publish a hand-written incident
with its own title, severity badge (`minor` / `major` / `critical`) and
updates. You can also publish an existing incident onto a page from the
incident's own detail view, and take it down again later.

Updates are **append-only**: there is no edit and no delete. A posted update is
a promise, not a draft — correcting one means posting the correction.

### API

| Endpoint | Purpose |
|---|---|
| `GET /api/v1/orgs/{org}/status-pages/{page}/incidents` | List publications |
| `POST /api/v1/orgs/{org}/status-pages/{page}/incidents` | Publish a hand-written incident |
| `GET`/`PATCH` `…/incidents/{uid}` | Read / edit title, severity, state |
| `POST …/incidents/{uid}/updates` | Append an update |
| `GET`/`POST` `/api/v1/orgs/{org}/incidents/{uid}/publications` | List / publish from the incident side |
| `DELETE …/publications/{uid}` | Unpublish |
| `GET /api/v1/status-pages/{org}/{slug}/incidents` | **Public** incident history (`?active=true` for open ones) |

The public page payload also gains an `activeIncidents[]` array, so a single
request renders the banner, the components and the incidents together.

### Webhooks

Publication lifecycle fires its own events on your `webhook` connections,
separate from the internal incident events:

| Event | When |
|---|---|
| `statuspage.incident.published` | An incident became visible on a page |
| `statuspage.incident.updated` | Title, severity, state or narrative changed |
| `statuspage.incident.resolved` | The public incident was closed or unpublished |

`incident.created` / `incident.resolved` are unchanged and still describe the
internal incident. The two are deliberately distinct: they happen at different
times (the publication delay sits between them), and most incidents never
produce a public one at all.

### Subscriber storm cap

Each public incident may trigger at most **4 subscriber email waves per hour**
(org parameter `status_page.publication_notify_cap`). Beyond that, updates are
still posted to the page and the feed — only the mail stops. A flapping group
incident must not fill anyone's inbox.

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

Rules placed inside a `.dark { … }` block apply when the page is in dark mode;
rules in `:root { … }` apply to light mode. A visitor lands in dark mode
either because they explicitly picked it with the sun/moon toggle in the page
header (their choice is remembered on that browser) or, absent a stored
choice, because their OS/browser requests it. Either way SolidPing adds a
`dark` class to `<html>` before the page paints, so `.dark { … }` overrides
apply consistently regardless of which of the two triggered it. You are not
limited to variables — any CSS you write is applied to the live page.

### Element hooks

Variables re-theme the page; the `sp-*` classes let you retarget individual
elements — replace the logo, hide the version, white-label the footer. They are
a **stable, supported API**: unlike the utility classes you may see in the
generated markup, these will not change under you.

| Class | Element |
|-------|---------|
| `sp-logo` | Header logo wrapper (the `<img>` sits inside it) |
| `sp-page-name` | Status page name shown next to the logo |
| `sp-page-title` | Page heading (`<h1>`) at the top of the body |
| `sp-page-description` | Page description under the heading |
| `sp-status-banner` | Overall-status banner strip below the heading |
| `sp-footer` | Footer container |
| `sp-powered-by` | "Powered by SolidPing" outbound link |
| `sp-version` | Version line (`v1.2.3`) |

The page also carries a `dark` class on its `<html>` ancestor whenever the
visitor is in dark mode (see [CSS variables](#css-variables) above) — you can
target it directly, e.g. `.dark .sp-logo img { content: url(...); }` for a
logo variant with better contrast on dark backgrounds. Most custom CSS never
needs this: an override written against the `--*` variables (`--brand`,
`--card`, `--status-ok`, …) already applies correctly in both modes, since the
tokens themselves swap value inside `.dark`. Reach for `.dark <selector>` only
when an override needs to differ structurally between modes — not just in
color — such as swapping an image asset.

#### Replacing the logo

The logo is a plain `<img>` inside `.sp-logo`, and its size comes from CSS (not
from an inline style), so both of these work without any upload:

```css
/* Simplest — swap the image the <img> paints (Chrome, Edge, Safari). */
.sp-logo img {
  content: url("https://cdn.example.com/logo.svg");
}
```

```css
/* Widest browser support — hide the <img>, paint the wrapper instead. */
.sp-logo img {
  display: none;
}

.sp-logo {
  background: url("https://cdn.example.com/logo.svg") center / contain no-repeat;
  width: 120px;
  height: 32px;
}
```

A non-square logo also just needs its own box:

```css
.sp-logo img {
  content: url("https://cdn.example.com/wordmark.svg");
  width: 140px;
  height: 32px;
}
```

The image must be reachable over HTTPS from your own host or CDN — `url()` is
allowed, `@import` is not.

#### Hiding the version and the credit

```css
.sp-version {
  display: none;
}

/* Fully white-label footer */
.sp-powered-by {
  display: none;
}
```

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

A status page can notify people and systems on three channels: **email**,
**webhook** and **Slack**.

### Email — self-serve

Visitors subscribe themselves from the page:

- **Double opt-in** — a confirmation link is emailed before any updates are sent.
- Subscribers can unsubscribe at any time via a link in every message.
- The subscriber list is admin-only; addresses are redacted in API responses.

### Webhook and Slack — you set these up

Webhook and Slack deliveries are registered by *you*, from **Status page → Edit
→ Subscribers**, not by visitors. That is deliberate: an incoming-webhook URL is
a credential, and there is no way to check that a visitor pasting one actually
owns the channel it posts to. The public subscribe form says so, so nobody goes
looking for a control that is not there.

Add one by choosing the channel and pasting the delivery URL:

- **Slack** — an [incoming webhook](https://api.slack.com/messaging/webhooks)
  URL (`https://hooks.slack.com/services/…`). Updates arrive as a Slack message.
- **Webhook** — any `https` endpoint of yours. It receives a JSON POST:

  ```json
  {
    "event": "status_update.published",
    "statusPageUid": "…",
    "pageName": "Acme Status",
    "incidentUid": "…",
    "kind": "info",
    "title": "Database degraded",
    "bodyMarkdown": "We are investigating.",
    "publishedAt": "2026-08-21T09:00:00Z"
  }
  ```

  The payload carries only what the public page already shows — never probe
  output, check identifiers or internal hostnames.

**Signed deliveries.** Every webhook POST carries an HMAC-SHA256 signature so
your receiver can prove it came from SolidPing:

| Header | Value |
|---|---|
| `X-SP-Signature` | `v1,<base64 HMAC>` |
| `X-SP-Timestamp` | Unix seconds, part of the signed string |
| `X-SP-Key-Id` | `status-page-subscriber` |

The signed string is `<timestamp>.POST.<path>.<sha256 of the raw body, hex>`.

You can paste your own signing secret when adding the subscription — useful if
your receiver already verifies a secret you control. Leave the field empty and
SolidPing generates one and **shows it to you once, right after you add the
delivery**. Copy it then: it is stored encrypted and is never displayed again,
so losing it means removing the subscription and adding it back.

**Your URL stays secret.** It is stored encrypted, and the dashboard and API only
ever show a masked form of it — enough to tell two webhooks apart, useless to
anyone who gets hold of a response.

**Broken endpoints stop being retried.** After five consecutive failed
deliveries the subscription is disabled, and an audit event records that it was
and why. One successful delivery resets the counter, so a receiver that blips
during a deploy is never disabled.

## Feeds

Each page also publishes an **Atom feed** (`/feed.xml`) of status updates, so users can follow along in a feed reader or pipe updates into other tools.

## Summary endpoint

For integrators who just want "is this service up right now?" without the full page payload, `GET /api/v1/status-pages/{org}/{slug}/summary` returns a lightweight JSON rollup:

```json
{
  "status": "operational",
  "counts": { "operational": 12, "degraded": 1, "down": 0, "maintenance": 0, "unknown": 0 },
  "page": { "name": "SolidPing", "slug": "main", "url": "https://status.example.com/" },
  "generatedAt": "2026-08-08T12:00:00Z"
}
```

It's public (no authentication), caches like the page it summarizes (see [Caching](#caching)), and computes `status`/`counts` from the exact same server-side rollup as the full page view — so the two can never disagree.

## Badge

`GET /api/v1/status-pages/{org}/{slug}/badge` returns an SVG badge showing the page's overall status — the static, script-free counterpart to the JS embed widget, for places scripts can't run (a GitHub README, a wiki, an email footer):

```markdown
![Status](https://your-solidping-instance/api/v1/status-pages/default/main/badge)
```

It's public, caches like the page it reflects (see [Caching](#caching)), and applies the same visibility gate and rollup as the summary endpoint above, so the badge can never disagree with the status page. Colors follow the rollup status: green (operational), yellow (degraded), red (down), blue (maintenance), gray (unknown). Customize with `label`, `style` (`flat` or `flat-square`), `minWidth`, and `width` query parameters, matching the per-check badges.

## Embeddable Live Widget

`GET /embed/v1/widget.js` serves a small, self-contained script that renders a live status pill on **your own** site — the "⊙ All systems operational" badge that links back to your status page:

```html
<script async src="https://your-solidping-instance/embed/v1/widget.js" data-page="default/main"></script>
```

The pill renders where the tag sits, in a shadow root, so your site's CSS can neither break it nor be affected by it. It polls the [summary endpoint](#summary-endpoint) every 60 seconds with an uncredentialed request, and if that request fails — or the page doesn't exist, or is private — it renders **nothing at all**, never an error state on your site.

Customization is entirely by data-attribute:

| Attribute | Values | Default |
|---|---|---|
| `data-page` | `org/slug` — required | — |
| `data-mode` | `inline`, `floating` | `inline` |
| `data-position` | `bottom-right`, `bottom-left` (floating only) | `bottom-right` |
| `data-theme` | `light`, `dark`, `auto` (follows `prefers-color-scheme`) | `auto` |
| `data-size` | `sm`, `md`, `lg` | `md` |
| `data-label-operational`<br/>`data-label-degraded`<br/>`data-label-down`<br/>`data-label-maintenance`<br/>`data-label-unknown` | any text | built-in English labels |
| `data-force-status` | `operational`, `degraded`, `down`, `maintenance`, `unknown` | — (normal polling) |

`data-force-status` skips polling entirely and renders that status statically, with no link — mainly useful for previewing the widget (the dashboard's snippet generator uses it) or for a demo/staging page that isn't backed by a real status page yet. An unrecognized value is ignored and normal polling resumes.

Everything under `/embed/v1/` is a **frozen contract**: once you've pasted the snippet it will keep working, and any future behavior change ships under `/embed/v2/` instead. The script is served with `Cache-Control: public, max-age=3600`.

The dashboard generates the snippet for you under **Status Pages → (your page) → Appearance**.

## Accessing Status Pages

Status pages are served directly by SolidPing at a dedicated URL path, making them easy to embed or link to from your own website. The default page is reachable at the organization root path, and named pages at their slug.

## Use Cases

- **Customer-facing status**: Show your users the health of your services
- **Internal dashboards**: Give teams visibility into infrastructure status
- **Incident communication**: Automatically reflect incidents on the status page
- **SLA reporting**: Track and display uptime metrics
