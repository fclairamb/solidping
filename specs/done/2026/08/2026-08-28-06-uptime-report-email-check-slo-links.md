---
model: sonnet
effort: medium
---

# The uptime (availability) report email names checks and objectives without linking to them, and renders every availability number in the same neutral color

> **Commit type: `fix`, not `feat`.** This patches gaps in the existing report
> email (unlinked names, uncolored figures), it does not introduce a feature —
> commits land as `fix(...)` so the changelog files it under Bug Fixes.

## Problem

The scheduled uptime-report email lists per-check availability and per-objective
attainment as plain text. A reader who spots a bad number ("api-prod: 97.2%",
"Checkout SLO: Breached") has no way to jump to the thing itself — the only link
in the whole digest is the generic "Open dashboard" button, which points at the
bare server base URL (`server/internal/uptimereport/report.go:155`), not even at
the org.

Concretely:

- `CheckRow` (`server/internal/uptimereport/report.go:56`) carries only
  `Name` / `HasData` / `AvailabilityPct` — no URL.
- `SLORow` (`server/internal/uptimereport/report.go:65`) likewise has no URL.
- The template renders both names as bare `{{.Name}}` text
  (`server/internal/email/templates/uptime-report.html:32` for checks,
  `:42` for objectives).

Other notification emails already deep-link: the escalation job builds
`{base}/dash0/orgs/{orgSlug}/checks/{checkUid}`
(`server/internal/jobs/jobtypes/job_escalation_step.go:1476`), and both dash0
routes exist (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`,
`web/dash0/src/routes/orgs/$org/slos.$uid.index.tsx`). The report email is the
odd one out.

Separately, every availability figure in the email renders in the same neutral
ink: the headline metric is a plain `metric-value` (base.html already ships
`is-good` / `is-warn` / `is-bad` state classes at
`server/internal/email/templates/base.html:120-122`, used by the burn-rate
emails), and the per-check percentages are undifferentiated `value` cells. The
rest of the product colors availability everywhere — badge SVGs
(`availabilityColor`, `server/internal/handlers/badges/service.go:817`), status
pages, the email's own SLO state pills — so a check at 91.0% and one at 99.99%
reading identically here is the digest failing at its one job: being scannable.

## Proposal

Render each check name and each objective name in the report as a link to its
dash0 page, and color the availability figures by their value.

1. **View model** (`server/internal/uptimereport/report.go`):
   - Add `URL string \`json:"URL,omitempty"\`` to both `CheckRow` and `SLORow`.
     Mind the file's PascalCase-JSON-tag rule — the view model round-trips
     through the email job's JSON config and the template looks fields up by
     the JSON key, so the tag must be `URL`, not `url` (see the comment block
     at `report.go:36-51`; `TestUptimeReportRendersRealContent` pins this).
   - In `Build`, when `b.cfg.Server.BaseURL` is non-empty, fill:
     - check rows: `{base}/dash0/orgs/{org.Slug}/checks/{check.UID}`
     - SLO rows: `{base}/dash0/orgs/{org.Slug}/slos/{objective.UID}`
     Trim the trailing `/` from the base like `job_escalation_step.go:1476`
     does. When the base URL is empty, leave `URL` empty and the template
     falls back to plain text (same behavior as today).
   - `Build` already receives `org *models.Organization`, so the slug is at
     hand; `sloRows` needs the org (or its slug) threaded through — today it
     only gets `orgUID`.

2. **Template** (`server/internal/email/templates/uptime-report.html`):
   - HTML: wrap the name cell in `<a href="{{.URL}}">…</a>` when `.URL` is set,
     plain `{{.Name}}` otherwise — for both the "Per check" table (line 32) and
     the "Objectives" table (line 42). Match the link styling other templates
     in `server/internal/email/templates/` use so the rows stay readable in
     dark-mode email clients.
   - Plain-text part: append the URL after each row's line when present, e.g.
     `  - api-prod: 97.2%\n    {url}` — keep the current line intact for
     text-only readers.

3. **Availability color** — a gradient over the established scale, computed
   server-side:
   - Add a small pure function in `server/internal/uptimereport` (e.g.
     `availabilityTextColor(pct float64) string` returning `#rrggbb`) that
     interpolates **piecewise-linearly between the product's existing
     threshold anchors**, NOT linearly over 0–100. Everything interesting in
     availability lives between 98 and 100 — a naive red→green ramp renders
     99.0 and 99.9 as the same green. Anchor the stops on the same boundaries
     as the badge scale (`server/internal/handlers/badges/service.go:817`):
     - `>= models.DefaultAvailabilityThresholdUp` (99.9,
       `server/internal/db/models/status_page_settings.go:20`) → full green
     - 99.0 (`DefaultAvailabilityThresholdDegraded`) → amber, blending toward
       green as it approaches 99.9
     - 98.0 → orange, blending toward amber
     - below 98, blend toward full red; clamp to full red at ~95 and under so
       the ramp isn't wasted on values nobody needs to tell apart.
   - **Use dark text-safe anchor colors, not the SVG badge fills.** The badge
     palette (`#4c1`, `server/internal/handlers/badges/svg.go:11`) is a fill
     color and is unreadable as text on the email's white cells. Interpolate
     across the darker family base.html already uses for state text: green
     `#15803d`, amber `#b45309`, red `#b91c1c` (base.html:120-122), with an
     orange stop blended between the last two.
   - Wire it up:
     - `CheckRow`: add `AvailabilityColor string
       \`json:"AvailabilityColor,omitempty"\`` (PascalCase tag — same
       round-trip rule as `URL`), filled only when `HasData`. Render as an
       inline style on the value cell:
       `<span style="color: {{.AvailabilityColor}}; font-weight: 600">…</span>`
       — inline because the value varies continuously (per-value classes can't
       exist) and email clients demand inline styles anyway. html/template's
       CSS context handles escaping; the value is a Go-built hex constant
       shape, so nothing user-controlled reaches the style attribute.
     - Headline metric (`Data`): add `AvailabilityColor` the same way and
       apply it to the `metric-value` span — a continuous color here
       supersedes picking one of the three `is-*` classes.
     - SLO rows: **leave them alone.** They already carry a colored state
       badge (Healthy/At risk/Breached) driven by the objective's own target,
       which is the correct scale for an SLO — an absolute availability
       gradient next to it would disagree with the badge (99.5% is "green-ish"
       on the gradient but Breached against a 99.9% target).
   - Plain-text part unaffected — color is HTML-only.

4. **Tests**:
   - Extend `server/internal/uptimereport/render_test.go` (it already builds a
     formatter with `email.WithBaseURL("https://solidping.example")`) to assert
     the rendered HTML contains the check and SLO hrefs, and that the text part
     carries the URLs.
   - Assert the no-base-URL path renders names without `<a>` (no half-built
     `href=""` links).
   - Table-driven test on `availabilityTextColor`: pin the anchor values
     (100 and 99.9 → `#15803d`, 99.0 → `#b45309`, ≤95 → `#b91c1c`), one
     midpoint per segment to prove interpolation actually moves (e.g. 99.45
     strictly between the amber and green anchors), and monotonicity is worth
     a quick sweep — a higher pct must never render redder.
   - Render test: a check with data carries `style="color: #` in its value
     cell; a no-data check carries none.

Out of scope: changing what `DashboardURL` points at (org-level dashboard vs
base URL) — worth a separate look, not needed for this.
