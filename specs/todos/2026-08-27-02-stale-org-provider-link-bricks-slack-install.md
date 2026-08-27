---
model: opus
effort: high
---

# A stale `organization_providers` link permanently bricks the Slack app install

## Problem

Spec 2026-08-25-01 fixed the "provider link outlives the org it points at"
class — but only on the **SSO login** paths. The **app install** path was
missed, and it fails exactly the same way.

`organization_providers` rows are not cascaded when an organization is
soft-deleted (the `on delete cascade` on `organization_uid` only fires for
hard deletes). `DeleteOrg` now releases them itself
(`server/internal/handlers/auth/org_delete.go`, `releaseOrgProviderLinks`),
and `resolveLinkedOrganization`
(`server/internal/handlers/auth/provider_links.go:39`) repairs rows that
went stale before that landed — clearing the dead link and returning
`(nil, nil)` so the caller falls through to its create path.

`resolveLinkedOrganization` has exactly two callers, both SSO:

- `server/internal/handlers/auth/slack_service.go:417` — Sign in with Slack
- `server/internal/handlers/auth/discord_service.go:386` — Sign in with Discord

The Slack **integration install** resolves the org through its own,
unhealed copy of the logic:

```go
// server/internal/integrations/slack/service.go:802
orgProvider, err := s.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeSlack, teamID)
if err == nil && orgProvider != nil {
    org, getErr := s.db.GetOrganization(ctx, orgProvider.OrganizationUID)
    if getErr != nil {
        return nil, fmt.Errorf("failed to get organization: %w", getErr)  // fatal
    }
    return org, nil
}
```

A live link pointing at a soft-deleted org therefore wins the partial
unique lookup on every attempt, `GetOrganization` filters
`deleted_at IS NULL` and answers `sql.ErrNoRows`, and the OAuth callback
returns an error instead of creating a fresh org. **The workspace can never
be reinstalled** — every retry takes the same branch.

### Observed in the field

On the dev deployment, the install for one Slack workspace has been failing
since its organization was deleted:

```
level=ERROR msg="Slack OAuth callback failed"
  error="failed to find or create organization: failed to get organization: sql: no rows in result set"
```

| slack team | link row | target org | org state |
|---|---|---|---|
| `T0ACME0001` | live | `acme-one` | live — install works |
| `T0ACME0002` | live | `acme-two` | live — install works |
| `T0ACME0003` | **live** | `acmecorp2` | **soft-deleted 2026-08-10** — install bricked |

The organization was deleted on 2026-08-10, before spec 2026-08-25-01
landed, so its link was never released; the healer that exists for precisely
this row is not reachable from this code path. The workaround in the field
was to hand-create a replacement organization four days later, which does
not clear the dead link and does not unbrick the install.

### Adjacent, same class, lower severity

`findOrCreateUser` in the same file
(`server/internal/integrations/slack/service.go`, ~line 845) has the mirror
gap: it looks the user up by **email**, so a soft-deleted user yields a
freshly created one — but it then checks `user_providers` by provider id
and, finding a stale live row pointing at the dead user, takes the
`provider != nil` branch and skips linking. The install succeeds, and the
new user is silently left with no Slack identity link.
`resolveLinkedUser` (`provider_links.go`) is the existing helper for this
and is likewise uncalled here.

## Proposal

1. **Reuse the healer, do not fork it.** `internal/integrations/slack`
   already imports `internal/handlers/auth` (one direction only —
   `slack.NewService` takes an `*auth.Service`), so exporting
   `resolveLinkedOrganization` / `resolveLinkedUser` from `handlers/auth`
   and calling them from `integrations/slack` is cycle-free. A second copy
   of the "is this link stale?" decision is the thing to avoid: the doc
   comment in `provider_links.go` states that these helpers are *the single
   place* that decides what a link resolving to nothing means, and this bug
   is what happens when that stops being true.
2. **Fix `findOrCreateOrganizationByTeamID`**
   (`server/internal/integrations/slack/service.go:802`) to route the
   dereference through the helper: link resolves → use the org; link stale
   → it is cleared, fall through to the existing create path; anything else
   → error.
3. **Fix `findOrCreateUser`** in the same file to route its
   `user_providers` lookup through `resolveLinkedUser`, so a stale link is
   cleared and the fresh user gets linked instead of silently unlinked.
