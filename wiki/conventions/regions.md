# Region Naming Conventions

## Overview

Regions use a hierarchical format to enable flexible matching at different geographic levels.

## Format

```
$continent-$region-$city
```

Each component should use lowercase identifiers (e.g., `eu`, `fr`, `idf`, `paris`).

## Matching Patterns

The hierarchical structure supports wildcard matching at any level:

| Pattern | Matches |
|---------|---------|
| `$continent-*` | All locations in a continent |
| `$continent-$region-*` | All locations in a region |
| `$continent-$region-$city` | Specific city |

## Examples

| Region ID | Description |
|-----------|-------------|
| `eu-west-paris-1` | Paris, Île-de-France, France, Europe |
| `eu-west-munich-1` | Munich, Bavaria, Germany, Europe |
| `us-west-sf-1` | San Francisco, California, USA, North America |
| `as-jp-tokyo-1` | Tokyo, Japan, Asia |

### Matching Examples

- `eu-*` matches all European locations
- `eu-fr-*` matches all French locations
- `eu-fr-idf-*` matches all Île-de-France locations
- `eu-fr-idf-paris` matches only Paris

---

# Private regions (`@<slug>`)

A **private region** is an org-scoped location served by deported agents
(see [`features/deported-agents.md`](../features/deported-agents.md)). It is
written with the reserved `@` prefix and is stored **org-relatively**:

```
@aws-paris        ← the stored form, everywhere
```

**The org slug is NOT part of the region string.** It used to be
(`@<org-slug>/<region-slug>`, up to v0.10) and that was a bug: the org slug was
baked in at *write* time into five denormalized places
(`agents.region`, `checks.regions`, `check_jobs.region`, the org
`default_regions` parameter, `agent_enrollment_tokens.region`, plus historical
`results.region`), while private matching is exact-equality only. Renaming an
org therefore left every one of those copies stale, and the regions API started
advertising a *new* string no agent was bound to — so every check created after
the rename sat in `validating` forever, silently. Observed live on a
production deployment after `acmetech` → `acme` (spec
`2026-08-13-01-org-rename-private-region-routing`).

Deriving identity beats repairing it: every row that carries a region already
carries an `organization_uid`, so the org is implicit and a rename is a pure
metadata change that touches no region string at all.

## The uniqueness question (read this before touching any matching path)

Dropping the org slug means `@aws-paris` is **no longer globally unique** — two
orgs can each define it. Anything that resolves a private region to rows MUST
therefore carry an explicit `organization_uid` predicate. Below is the full
audit of every path that matches a private region, done as part of the change.

