---
model: sonnet
effort: medium
---

# Deleting a private region leaves its shared region key behind, and a new region with the same slug inherits it

## Problem

Found by the security scan of 2026-11-11 (finding AJ3).

- **Severity:** Low.
- **Who can trigger it:** nobody on their own. It needs an org admin to delete a private region that used a shared
  region key, then create one with the same slug. The party who gains is anyone who kept a copy of the old
  region key, for example the operator of a revoked agent.

Shared region keys live in the org parameter `sealing.region_keys`, keyed by `@slug`
(`server/internal/sealing/sealing.go:27`, `RegionKeys` `:114`, `SetRegionKeys` `:146`).

- `DeletePrivateRegion` (`server/internal/handlers/agents/service.go:390-443`) removes the region definition and its
  liveness monitor. It never removes the region's entry from `sealing.region_keys`.
- After that, `DeleteRegionKey` (`region_keys.go:169`) cannot remove it either, because `ownedRegion` refuses a
  slug that is no longer a region.
- `CreatePrivateRegion` (`service.go:282`) does not reset it.

So a region created later with the same slug starts in shared mode, with the old key.
`RecipientsForRegions` (`sealing.go:169-190`) returns that key as the region's only recipient, and every new
browser-sealed secret of the region is sealed to it. Anyone who kept that key can open them: revoked agents, an
old key file, a copy in a former admin's password manager.

The location page does show the region as shared, with "no active agent holds the current key"
(`regionKeyRow`, `region_keys.go:31`). That warning is visible, but nothing says the key comes from a deleted
region, and an admin who just pastes the old key into the new agents (to clear the warning) gives it back its
reach.

## Proposal

1. In `DeletePrivateRegion`, after `SetOrgCustomRegions` succeeds, delete `@slug` from `sealing.region_keys` (same
   code as `DeleteRegionKey`), and record an audit event `region_key_removed` with reason `region_deleted`.
2. At startup, or lazily in `RegionKeys`, drop the entries whose slug is not an existing private region of the
   org. That cleans up keys orphaned before this fix.
3. `CreatePrivateRegion` asserts that no key exists for the slug (defence in depth after 1 and 2).

## Tests

- Go, `server/internal/handlers/agents`: create region `office`, set a region key, revoke and delete its agents,
  delete the region, create `office` again → `RecipientsForRegions` lists the new region's active agents, not
  the old key, and the region row reports per-agent mode. Positive control: without the delete, the key is
  still listed.
- Go: an orphaned `@gone` entry in `sealing.region_keys` is not returned once `gone` is not a region.
