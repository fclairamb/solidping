---
model: sonnet
effort: high
---

# No way to get alerted when a vendor (GitHub, Cloudflare, OpenAI...) reports an outage

## Problem
Users depend on third-party services they cannot probe themselves. Today the only option is an
HTTP check on a Statuspage `status.json` with a JSON-path assertion, like the Claude API sample
(`server/internal/checkers/checkhttp/samples.go:238-253`, `$.status.indicator eq "none"`).
That approach has four gaps:

- Every value except `none` counts as down. A `minor` incident or a maintenance pages someone.
  `checkhttp` has no path to `StatusWarning`.
- It cannot target one component ("GitHub Actions", not all of GitHub). Big providers almost
  always have a minor issue open somewhere.
- The alert says "assertion failed", not the vendor's incident title.
- There is no catalog. The user has to know the status page URL and its format.

Pulsetic sells this as dependency monitoring over 4,300+ providers, with email-only alerts
(`wiki/competitors/pulsetic.md:56-64`). Our wiki calls it the one capability gap that runs the
wrong way (`wiki/competitors/positioning.md:57`, `comparison/monitor-types.md:34,60-61`).
Uptime Kuma has an open request for it (issue #7688, `pulsetic.md:66-69`). Spec
`2026-05-18-06` already named a "well-known service" catalog as follow-up work.

## Proposal
A new check type `vendor-status`, labelled "Vendor status page". The id avoids `dependency`:
that word already means check-to-check dependencies (`internal/db/models/check_dependency.go`).
It also avoids `statuspage`, which reads like SolidPing's own status pages.

### Checker
1. New package `server/internal/checkers/checkvendorstatus/`, modelled on `checkhealth`:
   - `config/config.go`: `VendorStatusConfig` with
     - `url`: the status page root, e.g. `https://www.githubstatus.com`;
     - `components`: optional list of component ids;
     - `provider`: optional catalog slug, informational only.
     Implement `FromMap`/`GetConfig`/`Validate`.
   - `config/validate.go` `ValidateSpec`: require `https`; strip trailing `/` and any
     `/api/v2/...` suffix; default name `<Provider> status` and slug `vendor-<host>`. Follow
     `checkhealth/config/validate.go:28-62`.
   - `alias.go`, as in `checkhealth/alias.go`.
   - `checker.go`: GET `<url>/api/v2/summary.json` through `checkhttp.HTTPChecker`
     (`ExecuteCapturingBody`, `checkhttp/checker.go:126`, as `checkhealth/checker.go:68`
     does). That keeps the egress guard (`checkerdef.HTTPTransportFor`), tunnels and timeouts
     for free. Parse the Atlassian Statuspage v2 summary format.
   - `samples.go`: one sample, GitHub, whole page.
2. Status mapping:

   | Scope | Up | Warning (shown as degraded, no incident) | Down (opens an incident) |
   |---|---|---|---|
   | Whole page (`components` empty) | `status.indicator` = `none` or `maintenance` | `minor` | `major`, `critical` |
   | Selected components (worst one wins) | `operational`, `under_maintenance` | `degraded_performance` | `partial_outage`, `major_outage` |

   - A selected component id missing from the summary is a `StatusError` naming the id. It must
     not read as "up": the vendor renamed or removed it and the check no longer watches anything.
   - A non-200 answer, or a body that is not the v2 format, is a `StatusError` with a clear
     message ("not a Statuspage v2 summary").
   - The vendor being unreachable is not the same as the vendor reporting an outage, so this is
     never `Down`.
3. Output, following `checkhealth/checker.go:143-153`:
   - `Output[OutputKeyError]` is a one-line message that ends up in notifications, e.g.
     `GitHub: "Incident with Actions" (major) https://stspg.io/xxxx`, taken from the first
     unresolved `incidents[]` entry that touches a selected component (or any, in whole-page
     mode).
   - `components`: `[{id, name, status}]` for every component of the page, not only the selected
     ones. The form reads it (step 6).
   - `incidents`: `[{name, impact, status, shortlink, updatedAt}]` for unresolved incidents.
4. Register the type everywhere `health` is registered:
   - `checkerdef/types.go:267-269` (constant), `:467` (metadata row; `DefaultPeriod: 5 *
     time.Minute`, the pages update slowly and we should be polite), `:587` (list);
   - `registry/registry.go:18,163-164`;
   - `configregistry/configregistry.go:30,158-159,274-275`;
   - the case table in `registry/egress_test.go:78-80`;
   - then `go generate ./internal/checkers/schemas/...` for `schemas/vendor-status.json`.

### Catalog
5. Embed `checkvendorstatus/catalog.json`: `[{slug, name, url, category}]`.
   - Seed it with about 40 providers whose page serves `/api/v2/summary.json`. The implementer
     verifies every URL; drop any that does not answer the v2 format.
   - Candidates: GitHub, Cloudflare, Atlassian (Jira, Confluence, Bitbucket), Anthropic
     (`status.claude.com`), OpenAI, Discord, Twilio, DigitalOcean, Datadog, Vercel, Netlify,
     Sentry, CircleCI, npm, Dropbox, Zoom, Reddit, HashiCorp, Mailgun, SendGrid, Linode,
     Fastly, Render, Fly.io, Supabase, Railway, PlanetScale, Postmark, Intercom, Zendesk,
     HubSpot, Notion, Figma, Linear, GitLab, Docker, Heroku, Coinbase, Shopify, Atlassian
     Statuspage itself.
   - Expose it at `GET /api/v1/check-types/vendor-status/catalog`, next to the samples route
     (`internal/app/server.go:1150`). Answer `{ "data": [...] }`, public like the samples
     route.

### Dashboard
6. New form `web/dash0/src/components/checks/form/types/vendor-status.tsx`, modelled on
   `health.tsx`. Register it in `types/index.ts:104,149`, `shared/check-form.tsx:191`,
   `types/common.ts:63` and the `api/hooks.ts:189,410` unions.
   - A searchable catalog picker that fills `url` and `provider`, plus a "Custom URL" option.
   - A component picker. dash0's CSP forbids fetching the vendor page from the browser, and
     `/checks/validate` only validates config. So add
     `POST /api/v1/orgs/$org/checks/vendor-status/components` with `{url}`. It returns
     `{ "data": [{id, name, group}] }`, goes through the same egress-guarded fetch as the
     checker, needs org membership, and shares the `/checks/validate` rate limit. When editing,
     preselect from the saved config.
   - `data-testid` on the catalog picker, its items, the custom-URL input and the component
     checkboxes.
   - Strings in `src/locales/{en,fr,de,es}/`.
7. The check detail page shows the `incidents` output (title, impact, shortlink) when the
   latest result has any. Reuse an existing output/key-value primitive from the design
   reference page; add none.

### Docs and counts
8. Add `### Vendor status page {#vendor-status}` to `web/docs/docs/features/check-types.md`,
   next to `:585`. `registry/docs_anchor_test.go:91` requires the anchor. Explain the mapping
   table, the 5-minute default, the "unreachable is an error, not an outage" rule, and that
   only the Statuspage v2 format is supported.
9. The count goes from 43 to 44. Update `README.md:8,58` (the list too), `check-types.md:8` and
   `web/docs/docusaurus.config.ts:129`. `TestAdvertisedCheckTypeCountInDocs` and
   `TestReadmeCheckTypeListMatchesCount` (`registry/check_type_count_test.go:82,192`) enforce
   it.
10. Update the wiki claims that call this a gap: `pulsetic.md`, `positioning.md:57`,
    `comparison/monitor-types.md:34,60-61`. Add a `CHANGELOG.md` entry per
    `wiki/conventions/changelog.md`.

## Tests
- `checkvendorstatus/checker_test.go`, table-driven against an `httptest` server serving
  fixture summaries in `testdata/`. Capture one real summary (GitHub) and derive the rest:
  - whole page `none`, `maintenance`: Up; `minor`: Warning; `major`, `critical`: Down;
  - components: worst-wins across 3 selected, `degraded_performance` alone is Warning,
    `partial_outage` is Down, an unselected component in `major_outage` stays Up;
  - a selected id missing from the summary is Error, naming the id;
  - a 404, a 500, HTML, and valid JSON in another shape are each Error, never Down;
  - the error message carries the incident title, impact and shortlink.
- `config/validate_test.go`: `http://` rejected; `/api/v2/summary.json` and trailing `/`
  stripped; default name and slug.
- `registry/egress_test.go`: the new row. A URL resolving to a private address is refused like
  every other HTTP checker.
- Handler tests for the components endpoint: 401 without auth, 403 for a non-member, refusal
  of a private-address URL through the egress guard, and success against an `httptest` server.
- A `slowtests` test (nightly, live network) that fetches every catalog URL and asserts it
  parses as a v2 summary. A vendor that changes format breaks a nightly, not a customer.
- `web/dash0/e2e/`: create a vendor-status check from the catalog with one component selected,
  then reopen it and see the component still checked. Point it at a fixture server: no live
  vendor in E2E.
- The existing count and docs-anchor tests pass at 44.

## To verify
- Whether incident.io- and Instatus-hosted pages serve a Statuspage-compatible
  `/api/v2/summary.json`. If they do, list them in the catalog. If they don't, leave them out
  (see Non-goals).
- Region placement for this type. The answer is the same from every region, so one region is
  enough. Check whether the metadata row can say so, or whether auto-placement
  (`web/docs/docs/features/region-placement.md`) needs a per-type default.
- Whether a `Warning` result shows the output error line on the check detail page, or only on
  `Down`.

## Non-goals
- Other formats: RSS/Atom, AWS Health, Google Cloud `incidents.json`, Azure, Stripe's own page.
  Each is a follow-up parser on the same check type, chosen by usage.
- Copying vendor incident updates into the SolidPing incident timeline.
- Showing vendor status on SolidPing's public status pages.