| # | Path | File | Org predicate |
|---|---|---|---|
| 1 | Deported-agent job claim | `checkjobsvc.AgentScope.apply` → `ClaimJobsForAgent` | `organization_uid = scope.OrgUID`, and `AgentScope.validate` fails closed on an org-less non-system scope |
| 2 | Agent result submission | `agentws.agentOwnsJob` | `job.OrganizationUID == agent.OrgUID()` for org agents |
| 3 | Agent claim scope construction | `agentws.agentClaimScope` | carries `agent.OrgUID()` |
| 4 | Reseal after enroll/revoke | `agents.Service.ResealRegion(ctx, orgUID, region)` | lists checks with `ListChecks(ctx, orgUID, …)`; `regionTargeted` only compares strings *within* that org's checks |
| 5 | Recipient lookup for sealing | `db.ListActiveAgentsByRegion(ctx, orgUID, region)` | `organization_uid = ?` in both dialects |
| 6 | Private-region agent count / delete guard | `agents.Service.ListPrivateRegions` / `DeletePrivateRegion` | same, via #5 |
| 7 | SSH-tunnel region re-assertion | `agentws.buildTunnelBlock` | `GetCheck(ctx, job.OrganizationUID, …)` |
| 8 | Aggregation per region | `jobtypes.job_aggregation` | every query takes `orgUID` |
| 9 | Results read filters (`?region=`) | `handlers/results` | org comes from the `/orgs/:org/…` path; the filter is ALSO normalized like any other input (legacy `@<org>/<slug>` folded for this org's current/previous slug, `400` for a foreign one) so a pre-rename bookmark does not silently return an empty series |
| 10 | **Cloud** worker claim | `checkjobsvc.ClaimJobs` / `ClaimJobsForCheck` (`cloudScope`) | **no org predicate — by design**, a cloud region is shared across orgs. Safe because it now *explicitly* excludes `region LIKE '@%'` in SQL, on top of `ValidateWorkerRegion` refusing an `@` worker region and `MatchesRegion` being exact-only for private regions |
| 11 | System (platform) agent claim | `AgentScope{System: true}` | no org predicate, and `validate()` **rejects** a system scope pointed at an `@…` region outright |

Conclusion of the audit: the isolation was *already* carried by
`organization_uid` on every private path — the org slug inside the string was
decorative, never the enforcement. The one place where the string was the only
guard was the cloud claim path (#10), which relied on "a cloud worker's region
can never start with `@`" plus the possibility of a `nil` worker region
disabling the region predicate entirely; that is now an explicit
`region NOT LIKE '@%'` predicate in the SQL, so the cloud lane can never see a
private-region job even with an unconfigured region.

## Helpers

`internal/regions` deliberately exposes **no org parameter**:

```go
regions.PrivateRegionSlug("aws-paris")        // "@aws-paris"
regions.ParsePrivateRegion("@aws-paris")      // "aws-paris", true
regions.ParseLegacyPrivateRegion("@o/p")      // "o", "p", true   (input compat only)
```

Removing the parameter rather than ignoring it is the point: it makes the old
mistake unrepresentable.

## Backward-compatible input

`@<org>/<slug>` is still **accepted on input** — check `regions` arrays *and*
the `?region=` read filters on `/results` — so old API clients, saved exports and
bookmarked or shared links keep working:

- normalized to `@<slug>` when `<org>` is the org's **current slug or one of its
  recorded previous slugs** (`organization_previous_slugs`);
- **rejected** (`ErrForeignPrivateRegion`) when `<org>` names any other org.

That rejection is a security property, not a nicety — accepting a foreign
spelling and silently stripping it would let org A name org B's region.
Normalization also de-duplicates, so posting both `@old/paris` and `@paris`
yields exactly one `@paris`.

## The org-relative migration (part 2 of `011_v0_14_0`)

Authored as the scratch migration `012_private_region_org_relative` and folded
into the consolidated v0.14.0 release migration at consolidation time, per
[database.md](database.md) — it is PART 2 of `011_v0_14_0.up.sql` in both
dialects. It is a **data** migration, not DDL, so it must survive every future
consolidation: dropping it as "the fresh-database schema doesn't need it" would
silently strand every install that still holds the old spelling.

One-time, both dialects, rewriting `@<anything>/<slug>` → `@<slug>` in
`agents.region`, `checks.regions`, `check_jobs.region`,
`agent_enrollment_tokens.region`, the `default_regions` parameter, and
`results.region`. It also **retroactively repairs** installs already broken by a
past rename, because both spellings collapse onto the same org-relative form.

Two collision hazards had to be handled explicitly, because collapsing two
spellings onto one can violate a unique index:

- `check_jobs (check_uid, region)` is UNIQUE — a check that listed both
  `@old/paris` and `@new/paris` has two job rows that become one. The migration
  deletes all but one (keeping the earliest-scheduled row) **before** rewriting.
- `results (organization_uid, check_uid, coalesce(region,''), period_type,
  period_start)` is UNIQUE for aggregated rows. The migration deletes all but
  one row per collapsed key (keeping the one with the largest `total_checks`,
  i.e. the most data) before rewriting.
- `checks.regions` is de-duplicated after the rewrite, order-preserving.

**`results` is rewritten synchronously, in one statement, not batched.** The
decision, stated explicitly because the spec asked for it: the predicate is
`region LIKE '@%/%'`, which only ever matches rows produced by a deported agent,
so the *write* volume is tiny; the cost is a single sequential scan of `results`
during startup migration. Batching would not remove that scan (there is no index
on `region`), so it buys nothing but complexity. If a future install grows a
`results` table where a one-pass scan at boot is unacceptable, the escape hatch
is to pre-run the same UPDATE manually out of band — the migration is idempotent
and will then find nothing to do.

### Migrations are NOT transactional unless you name them so

Worth knowing before writing the next one, and the reason the paragraph above
argues from idempotence rather than atomicity: **bun wraps a migration file in a
transaction only when its name ends in `.tx.up.sql` / `.tx.down.sql`**
(`bun/migrate/migration.go`). Plain `NNN_name.up.sql` files — which is all of
ours, 012 included — are executed outside one. On PostgreSQL the multi-statement
simple query still gets an implicit server-side transaction, so PG is atomic *by
accident*; on SQLite it is not, and a crash mid-file leaves a partial apply.

What makes 012 safe under a partial apply is therefore not a transaction: every
statement in it is idempotent and the whole file is re-runnable (nothing is left
matching `@%/%`, and no duplicates are left to collapse). Preserve that property
in any migration that is not `.tx.`-named, or name it `.tx.` and get real
atomicity on both engines.

`down` is a deliberate **no-op**: the rewrite is lossy (the org slug is gone and
is not recoverable from the row), so pretending to reverse it would be worse
than admitting it cannot be.

## Sealing does NOT involve the region string

Checked as part of this change, and stated here so nobody has to re-derive it:
`credentials.SealForRecipients` (`internal/crypto/credentials/sealing.go`) is
age/X25519 to a list of agent public keys. The region string is **not** an AAD,
**not** part of any key derivation, and **not** stored in the sealed envelope —
the envelope carries only `{v, alg, fingerprints, ct}`, where the fingerprints
are SHA-256 of the *recipient public keys*.

**Therefore renaming a region string cannot invalidate a seal, and the migration
does not need to reseal anything.** The recipient set is unchanged because
`agents.region` and `checks.regions` are rewritten with the same expression, so
`ListActiveAgentsByRegion` keeps returning the same agents.

## Placement: pinned vs auto (spec 2026-09-25-06)

`checks.placement` records the INTENT behind `checks.regions`:

- `pinned` — `regions` is the user's explicit list. Nothing ever rewrites it.
- `auto` — `regions` is the CURRENT placement, chosen by
  `regions.Place` (org defaults → system defaults → declared order, filtered by
  `region_pool`, the check's required capabilities and region health) and
  rewritten by the region sweep (`checks.Service.ReplaceAutoChecks`, hooked into
  `regionsweep` on the dark transition and every dark sweep after it).

The placement deliberately lives in `checks.regions` rather than in a side
table: `ReconcileStaleJobSchedules` (boot repair), `reconcileCheckJobs`, the
phase computation and the rate accounting all read `checks.regions`, so any
placement kept elsewhere would be "healed" away at the next boot.

Rules that must hold on every write path (create, PATCH, PUT-by-slug, import,
apply, clone, the bulk switch):

- a private (`@`) region is pinned-only: auto never places into one, a pool may
  not name one, and a check naming one cannot be auto;
- passive checks (heartbeat, email, private-location) have no regions and are
  always `pinned` with no count/pool (`models.Check.NormalizePassiveRegions`);
- `placement: auto` with a non-empty `regions` is a contradiction
  (`INVALID_PLACEMENT`), an explicit `regions` list means pinned;
- nothing ever moves an auto check back to a recovered region.

Every path resolves placement through `internal/handlers/checks/placement.go`
(`resolveCreatePlacement` / `resolveUpdatePlacement`); the config-as-code diff
calls the same resolution so a capped `regionCount` converges.

## Multi-region quorum (spec 2026-09-25-10)

`checks.fail_quorum` (NULL = default) says how many of a check's regions must
be failing, for the confirmation period, before it is down. Resolution lives in
`internal/regionquorum` (the only place): default = all for N ≤ 2, majority
for N ≥ 3; `all`; `majority` = ⌊N/2⌋+1; an integer clamped to N.

- **Legacy mode** (N ≤ 1, passive, or Q ≥ N): `ProcessCheckResult` reads each
  result on its own exactly as before. This is every existing 1- and 2-region
  check, and any explicit `all`. `TestQuorumLegacyEquivalence` replays one
  timeline on a regionless check and on these, step by step.
- **Quorum mode** (Q < N): the signal comes from `check_region_states` (newest
  real reading per check × region, written for non-passive checks with 2+
  regions, maintenance included). ≥ Q failing = failure; 0 < failing < Q =
  success for the clocks/incident but visible `warning` (the regional issue);
  none = the result's own up/warning. Only a result that itself failed opens
  an incident or grows `failure_count`; a passing region's result while the
  quorum fails keeps the check `validating`.
- **Only CURRENT regions count.** The freshness prelude re-reads `regions` and
  `fail_quorum` from the live row, and `regionquorum.Evaluate` ignores rows for
  a region not in `checks.regions` and rows older than the check's stale
  threshold. Rows are never pruned on `check.placement_changed`: correctness
  must not depend on an event being seen.
- status0 has no "regional issue" state: it is a plain `warning` there.
