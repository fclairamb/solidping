---
model: sonnet
effort: medium
---

# There is no way to see checks organized by the host they probe, even though "same host" is the dominant failure-correlation in practice

## Problem

Check groups are whatever the operator made them — in real configs (see the
acmetech export) they are often organized by check *type* ("TLS certificate
expiry" = 40 hosts × 1 check) rather than by host, so the one grouping that
matches actual failure correlation — "everything probing
`cup.abyla.acme-secnum.io`" — exists nowhere in the UI. Restructuring groups
to be host-shaped is an operator decision we can't force; a derived by-host view
costs no schema and lets users see the host-shaped reality regardless of how
their groups are organized.

Precedent in the codebase: the discovery layer already does host-level
aggregation as an explicit render-time GROUP BY
(`discovered_checks.group_key`/`group_label`,
[002_v0_2_0.up.sql:44-81](server/internal/db/postgres/migrations/002_v0_2_0.up.sql:44)).

## Proposal

1. **Backend — derived `targetHost` field** on check responses:
   - computed at read time from the public config, per check type: `host` when
     present, else the hostname parsed from `url`, else `target`; `null` when
     none apply (e.g. heartbeat). Implement once, next to the checker
     definitions ([checkerdef/](server/internal/checkers/checkerdef/)) so each
     type's extraction lives with its config schema rather than in a
     string-key grab-bag;
   - exposed as `targetHost` (camelCase) on the checks list/get responses;
     OpenAPI + `make generate`;
   - support `?sort=targetHost` on the checks list so a by-host view can
     paginate consistently server-side (nulls last, then name as tiebreaker).

2. **Frontend — view toggle** on the checks index
   ([checks.index.tsx](web/dash0/src/routes/orgs/$org/checks.index.tsx)):
   - a small "Group by: **Groups** / **Host**" toggle (design-reference
     pattern; segmented control or equivalent — check the reference first);
   - in Host mode, fetch with `sort=targetHost` and bucket rows client-side by
     `targetHost` — section header shows the hostname and member count, with
     checks with `targetHost: null` in a trailing "No host" bucket;
   - reuse the section/summary rendering built in spec `2026-08-01-02`
     (aggregate section status computed client-side with the same worst-of
     rules — it's a pure function of the visible rows' statuses here, since
     host buckets have no server-side identity);
   - the chosen mode goes in the URL search params (follow the existing
     search-param conventions on this route; note the cold deep-link seeding
     caveat from `wiki`/memory — seed local state from the URL on mount);
   - default remains Groups mode.

3. **Tests**:
   - Go: `targetHost` extraction table-test across types (http with url, tcp
     with host+port, ssl with host, dnsbl with target, heartbeat → null), and
     the `sort=targetHost` ordering incl. null placement;
   - Playwright: toggle to Host view, see the tcp+http+ssl checks of one host
     under a single header; deep-link with the mode in the URL renders Host
     view directly.

4. **Docs**: one paragraph in the checks documentation describing the by-host
   view and that it is derived (renaming a host in one check's config moves it
   to a different bucket).

### Out of scope

- No persistence of host buckets, no host entity, no host-level alerting or
  status pages — this is strictly a view. If host-shaped monitoring proves to
  be what users want, the durable answer is host-shaped check groups
  (specs `2026-08-01-01`..`03`), not a parallel entity.
- No normalization beyond exact hostname match (no `www.` folding, no IP/DNS
  resolution).