4. **Audit for further unhealed dereferences.** Grep every
   `GetOrganizationProviderByProviderID` / `GetUserProviderByProviderID`
   call site and confirm each one either goes through a helper or provably
   cannot see a stale row. Discord's integration install path
   (`internal/integrations/discord`) is the first place to look — its SSO
   twin was fixed, its install path may not have been. Whatever the audit
   finds, record the full call-site list in the spec's Decisions so the next
   person does not have to redo it.
5. **Tests proving the negative** — a green build proves nothing here:
   - install against a workspace whose linked org is soft-deleted
     **succeeds** and produces a *new* org, with the stale link left
     soft-deleted; positive control: install against a workspace with a live
     link reuses that org and creates no second one;
   - the heal is idempotent — a second install after the heal reuses the org
     created by the first, rather than making a third;
   - user linking: install where a live `user_providers` row points at a
     soft-deleted user leaves the newly created user **linked**; positive
     control: an existing live link is reused untouched;
   - a regression test at the `DeleteOrg` level asserting that deleting an
     org releases its provider links, so the prevention half of
     2026-08-25-01 cannot silently rot.
6. **Operational note, not a migration.** One stale row exists on the dev
   deployment and is being cleared by hand. Add a startup or admin-visible
   count of live `organization_providers` rows whose organization does not
   resolve — the same "degrade cleanly and observably" treatment
   `ReportDMCapability` gives the Slack `im:history` gap. Without it, this
   failure mode is invisible until someone tries to reinstall and reads a
   server log.

## Decisions

- **Heal, never resurrect.** Clearing the stale link and creating a fresh
  org is the established behavior from 2026-08-25-01 and this spec does not
  revisit it: the deletion was an explicit act, so the workspace gets a new
  org rather than its old one back. Do not add a restore path.
- **Every heal logs a WARN** naming the provider identity and the dangling
  UID, matching what `resolveLinkedOrganization` already does.

## Out of scope

- The Slack `im:history` scope gap (a workspace installed before that scope
  was requested never receives `message.im`). That is a separate, already
  understood and already instrumented problem — see `ReportDMCapability` —
  and is fixed by reinstalling, which is what this spec unblocks.
- Hard-delete/cascade semantics for soft-deleted rows in general.

## Implementation Plan

1. **Export the healers.** Rename `resolveLinkedOrganization` /
   `resolveLinkedUser` to `ResolveLinkedOrganization` / `ResolveLinkedUser` in
   `server/internal/handlers/auth/provider_links.go` and update the existing
   in-package callers (Slack/Discord SSO, and every other SSO connector that
   already routes its `user_providers` lookup through the helper). No second
   copy of the staleness decision is created anywhere.
2. **Slack integration install** (`internal/integrations/slack/service.go`):
   route `findOrCreateOrganizationByTeamID` through
   `auth.ResolveLinkedOrganization`, and `findOrCreateUser` through
   `auth.ResolveLinkedUser`, in both cases falling through to the existing
   create/link path when the helper reports the link cleared.
3. **Discord integration install** (`internal/integrations/discord/service.go`):
   the same two fixes — `findOrCreateOrganizationByGuildID` (identical
   dereference bug) and `linkGuildToOrg` (a stale link makes it take the
   "already mapped" branch and silently skip linking the org-scoped install's
   org).
4. **Audit.** Enumerate every `GetOrganizationProviderByProviderID` /
   `GetUserProviderByProviderID` call site, classify each as healed / fixed /
   provably safe, and write the list into `## Decisions`.
5. **Observability** (spec point 6, no migration): add
   `CountDanglingOrganizationProviders` to `db.Service` plus both dialect
   implementations, a `solidping_org_provider_links_dangling` gauge, and
   `auth.Service.ReportDanglingProviderLinks(ctx)` called once at boot from
   `internal/app/server.go` right where `ReportDMCapability` is — one WARN line
   and one gauge, exactly the "degrade cleanly and observably" precedent.
6. **Tests proving the negative**, on the real install harness
   (`setupInstallService` / `runInstallCallback` for Slack, `setupDiscordService`
   for Discord): soft-deleted linked org → install succeeds with a NEW org and
   the stale link left cleared; positive control (live link) → same org reused,
   no second org; idempotence (second install after the heal reuses the healed
   org); user link heal + its positive control; and a `DeleteOrg`-level
   regression test that deleting an org releases its provider links.
