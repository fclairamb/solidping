---
model: opus
effort: xhigh
---

# Org rename breaks private-region routing: stop embedding the org slug in private-region strings

## Problem

Fully-qualified private-region strings (`@<org-slug>/<region-slug>`) are built
from the org slug **at write time** (`regions.PrivateRegionSlug`,
`server/internal/regions/regions.go:69`) and stored denormalized in several
places:

- `agents.region` — set when the enrollment token is minted
  (`server/internal/handlers/agents/service.go:292`) and frozen at enrollment;
- `checks.regions` — the per-check region array;
- `check_jobs` — the dispatch rows derived from checks;
- the org `default_regions` parameter (when it references a private region);
- historical `results.region` rows.

Private regions match **only on exact string equality** — deliberately, as a
security boundary (`MatchesRegion`, `server/internal/regions/regions.go:273`;
`regionTargeted`, `server/internal/handlers/agents/service.go:633`).

The org rename flow (`server/internal/handlers/auth/org_profile.go` +
`organization_previous_slugs`, spec 2026-08-08-11) only handles URL/API
redirects. It never rewrites the denormalized region strings — and it
shouldn't have to. After renaming `acmetech` → `acme`:

- the agent stays bound to `@acmetech/aws-paris`;
- pre-rename checks keep `@acmetech/aws-paris` and continue to work (both
  sides are stale, so they still match each other);
- the regions API and dashboard now advertise the canonical
  `@acme/aws-paris`, so **every new check targets a region string no agent
  is bound to** and sits in `validating` forever, with no error surfaced
  anywhere.

Observed live on solidping.k8xp.com org `acme`: check
`http-dbbat-tools-acme-io` pinned to `@acme/aws-paris` was never picked up
while the `@acmetech/aws-paris` agent polled healthily the whole time.
(Worked around by patching the check to the stale spelling; the migration
below normalizes that away.)

## Proposal

**Make the org implicit.** The org slug has no business inside the stored
region identifier: every row that carries a private region already carries an
`organization_uid`. Store private regions **org-relatively** as
`@<region-slug>` (e.g. `@aws-paris`), and an org rename touches nothing at
all — no rewrite job, no denormalized copies to chase, nothing to forget.

Rejected alternative: keep `@<org>/<region>` and rewrite every copy inside the
rename transaction. This chases denormalized state across five-plus locations
forever, and every future feature that stores a region string inherits the
bug. Deriving identity beats repairing it.

### Storage & matching

1. Change the stored form to `@<region-slug>` everywhere: `agents.region`,
   `checks.regions`, `check_jobs`, the org `default_regions` parameter,
   pending enrollment tokens, and new `results.region` rows. The reserved `@`
   prefix keeps the existing structural guarantee intact: cloud/in-process
   workers still can't carry it (`ValidateWorkerRegion`,
   `server/internal/regions/regions.go:242`) and private matching stays exact
   (`MatchesRegion`).
2. **Org scoping becomes load-bearing.** With the org slug gone from the
   string, `@aws-paris` is no longer globally unique — two orgs can both
   define it. Audit every claim/dispatch path that matches private regions
   (`regionTargeted` in `server/internal/handlers/agents/service.go:633`, the
   agent WS backend, `checkworker/checkjobsvc` — note its local
   `privateRegionPrefix` const at `service.go:350` — and `ResealRegion`,
   `server/internal/handlers/agents/service.go:535`) and ensure each one
   filters by `organization_uid` **in addition to** the region string. Any
   path that today relies on the embedded org slug for cross-org isolation
   must gain an explicit org-UID predicate.
3. `PrivateRegionSlug` / `ParsePrivateRegion` in `internal/regions` shrink to
   the org-less form; remove the org parameter so no future caller can
   reintroduce the coupling.

### API surface

The org is already implied by the URL (`/orgs/:org/...`). Expose the
org-relative form (`@aws-paris`) in the regions API, check payloads, and agent
listings, and update dash0 accordingly (the dashboard treats region slugs as
opaque values, so this should be mostly transparent). For backward
compatibility, accept the legacy `@<org>/<slug>` spelling on input: normalize
it to `@<slug>` when `<org>` matches the current org (current slug or a
recorded previous slug), reject it otherwise.

### Migration

One-time, both backends (`internal/db/postgres`, `internal/db/sqlite`):
rewrite every stored `@<anything>/<slug>` to `@<slug>` in `agents.region`,
`checks.regions` (dedupe the array afterwards — a check that listed both the
old and new spelling of the same region, e.g. `rabbitmq-k8s-prod`, must end up
with exactly one entry), `check_jobs`, the `default_regions` parameter, and
enrollment tokens. Rewrite `results.region` in the same migration so
per-region aggregation series stay continuous; if the table is too large for a
synchronous migration, do it batched but do decide explicitly — don't ignore.

This migration also **retroactively repairs** installs already broken by a
past rename (the incident above), since both spellings collapse to the same
org-relative form.

Sealing: private-region check configs are sealed per region (`ResealRegion`).
Verify whether the region string participates in the seal (AAD or key
derivation); if it does, reseal every private region after the migration. If
it does not, state so in the implementation notes.

### Tests

- **Headline regression:** rename an org with a private region; an agent
  enrolled pre-rename picks up a check created post-rename, with zero rows
  rewritten by the rename itself.
- **Cross-org isolation (security):** two orgs each define private region
  `@aws-paris`; org A's agent must never receive, claim, or unseal org B's
  jobs, and `ResealRegion` on org A must not touch org B's checks.
- Migration collapses `@old/aws-paris` and `@new/aws-paris` on the same check
  to a single `@aws-paris` entry, and a previously-stranded check gets picked
  up afterwards.
- Legacy `@<org>/<slug>` input: accepted and normalized for the org's own
  current and previous slugs, rejected for a foreign org slug.
- Cloud worker with region `@anything` is still rejected
  (`ValidateWorkerRegion`), and a cloud worker can still never match a
  private-region job.
