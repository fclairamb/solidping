---
model: opus
effort: high
---

# A browser check's screenshots can only be found by opening its incidents one by one

## Problem

A browser check with `screenshot: true` (and a JS check calling `page.screenshot()`)
captures a WebP of the page when a run ends `down` / `timeout`. That capture reaches
exactly one place: `persistScreenshot` (`server/internal/handlers/incidents/service.go:456`)
stores it as an attachment on the incident the run opened or reopened, under the topic
`incidents/<uid>/screenshot` (`server/internal/handlers/attachments/topics.go:57`). The
only UI that shows it is `IncidentScreenshotCard` on the incident detail page
(`web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx:1465`).

The check page (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`) shows no
screenshot, not even a link to one. Seen on 2026-09-25: an operator added a browser
check on a public site to see what the site looks like from the probe, and there was no
way to get that answer from the check. Getting the image took a throwaway clone that
failed on purpose, with its notification channels detached, so it would open an incident
that carried the capture.

Three gaps, in the order they bite:

1. **Not discoverable.** A capture exists but lives one or more clicks away, on an
   incident the operator has to find first. Once a check has had a few incidents, the
   latest capture is buried.
2. **Captures that open no incident are thrown away.** A `down` run inside the
   confirmation period (`status: validating`) produces a capture that no code path
   persists. That covers the first failures of any check with `confirmationPeriodSeconds > 0`,
   and every blip that recovers before the period elapses. That is exactly the "what did
   the page look like during that blip?" question.
3. **A healthy check never produces one.** Capture is gated on a failing verdict
   (`capturableStatus`, `server/internal/checkers/checkbrowser/checker.go:331`), so the
   check page of a site that is up can never show what the probe sees.

## Proposal

### 1. Show the check's latest screenshots on the check page (required)

- New endpoint `GET /api/v1/orgs/{org}/checks/{checkUid}/screenshots?limit=N` returning
  `{ "data": [ { uid, mimeType, size, downloadUrl, capturedAt, region, trigger, incidentUid } ] }`,
  newest first, default `limit` 5. The source is the existing incident attachments
  whose `details.checkUid` matches. `files.details` already carries `checkUid`,
  `capturedAt`, `region` and `trigger`. The incident UID comes from the topic. Add an index
  or query path that does not scan every file in the org, and check the plan on Postgres
  and SQLite (`/sync-pg-to-sqlite`).
- `downloadUrl` is the same short-lived signed `/pub/files/...` URL the incident detail
  returns. Same authorization as reading the check. Operator-only, exactly like incident
  attachments: never exposed on a status page, badge or subscriber payload.
- dash0: a "Screenshots" card on the check overview, visible only for check types
  that can capture (`browser`, `js`). It shows the latest capture as a clickable thumbnail
  that opens it full size, plus `capturedAt` (relative time with absolute time on hover),
  region, and a link to the incident. Older captures sit in a compact strip underneath.
  Empty state: "No screenshot yet. Captures are taken when a run fails," plus a
  link to the edit form's screenshot toggle if it is off. Reuse the primitives from
  `web/dash0/src/routes/orgs/$org/design-reference.tsx`, and share the image and lightbox
  rendering with `IncidentScreenshotCard` rather than forking it. Must work at phone width.
- Locale keys in every shipped locale (`bun run test:unit` catches a missing one).

### 2. Keep captures from runs that open no incident (decide, then implement)

Persist the capture of a failing run that did not open or reopen an incident, under a
check-scoped topic (`checks/<uid>/screenshot`), and include it in the same listing.
Constraints to settle in the implementation plan:

- **Retention.** Keep the latest capture only (replace on write) or the last N per check.
  Captures are up to 4 MiB (`MaxScreenshotBytes`), and a flapping check on a 1-minute
  period must not fill storage. Proposal: latest one only, overwritten.
- **Agent path.** A private agent does not ship the bytes with the result; it is asked to
  upload them (`requestAgentScreenshot`, `incidents/service.go:514`). That handshake is
  wired to incident topics only. A new `checks` topic entity needs its own authorizer in
  the attachment rail (`topics.go` fails closed on an unknown entity).
- **Reaping.** Deleting a check must reap `checks/<uid>/` like deleting an incident reaps
  `incidents/<uid>/`.

### 3. A screenshot of a healthy page (open question, do not implement without a decision)

Two shapes, not mutually exclusive:

- **"Capture now" button** on the Screenshots card: runs the check once on demand, from
  one of its regions, with capture forced regardless of verdict, and stores the result
  under the check topic. Needs a run-now path (none exists today) and rate limiting, since
  a browser run is the most expensive check type.
- **`screenshot: "always"`** as a third config value next to `false` / on-failure: capture
  every run, keep the latest only. It costs a CDP round-trip and a few MiB of memory on every
  run (the reason `screenshot` defaults to off, see `checkbrowser/config/config.go:31`).

Pick one before starting part 3. The on-demand button is the cheaper default.

## Out of scope

- Changing when an incident attaches its screenshot.
- The capture budget bug that drops screenshots of `timeout` runs
  (`2026-09-25-35-browser-screenshot-lost-on-timeout.md`). It is independent of this
  spec, but without that fix, part 1 shows nothing for checks that fail by timing out.

## Tests

- Backend: listing returns captures from several incidents newest first, respects `limit`,
  excludes other checks' captures, returns `{ "data": [] }` for a check with none, and
  refuses a caller who cannot read the check. Postgres and SQLite.
- Part 2: a `down` run inside the confirmation period leaves exactly one check-scoped
  capture, and a second one replaces it. Deleting the check reaps it.
- dash0 Playwright: the card renders the latest capture with its region and incident link
  on a seeded check, the empty state on a check without captures, and nothing for an
  `http` check.

## Resolved open questions

- **Part 3, "A screenshot of a healthy page (open question, do not implement without a
  decision)": which shape?** Decision: implement the **"Capture now" button** only. Do not
  add `screenshot: "always"`. The button runs the check once on demand from one of its
  regions with capture forced regardless of verdict, stores the capture under the
  check-scoped topic, and is rate limited, because a browser run is the most expensive
  check type. Building the run-now path it needs is part of this spec.
- **Part 2, retention of captures from runs that open no incident.** Decision: keep the
  **last 5 captures per check**, not the latest only. When a sixth is written, delete the
  oldest. The storage bound is 5 × `MaxScreenshotBytes` per check. Captures from the
  "Capture now" button share this same check-scoped topic and the same cap of 5. Deleting
  the check reaps all of them.
  This overrides the Tests bullet "a second one replaces it": test instead that six
  check-scoped captures leave exactly five, and that the oldest is the one removed.
