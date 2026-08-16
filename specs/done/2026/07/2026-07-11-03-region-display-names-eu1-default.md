# Main region shows as raw "default" instead of a friendly "EU1 (default)" label

## Problem

On the k8xp deployment (`solidping.k8xp.com`), the main server's region is
displayed as the bare string `default` in two places:

1. **Check edit page**
   (`/dash0/orgs/acmetech/checks/$checkUid/edit`) — the Regions
   checkbox list renders `{emoji} {name}` from the org regions API
   (`web/dash0/src/components/shared/check-form.tsx:2674`), so the label comes
   straight from the region definition's `name`. On k8xp the default region's
   entry shows as `default`, where the operator wants **"EU1 (default)"** (the
   main server physically runs in the EU).

2. **Server performance page**
   (`/dash0/orgs/acmetech/server/performance`) — the per-worker lane-load
   card renders the worker's **raw region slug** as a badge:
   `{w.region && <Badge variant="outline">{w.region}</Badge>}`
   (`web/dash0/src/routes/orgs/$org/server.performance.tsx:306`). The backend
   `WorkerLaneLoad.Region` (`server/internal/handlers/system/service.go:512`)
   is the worker row's region string, never resolved against the region
   definitions — so even nicely-named regions display as `us-1`, `eu-2`,
   `default`.

Where the data comes from:

- Region definitions (slug + emoji + name) live in the `regions` **system
  parameter** (`server/internal/regions/regions.go:22-36`); when the parameter
  is unset the fallback is a single `{slug: "default", emoji: "📍", name:
  "Default"}` entry (`DefaultRegions()`, `regions.go:32-36`).
- The main server registers itself with region `default` — `SP_REGION`
  defaults to `"default"` when unset
  (`server/internal/config/config.go:836-846`); k8xp workers set
  `SP_REGION=us-1` / `SP_REGION=eu-2`.
- There is **no env/deployment knob that seeds the `regions` parameter** — it
  is only ever read. The precedent for env-seeded system parameters is
  `SeedSaaSEntitlements` (`server/internal/app/saas.go`), which writes
  system parameters from `SP_ENTITLEMENTS_*` at startup.

## Proposal

Two parts — a display fix that benefits everyone, and a config path so the
deployment can name its regions:

1. **Performance page: render the region's display name, not the slug.**
   In the lane-load card (`server.performance.tsx`), resolve `w.region`
   against the regions list (already available via the org regions hook,
   `web/dash0/src/api/hooks.ts:2454`) and render `{emoji} {name}` in the
   badge, falling back to the raw slug when the slug has no definition
   (workers can report regions that were never defined). Any other admin
   surface that badges a raw worker-region slug should get the same
   treatment.

2. **Make the region names configurable from deployment env.** Add startup
   seeding of the `regions` system parameter from an env var (e.g.
   `SP_REGIONS`, JSON list of `{slug, emoji, name}`), following the
   `SeedSaaSEntitlements` pattern in `server/internal/app/saas.go` — only
   overwriting when the env var is set, so DB edits still win day-to-day.
   k8xp then ships something like:

   ```json
   [
     {"slug": "default", "emoji": "🇪🇺", "name": "EU1 (default)"},
     {"slug": "us-1", "emoji": "🇺🇸", "name": "US1"},
     {"slug": "eu-2", "emoji": "🇪🇺", "name": "EU2"}
   ]
   ```

   With that in place the check edit page shows "EU1 (default)" with no
   frontend change (it already renders `name`), and part 1 makes the
   performance page follow suit.

## Open questions

- What is the current `regions` parameter value on k8xp? If it is simply
  unset, the edit page would show "Default" (capital D) from the fallback —
  the lowercase `default` suggests the parameter was set with `name == slug`.
  If so, the short-term fix is just updating that parameter's `name`; part 2
  makes it declarative in the k8xp deployment repo.
- Should the seeding env var live in this repo's config (`SP_REGIONS`) or
  should we instead expose a small admin UI for editing region definitions?
  Env seeding matches the "it might simply be a deployment env config" intent
  and is the smaller change.

## Implementation Plan

Open questions resolved:

- **Implement both parts.** Whatever the current k8xp parameter value is, part 1
  (display fix) is correct on its own, and part 2 (`SP_REGIONS` seeding) makes
  the k8xp fix declarative in the deployment repo instead of a manual DB edit.
- **Env seeding (`SP_REGIONS`), not an admin UI** — matches the "deployment env
  config" intent and is the smaller change. Malformed JSON logs an error and
  skips seeding (never crashes the boot); unset leaves the DB value alone so
  API/DB edits win day-to-day.

Steps:

1. **Backend seeding** — new `server/internal/app/regions_seed.go` with
   `(*Server).SeedRegionsFromEnv(ctx)`: read `SP_REGIONS` manually via
   `os.Getenv` (same pattern as the `SP_ENTITLEMENTS_*` seeds in `saas.go`),
   parse a JSON list of `{slug, emoji, name}` into
   `[]regions.RegionDefinition`, validate (non-empty list, every entry has a
   slug, no duplicate slugs; an empty `name` falls back to the slug), then
   upsert the `regions` system parameter via `SetSystemParameter`. Invalid
   values log an error and skip (return nil). Wire the call in
   `server/main.go` right after `SeedSaaSEntitlements`. Unit tests
   (`regions_seed_test.go`, in-memory SQLite): unset → no-op (fallback
   `DefaultRegions` intact), valid JSON → `GetGlobalRegions` returns the
   seeded defs, re-seed overwrites an existing parameter, malformed JSON /
   empty list / missing slug / duplicate slug → parameter untouched, empty
   name defaults to slug.
2. **Shared frontend resolver** — `web/dash0/src/lib/region-label.ts`
   exporting `regionDisplayLabel(regions, slug)` → `"{emoji} {name}"` with
   raw-slug fallback, plus a vitest unit test.
3. **Performance page fix** — in `server.performance.tsx`, `LaneLoadCard`
   fetches `useRegions(org)` and renders the worker badge through
   `regionDisplayLabel`; badge gets `data-testid="worker-region-badge"`.
4. **Same treatment on the other admin surfaces that show raw region slugs** —
   jobs list region column (`jobs.index.tsx`), check-job detail region row
   (`jobs.check.$checkJobUid.tsx`), result detail region
   (`checks.$checkUid.results.$resultUid.tsx`); refactor the existing inline
   `emoji + name` resolutions in `checks.$checkUid.index.tsx` onto the shared
   helper so there is one canonical implementation.
5. **Docs** — document `SP_REGIONS` in
   `web/docs/docs/configuration/index.md` (Distributed Workers section).
6. **E2E** — extend the lane-load test in
   `web/dash0/e2e/server-admin.spec.ts` to assert the worker region badge
   shows the resolved label ("📍 Default" with no regions parameter set), not
   the raw `default` slug.
7. **QA** — `make build-backend lint-back test`; `make build-dash0` + dash0
   `bun run lint` (no new errors in touched files); `make build-docs`; run the
   affected E2E file against a test-mode server if available.
